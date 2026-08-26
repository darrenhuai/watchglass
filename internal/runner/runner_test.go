package runner

import (
	"context"
	"image"
	"image/color"
	"reflect"
	"testing"
	"time"

	"watchglass/internal/config"
	"watchglass/internal/trigger"
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
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("no fire expected yet, got %v", notifier.sent)
	}
	if err := r.Tick(ctx); err != nil {
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
		if err := r.Tick(ctx); err != nil {
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
		if err := r.Tick(ctx); err != nil {
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
	if err := r.Tick(context.Background()); err != nil {
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
