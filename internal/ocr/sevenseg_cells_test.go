package ocr

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
)

// The decoder finding every digit cell, on round 6's photos where it
// didn't: a frame edge read as a lone "1", a colon whose dots don't stand
// one above the other, a "1" right after a colon, a gap that dropped the
// digits after it, ink on the edge of the box. The photos are crops in
// testdata/photos (see the README there for their sources and licences);
// the two that aren't free to ship (an oven clock, a Landis meter) are
// drawn here with the same fault.

// photoBox is a box on one of the fixtures, in its own pixels, as round 6
// drew it on the full photo.
type photoBox struct {
	file string
	box  image.Rectangle
	pp   config.Preprocess
}

var (
	vwBox      = photoBox{file: "odo-vw.jpg", box: image.Rect(16, 16, 361, 112)}
	seikoBox   = photoBox{file: "clock-seiko-wall.jpg", box: image.Rect(16, 16, 438, 297)}
	pumpBox    = photoBox{file: "pump-us-834.jpg", box: image.Rect(16, 16, 240, 136)}
	scaleBox   = photoBox{file: "scale-kitchen-314.jpg", box: image.Rect(20, 22, 225, 131)}
	scaleTight = photoBox{file: "scale-kitchen-314.jpg", box: image.Rect(32, 29, 213, 125)}
	scaleWide  = photoBox{file: "scale-kitchen-314.jpg", box: image.Rect(16, 16, 245, 138)}
	wbBox      = photoBox{file: "meter-wb-0625.jpg", box: image.Rect(16, 16, 176, 108)}
	aclaraBox  = photoBox{file: "meter-aclara.jpg", box: image.Rect(16, 16, 208, 74)}
	unitBox    = photoBox{file: "meter-unit-496.jpg", box: image.Rect(16, 16, 228, 145)}
)

func (p photoBox) with(pp config.Preprocess) photoBox { p.pp = pp; return p }

// grown is p with its box d px bigger on every side (smaller for d < 0).
func (p photoBox) grown(d int) photoBox { p.box = p.box.Inset(-d); return p }

// read decodes the box the way a watch does (crop, preprocess, decoder).
func (p photoBox) read(t *testing.T) string {
	t.Helper()
	img := loadPhoto(t, p.file)
	text, err := readSafely(img, region(img, p.box), p.pp)
	if err != nil {
		t.Fatalf("%s: %v", p.file, err)
	}
	return text
}

// frameEdgeCrop is the Landis meter's fault drawn: a dark-on-light LCD
// whose digits the box cuts off halfway down, and the bright edge of the
// window frame standing the whole height of the box on its right, edge px
// in from it.
func frameEdgeCrop(ground uint8, edge int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 149, 53))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{ground, ground + 10, ground - 10, 255}), image.Point{}, draw.Src)
	lcd := render("0004", renderOpts{H: 60, Stroke: 5, Gap: 2, Seed: 1, Dark: true})
	for y := 0; y < 53; y++ {
		for x := 0; x < 120 && x < lcd.Bounds().Dx(); x++ {
			if c := lcd.RGBAAt(x, y+20); c.R < 100 {
				img.SetRGBA(x, y, c)
			}
		}
	}
	x1 := 149 - edge
	draw.Draw(img, image.Rect(x1-8, 0, x1, 53), image.NewUniform(color.RGBA{250, 250, 250, 255}), image.Point{}, draw.Src)
	return img
}

// A frame edge, a reflection or a speck is a bar standing alone; read as a
// "1" at 100% it gave a watch a number the display never showed (Landis:
// "1" for 000004; Aclara at Binarize 160 the same).
func TestSevenSegFrameEdgeIsNotAOne(t *testing.T) {
	for _, ground := range []uint8{110, 150} {
		for _, edge := range []int{0, 1, 2} {
			got, words := decode(t, frameEdgeCrop(ground, edge))
			if got == "1" || (got != "" && !strings.Contains(got, "?") && confident(words)) {
				t.Errorf("ground %d, frame %d px from the edge: read %q, want a '?' (%+v)", ground, edge, got, words)
			}
		}
	}
	if got := aclaraBox.with(config.Preprocess{Threshold: 160}).read(t); got == "1" {
		t.Errorf("Aclara meter at Binarize 160: read %q, the frame edge as a 1", got)
	}
	// A "1" alone and clear of the box is still a 1.
	if got, words := decode(t, render("1", clean)); got != "1" || !confident(words) {
		t.Errorf("a lone 1 clear of the box: read %q (%+v)", got, words)
	}
}

// leaningColon draws text with its colon's dots at ColonAt and the upper
// one shift px right of the lower, as a slanted clock's colon leans.
func leaningColon(text string, o renderOpts, shift int) *image.RGBA {
	img := render(text, o)
	margin := max(2, o.H/5)
	W := o.cellW()
	spacing := int(math.Round(float64(W) * 0.4))
	x := margin + (W+spacing)*strings.IndexByte(text, ':')
	cy := margin + int(math.Round(o.ColonAt[0]*float64(o.H)))
	r := image.Rect(x, cy-o.Dot/2, x+o.Dot, cy-o.Dot/2+o.Dot)
	fg := img.RGBAAt(x+o.Dot/2, cy)
	draw.Draw(img, r, image.NewUniform(img.RGBAAt(0, 0)), image.Point{}, draw.Src)
	draw.Draw(img, r.Add(image.Pt(shift, 0)), image.NewUniform(fg), image.Point{}, draw.Src)
	return img
}

// An oven clock sets its colon's dots level with the top and bottom bars,
// and slanted: the upper dot stands right of the lower one. The colon was
// dropped (939, 9.39 at 100) for 9:39.
func TestSevenSegLeaningColon(t *testing.T) {
	for _, at := range [][2]float64{{0.1, 0.9}, {0.15, 0.85}, {0.3, 0.7}} {
		for _, shift := range []int{0, 10, 16} {
			if at[0] == 0.3 && shift > 10 {
				continue // leaning further than any italic font
			}
			o := renderOpts{H: 100, Stroke: 12, Gap: 2, Seed: 1, Dot: 14, ColonAt: at}
			if got, words := decode(t, leaningColon("9:39", o, shift)); got != "9:39" {
				t.Errorf("dots at %v, upper %d px right: read %q, want 9:39 (%+v)", at, shift, got, words)
			}
		}
	}
	// A meter's small colon, its dots offset the same way, read 06?5.
	if got := wbBox.read(t); got != "06:25" {
		t.Errorf("meter showing 06:25: read %q", got)
	}
}

// A "1" laid out in a full-width cell took in the colon to its left, whose
// dots the probes read as its top and middle bars: 1:16 read 1:?6.
func TestSevenSegOneBesideAColon(t *testing.T) {
	if got := seikoBox.read(t); got != "1:16" {
		t.Errorf("wall clock showing 1:16: read %q", got)
	}
}

// The odometer's last two digits stand apart from the rest and are paler
// (the lamp is on the other side): the threshold that read 1056 left only
// pieces of them, and the watch got a confident 1056 for 105616. Now the
// pieces past the end are read again; a cell that still can't be read is a
// '?', never a shorter number.
func TestSevenSegGapKeepsTheDigitsAfterIt(t *testing.T) {
	got := vwBox.read(t)
	cells := len(strings.Map(func(r rune) rune {
		if r == '.' || r == ':' {
			return -1
		}
		return r
	}, got))
	if got != "105616" && (cells < 6 || !strings.Contains(got, "?")) {
		t.Errorf("odometer showing 105616: read %q, want 105616 or six cells with a '?' for one it can't read", got)
	}
	if strings.HasPrefix(got, "1056") && !strings.Contains(got, "?") && got != "105616" {
		t.Errorf("odometer showing 105616: read a confident %q", got)
	}
}

// Ink on the edge of the box: a strip of the pump's housing along the top
// and down the right side folded every digit into one ('?'), and a scale's
// "g" over its last digit lit that digit's top bar (314 read 319 at 100).
func TestSevenSegEdgeInk(t *testing.T) {
	for _, thr := range []int{0, 96, 128, 160, 192} {
		if got := pumpBox.with(config.Preprocess{Threshold: thr}).read(t); got != "8.34" {
			t.Errorf("pump showing 8.34, Binarize %d: read %q", thr, got)
		}
	}
	if got := scaleTight.read(t); got != "314" {
		t.Errorf("scale showing 314, tight box: read %q", got)
	}
	if got := scaleTight.with(config.Preprocess{Threshold: 128}).read(t); got != "314" && !strings.Contains(got, "?") {
		t.Errorf("scale showing 314, tight box, Binarize 128: read a confident %q", got)
	}
	if got := unitBox.read(t); got != "4.96" {
		t.Errorf("meter showing 4.96: read %q", got)
	}
}

// Every fixture's reading, pinned: a change to the decoder that moves any
// of them shows up here, to be looked at and the number updated on purpose.
func TestSevenSegPhotoFixturesPinned(t *testing.T) {
	for _, c := range []struct {
		name string
		box  photoBox
		want string
	}{
		{"odometer 105616", vwBox, "10561?"},
		{"wall clock 1:16", seikoBox, "1:16"},
		{"pump 8.34", pumpBox, "8.34"},
		{"pump 8.34, Binarize 64", pumpBox.with(config.Preprocess{Threshold: 64}), "?"},
		{"scale 314", scaleBox, "314"},
		{"scale 314, tight box", scaleTight, "314"},
		{"scale 314, tight box, Binarize 128", scaleTight.with(config.Preprocess{Threshold: 128}), "31?"},
		{"scale 314, box grown and moved right", scaleWide, "31?"},
		{"meter 06:25", wbBox, "06:25"},
		{"meter 74590", aclaraBox, "7459?"},
		{"meter 74590, Binarize 160", aclaraBox.with(config.Preprocess{Threshold: 160}), "?"},
		{"meter 4.96", unitBox, "4.96"},
		{"angled clock 1:06", photoBox{file: "clock-philips-106.jpg", box: philipsBox}, "?"},
		// S2's reflective LCDs (sevenseg_lcd_test.go reads them as drawn).
		{"thermometer 28.8, tight box", seikoTempBox.grown(-6), "28.8"},
		{"thermometer 28.8, loose box", seikoTempBox.grown(12), "28.8"},
		{"thermometer 28.8, Binarize 128", seikoTempBox.with(config.Preprocess{Threshold: 128}), "?"},
		{"thermometer 22.0, Binarize 128", tfaTempBox.with(config.Preprocess{Threshold: 128}), "22.0"},
		{"thermometer 22.3, Binarize 128", hamaBox.with(config.Preprocess{Threshold: 128}), "?2.3"},
		{"thermometer 22.3, loose box", hamaBox.grown(6), "22.?"},
		{"clock 852, tight box", atomixBox.grown(-15), "852"},
		{"clock 852, loose box", atomixBox.grown(30), "?"},
		{"kettle meter 120, tight box", kettleBox.grown(-2), "120"},
		{"kettle meter 120, loose box", kettleBox.grown(4), "?"},
		{"meter 74590 at q95, tight box", aclaraRimBox.grown(-2), "7459?."},
	} {
		if got := c.box.read(t); got != c.want {
			t.Errorf("%s: read %q, pinned %q", c.name, got, c.want)
		}
	}
}

// boxAround crops img to its ink plus l, t, r, b pixels on each side (a
// negative number cuts into the ink), as a box drawn round the digits.
func boxAround(img *image.RGBA, l, t, r, b int) *image.RGBA {
	ink := inkBounds(img, img.RGBAAt(0, 0))
	box := image.Rect(ink.Min.X-l, ink.Min.Y-t, ink.Max.X+r, ink.Max.Y+b)
	out := image.NewRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	draw.Draw(out, out.Bounds(), image.NewUniform(img.RGBAAt(0, 0)), image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), img, box.Min, draw.Src)
	return out
}

// smallTenths draws a thermometer's reading: the whole degrees full size,
// then the tenths at share of their height, standing on the same baseline.
func smallTenths(whole, tenths string, share float64, dark bool) *image.RGBA {
	o := clean
	o.Dark = dark
	return smallTail(whole, tenths, share, o)
}

// smallTail is whole rendered with o, and tail after it share as tall, on
// the same baseline: a thermometer's tenths, a clock's seconds.
func smallTail(whole, tail string, share float64, o renderOpts) *image.RGBA {
	tenths := tail
	a := render(whole, o)
	so := o
	so.H = int(float64(o.H) * share)
	so.Stroke = max(2, int(float64(o.Stroke)*share))
	b := render(tenths, so)
	am, bm := max(2, o.H/5), max(2, so.H/5)
	img := image.NewRGBA(image.Rect(0, 0, a.Bounds().Dx()+b.Bounds().Dx()-bm, a.Bounds().Dy()))
	draw.Draw(img, img.Bounds(), image.NewUniform(a.RGBAAt(0, 0)), image.Point{}, draw.Src)
	draw.Draw(img, a.Bounds(), a, image.Point{}, draw.Src)
	at := image.Pt(a.Bounds().Dx()-bm, am+o.H-(bm+so.H))
	draw.Draw(img, b.Bounds().Add(at), b, image.Point{}, draw.Src)
	return img
}

// Smaller tenths after full-size degrees are the display's own, not a
// full-size digit with someone else's ink: the scale's rule for a unit
// sign over a digit read 21.5 as ?1.5 and 8.5 as ?.5.
func TestSevenSegSmallTenths(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, c := range [][2]string{{"21.", "5"}, {"18.", "5"}, {"8.", "5"}, {"7.", "5"}} {
			for _, share := range []float64{0.65, 0.75} {
				img := smallTenths(c[0], c[1], share, dark)
				want := c[0] + c[1]
				if got, words := decode(t, img); got != want {
					t.Errorf("dark %v, %s with tenths at %.2f of the height: read %q, want %s (%+v)", dark, want, share, got, want, words)
				}
				// With a box a little round it: ?8.5 for 18.5 is the "1"
				// on the box's left edge, an honest '?'.
				m := int(0.05 * float64(img.Bounds().Dy()))
				got, words := decode(t, boxAround(img, m, m, m, m))
				if got != want && (!strings.Contains(got, "?") || strings.HasPrefix(want, "8") || strings.HasPrefix(want, "7")) {
					t.Errorf("dark %v, %s with tenths at %.2f, 5%% box: read %q (%+v)", dark, want, share, got, words)
				}
			}
		}
	}
}

// A lone "1" in a box drawn a few pixels clear of it top and bottom is a
// 1. The rule that a bar running the box's height is the frame read it ?
// there, and the ground round it, read the other way round, won as a
// confident 8. A "-1" the same, and an LCD's lone 1 with more room.
func TestSevenSegLoneOneTightBox(t *testing.T) {
	dark := clean
	dark.Dark = true
	small := renderOpts{H: 40, Stroke: 6, Gap: 1, Seed: 1}
	for _, o := range []struct {
		name string
		o    renderOpts
	}{{"LED", clean}, {"LCD", dark}, {"small LED", small}} {
		for _, text := range []string{"1", "-1"} {
			for _, v := range []int{2, 3, 4} {
				for _, s := range []int{15, 30} {
					got, words := decode(t, boxAround(render(text, o.o), s, v, s, v))
					if got != text {
						t.Errorf("%s %q, box %d px above and below, %d px aside: read %q (%+v)", o.name, text, v, s, got, words)
					}
				}
			}
		}
	}
	for _, o := range []renderOpts{clean, dark, {H: 60, Stroke: 9, Gap: 0, Seed: 4, Blur: 1}, {H: 60, Stroke: 9, Gap: 0, Seed: 4, Blur: 1, Dark: true}} {
		img := render("1", o)
		for _, m := range []float64{0.1, 0.15, 0.2} {
			p := int(m * float64(o.H))
			if got, words := decode(t, boxAround(img, p, p, p, p)); got != "1" {
				t.Errorf("H %d dark %v, lone 1 with %.0f%% room: read %q (%+v)", o.H, o.Dark, 100*m, got, words)
			}
		}
	}
}

// The ground round the digits, read the wrong way round, is one piece
// from edge to edge of the box; every probe finds a bar lit there, and it
// read a confident 8. Never an 8 where there is none.
func TestSevenSegGroundIsNotAnEight(t *testing.T) {
	for _, o := range []renderOpts{clean, {H: 100, Stroke: 12, Gap: 2, Seed: 1, Dark: true}, {H: 40, Stroke: 6, Gap: 1, Seed: 3, Noise: 4}, {H: 60, Stroke: 9, Gap: 0, Seed: 4, Blur: 1, Dark: true}} {
		for _, text := range []string{"1", "10"} {
			for _, m := range []float64{0.03, 0.1, 0.15} {
				p := max(1, int(m*float64(o.H)))
				got, words := decode(t, boxAround(render(text, o), p, p, p, p))
				if strings.Contains(got, "8") {
					t.Errorf("H %d dark %v, %q with %.0f%% room: read %q (%+v)", o.H, o.Dark, text, 100*m, got, words)
				}
			}
		}
	}
}

// What the box leaves of a digit it cuts through is a '?', never a
// shorter number: 1234 with the box cut into the 4 read a confident 123;
// a calculator's 9.8696044 with the 9 cut, leaving its point, read
// 8696044; a clock's 12:21 whose last digit was too pale read 12:2.
func TestSevenSegCutDigitAtTheEdge(t *testing.T) {
	for _, cut := range []int{20, 30, 40} {
		got, words := decode(t, boxAround(render("1234", clean), 10, 10, -cut, 10))
		if got != "123?" {
			t.Errorf("1234, the box %d px into the 4: read %q, want 123? (%+v)", cut, got, words)
		}
	}
	// Of the 9 only a sliver is left, and its point.
	W := clean.cellW()
	for _, left := range []int{2, 10} {
		if got, words := decode(t, boxAround(render("9.86", clean), -(W-left), 10, 10, 10)); !strings.HasPrefix(got, "?") || !strings.HasSuffix(got, "86") {
			t.Errorf("9.86, the box cut through the 9 leaving %d px of it: read %q, want ?86 or ?.86 (%+v)", left, got, words)
		}
	}
	// The last digit of 12:21 not there at all.
	pale := clean
	pale.DimIndex, pale.DimLevel = 4, 0.02
	if got, words := decode(t, render("12:21", pale)); got != "12:2?" {
		t.Errorf("12:21 with its last digit too pale to see: read %q, want 12:2? (%+v)", got, words)
	}
	// Units and slivers stay out: a sliver of a digit on the edge.
	if got, words := decode(t, boxAround(render("1234", clean), 10, 10, -(W-2), 10)); got != "123" {
		t.Errorf("1234 with a 2 px sliver of the 4 in the box: read %q, want 123 (%+v)", got, words)
	}
}

// What the box leaves of a digit can be a single bar: a 7 cut down the
// middle leaves its top bar, a small 4 cut a third of the way in its upper
// left bar, a 2 cut on the left its upper right one. Dropped as too small
// for a digit, 9087 read a confident 908 and 2047 read 047.
func TestSevenSegCutBarAtTheEdge(t *testing.T) {
	dark := clean
	dark.Dark = true
	small := renderOpts{H: 40, Stroke: 6, Gap: 1, Seed: 3}
	for _, o := range []renderOpts{clean, dark, small} {
		W := o.cellW()
		seven := W - o.Stroke - o.Gap // a 7 has no bars down its left side
		for _, keep := range []float64{0.3, 0.45, 0.6} {
			cut := int((1 - keep) * float64(seven))
			if got, words := decode(t, boxAround(render("9087", o), 10, 10, -cut, 10)); got != "908?" {
				t.Errorf("H %d dark %v, 9087 with %.0f%% of the 7 in the box: read %q, want 908? (%+v)", o.H, o.Dark, 100*keep, got, words)
			}
		}
	}
	W := small.cellW()
	keep := int(0.3 * float64(W))
	for _, c := range []struct {
		text, want string
		l, r       int
	}{
		{"1234", "123?", 10, -(W - keep)},
		{"10.25", "10.2?", 10, -(W - keep)},
		{"2047", "?047", -(W - keep), 10},
	} {
		if got, words := decode(t, boxAround(render(c.text, small), c.l, 10, c.r, 10)); got != c.want {
			t.Errorf("H 40 %s with 30%% of its edge digit in the box: read %q, want %s (%+v)", c.text, got, c.want, words)
		}
	}
}

// A lone "1" with a digit's height of room round it is a 1, and so is -1:
// the rule that a bar far shorter than the box is a speck read them '?'.
func TestSevenSegLoneOneRoomyBox(t *testing.T) {
	dark := clean
	dark.Dark = true
	for _, o := range []renderOpts{clean, dark, {H: 60, Stroke: 9, Gap: 1, Seed: 4, Blur: 1}, {H: 40, Stroke: 6, Gap: 1, Seed: 3}} {
		for _, text := range []string{"1", "-1"} {
			for _, m := range []float64{0.75, 1} {
				p := int(m * float64(o.H))
				if got, words := decode(t, boxAround(render(text, o), p, p, p, p)); got != text {
					t.Errorf("H %d dark %v, %q with %.2f of its height of room: read %q (%+v)", o.H, o.Dark, text, m, got, words)
				}
			}
		}
	}
}

// Tenths smaller than the degrees, after the point, are part of the
// reading even when the tenths are a lone 1: 20.1 read 20.?.
func TestSevenSegSmallOneAfterThePoint(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, share := range []float64{0.7, 0.75, 0.8} {
			if got, words := decode(t, smallTenths("20.", "1", share, dark)); got != "20.1" {
				t.Errorf("dark %v, 20.1 with the 1 at %.2f of the height: read %q (%+v)", dark, share, got, words)
			}
		}
	}
}

// openDots redraws the colon of a render of text in o (the colon is the
// glyph at index at) as a dot-matrix display shows it binarized: each dot
// a cross of LEDs, its corners dark, under half of its square lit.
func openDots(text string, at int, o renderOpts) *image.RGBA {
	img := render(text, o)
	W, dot := o.cellW(), o.Dot
	spacing := int(math.Round(float64(W) * 0.4))
	margin := max(2, o.H/5)
	x := margin + at*(W+spacing)
	bg := img.RGBAAt(0, 0)
	c := dot * 2 / 5
	for _, cy := range []int{margin + o.H*3/10, margin + o.H*7/10} {
		y := cy - dot/2
		for _, corner := range []image.Point{{x, y}, {x + dot - c, y}, {x, y + dot - c}, {x + dot - c, y + dot - c}} {
			draw.Draw(img, image.Rect(corner.X, corner.Y, corner.X+c, corner.Y+c), image.NewUniform(bg), image.Point{}, draw.Src)
		}
	}
	return img
}

// A dot-matrix clock's colon, binarized, is two crosses of LEDs rather
// than two solid dots; dropped as pieces of nothing, 12:25 read 1225. And
// a flat sliver in the box's bottom corner after the last digit is the
// frame, not a decimal point (12:25 read 12:25.).
func TestSevenSegDotMatrixColon(t *testing.T) {
	o := clean
	o.Dot = 15
	for _, dark := range []bool{false, true} {
		o.Dark = dark
		if got, words := decode(t, openDots("12:25", 2, o)); got != "12:25" {
			t.Errorf("dark %v, 12:25 with open colon dots: read %q (%+v)", dark, got, words)
		}
	}
	img := boxAround(render("12:25", clean), 10, 10, 24, 3)
	b := img.Bounds()
	draw.Draw(img, image.Rect(b.Max.X-14, b.Max.Y-6, b.Max.X, b.Max.Y), image.NewUniform(color.RGBA{230, 40, 40, 255}), image.Point{}, draw.Src)
	if got, words := decode(t, img); got != "12:25" {
		t.Errorf("12:25 with a sliver of the frame in the box's corner: read %q (%+v)", got, words)
	}
}

// Two marks one over the other after the last digit are the pieces of a
// unit's letter (the s of a clock's seconds label), not two decimal
// points: 12:34 read 12:34.. .
func TestSevenSegStackedMarksAreNoPoint(t *testing.T) {
	img := render("12:34  ", clean)
	ink := inkBounds(img, img.RGBAAt(0, 0))
	fg := image.NewUniform(color.RGBA{230, 40, 40, 255})
	x := ink.Max.X + 14
	draw.Draw(img, image.Rect(x, ink.Max.Y-24, x+10, ink.Max.Y-14), fg, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(x, ink.Max.Y-10, x+10, ink.Max.Y), fg, image.Point{}, draw.Src)
	if got, words := decode(t, img); got != "12:34" {
		t.Errorf("12:34 with two marks stacked after it: read %q (%+v)", got, words)
	}
}

// A slanted display's degree sign stands over the foot of the 5 before it,
// which leans out under it once the lean is taken out: joined to the 5,
// 25° read '?'.
func TestSevenSegDegreeOverASlantedFive(t *testing.T) {
	o := clean
	o.Shear = 0.25
	img := render("225 ", o)
	ink := inkBounds(img, img.RGBAAt(0, 0))
	fg := image.NewUniform(color.RGBA{230, 40, 40, 255})
	bg := image.NewUniform(img.RGBAAt(0, 0))
	x, y, s := ink.Max.X+4, ink.Min.Y, 22
	draw.Draw(img, image.Rect(x, y, x+s, y+s), fg, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(x+6, y+6, x+s-6, y+s-6), bg, image.Point{}, draw.Src)
	if got, words := decode(t, img); got != "225" {
		t.Errorf("slanted 225 with a degree sign: read %q (%+v)", got, words)
	}
}
