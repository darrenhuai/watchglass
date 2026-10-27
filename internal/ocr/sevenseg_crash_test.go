package ocr

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
)

// The tests in this file hold the decoder to one promise: whatever the
// frame, it answers. A panic here takes a saved watch, and every other
// watch in the process, down with it.

// philipsBox is where round 6's watch had its box on the angled LED clock
// photo (testdata/photos/clock-philips-106.jpg, see the README there), in
// that crop's pixels. With Binarize 128 the decoder used to panic on it.
var philipsBox = image.Rect(48, 48, 369, 310)

func loadPhoto(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open("testdata/photos/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// region turns a pixel box inside img into the fractions a watch saves.
func region(img image.Image, r image.Rectangle) config.Region {
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	return config.Region{X: float64(r.Min.X-b.Min.X) / w, Y: float64(r.Min.Y-b.Min.Y) / h, W: float64(r.Dx()) / w, H: float64(r.Dy()) / h}
}

// readSafely runs the decoder the way a watch does, crop and preprocessing
// first, and turns a panic into an error that names the input.
func readSafely(img image.Image, reg config.Region, p config.Preprocess) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	crop := imgproc.Apply(imgproc.Crop(img, reg), p)
	text, _, err = NewSevenSeg().RecognizeWords(context.Background(), crop)
	return text, err
}

func TestSevenSegAngledClockPhotoBinarized(t *testing.T) {
	img := loadPhoto(t, "clock-philips-106.jpg")
	text, err := readSafely(img, region(img, philipsBox), config.Preprocess{Threshold: 128})
	if err != nil {
		t.Fatalf("Binarize 128 on the angled clock: %v", err)
	}
	t.Logf("read %q", text)
}

// The lean of bars leaning far past shearMax keeps sharpening the column
// projection all the way out. The refinement search used to follow it,
// moving its own bound as it went, until a row's shift fell outside the
// counts buffer.
func TestShearEstimateStaysInRange(t *testing.T) {
	for _, lean := range []float64{0.45, 0.7, 1.0, 1.5, -0.45, -1.0} {
		t.Run(fmt.Sprint(lean), func(t *testing.T) {
			const w, h = 160, 60
			m := bitmap{w: w, h: h, on: make([]bool, w*h)}
			for y := 0; y < h; y++ {
				for _, x0 := range []int{40, 80, 120} {
					x := x0 - int(lean*float64(y-h/2))
					for dx := 0; dx < 4; dx++ {
						if xx := x + dx; xx >= 0 && xx < w {
							m.on[y*w+xx] = true
						}
					}
				}
			}
			wk := getWork(w, h)
			defer workPool.Put(wk)
			var s float64
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("shearEstimate panicked: %v", r)
					}
				}()
				s = shearEstimate(m, wk)
			}()
			if s < -shearMax || s > shearMax {
				t.Fatalf("shear %g, outside ±%g", s, shearMax)
			}
		})
	}
}

// A fuzzer found the same fault from the other side: an LCD leaning -0.58,
// past what the decoder corrects, sent the lean search out of range and
// deshear wrote past its buffer ("slice bounds out of range").
func TestSevenSegSteepLeanLCD(t *testing.T) {
	o := renderOpts{H: 122, Stroke: 6, Gap: 1, Dark: true, Noise: 30, Blur: 1, Seed: 1, Shear: -0.58}
	img := boxBlur(shearImage(render("00", o), o.Shear, color.RGBA{255, 255, 255, 255}), o.Blur)
	b := img.Bounds()
	r := image.Rect(b.Min.X+5, b.Min.Y-3, b.Max.X-39, b.Max.Y-4)
	crop := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(crop, crop.Bounds(), image.NewUniform(color.RGBA{128, 128, 128, 255}), image.Point{}, draw.Src)
	draw.Draw(crop, crop.Bounds(), img, r.Min, draw.Src)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	// Buffers sized for this crop, not left in the pool by a bigger one an
	// earlier test read, which would hide the overrun.
	workPool = sync.Pool{}
	NewSevenSeg().RecognizeWords(context.Background(), crop)
}

// deshear's output buffer holds a lean of at most shearMax; a larger one
// is held to it rather than written past the buffer.
func TestDeshearHoldsLeanToRange(t *testing.T) {
	const w, h = 50, 40
	m := bitmap{w: w, h: h, on: make([]bool, w*h)}
	for i := range m.on {
		m.on[i] = i%7 == 0
	}
	// Sized as getWork sizes them; a pooled work could be bigger.
	n := (w + 2*(int(math.Ceil(shearMax*h))+1)) * h
	for _, s := range []float64{shearMax, -shearMax, 0.8, -2} {
		wk := &work{shifts: make([]int, h)}
		for i := range wk.bufs {
			wk.bufs[i] = make([]bool, n)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("deshear(%g) panicked: %v", s, r)
				}
			}()
			out := deshear(m, s, wk)
			if out.w*out.h > n {
				t.Fatalf("deshear(%g) is %dx%d, past its buffer", s, out.w, out.h)
			}
		}()
	}
}

// jitter is what a hand-drawn box varies by: its size and where it sits,
// and the Binarize level, over the levels the docs suggest trying.
func jitter(t *testing.T, img image.Image, box image.Rectangle, rotate int, n int, seed int64) {
	t.Helper()
	b := img.Bounds()
	rnd := rand.New(rand.NewSource(seed))
	levels := []int{0, 64, 96, 112, 120, 128, 136, 144, 160, 192}
	for i := 0; i < n; i++ {
		sc := 0.85 + 0.3*rnd.Float64()
		bw, bh := float64(box.Dx())*sc, float64(box.Dy())*sc
		cx := float64(box.Min.X+box.Max.X)/2 + (rnd.Float64()-0.5)*0.16*float64(box.Dx())
		cy := float64(box.Min.Y+box.Max.Y)/2 + (rnd.Float64()-0.5)*0.16*float64(box.Dy())
		r := image.Rect(int(cx-bw/2), int(cy-bh/2), int(cx+bw/2), int(cy+bh/2)).Intersect(b)
		p := config.Preprocess{Threshold: levels[rnd.Intn(len(levels))], Rotate: rotate}
		if _, err := readSafely(img, region(img, r), p); err != nil {
			t.Fatalf("box %v, binarize %d: %v", r, p.Threshold, err)
		}
	}
}

// Boxes and settings on the repo's own samples (the recipe photos and the
// demo's seven-segment frames) that the decoder used to panic on, found
// by drawing random boxes over them. A non-right-angle Rotate the search
// tried is left out: the preprocessing ignores it.
var samplePanics = []struct {
	file string
	reg  config.Region
	p    config.Preprocess
}{
	{"../../docs/recipes/samples/boiler-seven-segment-fault.jpg", config.Region{X: 0.3294, Y: 0.6062, W: 0.6583, H: 0.3071}, config.Preprocess{Threshold: 218}},
	{"../../docs/recipes/samples/boiler-seven-segment-temp.jpg", config.Region{X: 0.3429, Y: 0.5438, W: 0.5264, H: 0.3339}, config.Preprocess{Grayscale: true, Invert: true, Threshold: 43}},
	{"../../docs/recipes/samples/heat-pump-panel.jpg", config.Region{X: 0.1828, Y: 0.4535, W: 0.7760, H: 0.5337}, config.Preprocess{Grayscale: true, Threshold: 187, Upscale: 2}},
	{"../../docs/recipes/samples/washer-0-00.jpg", config.Region{X: 0.0063, Y: 0.0375, W: 0.9484, H: 0.2412}, config.Preprocess{Invert: true}},
	{"../../docs/recipes/samples/washer-0-00.jpg", config.Region{X: 0.0614, Y: 0.1191, W: 0.6426, H: 0.1445}, config.Preprocess{Grayscale: true, Invert: true, Threshold: 43}},
	{"../../docs/recipes/samples/washer-0-00.jpg", config.Region{X: 0.0131, Y: 0.4917, W: 0.1480, H: 0.3951}, config.Preprocess{Rotate: 90, Grayscale: true, Invert: true, Threshold: 67, Upscale: 2}},
	{"../../docs/recipes/samples/washer-1-23.jpg", config.Region{X: 0.0264, Y: 0.4392, W: 0.9391, H: 0.5178}, config.Preprocess{Grayscale: true, Threshold: 206}},
	{"../../docs/recipes/samples/washer-1-23.jpg", config.Region{X: 0.0910, Y: 0.0122, W: 0.8440, H: 0.2858}, config.Preprocess{Grayscale: true, Invert: true, Threshold: 48, Upscale: 2}},
	{"../../internal/demo/frames/sevenseg/frame_0.png", config.Region{X: 0.0501, Y: 0.3651, W: 0.5835, H: 0.2265}, config.Preprocess{Grayscale: true, Invert: true, Upscale: 2}},
	{"../../internal/demo/frames/sevenseg/frame_0.png", config.Region{X: 0.1780, Y: 0.4222, W: 0.4087, H: 0.1231}, config.Preprocess{Invert: true, Threshold: 187, Upscale: 2}},
	{"../../internal/demo/frames/sevenseg/frame_1.png", config.Region{X: 0.2194, Y: 0.0268, W: 0.3960, H: 0.6349}, config.Preprocess{Grayscale: true, Threshold: 58}},
}

func TestSevenSegRepoSamplesNeverPanic(t *testing.T) {
	imgs := map[string]image.Image{}
	for _, c := range samplePanics {
		img, ok := imgs[c.file]
		if !ok {
			f, err := os.Open(c.file)
			if err != nil {
				t.Fatal(err)
			}
			img, _, err = image.Decode(f)
			f.Close()
			if err != nil {
				t.Fatalf("%s: %v", c.file, err)
			}
			imgs[c.file] = img
		}
		if _, err := readSafely(img, c.reg, c.p); err != nil {
			t.Errorf("%s, box %+v, %+v: %v", c.file, c.reg, c.p, err)
		}
	}
}

func TestSevenSegJitteredBoxesNeverPanic(t *testing.T) {
	n := 400
	if testing.Short() {
		n = 100
	}
	t.Run("angled clock photo", func(t *testing.T) {
		t.Parallel()
		img := loadPhoto(t, "clock-philips-106.jpg")
		// The photo is the slow one (a big crop, read whole each time);
		// this many still fails within the first few dozen on the old
		// decoder.
		jitter(t, img, philipsBox, 0, n/4, 1)
	})
	t.Run("rendered fixture", func(t *testing.T) {
		t.Parallel()
		f, err := os.Open("testdata/sevenseg-23.5.png")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		jitter(t, img, b.Inset(b.Dy()/10), 0, n/4, 2)
	})
	// Italic displays, some leaning past what the decoder corrects, in
	// both polarities.
	for i, lean := range []float64{0.2, 0.45, -0.45, 0.8} {
		t.Run(fmt.Sprintf("rendered lean %g", lean), func(t *testing.T) {
			t.Parallel()
			o := clean
			o.H, o.Stroke, o.Dark, o.Noise, o.Seed = 60, 8, i%2 == 1, 6, int64(i)
			bg := color.RGBA{0, 0, 0, 255}
			if o.Dark {
				bg = color.RGBA{255, 255, 255, 255}
			}
			src := shearImage(render("12:38", o), lean, bg)
			img := image.NewRGBA(src.Bounds().Inset(-20))
			draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
			draw.Draw(img, src.Bounds(), src, image.Point{}, draw.Src)
			jitter(t, img, src.Bounds(), 0, n/4, int64(10+i))
		})
	}
}
