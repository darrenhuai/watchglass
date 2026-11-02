package ocr

import (
	"image"
	"image/color"
	"math"
	"math/rand"
	"runtime"
	"testing"
)

// Reflective LCDs and small digits, where round 6's photos found the
// decoder weakest: smaller tenths after the point, a light gradient or a
// shadow across the glass, faint marks and ghost segments, the window's
// rim beside the last digit, and digits a few pixels tall. The photos are
// crops in testdata/photos (see the README there).

var (
	seikoTempBox = photoBox{file: "clock-seiko-wall-temp.jpg", box: image.Rect(16, 16, 240, 144)}
	tfaTempBox   = photoBox{file: "clock-tfa-lcd-temp.jpg", box: image.Rect(16, 16, 140, 104)}
	hamaBox      = photoBox{file: "therm-hama-223.jpg", box: image.Rect(16, 16, 138, 90)}
	atomixBox    = photoBox{file: "clock-atomix.jpg", box: image.Rect(8, 8, 482, 315)}
	kettleBox    = photoBox{file: "meter-kettle-120.jpg", box: image.Rect(16, 16, 38, 28)}
	aclaraRimBox = photoBox{file: "meter-aclara-rim.jpg", box: image.Rect(16, 16, 208, 74)}
	cyanBox      = photoBox{file: "clock-cyan-2339.jpg", box: image.Rect(16, 16, 579, 252)}
	mwBox        = photoBox{file: "oven-mw-2400.jpg", box: image.Rect(16, 16, 136, 63)}
	ledClockBox  = photoBox{file: "clock-led-154516.jpg", box: image.Rect(16, 16, 964, 204)}
)

func TestSevenSegLCDPhotos(t *testing.T) {
	for _, c := range []struct {
		name string
		box  photoBox
		want string
	}{
		// A °C sign over the smaller tenths joined them into one blob.
		{"wall clock's thermometer 28.8", seikoTempBox, "28.8"},
		{"alarm clock's thermometer 22.0", tfaTempBox, "22.0"},
		// A faint mark beside the first 2 lit its upper-left bar.
		{"thermometer 22.3", hamaBox, "22.3"},
		// A lamp lights the right half of the glass: at the level that
		// read the 8 the ground there was lit too.
		{"clock in uneven light 852", atomixBox, "852"},
		// The digits are 10 px tall.
		{"kettle meter 120", kettleBox, "120"},
		// The glass's rim stands a few pixels after the last 0, nearer it
		// than half the gap between digits: read as a "1" that doesn't
		// stand, it ended the reading in '?'. The same crop as
		// meter-aclara.jpg, saved at a higher JPEG quality: at 90 the
		// 0 itself reads unsure.
		{"meter 74590", aclaraRimBox, "74590"},
	} {
		if got := c.box.read(t); got != c.want {
			t.Errorf("%s: read %q, want %q", c.name, got, c.want)
		}
	}
}

// lightField multiplies every pixel by f(x, y), x and y running 0..1
// across the image: a lamp's falloff or a shadow, on ink and ground alike.
func lightField(src *image.RGBA, f func(x, y float64) float64) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			k := f(float64(x)/float64(b.Dx()-1), float64(y)/float64(b.Dy()-1))
			c := src.RGBAAt(b.Min.X+x, b.Min.Y+y)
			out.SetRGBA(b.Min.X+x, b.Min.Y+y, color.RGBA{clamp8(float64(c.R) * k), clamp8(float64(c.G) * k), clamp8(float64(c.B) * k), 255})
		}
	}
	return out
}

var lightTexts = []string{"23.5", "1234", "852", "907.6", "4.18", "12:47", "0.39", "7215", "68.1", "560"}

// A gradient or a shadow across a reflective LCD: one threshold for the
// whole crop lit the dim end's ground or lost its digits (852 read 5?).
func TestSevenSegUnevenLight(t *testing.T) {
	for _, c := range []struct {
		name string
		f    func(x, y float64) float64
		skip map[string]bool // still misread, as a '?' (see below)
	}{
		{"lit from the right, the left end at 25%", func(x, y float64) float64 { return 0.25 + 0.75*x }, nil},
		{"lit from the left, the right end at 35%", func(x, y float64) float64 { return 1 - 0.65*x }, nil},
		{"a shadow over the right 40% at 35%", func(x, y float64) float64 {
			if x > 0.6 {
				return 0.35
			}
			return 1
		}, map[string]bool{"907.6": true}},
	} {
		for seed := int64(0); seed < 2; seed++ {
			for _, want := range lightTexts {
				img := lightField(render(want, renderOpts{H: 60, Stroke: 8, Gap: 1, Dark: true, Noise: 6, Blur: 1, Seed: seed}), c.f)
				got, words := decode(t, img)
				if c.skip[want] {
					if got != want && confident(words) {
						t.Errorf("%s, seed %d: read a confident %q for %s (%+v)", c.name, seed, got, want, words)
					}
					continue
				}
				if got != want {
					t.Errorf("%s, seed %d: read %q, want %s (%+v)", c.name, seed, got, want, words)
				}
			}
		}
	}
}

// A shadow in one corner of the glass, darkest at the bottom right, at a
// fifth of the light elsewhere: one threshold for the crop read none of
// these right.
func TestSevenSegDarkCorner(t *testing.T) {
	corner := func(x, y float64) float64 {
		d := math.Hypot(1-x, 1-y) / math.Sqrt2
		return 0.2 + 0.8*math.Min(1, 1.6*d)
	}
	right := 0
	for _, want := range lightTexts {
		img := lightField(render(want, renderOpts{H: 60, Stroke: 8, Gap: 1, Dark: true, Noise: 6, Blur: 1, Seed: 0}), corner)
		got, words := decode(t, img)
		if got == want {
			right++
		} else if confident(words) {
			t.Errorf("dark corner: read a confident %q for %s (%+v)", got, want, words)
		}
	}
	if right < 8 {
		t.Errorf("dark corner: %d of %d read right, want 8 or more", right, len(lightTexts))
	}
}

// Tenths drawn at 0.55 to 0.65 of the whole degrees' height, on their
// baseline after the point, were taken for a fragment and left out: 20.1
// read a confident 20., 25.4 a confident 25. A smaller digit after the
// point is a digit. On a smaller, noisier render the small digit comes
// apart: a 4 into its upper left bar, its middle bar (point-sized) and its
// right-hand bars, which alone read a confident 25.1; a 1 into two dots
// that looked like a colon, dropped (20.1 read 20.). A clock's smaller
// seconds after the minutes were dropped the same way: 12:34 with seconds
// 45 read a confident 12:35, 8:30 with 45 read 8:305.
func TestSevenSegSmallTenthsAfterThePoint(t *testing.T) {
	cases := [][2]string{{"21.", "5"}, {"18.", "0"}, {"7.", "9"}, {"23.", "6"}, {"-4.", "2"}, {"36.", "8"}, {"20.", "1"}, {"25.", "4"}, {"8.", "3"}, {"19.", "7"},
		{"12:3", "45"}, {"8:30", "45"}, {"11.", "1"}, {"1.", "7"}, {"37.", "2"}}
	renders := []struct {
		name string
		o    renderOpts
		pct  int // the share that reads right, at least; the rest read '?'
	}{
		{"clean", clean, 90},
		{"60 px, noisy and soft", renderOpts{H: 60, Stroke: 8, Gap: 1, Noise: 6, Blur: 1, Seed: 3}, 90},
		// A small digit's bars are two or three pixels here.
		{"40 px, noisy", renderOpts{H: 40, Stroke: 6, Gap: 1, Noise: 4, Seed: 5}, 60},
	}
	for ri, r := range renders {
		right, n := 0, 0
		for _, dark := range []bool{false, true} {
			o := r.o
			o.Dark = dark
			for _, share := range []float64{0.55, 0.6, 0.65, 0.7, 0.75} {
				for i, c := range cases {
					want := c[0] + c[1]
					got, words := decode(t, smallTail(c[0], c[1], share, o))
					n++
					if got == want {
						right++
					}
					// On the clean render the first eight and both clocks
					// read at 0.6 and 0.65; every one, at every share,
					// reads right or with a '?', never a wrong number.
					must := ri == 0 && share >= 0.6 && share <= 0.65 && (i < 8 || i == 10 || i == 11)
					if (must && got != want) || (got != want && confident(words)) {
						t.Errorf("%s, dark %v, %s with the tail at %.2f: read %q (%+v)", r.name, dark, want, share, got, words)
					}
				}
			}
		}
		if 100*right < r.pct*n {
			t.Errorf("%s: %d of %d read right, want at least %d%%", r.name, right, n, r.pct)
		}
	}
}

// Ghost segments. On many displays the unlit segments show faintly, and
// the second reading with the light evened out (sevenseg_light.go) lifted
// them on a light-on-dark display, where a lit segment and an unlit one
// both stand far over the ground's mean: an LED showing 23.5 with its
// unlit segments at 0.4 of the lit ones read a confident 88.8, 12:47 read
// 88:88. Every ghost read here comes back right or with a '?', and the
// three real ghost-segment photos read right.
func TestSevenSegGhostSegments(t *testing.T) {
	texts := []string{"23.5", "1234", "852", "907.6", "4.18", "12:47", "0.39", "7215", "68.1", "560", "1111", "7.1", "10.0", "88", "-1.5", "20:00", "17", "41"}
	right, n := 0, 0
	for _, dark := range []bool{true, false} {
		for _, h := range []int{30, 60, 100} {
			for _, ghost := range []float64{0.2, 0.3} {
				for _, want := range texts {
					o := renderOpts{H: h, Stroke: max(3, h*2/15), Gap: 1, Dark: dark, Ghost: ghost, Noise: 5, Blur: 1, Seed: int64(h)}
					got, words := decode(t, render(want, o))
					n++
					if got == want {
						right++
					} else if confident(words) {
						t.Errorf("dark %v, %d px, unlit segments at %.1f: read a confident %q for %s", dark, h, ghost, got, want)
					}
				}
			}
		}
	}
	if right < n*9/10 {
		t.Errorf("ghost segments: %d of %d read right, want at least 90%%", right, n)
	}
	// The light-on-dark ones that read 88s, at the levels they did.
	for _, c := range []struct {
		want  string
		o     renderOpts
		ghost float64
	}{
		{"23.5", renderOpts{H: 60, Stroke: 8, Gap: 1, Noise: 5, Blur: 1, Seed: 60}, 0.4},
		{"12:47", renderOpts{H: 30, Stroke: 4, Gap: 1, Noise: 5, Blur: 1, Seed: 30}, 0.3},
		{"7215", renderOpts{H: 30, Stroke: 4, Gap: 1, Noise: 5, Blur: 1, Seed: 30}, 0.4},
		{"0.39", renderOpts{H: 60, Stroke: 8, Gap: 1, Noise: 5, Blur: 1, Seed: 60}, 0.4},
	} {
		o := c.o
		o.Ghost = c.ghost
		if got, words := decode(t, render(c.want, o)); got != c.want && confident(words) {
			t.Errorf("LED, unlit segments at %.1f: read a confident %q for %s", c.ghost, got, c.want)
		}
	}
	for _, c := range []struct {
		name string
		box  photoBox
		want string
	}{
		{"clock in uneven light 852", atomixBox, "852"},
		// A VFD clock, its unlit segments a faint blue.
		{"VFD clock 23:39", cyanBox, "23:39"},
		// A microwave's clock, its unlit segments showing, and a fleck
		// of glare past it.
		{"microwave 24:00", mwBox, "24:00"},
	} {
		if got := c.box.read(t); got != c.want {
			t.Errorf("%s: read %q, want %q", c.name, got, c.want)
		}
	}
}

// A box that cuts a third off the leading 1 of a clock: the rest of the 1
// ran into the 5 after it and read '?' as the crop was, and the reading
// with the light evened out lost it and read a confident 5:45:16.
func TestSevenSegCutLeadingDigitEvenedOut(t *testing.T) {
	if got := ledClockBox.read(t); got != "15:45:16" {
		t.Errorf("as drawn: read %q, want 15:45:16", got)
	}
	cut := ledClockBox
	cut.box = cut.box.Add(image.Pt(cut.box.Dx()*4/100, 0))
	if got := cut.read(t); got != "?:45:16" {
		t.Errorf("box 4%% to the right: read %q, want ?:45:16", got)
	}
}

// flatten sums the crop's pixels: on a 32-bit build (an ARM board) the
// sums overflowed an int, and a uniform 1600x800 crop flattened to
// anything from 0 to 255. Every pixel of a uniform crop is at its
// window's mean, 128.
func TestSevenSegFlattenLargeCrop(t *testing.T) {
	g := grayImg{w: 1600, h: 800, pix: make([]uint8, 1600*800)}
	for i := range g.pix {
		g.pix[i] = 200
	}
	f := flatten(g)
	for i, v := range f.pix {
		if v != 128 {
			t.Fatalf("pixel %d,%d flattened to %d, want 128", i%f.w, i/f.w, v)
		}
	}
}

// A loose box round small digits: the decoder reads the digits again
// enlarged, and enlarged whole a 1280x720 box with 14 px digits took 2 s
// and 240 MB for one reading (30 MB before). Past smallMaxPixels only the
// digits' band is enlarged.
func TestSevenSegSmallDigitsInABigBox(t *testing.T) {
	d := shrinkTo(render("23.5", renderOpts{H: 200, Stroke: 24, Gap: 4, Dark: true, Seed: 1}), 200/14.0)
	big := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	bg := d.RGBAAt(0, 0)
	for i := 0; i < len(big.Pix); i += 4 {
		big.Pix[i], big.Pix[i+1], big.Pix[i+2], big.Pix[i+3] = bg.R, bg.G, bg.B, 255
	}
	at := image.Pt(640-d.Bounds().Dx()/2, 360-d.Bounds().Dy()/2)
	for y := 0; y < d.Bounds().Dy(); y++ {
		for x := 0; x < d.Bounds().Dx(); x++ {
			big.SetRGBA(at.X+x, at.Y+y, d.RGBAAt(x, y))
		}
	}
	big = withNoise(big, 4, 9)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, _ := decode(t, big)
	runtime.ReadMemStats(&after)
	if got != "23.5" {
		t.Errorf("read %q, want 23.5", got)
	}
	if mb := (after.TotalAlloc - before.TotalAlloc) >> 20; mb > 120 {
		t.Errorf("one reading allocated %d MB, want 120 or less", mb)
	}
}

// shrinkTo scales src down by k, averaging the pixels each new pixel
// covers, as a camera's sensor does with a display far from it.
func shrinkTo(src *image.RGBA, k float64) *image.RGBA {
	b := src.Bounds()
	w, h := int(float64(b.Dx())/k), int(float64(b.Dy())/k)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, n float64
			for yy := int(float64(y) * k); yy < int(float64(y+1)*k) && yy < b.Dy(); yy++ {
				for xx := int(float64(x) * k); xx < int(float64(x+1)*k) && xx < b.Dx(); xx++ {
					c := src.RGBAAt(xx, yy)
					r, g, bl, n = r+float64(c.R), g+float64(c.G), bl+float64(c.B), n+1
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255})
		}
	}
	return out
}

// withNoise adds gaussian noise of sigma to every pixel.
func withNoise(img *image.RGBA, sigma float64, seed int64) *image.RGBA {
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < len(img.Pix); i += 4 {
		n := rng.NormFloat64() * sigma
		for c := 0; c < 3; c++ {
			img.Pix[i+c] = clamp8(float64(img.Pix[i+c]) + n)
		}
	}
	return img
}

// padded grows img by p px of its corner's colour on every side: a box
// drawn loosely round small digits.
func padded(img *image.RGBA, p int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()+2*p, b.Dy()+2*p))
	bg := img.RGBAAt(0, 0)
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			out.SetRGBA(x, y, bg)
		}
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.SetRGBA(x+p, y+p, img.RGBAAt(x, y))
		}
	}
	return out
}

// Digits 10 to 14 px tall, shrunk from a large render the way a camera
// shrinks them: read as they were, most came back a '?' or a confident
// wrong number (12 px: 43 right and 77 confident wrongs in 240 on S2's
// probe). The decoder enlarges them itself.
func TestSevenSegSmallDigits(t *testing.T) {
	right, n := 0, 0
	for _, h := range []int{10, 12, 14} {
		for _, dark := range []bool{true, false} {
			for _, want := range []string{"23.5", "1234", "-8.0", "907.6", "56", "12:47", "7215", "68.1"} {
				o := renderOpts{H: 200, Stroke: 24, Gap: 4, Dark: dark, Seed: int64(h)}
				img := withNoise(shrinkTo(render(want, o), 200/float64(h)), 6, int64(h))
				for _, crop := range []*image.RGBA{img, padded(img, h)} {
					got, words := decode(t, crop)
					n++
					if got == want {
						right++
					} else if confident(words) {
						t.Errorf("%s at %d px, dark %v, %v box: read a confident %q", want, h, dark, crop.Bounds().Size(), got)
					}
				}
			}
		}
	}
	if right < n*9/10 {
		t.Errorf("small digits: %d of %d read right, want at least 90%%", right, n)
	}
}

// A clock's smaller seconds end in a 1 as often as in anything else, and
// that 1, two bars on the baseline, looked like a colon with nothing after
// it and was dropped: 12:34 41 read a confident 12:344 and 1:05 41 read
// 1:054. Noise breaks a small 1 into two marks, or a small 0 into its two
// sides, the same way (1:0 41 read 1:04, 72.0 read 72.11). Every tail
// here reads right or with a '?', never a shorter or a different number,
// and on the clean render the tails read whole.
func TestSevenSegSmallSecondsEndingInOne(t *testing.T) {
	cases := [][2]string{{"12:34", "41"}, {"1:05", "41"}, {"12:34", "11"}, {"12:05", "21"}, {"12:34", "01"}, {"12:34", "10"}, {"8:30", "11"},
		{"1:0", "41"}, {"12:5", "01"}, {"15:2", "07"}, {"72.", "0"}, {"36.", "8"}, {"23.", "1"}, {"-4.", "1"}}
	renders := []struct {
		name string
		o    renderOpts
		pct  int // the share that reads right, at least; the rest read '?'
	}{
		{"clean", clean, 90},
		{"60 px, noisy and soft", renderOpts{H: 60, Stroke: 8, Gap: 1, Noise: 6, Blur: 1, Seed: 3}, 90},
		{"50 px, noisy", renderOpts{H: 50, Stroke: 7, Gap: 1, Noise: 3, Seed: 21}, 65},
		{"80 px, noisy and soft", renderOpts{H: 80, Stroke: 11, Gap: 2, Noise: 8, Blur: 1, Seed: 22}, 85},
	}
	for ri, r := range renders {
		right, n := 0, 0
		for _, dark := range []bool{false, true} {
			o := r.o
			o.Dark = dark
			for _, share := range []float64{0.55, 0.6, 0.65, 0.7, 0.75} {
				for _, c := range cases {
					want := c[0] + c[1]
					got, words := decode(t, smallTail(c[0], c[1], share, o))
					n++
					if got == want {
						right++
					}
					must := ri == 0 && share >= 0.6 && share <= 0.65
					if (must && got != want) || (got != want && confident(words)) {
						t.Errorf("%s, dark %v, %s with the tail at %.2f: read %q (%+v)", r.name, dark, want, share, got, words)
					}
				}
			}
		}
		t.Logf("%s: %d of %d read right", r.name, right, n)
		if 100*right < r.pct*n {
			t.Errorf("%s: %d of %d read right, want at least %d%%", r.name, right, n, r.pct)
		}
	}
}
