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

// grid builds an image from rows of single-byte pixel values (R=G=B=v), so
// a turned image can be compared with the picture drawn out by hand.
func grid(rows ...[]uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, len(rows[0]), len(rows)))
	for y, row := range rows {
		for x, v := range row {
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

// sameGrid compares got with the rows want spells out: size, every pixel,
// and bounds that start at (0,0) like Crop's.
func sameGrid(t *testing.T, label string, got *image.RGBA, want ...[]uint8) {
	t.Helper()
	b := got.Bounds()
	if b.Min != (image.Point{}) || b.Dx() != len(want[0]) || b.Dy() != len(want) {
		t.Fatalf("%s: bounds = %v, want %dx%d at (0,0)", label, b, len(want[0]), len(want))
	}
	for y, row := range want {
		for x, v := range row {
			if c := got.RGBAAt(x, y); c != (color.RGBA{R: v, G: v, B: v, A: 255}) {
				t.Errorf("%s: pixel (%d,%d) = %v, want %d", label, x, y, c, v)
			}
		}
	}
}

// TestRotateExactPixels turns a 3x2 image with six different pixels each
// way. Clockwise: the left column, read bottom to top, becomes the top row.
func TestRotateExactPixels(t *testing.T) {
	src := func() *image.RGBA {
		return grid(
			[]uint8{1, 2, 3},
			[]uint8{4, 5, 6})
	}
	sameGrid(t, "90", Rotate(src(), 90),
		[]uint8{4, 1},
		[]uint8{5, 2},
		[]uint8{6, 3})
	sameGrid(t, "180", Rotate(src(), 180),
		[]uint8{6, 5, 4},
		[]uint8{3, 2, 1})
	sameGrid(t, "270", Rotate(src(), 270),
		[]uint8{3, 6},
		[]uint8{2, 5},
		[]uint8{1, 4})
}

// A quarter turn of a non-square crop swaps its width and height; a half
// turn keeps them.
func TestRotateSwapsSizeOnQuarterTurns(t *testing.T) {
	for deg, want := range map[int]image.Point{90: {X: 3, Y: 7}, 270: {X: 3, Y: 7}, 180: {X: 7, Y: 3}} {
		if got := Rotate(gray(7, 3, 9), deg).Bounds(); got.Min != (image.Point{}) || got.Size() != want {
			t.Errorf("rotate %d of 7x3: bounds = %v, want size %v at (0,0)", deg, got, want)
		}
	}
}

func TestRotateFourQuarterTurnsIsIdentity(t *testing.T) {
	rows := [][]uint8{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}}
	img := grid(rows...)
	for i := 0; i < 4; i++ {
		img = Rotate(img, 90)
	}
	sameGrid(t, "4x90", img, rows...)
	sameGrid(t, "90 then 270", Rotate(Rotate(grid(rows...), 90), 270), rows...)
	sameGrid(t, "180 twice", Rotate(Rotate(grid(rows...), 180), 180), rows...)
	sameGrid(t, "90 twice is 180", Rotate(Rotate(grid(rows...), 90), 90),
		[]uint8{12, 11, 10, 9}, []uint8{8, 7, 6, 5}, []uint8{4, 3, 2, 1})
}

// 0 is off and returns the very image it was given; so does an angle that
// isn't a quarter turn (config.Validate refuses those before they get here).
func TestRotateZeroAndOddAnglesReturnInput(t *testing.T) {
	src := gray(4, 2, 100)
	for _, deg := range []int{0, 45, -90, 360, 91} {
		if got := Rotate(src, deg); got != src {
			t.Errorf("rotate %d: want the input image back unchanged", deg)
		}
	}
}

// A crop that is a view into a larger frame (bounds not at the origin)
// turns the same as a copy of it.
func TestRotateSubImage(t *testing.T) {
	frame := grid(
		[]uint8{0, 0, 0, 0, 0},
		[]uint8{0, 1, 2, 3, 0},
		[]uint8{0, 4, 5, 6, 0},
		[]uint8{0, 0, 0, 0, 0})
	sub := frame.SubImage(image.Rect(1, 1, 4, 3)).(*image.RGBA)
	sameGrid(t, "sub 90", Rotate(sub, 90), []uint8{4, 1}, []uint8{5, 2}, []uint8{6, 3})
	sameGrid(t, "sub 270", Rotate(sub, 270), []uint8{3, 6}, []uint8{2, 5}, []uint8{1, 4})
	sameGrid(t, "sub 180", Rotate(sub, 180), []uint8{6, 5, 4}, []uint8{3, 2, 1})
}

// Apply turns the crop before the rest of the chain: the upscaled result
// is the turned picture, twice the size, and binarize works on it too.
func TestApplyRotatesFirst(t *testing.T) {
	src := func() *image.RGBA {
		return grid(
			[]uint8{10, 20, 30},
			[]uint8{200, 210, 220})
	}
	sameGrid(t, "rotate only", Apply(src(), config.Preprocess{Rotate: 90}),
		[]uint8{200, 10},
		[]uint8{210, 20},
		[]uint8{220, 30})
	sameGrid(t, "rotate + upscale", Apply(src(), config.Preprocess{Rotate: 90, Upscale: 2}),
		[]uint8{200, 200, 10, 10},
		[]uint8{200, 200, 10, 10},
		[]uint8{210, 210, 20, 20},
		[]uint8{210, 210, 20, 20},
		[]uint8{220, 220, 30, 30},
		[]uint8{220, 220, 30, 30})
	sameGrid(t, "rotate + invert + binarize", Apply(src(), config.Preprocess{Rotate: 270, Invert: true, Threshold: 128}),
		[]uint8{255, 0},
		[]uint8{255, 0},
		[]uint8{255, 0})
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
