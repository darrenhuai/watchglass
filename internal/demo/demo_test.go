package demo_test

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/demo"
	"github.com/darrenhuai/watchglass/internal/imgproc"
	"github.com/darrenhuai/watchglass/internal/ocr"
	"github.com/darrenhuai/watchglass/internal/runner"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

func TestFrameIndexFollowsTheLoop(t *testing.T) {
	s := time.Second
	cases := []struct {
		at   time.Duration
		want int
	}{
		{-5 * s, 0}, {0, 0}, {3900 * time.Millisecond, 0}, {4 * s, 1}, {8 * s, 2}, {12 * s, 3},
		{16 * s, 4}, {19900 * time.Millisecond, 4}, {20 * s, 5}, {30 * s, 5}, {39900 * time.Millisecond, 5},
		{40 * s, 0}, {44 * s, 1}, {60 * s, 5}, {10*demo.Cycle + 21*s, 5},
	}
	for _, c := range cases {
		if got := demo.FrameIndex(c.at); got != c.want {
			t.Errorf("FrameIndex(%v) = %d, want %d", c.at, got, c.want)
		}
	}
	if demo.Cycle != 40*s {
		t.Errorf("Cycle = %v, want 40s", demo.Cycle)
	}
}

// Grab returns the frame for the injected clock: the same decoded image
// for the same moment, decoded once.
func TestSourceGrabUsesTheClock(t *testing.T) {
	for _, scene := range demo.Scenes {
		src, err := demo.NewSource(scene)
		if err != nil {
			t.Fatal(err)
		}
		frames, err := demo.Frames(scene)
		if err != nil || len(frames) != 6 {
			t.Fatalf("%s: %d frames, err %v", scene, len(frames), err)
		}
		start := time.Unix(1_700_000_000, 0)
		src.Start = start
		for _, c := range []struct {
			at   time.Duration
			want int
		}{{0, 0}, {9 * time.Second, 2}, {25 * time.Second, 5}, {41 * time.Second, 0}} {
			src.Now = func() time.Time { return start.Add(c.at) }
			img, err := src.Grab(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if img != frames[c.want] {
				t.Errorf("%s at %v: not frame %d", scene, c.at, c.want)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := src.Grab(ctx); err == nil {
			t.Errorf("%s: a cancelled grab should fail", scene)
		}
	}
	if _, err := demo.NewSource("camera"); err == nil {
		t.Error("an unknown scene should be refused")
	}
	if _, err := demo.NewSource(" sevenseg "); err != nil {
		t.Errorf("surrounding space is trimmed: %v", err)
	}
}

// loadDemoConfig writes demo.Config to a file and loads it the way
// watchglass does, so the seeded config passes config.Validate.
func loadDemoConfig(t *testing.T, readText bool) map[string]config.Watch {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, demo.Config(readText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("demo config doesn't load: %v\n%s", err, demo.Config(readText))
	}
	out := map[string]config.Watch{}
	for _, w := range cfg.Watches {
		out[w.Name] = w
	}
	if len(out) != 2 {
		t.Fatalf("want demo-printer and demo-scale, got %v", cfg.Watches)
	}
	return out
}

// The seven-segment decoder reads every frame of the scale, and only the
// held one is above demo-scale's threshold.
func TestSevenSegHeldFrameIsTheOnlyOneAboveThreshold(t *testing.T) {
	w := loadDemoConfig(t, true)["demo-scale"]
	frames, err := demo.Frames(demo.SevenSeg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"23.5", "23.7", "24.1", "24.6", "25.0", "25.3"}
	dec := ocr.NewSevenSeg()
	for i, f := range frames {
		text, err := dec.Recognize(context.Background(), imgproc.Apply(imgproc.Crop(f, w.Region), w.Preprocess))
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		cond, err := trigger.Check(w.Trigger, text)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("frame %d reads %q: %s", i, text, cond.Detail)
		if text != want[i] {
			t.Errorf("frame %d reads %q, want %q", i, text, want[i])
		}
		if cond.Met != (i == 5) {
			t.Errorf("frame %d (%q): condition met = %v, want %v", i, text, cond.Met, i == 5)
		}
	}
}

// printerText stands in for tesseract (not on every test box): what the
// printer LCD says in each frame.
type printerText struct{ at func() int }

var printerLines = []string{"PRINTING 12%", "PRINTING 34%", "PRINTING 58%", "PRINTING 79%", "PRINTING 94%", "PRINT COMPLETE"}

func (p printerText) Recognize(context.Context, image.Image) (string, error) {
	return printerLines[p.at()], nil
}

// firstFire drives a real runner on the demo camera every interval from a
// start offset into the loop, and returns how long it took to fire.
func firstFire(t *testing.T, w config.Watch, scene string, engine func(at func() int) ocr.Engine, offset time.Duration) (time.Duration, bool) {
	t.Helper()
	src, err := demo.NewSource(scene)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1_700_000_000, 0)
	now := start.Add(offset)
	src.Start, src.Now = start, func() time.Time { return now }
	var eng ocr.Engine
	if engine != nil {
		eng = engine(func() int { return demo.FrameIndex(now.Sub(start)) })
	}
	r, err := runner.New(w, src, eng, nil, nil, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	for elapsed := time.Duration(0); elapsed <= 45*time.Second; elapsed += time.Duration(w.Interval) {
		ev, err := r.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ev.Fired {
			return elapsed, true
		}
		now = now.Add(time.Duration(w.Interval))
	}
	return 0, false
}

// Whenever a demo watch starts (at boot, or created later from the UI), it
// fires within 45 seconds: the acceptance for `watchglass -demo`.
func TestDemoWatchesFireWithin45Seconds(t *testing.T) {
	withText := loadDemoConfig(t, true)
	pixels := loadDemoConfig(t, false)
	if pixels["demo-printer"].Trigger.Type != "pixel_change" || withText["demo-printer"].Trigger.Type != "ocr_match" {
		t.Fatalf("demo-printer should read text with tesseract and watch pixels without: %+v / %+v",
			withText["demo-printer"].Trigger, pixels["demo-printer"].Trigger)
	}
	sevenseg := func(func() int) ocr.Engine { return ocr.NewSevenSeg() }
	text := func(at func() int) ocr.Engine { return printerText{at} }
	for offset := time.Duration(0); offset < demo.Cycle; offset += time.Second {
		for _, c := range []struct {
			name   string
			w      config.Watch
			scene  string
			engine func(func() int) ocr.Engine
		}{
			{"demo-printer ocr_match", withText["demo-printer"], demo.Printer, text},
			{"demo-printer pixel_change", pixels["demo-printer"], demo.Printer, nil},
			{"demo-scale", withText["demo-scale"], demo.SevenSeg, sevenseg},
		} {
			took, fired := firstFire(t, c.w, c.scene, c.engine, offset)
			if !fired {
				t.Errorf("%s started %v into the loop never fired within 45s", c.name, offset)
			} else if offset == 0 {
				t.Logf("%s from a fresh start fired after %v", c.name, took)
			}
		}
	}
}

// Without tesseract demo-printer watches pixels, and it must fire on the
// flip to PRINT COMPLETE, not on every percent that ticks by.
func TestPixelFallbackFiresOnCompletionOnly(t *testing.T) {
	w := loadDemoConfig(t, false)["demo-printer"]
	frames, err := demo.Frames(demo.Printer)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(frames); i++ {
		pct := imgproc.PercentChanged(imgproc.Crop(frames[i-1], w.Region), imgproc.Crop(frames[i], w.Region), 32)
		t.Logf("frame %d -> %d: %.1f%% changed", i-1, i, pct)
		if big := pct >= w.Trigger.Threshold; big != (i == 5) {
			t.Errorf("frame %d -> %d changes %.1f%%: above threshold %v = %v, want %v", i-1, i, pct, w.Trigger.Threshold, big, i == 5)
		}
	}
}
