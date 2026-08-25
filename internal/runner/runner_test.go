package runner

import (
	"context"
	"image"
	"image/color"
	"testing"

	"watchglass/internal/config"
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
