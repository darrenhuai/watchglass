package supervisor

import (
	"context"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/source"
	"github.com/darrenhuai/watchglass/internal/state"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

type fakeSource struct{ img image.Image }

func (f *fakeSource) Grab(ctx context.Context) (image.Image, error) { return f.img, nil }

func testWatch(name string) config.Watch {
	return config.Watch{
		Name:     name,
		Source:   "http://unused.invalid/snap.jpg",
		Interval: config.Duration(time.Second),
		Region:   config.Region{X: 0, Y: 0, W: 1, H: 1},
		Trigger:  config.Trigger{Type: "pixel_change", Threshold: 10},
	}
}

func flat() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 100, B: 100, A: 255})
		}
	}
	return img
}

func newSup(reg *state.Registry) *Supervisor {
	s := New(nil, reg, nil, func(string, ...any) {})
	s.NewSource = func(w config.Watch) (source.Source, error) { return &fakeSource{img: flat()}, nil }
	return s
}

func waitForSample(t *testing.T, reg *state.Registry, watch string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := reg.Latest(watch); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no sample for %q within deadline", watch)
}

func TestStartFeedsRegistryAndStopBlocks(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Note: pixel watches baseline on the first tick (no sample); the second
	// tick arrives after ~1s interval.
	waitForSample(t, reg, "a")
	if got := s.Running(); len(got) != 1 || got[0] != "a" {
		t.Errorf("Running = %v", got)
	}
	s.Stop("a")
	if got := s.Running(); len(got) != 0 {
		t.Errorf("Running after Stop = %v", got)
	}
}

func TestStartDuplicateErrors(t *testing.T) {
	s := newSup(state.New(5))
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	if err := s.Start(context.Background(), testWatch("a")); err == nil {
		t.Error("duplicate Start should error")
	}
}

func TestRestartAndStopAll(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(context.Background(), testWatch("a")); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := s.Running(); len(got) != 1 {
		t.Errorf("Running after Restart = %v", got)
	}
	s.StopAll()
	if got := s.Running(); len(got) != 0 {
		t.Errorf("Running after StopAll = %v", got)
	}
}

func TestStartInvalidTriggerErrors(t *testing.T) {
	s := newSup(state.New(5))
	w := testWatch("bad")
	w.Trigger = config.Trigger{Type: "ocr_match", Pattern: "("}
	if err := s.Start(context.Background(), w); err == nil {
		t.Error("invalid trigger should error at Start")
	}
}

// TestExternalCancelRemovesFromRunning guards against Running() lying: when
// the parent ctx passed to Start is cancelled by something other than
// Stop/StopAll, the watch goroutine must still self-remove from the
// running set. It also checks that a subsequent Stop of the now-dead watch
// is a harmless, prompt no-op rather than blocking forever on a done
// channel nobody will ever close again.
func TestExternalCancelRemovesFromRunning(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx, testWatch("a")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Running()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.Running(); len(got) != 0 {
		t.Fatalf("Running after external cancel = %v, want empty within deadline", got)
	}

	done := make(chan struct{})
	go func() {
		s.Stop("a")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Stop on an already-dead watch did not return promptly")
	}
}

func TestStartFailsOnBadSource(t *testing.T) {
	s := New(nil, state.New(5), nil, func(string, ...any) {})
	// Default factory: an unsupported scheme must fail at Start.
	w := testWatch("bad")
	w.Source = "ftp://cam/x"
	if err := s.Start(context.Background(), w); err == nil {
		t.Error("expected Start to reject an unsupported source scheme")
	}
	if got := s.Running(); len(got) != 0 {
		t.Errorf("failed Start must not register a watch, got %v", got)
	}
}

func TestSupervisorThreadsEventHook(t *testing.T) {
	reg := state.New(5)
	s := newSup(reg)
	type got struct {
		watch string
		png   bool
	}
	ch := make(chan got, 10)
	s.OnEvent = func(watch string, ev trigger.Event, png []byte) {
		ch <- got{watch: watch, png: len(png) > 0}
	}
	if err := s.Start(context.Background(), testWatch("a")); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll()
	select {
	case g := <-ch:
		if g.watch != "a" || !g.png {
			t.Errorf("event hook got %+v, want watch a with png", g)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event hook never fired")
	}
}
