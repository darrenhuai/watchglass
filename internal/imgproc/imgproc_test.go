package imgproc

import (
	"image"
	"image/color"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
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

func px(img *image.RGBA, x, y int) (r, g, b uint8) {
	c := img.RGBAAt(x, y)
	return c.R, c.G, c.B
}

func TestApplyZeroValueIsNoOp(t *testing.T) {
	src := gray(4, 4, 100)
	if got := Apply(src, config.Preprocess{}); got != src {
		t.Error("zero-value preprocess should return the input image unchanged")
	}
}

func TestApplyUpscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{R: 10, G: 10, B: 10, A: 255})
	src.Set(1, 0, color.RGBA{R: 200, G: 200, B: 200, A: 255})
	got := Apply(src, config.Preprocess{Upscale: 2})
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 2 {
		t.Fatalf("bounds = %v, want 4x2", got.Bounds())
	}
	if r, _, _ := px(got, 1, 1); r != 10 {
		t.Errorf("replicated pixel (1,1) r = %d, want 10", r)
	}
	if r, _, _ := px(got, 2, 0); r != 200 {
		t.Errorf("replicated pixel (2,0) r = %d, want 200", r)
	}
}

func TestApplyInvert(t *testing.T) {
	src := gray(2, 2, 100)
	got := Apply(src, config.Preprocess{Invert: true})
	if r, _, _ := px(got, 0, 0); r != 155 {
		t.Errorf("inverted 100 -> r = %d, want 155", r)
	}
}

func TestApplyThresholdBinarizes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{R: 40, G: 40, B: 40, A: 255})
	src.Set(1, 0, color.RGBA{R: 200, G: 200, B: 200, A: 255})
	got := Apply(src, config.Preprocess{Threshold: 128})
	if r, _, _ := px(got, 0, 0); r != 0 {
		t.Errorf("below threshold -> r = %d, want 0", r)
	}
	if r, _, _ := px(got, 1, 0); r != 255 {
		t.Errorf("at/above threshold -> r = %d, want 255", r)
	}
}

func TestApplyGrayscaleAveragesColor(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255}) // pure red
	got := Apply(src, config.Preprocess{Grayscale: true})
	r, g, b := px(got, 0, 0)
	if r != g || g != b {
		t.Errorf("grayscale pixel not gray: %d %d %d", r, g, b)
	}
	if r == 0 || r == 255 {
		t.Errorf("red luma should be mid-range, got %d", r)
	}
}

func TestPercentChangedFastPathMatchesGeneric(t *testing.T) {
	mk := func(seed uint8) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 63, 41)) // odd sizes on purpose
		for y := 0; y < 41; y++ {
			for x := 0; x < 63; x++ {
				v := uint8((x*7 + y*13 + int(seed)*29) % 256)
				img.Set(x, y, color.RGBA{R: v, G: v / 2, B: 255 - v, A: 255})
			}
		}
		return img
	}
	a, b := mk(1), mk(9)
	fast := PercentChanged(a, b, 32)
	// Force the generic path by wrapping one argument so the type-assert fails.
	slow := PercentChanged(a, wrapImage{b}, 32)
	if fast != slow {
		t.Errorf("fast=%v generic=%v — paths disagree", fast, slow)
	}
	// Sub-image (non-zero origin) through the fast path must also agree.
	sub := mk(3).SubImage(image.Rect(5, 5, 40, 30)).(*image.RGBA)
	sub2 := mk(4).SubImage(image.Rect(5, 5, 40, 30)).(*image.RGBA)
	if got, want := PercentChanged(sub, sub2, 32), PercentChanged(wrapImage{sub}, wrapImage{sub2}, 32); got != want {
		t.Errorf("subimage fast=%v generic=%v", got, want)
	}
}

type wrapImage struct{ image.Image }

func BenchmarkPercentChangedRGBA(b *testing.B) {
	a := gray(1280, 720, 100)
	bb := gray(1280, 720, 140)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PercentChanged(a, bb, 32)
	}
}

func BenchmarkPercentChangedGeneric(b *testing.B) {
	a := gray(1280, 720, 100)
	bb := gray(1280, 720, 140)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PercentChanged(wrapImage{a}, wrapImage{bb}, 32)
	}
}
