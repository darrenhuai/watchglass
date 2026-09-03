package runner

import (
	"context"
	"errors"
	"image"
	"image/color"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/health"
	"github.com/darrenhuai/watchglass/internal/history"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// --- fakes ---

type fakeSource struct {
	imgs []image.Image
	i    int
}

func (f *fakeSource) Grab(ctx context.Context) (image.Image, error) {
	img := f.imgs[f.i%len(f.imgs)]
	f.i++
	return img, nil
}

type fakeOCR struct {
	texts []string
	i     int
}

func (f *fakeOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	t := f.texts[f.i%len(f.texts)]
	f.i++
	return t, nil
}

type fakeNotifier struct{ sent []string }

func (f *fakeNotifier) Send(ctx context.Context, title, body string) error {
	f.sent = append(f.sent, title+" | "+body)
	return nil
}

func flat(w, h int, v uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

func watchCfg(triggerCfg config.Trigger) config.Watch {
	return config.Watch{
		Name:    "test-watch",
		Source:  "fake",
		Region:  config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger: triggerCfg,
	}
}

func TestOCRWatchFiresAndNotifies(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	ocrEngine := &fakeOCR{texts: []string{"Printing 87%", "PRINT COMPLETE"}}
	notifier := &fakeNotifier{}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1}),
		src, ocrEngine, notifier, nil, t.Logf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("no fire expected yet, got %v", notifier.sent)
	}
	if _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected 1 notification, got %v", notifier.sent)
	}
}

func TestPixelWatchBaselinesThenFires(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{
		flat(10, 10, 0),   // tick 1: baseline, never fires
		flat(10, 10, 0),   // tick 2: unchanged
		flat(10, 10, 255), // tick 3: full change -> fire
	}}
	notifier := &fakeNotifier{}
	r, err := New(
		watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10}),
		src, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := r.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("expected exactly 1 notification, got %d: %v", len(notifier.sent), notifier.sent)
	}
}

func TestRunToleratesZeroIntervalAndCancels(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 0)}}
	r, err := New(
		watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10}),
		src, nil, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	// watchCfg leaves Interval at its zero value; Run must default it
	// instead of panicking in time.NewTicker.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

func TestOnReadingHookObservesTicks(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	ocrEngine := &fakeOCR{texts: []string{"A", "B"}}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1}),
		src, ocrEngine, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		reading string
		fired   bool
	}
	var calls []call
	r.OnReading = func(ev trigger.Event, crop image.Image) {
		if crop == nil {
			t.Error("hook received nil crop")
		}
		calls = append(calls, call{ev.Reading, ev.Fired})
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := []call{{"A", false}, {"B", true}}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestPreprocessAppliedBeforeOCR(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 200)}}
	var seen image.Image
	capture := &captureOCR{onImg: func(img image.Image) { seen = img }}
	w := watchCfg(config.Trigger{Type: "ocr_changed", Confirm: 1})
	w.Preprocess = config.Preprocess{Invert: true}
	r, err := New(w, src, capture, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen == nil {
		t.Fatal("OCR engine never called")
	}
	rr, _, _, _ := seen.At(0, 0).RGBA()
	if got := uint8(rr >> 8); got != 55 {
		t.Errorf("OCR saw pixel %d, want 55 (inverted 200)", got)
	}
}

type captureOCR struct{ onImg func(image.Image) }

func (c *captureOCR) Recognize(ctx context.Context, img image.Image) (string, error) {
	c.onImg(img)
	return "x", nil
}

func TestTickOrdersStoreThenHookThenNotify(t *testing.T) {
	// Create a real store
	store, err := history.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Set up sources and engine
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	ocrEngine := &fakeOCR{texts: []string{"GO"}}
	notifier := &fakeNotifier{}

	// Create runner with watch that fires on first tick
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "GO", Confirm: 1}),
		src, ocrEngine, notifier, store, t.Logf)
	if err != nil {
		t.Fatal(err)
	}

	var hookRan bool
	const watchName = "test-watch"
	r.OnReading = func(ev trigger.Event, crop image.Image) {
		// Assert: store already holds exactly 1 reading (record-before-hook)
		readings, err := store.LastN(watchName, 5)
		if err != nil {
			t.Errorf("hook: store.LastN failed: %v", err)
			return
		}
		if len(readings) != 1 {
			t.Errorf("hook: store has %d readings, want 1", len(readings))
			return
		}

		// Assert: notifier sent is still empty (hook-before-notify)
		if len(notifier.sent) != 0 {
			t.Errorf("hook: notifier already sent %d messages, want 0", len(notifier.sent))
			return
		}

		hookRan = true
	}

	ctx := context.Background()
	if _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// After tick: assert hook ran once and notifier sent 1 notification
	if !hookRan {
		t.Error("hook did not run")
	}
	if len(notifier.sent) != 1 {
		t.Errorf("notifier sent %d messages, want 1", len(notifier.sent))
	}
}

func TestNextIntervalDisabledWithoutMax(t *testing.T) {
	base := 5 * time.Second
	for _, max := range []time.Duration{0, base, -time.Second} {
		if got := NextInterval(base, max, base, false); got != base {
			t.Errorf("max=%v: got %v, want %v (adaptive off)", max, got, base)
		}
	}
}

func TestNextIntervalBacksOffAndCaps(t *testing.T) {
	base, max := 5*time.Second, 20*time.Second
	got := NextInterval(base, max, base, false)
	if got != 10*time.Second {
		t.Errorf("first backoff = %v, want 10s", got)
	}
	got = NextInterval(base, max, got, false)
	if got != 20*time.Second {
		t.Errorf("second backoff = %v, want 20s", got)
	}
	got = NextInterval(base, max, got, false)
	if got != 20*time.Second {
		t.Errorf("capped backoff = %v, want 20s", got)
	}
}

func TestNextIntervalResetsOnChange(t *testing.T) {
	base, max := 5*time.Second, 60*time.Second
	if got := NextInterval(base, max, 40*time.Second, true); got != base {
		t.Errorf("changed: got %v, want %v", got, base)
	}
}

func TestNextIntervalFloorsAtBase(t *testing.T) {
	base, max := 5*time.Second, 60*time.Second
	if got := NextInterval(base, max, time.Second, false); got != 10*time.Second {
		t.Errorf("current below base: got %v, want 10s", got)
	}
}

func TestTickChanged(t *testing.T) {
	cases := []struct {
		name        string
		triggerType string
		ev          trigger.Event
		err         error
		lastReading string
		want        bool
	}{
		{
			name:        "pixel with grab error resets",
			triggerType: "pixel_change",
			ev:          trigger.Event{},
			err:         errors.New("grab failed"),
			lastReading: "",
			want:        true,
		},
		{
			name:        "pixel with no change does not reset",
			triggerType: "pixel_change",
			ev:          trigger.Event{},
			err:         nil,
			lastReading: "",
			want:        false,
		},
		{
			name:        "pixel fired resets",
			triggerType: "pixel_change",
			ev:          trigger.Event{Fired: true},
			err:         nil,
			lastReading: "",
			want:        true,
		},
		{
			name:        "ocr reading changed resets",
			triggerType: "ocr_changed",
			ev:          trigger.Event{Reading: "B"},
			err:         nil,
			lastReading: "A",
			want:        true,
		},
		{
			name:        "ocr same reading does not reset",
			triggerType: "ocr_changed",
			ev:          trigger.Event{Reading: "A"},
			err:         nil,
			lastReading: "A",
			want:        false,
		},
		{
			name:        "ocr error resets regardless of reading",
			triggerType: "ocr_changed",
			ev:          trigger.Event{Reading: ""},
			err:         errors.New("ocr failed"),
			lastReading: "A",
			want:        true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tickChanged(tc.triggerType, tc.ev, tc.err, tc.lastReading); got != tc.want {
				t.Errorf("tickChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHealthNotifiesOnDownAndRecovery(t *testing.T) {
	failing := &flakySource{fail: true}
	notifier := &fakeNotifier{}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 2
	r, err := New(w, failing, nil, notifier, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("expected grab error")
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("one failure must not notify, got %v", notifier.sent)
	}
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("expected grab error")
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("threshold reached: want 1 notification, got %v", notifier.sent)
	}
	if !strings.Contains(notifier.sent[0], "unreachable") {
		t.Errorf("down notification = %q", notifier.sent[0])
	}
	if _, err := r.Tick(ctx); err == nil {
		t.Fatal("expected grab error")
	}
	if len(notifier.sent) != 1 {
		t.Errorf("already-down must not re-notify, got %v", notifier.sent)
	}
	failing.fail = false
	if _, err := r.Tick(ctx); err != nil {
		t.Fatalf("recovered grab: %v", err)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("recovery should notify, got %v", notifier.sent)
	}
	if !strings.Contains(notifier.sent[1], "recovered") {
		t.Errorf("recovery notification = %q", notifier.sent[1])
	}
}

func TestTickReturnsEvaluatedEvent(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	engine := &fakeOCR{texts: []string{"Printing", "PRINT COMPLETE"}}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1}),
		src, engine, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Reading != "Printing" || ev.Fired {
		t.Errorf("first tick event = %+v", ev)
	}
	ev, err = r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Fired || ev.Reading != "PRINT COMPLETE" {
		t.Errorf("second tick event = %+v, want fired", ev)
	}
}

// flakySource fails on demand so health transitions can be driven.
type flakySource struct {
	fail bool
}

func (f *flakySource) Grab(ctx context.Context) (image.Image, error) {
	if f.fail {
		return nil, errors.New("connection refused")
	}
	return flat(10, 10, 128), nil
}

type imageNotifier struct {
	fakeNotifier
	images int
}

func (n *imageNotifier) SendImage(ctx context.Context, title, body string, png []byte) error {
	if len(png) == 0 {
		return errors.New("empty png")
	}
	n.images++
	return nil
}

func TestFiredEventUsesImageSender(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{flat(10, 10, 128)}}
	engine := &fakeOCR{texts: []string{"PRINT COMPLETE"}}
	n := &imageNotifier{}
	r, err := New(
		watchCfg(config.Trigger{Type: "ocr_match", Pattern: "(?i)print complete", Confirm: 1}),
		src, engine, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n.images != 1 {
		t.Errorf("SendImage calls = %d, want 1", n.images)
	}
	if len(n.sent) != 0 {
		t.Errorf("plain Send should not be used when ImageSender available, got %v", n.sent)
	}
}

func TestOnHealthHookFires(t *testing.T) {
	failing := &flakySource{fail: true}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 10})
	w.HealthAfter = 1
	r, err := New(w, failing, nil, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var events []health.Event
	r.OnHealth = func(hev health.Event) { events = append(events, hev) }
	ctx := context.Background()
	r.Tick(ctx) // failure 1 -> down
	failing.fail = false
	r.Tick(ctx) // success -> healthy
	if len(events) != 2 || events[0].State != "down" || events[1].State != "healthy" {
		t.Errorf("health hook events = %+v", events)
	}
}
