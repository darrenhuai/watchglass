package runner

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/trigger"
)

// sidewaysFrame is 6 wide and 2 tall, dark except for one bright pixel in
// its top-left corner. Turned 90 degrees clockwise it is 2 wide and 6 tall
// with the bright pixel in the top-right corner.
func sidewaysFrame() *image.RGBA {
	img := flat(6, 2, 20).(*image.RGBA)
	img.SetRGBA(0, 0, color.RGBA{R: 240, G: 240, B: 240, A: 255})
	return img
}

// redAt is the red channel of a pixel, as 8 bits.
func redAt(img image.Image, x, y int) uint8 {
	r, _, _, _ := img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y).RGBA()
	return uint8(r >> 8)
}

type pngNotifier struct {
	fakeNotifier
	pngs [][]byte
}

func (n *pngNotifier) SendImage(ctx context.Context, title, body string, png []byte) error {
	n.pngs = append(n.pngs, png)
	return nil
}

// A watch with preprocess.rotate reads the turned crop, and the turned crop
// is also what the reading hook (the Live panel, Home Assistant's camera)
// and the alert get. The rest of the preprocessing stays for the engine's
// eyes only.
func TestRotateTurnsTheCropForEngineHookAndAlert(t *testing.T) {
	src := &fakeSource{imgs: []image.Image{sidewaysFrame()}}
	var read image.Image
	engine := &captureOCR{onImg: func(img image.Image) { read = img }}
	n := &pngNotifier{}
	w := watchCfg(config.Trigger{Type: "ocr_match", Pattern: "x", Confirm: 1})
	w.Preprocess = config.Preprocess{Rotate: 90, Invert: true}
	r, err := New(w, src, engine, n, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var shown image.Image
	r.OnReading = func(ev trigger.Event, crop image.Image, at time.Time) { shown = crop }
	ev, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Fired {
		t.Fatalf("expected the watch to fire, got %+v", ev)
	}

	upright := image.Pt(2, 6)
	if read == nil || read.Bounds().Size() != upright {
		t.Fatalf("engine read %v, want a 2x6 crop", read)
	}
	// Turned, then inverted: the bright corner is top-right and dark.
	if got := redAt(read, 1, 0); got != 15 {
		t.Errorf("engine: top-right pixel = %d, want 15 (240 inverted)", got)
	}
	if got := redAt(read, 0, 0); got != 235 {
		t.Errorf("engine: top-left pixel = %d, want 235 (20 inverted)", got)
	}

	if shown == nil || shown.Bounds().Size() != upright {
		t.Fatalf("OnReading got %v, want the 2x6 turned crop", shown)
	}
	// Turned but not inverted: the camera's own colours.
	if a, b := redAt(shown, 1, 0), redAt(shown, 0, 0); a != 240 || b != 20 {
		t.Errorf("OnReading crop: top-right = %d, top-left = %d, want 240 and 20", a, b)
	}

	if len(n.pngs) != 1 {
		t.Fatalf("alerts with a picture = %d, want 1", len(n.pngs))
	}
	sent, err := png.Decode(bytes.NewReader(n.pngs[0]))
	if err != nil {
		t.Fatal(err)
	}
	if sent.Bounds().Size() != upright || redAt(sent, 1, 0) != 240 || redAt(sent, 0, 0) != 20 {
		t.Errorf("alert picture: size %v, top-right %d, top-left %d; want 2x6, 240, 20",
			sent.Bounds().Size(), redAt(sent, 1, 0), redAt(sent, 0, 0))
	}
}

// pixel_change compares the crop as the camera sends it and doesn't turn
// it: a rotate left in the file by a watch that used to read text changes
// nothing about what is compared or shown.
func TestRotateLeavesPixelChangeAlone(t *testing.T) {
	changed := flat(6, 2, 200)
	src := &fakeSource{imgs: []image.Image{sidewaysFrame(), changed}}
	w := watchCfg(config.Trigger{Type: "pixel_change", Threshold: 50})
	w.Preprocess = config.Preprocess{Rotate: 90}
	r, err := New(w, src, nil, nil, nil, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var shown image.Image
	r.OnReading = func(ev trigger.Event, crop image.Image, at time.Time) { shown = crop }
	if _, err := r.Tick(context.Background()); err != nil { // the baseline frame
		t.Fatal(err)
	}
	ev, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Fired {
		t.Errorf("expected the changed frame to fire, got %+v", ev)
	}
	if shown == nil || shown.Bounds().Size() != image.Pt(6, 2) {
		t.Errorf("pixel_change crop = %v, want the 6x2 crop as the camera sent it", shown)
	}
}
