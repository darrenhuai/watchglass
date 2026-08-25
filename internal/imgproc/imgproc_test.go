package imgproc

import (
	"image"
	"image/color"
	"testing"

	"watchglass/internal/config"
)

func TestCropQuadrant(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	// paint the bottom-right quadrant red
	for y := 50; y < 100; y++ {
		for x := 50; x < 100; x++ {
			src.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	got := Crop(src, config.Region{X: 0.5, Y: 0.5, W: 0.5, H: 0.5})
	if got.Bounds().Dx() != 50 || got.Bounds().Dy() != 50 {
		t.Fatalf("bounds = %v, want 50x50", got.Bounds())
	}
	r, _, _, _ := got.At(10, 10).RGBA()
	if r>>8 != 255 {
		t.Errorf("expected red pixel inside crop, got r=%d", r>>8)
	}
}

func TestCropFullFrame(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 33, 17))
	got := Crop(src, config.Region{X: 0, Y: 0, W: 1, H: 1})
	if got.Bounds().Dx() != 33 || got.Bounds().Dy() != 17 {
		t.Fatalf("bounds = %v, want 33x17", got.Bounds())
	}
}

func gray(w, h int, v uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

func TestPercentChangedIdentical(t *testing.T) {
	if got := PercentChanged(gray(10, 10, 200), gray(10, 10, 200), 32); got != 0 {
		t.Errorf("identical frames: got %v, want 0", got)
	}
}

func TestPercentChangedFull(t *testing.T) {
	if got := PercentChanged(gray(10, 10, 0), gray(10, 10, 255), 32); got != 100 {
		t.Errorf("black vs white: got %v, want 100", got)
	}
}

func TestPercentChangedHalf(t *testing.T) {
	b := gray(10, 10, 0)
	for y := 0; y < 5; y++ {
		for x := 0; x < 10; x++ {
			b.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	if got := PercentChanged(gray(10, 10, 0), b, 32); got != 50 {
		t.Errorf("half changed: got %v, want 50", got)
	}
}

func TestPercentChangedSizeMismatch(t *testing.T) {
	if got := PercentChanged(gray(10, 10, 0), gray(9, 10, 0), 32); got != 100 {
		t.Errorf("size mismatch: got %v, want 100", got)
	}
}

func TestPercentChangedBelowTolerance(t *testing.T) {
	if got := PercentChanged(gray(10, 10, 100), gray(10, 10, 110), 32); got != 0 {
		t.Errorf("delta below tol: got %v, want 0", got)
	}
}
