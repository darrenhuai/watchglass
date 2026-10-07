package ocr

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// segs is each glyph's lit bars, in the conventional a-g order: a top,
// b upper-right, c lower-right, d bottom, e lower-left, f upper-left,
// g middle.
var segs = map[rune]string{
	'0': "abcdef", '1': "bc", '2': "abdeg", '3': "abcdg", '4': "bcfg",
	'5': "acdfg", '6': "acdefg", '7': "abc", '8': "abcdefg", '9': "abcdfg",
	'-': "g",
	// Letters a display spells with the same bars; the decoder must answer
	// '?' for them, never a digit.
	'H': "bcefg", 'E': "adefg",
}

// renderOpts drives the synthetic display renderer below. Every knob is
// something a real camera-on-a-display setup varies: how big the digits
// are in the crop, how fat the bars are and whether they touch, LED vs LCD
// polarity, sensor noise and lens softness.
type renderOpts struct {
	H      int     // digit height, px
	Stroke int     // bar thickness, px
	Gap    int     // gap between neighbouring bars, px (0 = bars touch)
	Jitter int     // each bar's thickness varies by up to ± this many px
	Dark   bool    // dark digits on a light ground (LCD) instead of lit on dark (LED)
	Noise  float64 // gaussian sigma added to every pixel, 0-255 scale
	Blur   int     // box-blur radius, 0 = sharp
	Seed   int64
	// Real-display conditions the reviewers found the first cut failing on.
	Ghost     float64 // unlit bars drawn at this share of the foreground (LCD ghosting)
	DimIndex  int     // index of the glyph drawn at DimLevel intensity (PWM under a rolling shutter)
	DimLevel  float64
	DotGap    int        // gap before a decimal point; -1 = the normal spacing, 0 = touching
	Bezel     int        // bright line this many px tall along the crop's top edge
	EdgeLine  int        // bright line this many px wide down the crop's left edge
	Shear     float64    // italic lean, x per row (positive: top leans right)
	Spacing   int        // px between glyphs; 0 keeps the usual 0.4 of a cell
	Dot       int        // side of a point or colon dot, px; 0 keeps the usual 0.12 of the height
	ColonAt   [2]float64 // centres of a colon's dots as shares of the height; zero keeps 0.3 and 0.7
	Overlap   int        // the vertical bars run this many px past the ends of the horizontal ones (an LED font whose side bars stand beside its top and bottom bars)
	dotGapSet bool       // DotGap was chosen; the zero value keeps the normal spacing
}

// withDotGap returns o with the decimal point placed gap px after its digit.
func (o renderOpts) withDotGap(gap int) renderOpts { o.DotGap, o.dotGapSet = gap, true; return o }

func (o renderOpts) cellW() int { return int(math.Round(float64(o.H) * 0.55)) }

// render draws text ("23.5", "-8.0", "1234") as a seven-segment display:
// each bar a rectangle, a "1" right-aligned in a full-width cell, a "."
// a small square on the baseline in the gap after its digit.
func render(text string, o renderOpts) *image.RGBA {
	if !o.dotGapSet {
		o.DotGap = -1
	}
	rnd := rand.New(rand.NewSource(o.Seed))
	W := o.cellW()
	spacing := int(math.Round(float64(W) * 0.4))
	if o.Spacing > 0 {
		spacing = o.Spacing
	}
	dot := max(2, int(math.Round(float64(o.H)*0.12)))
	if o.Dot > 0 {
		dot = o.Dot
	}
	margin := max(2, o.H/5)
	width := 2*margin - spacing
	for _, ch := range text {
		if ch == '.' {
			width += dot
		} else {
			width += W
		}
		width += spacing
	}
	img := image.NewRGBA(image.Rect(0, 0, width, o.H+2*margin))
	fg, bg := color.RGBA{230, 40, 40, 255}, color.RGBA{12, 10, 10, 255}
	if o.Dark {
		fg, bg = color.RGBA{30, 30, 34, 255}, color.RGBA{190, 200, 180, 255}
	}
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	fill := func(r image.Rectangle) {
		draw.Draw(img, r, image.NewUniform(fg), image.Point{}, draw.Src)
	}
	thick := func() int {
		s := o.Stroke
		if o.Jitter > 0 {
			s += rnd.Intn(2*o.Jitter+1) - o.Jitter
		}
		return max(1, s)
	}
	x, y := margin, margin
	fgLevel := func(idx int) color.RGBA {
		if o.DimLevel > 0 && idx == o.DimIndex {
			return blend(bg, fg, o.DimLevel)
		}
		return fg
	}
	for idx, ch := range text {
		if ch == '.' {
			if o.DotGap >= 0 {
				x += o.DotGap - spacing
			}
			draw.Draw(img, image.Rect(x, y+o.H-dot, x+dot, y+o.H), image.NewUniform(fgLevel(idx)), image.Point{}, draw.Src)
			x += dot + spacing
			continue
		}
		if ch == ':' {
			at := []int{y + o.H*3/10, y + o.H*7/10}
			if o.ColonAt != [2]float64{} {
				at = []int{y + int(math.Round(o.ColonAt[0]*float64(o.H))), y + int(math.Round(o.ColonAt[1]*float64(o.H)))}
			}
			for _, cy := range at {
				draw.Draw(img, image.Rect(x, cy-dot/2, x+dot, cy-dot/2+dot), image.NewUniform(fgLevel(idx)), image.Point{}, draw.Src)
			}
			x += dot + spacing
			continue
		}
		if ch == ' ' {
			ch = 0 // a blank cell: nothing lit, ghost bars only
		}
		on, ok := segs[ch]
		if ch != 0 && !ok {
			panic("render: no glyph for " + string(ch))
		}
		cellFG := fgLevel(idx)
		s, g, H, ov := o.Stroke, o.Gap, o.H, o.Overlap
		type bar struct {
			name byte
			rect func(t int) image.Rectangle
		}
		bars := []bar{ // a fixed order so the jitter sequence is reproducible
			{'a', func(t int) image.Rectangle { return image.Rect(x+s+g, y, x+W-s-g, y+t) }},
			{'g', func(t int) image.Rectangle { return image.Rect(x+s+g, y+(H-t)/2, x+W-s-g, y+(H-t)/2+t) }},
			{'d', func(t int) image.Rectangle { return image.Rect(x+s+g, y+H-t, x+W-s-g, y+H) }},
			{'f', func(t int) image.Rectangle { return image.Rect(x, y+s+g-ov, x+t, y+(H-s)/2-g+ov) }},
			{'b', func(t int) image.Rectangle { return image.Rect(x+W-t, y+s+g-ov, x+W, y+(H-s)/2-g+ov) }},
			{'e', func(t int) image.Rectangle { return image.Rect(x, y+(H+s)/2+g-ov, x+t, y+H-s-g+ov) }},
			{'c', func(t int) image.Rectangle { return image.Rect(x+W-t, y+(H+s)/2+g-ov, x+W, y+H-s-g+ov) }},
		}
		for _, b := range bars {
			t := thick()
			switch {
			case strings.IndexByte(on, b.name) >= 0:
				draw.Draw(img, b.rect(t), image.NewUniform(cellFG), image.Point{}, draw.Src)
			case o.Ghost > 0:
				draw.Draw(img, b.rect(o.Stroke), image.NewUniform(blend(bg, fg, o.Ghost)), image.Point{}, draw.Src)
			}
		}
		x += W + spacing
	}
	_ = fill
	if o.Bezel > 0 {
		fill(image.Rect(0, 0, width, o.Bezel))
	}
	if o.EdgeLine > 0 {
		fill(image.Rect(0, 0, o.EdgeLine, o.H+2*margin))
	}
	if o.Shear != 0 {
		img = shearImage(img, o.Shear, bg)
	}
	if o.Blur > 0 {
		img = boxBlur(img, o.Blur)
	}
	if o.Noise > 0 {
		for i := 0; i < len(img.Pix); i += 4 {
			n := rnd.NormFloat64() * o.Noise
			for c := 0; c < 3; c++ {
				img.Pix[i+c] = clamp8(float64(img.Pix[i+c]) + n)
			}
		}
	}
	return img
}

// blend mixes a towards b by t.
func blend(a, b color.RGBA, t float64) color.RGBA {
	m := func(x, y uint8) uint8 { return clamp8(float64(x) + t*(float64(y)-float64(x))) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 255}
}

// shearImage leans the picture like an italic font: each row shifts by
// s*(y-centre) px, the canvas grows to hold it, and bg fills what's bared.
func shearImage(src *image.RGBA, s float64, bg color.RGBA) *image.RGBA {
	b := src.Bounds()
	yc := b.Dy() / 2
	minS, maxS := 0, 0
	for y := 0; y < b.Dy(); y++ {
		d := int(math.Round(s * float64(y-yc)))
		minS, maxS = min(minS, d), max(maxS, d)
	}
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()+maxS-minS, b.Dy()))
	draw.Draw(out, out.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	for y := 0; y < b.Dy(); y++ {
		d := int(math.Round(s*float64(y-yc))) - minS
		draw.Draw(out, image.Rect(d, y, d+b.Dx(), y+1), src, image.Point{0, y}, draw.Src)
	}
	return out
}

func clamp8(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}

// boxBlur is a separable box blur of radius r with clamped edges.
func boxBlur(src *image.RGBA, r int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	tmp := image.NewRGBA(b)
	out := image.NewRGBA(b)
	n := float64(2*r + 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc [3]float64
			for k := -r; k <= r; k++ {
				xx := min(max(x+k, 0), w-1)
				i := y*src.Stride + xx*4
				for c := 0; c < 3; c++ {
					acc[c] += float64(src.Pix[i+c])
				}
			}
			i := y*tmp.Stride + x*4
			for c := 0; c < 3; c++ {
				tmp.Pix[i+c] = clamp8(acc[c] / n)
			}
			tmp.Pix[i+3] = 255
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc [3]float64
			for k := -r; k <= r; k++ {
				yy := min(max(y+k, 0), h-1)
				i := yy*tmp.Stride + x*4
				for c := 0; c < 3; c++ {
					acc[c] += float64(tmp.Pix[i+c])
				}
			}
			i := y*out.Stride + x*4
			for c := 0; c < 3; c++ {
				out.Pix[i+c] = clamp8(acc[c] / n)
			}
			out.Pix[i+3] = 255
		}
	}
	return out
}

// clean is the baseline render every table below starts from: 100px
// digits, a 12px bar, 2px gaps, LED polarity.
var clean = renderOpts{H: 100, Stroke: 12, Gap: 2, Seed: 1}

func decode(t *testing.T, img image.Image) (string, []Word) {
	t.Helper()
	text, words, err := NewSevenSeg().RecognizeWords(context.Background(), img)
	if err != nil {
		t.Fatalf("RecognizeWords: %v", err)
	}
	return text, words
}

func minConf(words []Word) float64 {
	m := 100.0
	for _, w := range words {
		m = math.Min(m, w.Conf)
	}
	return m
}

func TestSevenSegIsDetailedEngine(t *testing.T) {
	var _ DetailedEngine = NewSevenSeg()
}

func TestSevenSegEachDigitAlone(t *testing.T) {
	for _, d := range "0123456789" {
		want := string(d)
		got, words := decode(t, render(want, clean))
		if got != want {
			t.Errorf("%s: read %q", want, got)
		}
		if len(words) != 1 || words[0].Text != want || words[0].Conf < 90 {
			t.Errorf("%s: words = %+v, want one high-confidence glyph", want, words)
		}
	}
}

func TestSevenSegReadings(t *testing.T) {
	for _, want := range []string{"23.5", "-8.0", "1234", "1.1", "0.00", "7.5", "-12.34", "100", "11"} {
		got, words := decode(t, render(want, clean))
		if got != want {
			t.Errorf("read %q, want %q (words %+v)", got, want, words)
		}
		if c := minConf(words); c < 90 {
			t.Errorf("%s: min confidence %.0f, want a clean render to score high (%+v)", want, c, words)
		}
	}
}

func TestSevenSegBothPolarities(t *testing.T) {
	for _, want := range []string{"23.5", "-8.0", "1234"} {
		for _, dark := range []bool{false, true} {
			o := clean
			o.Dark = dark
			got, words := decode(t, render(want, o))
			if got != want {
				t.Errorf("dark=%v: read %q, want %q (%+v)", dark, got, want, words)
			}
		}
	}
}

func TestSevenSegScales(t *testing.T) {
	cases := []struct {
		name string
		o    renderOpts
	}{
		{"40px", renderOpts{H: 40, Stroke: 5, Gap: 1, Seed: 2}},
		{"40px thin", renderOpts{H: 40, Stroke: 3, Gap: 1, Seed: 2}},
		{"400px", renderOpts{H: 400, Stroke: 50, Gap: 8, Seed: 3}},
		{"400px thin", renderOpts{H: 400, Stroke: 24, Gap: 6, Seed: 3}},
	}
	for _, c := range cases {
		for _, want := range []string{"23.5", "1234", "-8.0"} {
			got, words := decode(t, render(want, c.o))
			if got != want {
				t.Errorf("%s: read %q, want %q (%+v)", c.name, got, want, words)
			}
		}
	}
}

func TestSevenSegStrokeJitter(t *testing.T) {
	cases := []renderOpts{
		{H: 100, Stroke: 12, Gap: 2, Jitter: 3, Seed: 4},
		{H: 100, Stroke: 12, Gap: 2, Jitter: 3, Seed: 5},
		{H: 40, Stroke: 5, Gap: 1, Jitter: 1, Seed: 6},
		{H: 200, Stroke: 20, Gap: 4, Jitter: 3, Seed: 7},
	}
	for _, o := range cases {
		for _, want := range []string{"23.5", "1234", "0.00"} {
			got, words := decode(t, render(want, o))
			if got != want {
				t.Errorf("%+v: read %q, want %q (%+v)", o, got, want, words)
			}
		}
	}
}

func TestSevenSegGapsBetweenBars(t *testing.T) {
	for _, gap := range []int{0, 1, 3, 6} {
		o := clean
		o.Gap = gap
		for _, want := range []string{"23.5", "8", "0.00"} {
			got, words := decode(t, render(want, o))
			if got != want {
				t.Errorf("gap %d: read %q, want %q (%+v)", gap, got, want, words)
			}
		}
	}
}

func TestSevenSegNoiseAndBlur(t *testing.T) {
	cases := []renderOpts{
		{H: 100, Stroke: 12, Gap: 2, Noise: 18, Blur: 1, Seed: 8},
		{H: 100, Stroke: 12, Gap: 2, Noise: 25, Blur: 2, Jitter: 2, Seed: 9},
		{H: 100, Stroke: 12, Gap: 2, Noise: 18, Blur: 1, Dark: true, Seed: 10},
		{H: 60, Stroke: 7, Gap: 1, Noise: 15, Blur: 1, Seed: 11},
	}
	for _, o := range cases {
		for _, want := range []string{"23.5", "1234", "-8.0"} {
			got, words := decode(t, render(want, o))
			if got != want {
				t.Errorf("%+v: read %q, want %q (%+v)", o, got, want, words)
			}
		}
	}
}

// A clean render is never less confident than a degraded one of the same
// reading, and a segment that is only half there drags confidence down
// (or turns the glyph into '?').
func TestSevenSegConfidenceMonotone(t *testing.T) {
	_, cleanWords := decode(t, render("23.5", clean))
	cleanMin := minConf(cleanWords)
	if cleanMin < 95 {
		t.Fatalf("clean render min confidence %.0f, want ~100 (%+v)", cleanMin, cleanWords)
	}
	noisy := renderOpts{H: 100, Stroke: 12, Gap: 2, Noise: 25, Blur: 2, Jitter: 3, Seed: 12}
	if got, words := decode(t, render("23.5", noisy)); got == "23.5" && minConf(words) > cleanMin {
		t.Errorf("noisy render scored %.0f, above the clean render's %.0f", minConf(words), cleanMin)
	}

	// Damage the "0": keep only the left 45% of its top bar, so the a
	// segment is neither clearly on nor clearly off.
	o := clean
	img := render("0", o)
	W := o.cellW()
	margin := max(2, o.H/5)
	draw.Draw(img, image.Rect(margin+int(float64(W)*0.45), margin, margin+W, margin+o.Stroke),
		image.NewUniform(color.RGBA{12, 10, 10, 255}), image.Point{}, draw.Src)
	got, words := decode(t, img)
	if len(words) != 1 {
		t.Fatalf("damaged 0: words = %+v, want exactly one glyph", words)
	}
	if got != "0" && got != "?" {
		t.Errorf("damaged 0: read %q, want 0 (marginal) or ?", got)
	}
	if words[0].Conf >= cleanMin || words[0].Conf >= 60 {
		t.Errorf("damaged 0: confidence %.0f, want well below the clean %.0f and below the UI's 60 line", words[0].Conf, cleanMin)
	}
}

func TestSevenSegWordsMatchGlyphs(t *testing.T) {
	text, words := decode(t, render("23.5", clean))
	if text != "23.5" {
		t.Fatalf("read %q", text)
	}
	want := []string{"2", "3", ".", "5"}
	if len(words) != len(want) {
		t.Fatalf("words = %+v, want %d glyphs", words, len(want))
	}
	var joined strings.Builder
	for i, w := range words {
		if w.Text != want[i] {
			t.Errorf("word %d = %q, want %q", i, w.Text, want[i])
		}
		if w.Conf < 0 || w.Conf > 100 {
			t.Errorf("word %d confidence %.1f out of 0-100", i, w.Conf)
		}
		joined.WriteString(w.Text)
	}
	if joined.String() != text {
		t.Errorf("words join to %q, Recognize said %q", joined.String(), text)
	}
	single, err := NewSevenSeg().Recognize(context.Background(), render("23.5", clean))
	if err != nil || single != text {
		t.Errorf("Recognize = %q, %v; want %q", single, err, text)
	}
}

// Shapes a normal font makes — solid blobs, diagonals — are not digits: no
// confident digit may come out of them.
func TestSevenSegGarbageIsNotConfident(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 260, 100))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{12, 10, 10, 255}), image.Point{}, draw.Src)
	ink := image.NewUniform(color.RGBA{230, 40, 40, 255})
	// a bold solid block (like a heavy "I" or a filled letter)
	draw.Draw(img, image.Rect(10, 15, 50, 85), ink, image.Point{}, draw.Src)
	// a thick "/" stroke
	for i := 0; i < 70; i++ {
		draw.Draw(img, image.Rect(70+i, 85-i, 80+i, 93-i), ink, image.Point{}, draw.Src)
	}
	// an "N": two uprights joined by a diagonal
	draw.Draw(img, image.Rect(160, 15, 170, 85), ink, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(210, 15, 220, 85), ink, image.Point{}, draw.Src)
	for i := 0; i < 50; i++ {
		draw.Draw(img, image.Rect(165+i, 15+int(float64(i)*1.4), 173+i, 23+int(float64(i)*1.4)), ink, image.Point{}, draw.Src)
	}
	got, words := decode(t, img)
	for _, w := range words {
		if w.Text != "?" && w.Conf >= 60 {
			t.Errorf("garbage read as confident %q (%.0f); text %q, words %+v", w.Text, w.Conf, got, words)
		}
	}
}

func TestSevenSegEmptyAndOddImages(t *testing.T) {
	dark := image.NewRGBA(image.Rect(0, 0, 200, 80))
	light := image.NewRGBA(image.Rect(0, 0, 200, 80))
	draw.Draw(light, light.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	speck := image.NewRGBA(image.Rect(0, 0, 200, 80))
	speck.Set(100, 40, color.White)
	half := image.NewRGBA(image.Rect(0, 0, 200, 80))
	draw.Draw(half, image.Rect(0, 0, 100, 80), image.NewUniform(color.White), image.Point{}, draw.Src)
	offset := image.NewRGBA(image.Rect(50, 50, 250, 130)) // non-zero origin
	for name, img := range map[string]image.Image{
		"all dark":   dark,
		"all light":  light,
		"one speck":  speck,
		"1x1":        image.NewRGBA(image.Rect(0, 0, 1, 1)),
		"0x0":        image.NewRGBA(image.Rect(0, 0, 0, 0)),
		"3x2":        image.NewRGBA(image.Rect(0, 0, 3, 2)),
		"gray image": image.NewGray(image.Rect(0, 0, 100, 40)),
		"offset":     offset,
	} {
		text, words, err := NewSevenSeg().RecognizeWords(context.Background(), img)
		if err != nil {
			t.Errorf("%s: error %v", name, err)
		}
		if text != "" || len(words) != 0 {
			t.Errorf("%s: read %q %+v, want nothing", name, text, words)
		}
	}
	// Half-and-half is not a display; whatever it reads must not be a
	// confident digit, and must not panic.
	if text, words, err := NewSevenSeg().RecognizeWords(context.Background(), half); err != nil {
		t.Errorf("half: %v", err)
	} else {
		for _, w := range words {
			if w.Text != "?" && w.Conf >= 60 {
				t.Errorf("half image read as confident %q (%.0f): %q", w.Text, w.Conf, text)
			}
		}
	}
}

// The committed fixture is a PIL render with hexagonal bars, unlit ghost
// segments in the leading position, LED glow, anti-aliasing and sensor
// noise — the closest thing to a camera frame this package can carry.
func TestSevenSegFixture(t *testing.T) {
	f, err := os.Open("testdata/sevenseg-23.5.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	got, words := decode(t, img)
	if got != "23.5" {
		t.Fatalf("fixture read %q, want 23.5 (%+v)", got, words)
	}
	if len(words) != 4 {
		t.Fatalf("words = %+v, want 4", words)
	}
	for _, w := range words {
		if w.Conf < 60 {
			t.Errorf("glyph %q confidence %.0f below the UI's ok line", w.Text, w.Conf)
		}
	}
}

// The conditions the adversarial review found the first cut misreading on
// real displays. Each is a regression test for the fix that followed.
func TestSevenSegRealDisplayConditions(t *testing.T) {
	ghost := clean
	ghost.Ghost = 0.25
	dim := clean
	dim.DimIndex, dim.DimLevel = 1, 0.45
	bezel := clean
	bezel.Bezel = 2
	edge := clean
	edge.EdgeLine = 2
	italic := clean
	italic.Shear = 0.14
	// 20% of the height is the fattest the renderer's 0.55 aspect can draw
	// with a horizontal bar left between the verticals; bolder fonts widen
	// the cell instead.
	fat := renderOpts{H: 100, Stroke: 20, Gap: 2, Seed: 5}
	attached := clean.withDotGap(0)
	attached.Blur = 1
	cases := []struct {
		name, text, want string
		o                renderOpts
	}{
		{"ghost segments, leading blanks", "   1", "1", ghost},
		{"ghost segments, mostly ones", "11", "11", ghost},
		{"ghost segments, full reading", "23.5", "23.5", ghost},
		{"one dim digit", "235", "235", dim},
		{"decimal point touching its digit", "23.5", "23.5", attached},
		{"bezel line along the top", "23.5", "23.5", bezel},
		{"line down the left edge", "23.5", "23.5", edge},
		{"italic", "23.5", "23.5", italic},
		{"italic seven", "7.5", "7.5", italic},
		{"fat stroke", "0000", "0000", fat},
	}
	for _, c := range cases {
		got, words := decode(t, render(c.text, c.o))
		if got != c.want {
			t.Errorf("%s: read %q, want %q (%+v)", c.name, got, c.want, words)
		}
	}
}

// A clock's colon must not poison the digits either side of it.
func TestSevenSegColon(t *testing.T) {
	got, words := decode(t, render("12:34", clean))
	digits := strings.Map(func(r rune) rune {
		if r == ':' {
			return -1
		}
		return r
	}, got)
	if digits != "1234" || strings.Contains(got, "?") {
		t.Errorf("12:34 read %q (%+v)", got, words)
	}
	// A clean colon is as sure as the digits round it: Test this region
	// marks a glyph under 60 as low confidence, and a clock or countdown
	// that read perfectly shouldn't carry that warning.
	for _, w := range words {
		if w.Text == ":" && w.Conf < 60 {
			t.Errorf("the colon of a clean 12:34 scored %v (%+v)", w.Conf, words)
		}
	}
}

// What photos of real displays showed the decoder getting wrong: a red LED
// timer behind a filter window, and a backlit LCD panel meter reading 18.9
// with its unit beside the digits. Each case draws the condition onto a
// clean render, since the photos themselves aren't ours to ship.
func TestSevenSegRealPhotoConditions(t *testing.T) {
	led := color.RGBA{230, 40, 40, 255}
	lcdInk := color.RGBA{30, 30, 34, 255}
	rect := func(img *image.RGBA, r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	}
	lcd := clean
	lcd.Dark = true
	margin := max(2, clean.H/5)

	// The edge of the filter window runs along the top and bottom of the
	// crop: a few pixels thick, broken into pieces by glare, the lower one
	// just clear of the edge with sensor specks under it. It spans both
	// digits and used to fold them into one unreadable glyph.
	t.Run("window rim in pieces", func(t *testing.T) {
		img := render("09", clean)
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		rect(img, image.Rect(w/6, 0, w/2-4, 5), led)
		rect(img, image.Rect(w/2+4, 0, w-w/8, 5), led)
		rect(img, image.Rect(4, h-7, w/2-10, h-3), led)
		rect(img, image.Rect(w/2, h-7, w-6, h-3), led)
		rect(img, image.Rect(w/3, h-2, w/3+3, h), led)
		rect(img, image.Rect(w-20, h-2, w-17, h), led)
		got, words := decode(t, img)
		if got != "09" || minConf(words) < 60 {
			t.Errorf("read %q, want a confident 09 (%+v)", got, words)
		}
	})

	// A digit's own top and bottom bars on a box drawn tight to the digits
	// are a stroke thick, not a rim, and must stay.
	t.Run("tight box keeps the top and bottom bars", func(t *testing.T) {
		img := render("28", clean)
		b := img.Bounds()
		tight := img.SubImage(image.Rect(b.Min.X, b.Min.Y+margin, b.Max.X, b.Max.Y-margin))
		if got, words := decode(t, tight); got != "28" {
			t.Errorf("read %q, want 28 (%+v)", got, words)
		}
	})

	// A leading "1" is the leftmost thing on the display, and its bars sit
	// at the right of its cell, so the cell starts left of any box drawn
	// round the digits. That alone used to make it a '?'.
	barX := margin + lcd.cellW() - lcd.Stroke
	t.Run("leading 1 with a little room before it", func(t *testing.T) {
		img := render("18.9", lcd)
		b := img.Bounds()
		near := img.SubImage(image.Rect(barX-8, b.Min.Y, b.Max.X, b.Max.Y))
		got, words := decode(t, near)
		if got != "18.9" || minConf(words) < 60 {
			t.Errorf("read %q, want a confident 18.9 (%+v)", got, words)
		}
	})
	t.Run("bar on the crop's left edge is not a confident 1", func(t *testing.T) {
		img := render("18.9", lcd)
		b := img.Bounds()
		cut := img.SubImage(image.Rect(barX, b.Min.Y, b.Max.X, b.Max.Y))
		got, words := decode(t, cut)
		if len(words) == 0 || (words[0].Text == "1" && words[0].Conf >= 60) {
			t.Errorf("read %q with the first bar cut by the crop; it must not be a confident 1 (%+v)", got, words)
		}
	})

	// The left stroke of the unit ("A") caught at the right of the box:
	// short, narrow, two bars tall. Laid out as a "-" cell it overlapped
	// the last digit and added a '?'.
	t.Run("unit symbol beside the digits", func(t *testing.T) {
		img := render("18.9", lcd)
		b := img.Bounds()
		rect(img, image.Rect(b.Max.X-9, margin+55, b.Max.X-1, margin+79), lcdInk)
		got, words := decode(t, img)
		if got != "18.9" || minConf(words) < 60 {
			t.Errorf("read %q, want a confident 18.9 (%+v)", got, words)
		}
	})

	// A box that takes in the housing round the display: the housing is
	// brighter than the digits, and at the threshold that leaves only it
	// lit, its ring of edges spells a perfect "0".
	t.Run("bright frame round the display is not a 0", func(t *testing.T) {
		img := render("09", clean)
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		housing := color.RGBA{230, 200, 60, 255}
		rect(img, image.Rect(0, 0, w, 8), housing)
		rect(img, image.Rect(0, h-8, w, h), housing)
		rect(img, image.Rect(0, 0, 8, h), housing)
		rect(img, image.Rect(w-8, 0, w, h), housing)
		got, words := decode(t, img)
		for _, wd := range words {
			if wd.Text != "?" && wd.Conf >= 60 && got != "09" {
				t.Errorf("read %q off the frame (%+v); want 09 or no confident glyph", got, words)
				break
			}
		}
	})

	// A display showing a word keeps its colon marks in view with nothing
	// readable after them; the colon is not part of a reading then.
	t.Run("colon with nothing after it", func(t *testing.T) {
		if got, words := decode(t, render("8:", clean)); got != "8" {
			t.Errorf("read %q, want 8 (%+v)", got, words)
		}
	})
}

// onHousing puts img on a canvas of colour c with l, t, r and b px of it
// showing on each side: what a box drawn a little too big takes in.
func onHousing(img image.Image, l, t, r, b int, c color.RGBA) *image.RGBA {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := image.NewRGBA(image.Rect(0, 0, w+l+r, h+t+b))
	draw.Draw(out, out.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(l, t, l+w, t+h), img, img.Bounds().Min, draw.Src)
	return out
}

// inkBounds is the box drawn exactly round the lit pixels of a render.
func inkBounds(img *image.RGBA, bg color.RGBA) image.Rectangle {
	b := img.Bounds()
	out := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.RGBAAt(x, y) != bg {
				out = out.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return out
}

// overexpose scales every pixel by gain and clips: a bright LED that
// saturates the sensor, so a blurred bar's edge comes out as lit as its
// core and the bar fattens.
func overexpose(img *image.RGBA, gain float64) *image.RGBA {
	out := image.NewRGBA(img.Bounds())
	copy(out.Pix, img.Pix)
	for i := 0; i < len(out.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			out.Pix[i+c] = clamp8(float64(out.Pix[i+c]) * gain)
		}
	}
	return out
}

// confident reports whether every glyph of a reading is a sure one: the
// reading a numeric trigger would act on.
func confident(words []Word) bool {
	for _, w := range words {
		if w.Text == "?" || w.Conf < 60 {
			return false
		}
	}
	return len(words) > 0
}

// What the photos people posted in the Home Assistant forum showed the
// decoder still getting wrong after TestSevenSegRealPhotoConditions: a box
// drawn a little too big or a little too small, a glare speck, and small
// digits whose glow runs them together. Each case draws the condition onto
// a render, since the photos aren't ours to ship, and each guard beside a
// fix pins what the fix must not start doing.
func TestSevenSegForumPhotoConditions(t *testing.T) {
	led := color.RGBA{230, 40, 40, 255}
	ledGround := color.RGBA{12, 10, 10, 255}
	housing := color.RGBA{230, 200, 60, 255} // brighter than the digits
	rect := func(img *image.RGBA, r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	}
	margin := max(2, clean.H/5)
	lcd := clean
	lcd.Dark = true

	// The box takes in a few pixels of the housing round the display
	// window. The housing is one bright ring that every digit overlaps, so
	// the whole crop used to cluster into a single unreadable glyph.
	t.Run("housing in the box", func(t *testing.T) {
		soft := boxBlur(onHousing(render("09", clean), 9, 9, 9, 9, housing), 2)
		cases := []struct {
			name, want string
			img        image.Image
		}{
			{"all round", "09", onHousing(render("09", clean), 9, 9, 9, 9, housing)},
			{"all round, wider", "09", onHousing(render("09", clean), 16, 14, 15, 17, housing)},
			{"all round, soft edges", "09", soft},
			{"above and to the left", "09", onHousing(render("09", clean), 12, 10, 0, 0, housing)},
			{"above only", "09", onHousing(render("09", clean), 0, 10, 0, 0, housing)},
			{"dark bezel round an LCD", "18.9", onHousing(render("18.9", lcd), 8, 8, 8, 8, color.RGBA{20, 20, 24, 255})},
		}
		for _, c := range cases {
			got, words := decode(t, c.img)
			if got != c.want || !confident(words) {
				t.Errorf("%s: read %q, want a confident %s (%+v)", c.name, got, c.want, words)
			}
		}
	})

	// A strip of housing down one side alone, with no housing above or
	// below: the strip is brighter than the digits, so at the tightest
	// threshold it is the only thing lit, and it read as a confident "1".
	// It is cut like the rest of the housing: a "1" on the edge of a tight
	// box goes out with the digits beside it, the housing does not.
	t.Run("a strip down one side is housing", func(t *testing.T) {
		for _, px := range []int{4, 12, 25} {
			for _, c := range []struct {
				name string
				img  image.Image
			}{
				{"left", onHousing(render("09", clean), px, 0, 0, 0, housing)},
				{"right", onHousing(render("09", clean), 0, 0, px, 0, housing)},
				{"right, white", onHousing(render("18.9", clean), 0, 0, px, 0, color.RGBA{255, 255, 255, 255})},
			} {
				want := "09"
				if c.name == "right, white" {
					want = "18.9"
				}
				got, words := decode(t, c.img)
				if got != want || !confident(words) {
					t.Errorf("%d px of housing %s: read %q, want a confident %s (%+v)", px, c.name, got, want, words)
				}
			}
		}
	})

	// A box drawn exactly round the lit bars: the leading "1" lies on the
	// crop's left edge from top to bottom, as a strip of housing would. It
	// must not be taken for housing and the rest read as the whole number.
	t.Run("a 1 on the edge of a tight box is not housing", func(t *testing.T) {
		joined := clean
		joined.Gap = 0
		for _, text := range []string{"14", "141", "11", "17"} {
			img := render(text, joined)
			tight := img.SubImage(inkBounds(img, ledGround))
			got, words := decode(t, tight)
			if got != text && confident(words) {
				t.Errorf("%s in a tight box read as a confident %q (%+v)", text, got, words)
			}
		}
	})
	t.Run("a tight box on one digit still reads", func(t *testing.T) {
		for _, text := range []string{"0", "8", "7", "28"} {
			img := render(text, clean)
			tight := img.SubImage(inkBounds(img, ledGround))
			if got, words := decode(t, tight); got != text {
				t.Errorf("%s in a tight box read %q (%+v)", text, got, words)
			}
		}
	})

	// A glare speck in the corner of the window, below the digits' feet,
	// used to pass for a decimal point: "09" read "09.".
	t.Run("speck below the baseline is not a point", func(t *testing.T) {
		img := render("09", clean)
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		rect(img, image.Rect(w-9, h-5, w-3, h), led)
		got, words := decode(t, img)
		if got != "09" || !confident(words) {
			t.Errorf("read %q, want a confident 09 (%+v)", got, words)
		}
	})
	// The same mark on the baseline but a digit's width clear of the last
	// digit is an indicator lamp or a screw, not this reading's point.
	t.Run("mark far from any digit is not a point", func(t *testing.T) {
		img := onHousing(render("09", clean), 0, 0, 90, 0, ledGround)
		x := img.Bounds().Dx() - 40
		rect(img, image.Rect(x, margin+clean.H-12, x+12, margin+clean.H), led)
		got, words := decode(t, img)
		if got != "09" || !confident(words) {
			t.Errorf("read %q, want a confident 09 (%+v)", got, words)
		}
	})
	t.Run("a real trailing point stays", func(t *testing.T) {
		if got, words := decode(t, render("25.", clean)); got != "25." {
			t.Errorf("read %q, want 25. (%+v)", got, words)
		}
	})

	// A box that shaves the bottom of the digits leaves a few rows of the
	// decimal point on the crop's edge. That sliver was dropped as a piece
	// of window rim and 8.72 read 872: a wrong number, a hundred times too
	// big, with nothing to say so. A sliver is weaker evidence than a whole
	// point, so it goes in flagged (at most missedPointConf): the number is
	// right and the red chip says the box wants redrawing.
	t.Run("point cut by the bottom of the box", func(t *testing.T) {
		joined := clean
		joined.Gap = 0 // a vacuum fluorescent display: the bars of a digit touch
		img := render("8.72", joined)
		b := img.Bounds()
		for _, left := range []int{3, 5} {
			cut := img.SubImage(image.Rect(b.Min.X, b.Min.Y, b.Max.X, margin+clean.H-clean.Stroke+left))
			got, words := decode(t, cut)
			if got != "8.72" {
				t.Errorf("%d rows of the point left: read %q, want 8.72 (%+v)", left, got, words)
				continue
			}
			for _, w := range words {
				if w.Text == "." && w.Conf > missedPointConf {
					t.Errorf("%d rows of the point left: the cut point scored %.0f, want at most %d (%+v)", left, w.Conf, missedPointConf, words)
				}
			}
		}
	})

	// A washing machine's digits, 24 px tall in the frame, glowing: the
	// glow fills the gaps inside each digit and leaves a pixel or two
	// between one digit and the next, no more than the sliver between two
	// bars of one digit. "00" used to be one glyph.
	small := renderOpts{H: 24, Stroke: 4, Gap: 0, Seed: 3}
	t.Run("small digits a pixel apart", func(t *testing.T) {
		for _, spacing := range []int{1, 2} {
			o := small
			o.Spacing = spacing
			for _, want := range []string{"1400", "40", "88", "100"} {
				got, words := decode(t, render(want, o))
				if got != want || !confident(words) {
					t.Errorf("%d px apart: read %q, want a confident %s (%+v)", spacing, got, want, words)
				}
			}
		}
	})
	// The same display's clock: colon dots as big as the bars are thick, a
	// pixel or two from the "1" on one side and the "0" on the other.
	t.Run("colon with dots as big as the bars", func(t *testing.T) {
		for _, spacing := range []int{1, 2} {
			o := small
			o.Spacing, o.Dot = spacing, 5
			for _, want := range []string{"1:03", "0:47", "12:30"} {
				got, words := decode(t, render(want, o))
				if got != want || !confident(words) {
					t.Errorf("%d px apart: read %q, want a confident %s (%+v)", spacing, got, want, words)
				}
			}
		}
	})
	// On a display about 28 px tall a digit's middle and bottom bars are
	// short, square and stacked one above the other astride the middle,
	// just like a colon's dots: 88 read a confident 8:8 and 1234 12::34.
	// The digit's own side bars stand beside the gap between them, where
	// the digits round a colon have nothing.
	t.Run("a digit's middle and bottom bars are not a colon", func(t *testing.T) {
		o := renderOpts{H: 28, Stroke: 4, Gap: 1}
		for seed := int64(1); seed <= 3; seed++ {
			o.Seed = seed
			for _, want := range []string{"88", "1234", "5678", "12:30"} {
				if got, words := decode(t, render(want, o)); got != want || !confident(words) {
					t.Errorf("seed %d: read %q, want a confident %s (%+v)", seed, got, want, words)
				}
			}
		}
	})

	// Bars that short and square are also what a fat font's side bars look
	// like, stacked in pairs like a colon. A letter in such a font must
	// stay a '?': its side bars are not colons and what is left of it is
	// not a minus sign.
	t.Run("fat letters are not colons round a minus", func(t *testing.T) {
		fat := renderOpts{H: 100, Stroke: 20, Gap: 2, Seed: 5}
		for _, text := range []string{"H", "E", "1H", "H1", "1H1", "E4"} {
			got, words := decode(t, render(text, fat))
			if confident(words) || !strings.Contains(got, "?") {
				t.Errorf("%s read %q; a letter must come back as '?' (%+v)", text, got, words)
			}
		}
		if got, words := decode(t, render("1058", fat)); got != "1058" {
			t.Errorf("fat digits read %q, want 1058 (%+v)", got, words)
		}
		// A fat "1" is a pair of square bars stacked astride the middle,
		// with no digit before it to make it a colon. Taken for one and
		// dropped, the display would read as blank.
		for _, want := range []string{"1", "11", "71"} {
			got, words := decode(t, render(want, fat))
			if len(words) == 0 || (got != want && confident(words)) {
				t.Errorf("fat %s read %q; want it, or a '?', never nothing (%+v)", want, got, words)
			}
		}
	})

	// The real housing round a filter window: the window's corners are
	// rounded, so the housing reaches in at each corner, and a bevel just
	// inside the housing catches the light as a thin line, broken by
	// glare. What the cut leaves of them must not cluster with the digits.
	t.Run("housing with round corners and a bevel", func(t *testing.T) {
		for _, side := range []int{9, 15} {
			img := onHousing(render("09", clean), side, side, side, side, housing)
			b := img.Bounds()
			win := image.Rect(side, side, b.Dx()-side, b.Dy()-side)
			for _, c := range []image.Point{win.Min, {win.Max.X - 1, win.Min.Y}, {win.Min.X, win.Max.Y - 1}, {win.Max.X - 1, win.Max.Y - 1}} {
				for dy := 0; dy < 8; dy++ {
					for dx := 0; dx < 8-dy; dx++ {
						x, y := c.X+dx, c.Y+dy
						if c.X > win.Min.X {
							x = c.X - dx
						}
						if c.Y > win.Min.Y {
							y = c.Y - dy
						}
						img.Set(x, y, housing)
					}
				}
			}
			bevel := color.RGBA{150, 60, 50, 255}
			for y := win.Min.Y + 10; y < win.Max.Y-10; y++ {
				if (y/9)%3 == 2 {
					continue // glare breaks the line
				}
				img.Set(win.Min.X+3, y, bevel)
				img.Set(win.Min.X+4, y, bevel)
				img.Set(win.Max.X-4, y, bevel)
				img.Set(win.Max.X-5, y, bevel)
			}
			for x := win.Min.X + 10; x < win.Max.X-10; x++ {
				if (x/11)%3 != 2 {
					img.Set(x, win.Max.Y-4, bevel)
				}
			}
			got, words := decode(t, img)
			if got != "09" || !confident(words) {
				t.Errorf("%d px of housing: read %q, want a confident 09 (%+v)", side, got, words)
			}
		}
	})

	// A small LED display overexposed: the glow fattens every bar by a
	// pixel or two, into the holes the decoder checks to tell a digit
	// from a solid blob. Ink that only reaches in from a bar is the bar.
	t.Run("small glowing digits", func(t *testing.T) {
		for _, c := range []struct {
			blur, spacing int
			gain          float64
		}{{2, 3, 1.5}, {1, 2, 2}, {1, 3, 2}} {
			o := small
			o.Spacing = c.spacing
			img := overexpose(boxBlur(render("80", o), c.blur), c.gain)
			if got, words := decode(t, img); got != "80" || !confident(words) {
				t.Errorf("blur %d, %d px apart, gain %.1f: read %q, want a confident 80 (%+v)", c.blur, c.spacing, c.gain, got, words)
			}
		}
	})

	// The glow is rarely even: on the real display the left bars of a "0"
	// came out two pixels fatter than the right ones, into the hole zone
	// the decoder checks for a solid blob, and the "0" read '?'.
	t.Run("glow fattening one side of a digit", func(t *testing.T) {
		o := small
		o.Spacing = 3
		for _, want := range []string{"00", "80", "08"} {
			img := render(want, o)
			W, margin := o.cellW(), max(2, o.H/5)
			for i := range want {
				x := margin + i*(W+o.Spacing) + o.Stroke // just inside the left bars
				for y := margin + o.Stroke; y < margin+o.H-o.Stroke; y++ {
					if f := float64(y-margin) / float64(o.H); f > 0.3 && f < 0.7 {
						continue // a glow halo, not a thicker bar: the middle is as it was
					}
					img.Set(x, y, led)
					img.Set(x+1, y, led)
				}
			}
			if got, words := decode(t, img); got != want || !confident(words) {
				t.Errorf("read %q, want a confident %s (%+v)", got, want, words)
			}
		}
	})

	// The washing machine's colon: big dots set low, the upper one just
	// below the middle, glowing until only two pixels part them.
	t.Run("low colon with its dots nearly touching", func(t *testing.T) {
		for _, at := range [][2]float64{{0.54, 0.85}, {0.54, 0.78}} {
			o := small
			o.Spacing, o.Dot, o.ColonAt = 2, 5, at
			for _, want := range []string{"1:03", "0:47"} {
				if got, words := decode(t, render(want, o)); got != want || !confident(words) {
					t.Errorf("dots at %v: read %q, want a confident %s (%+v)", at, got, want, words)
				}
			}
		}
	})

	// Blurred, a small "1" leading the reading breaks into two square dots
	// stacked like a colon. A colon with nothing before it is no colon;
	// dropped, it turned 1400 into a confident 400.
	t.Run("a blurred leading 1 is not dropped", func(t *testing.T) {
		o := small
		o.Spacing = 3
		got, words := decode(t, boxBlur(render("1400", o), 2))
		if got != "1400" && confident(words) {
			t.Errorf("read a confident %q, want 1400 or a '?' (%+v)", got, words)
		}
	})

	// The same glow can run a "1", the colon and a "0" into one blob as
	// wide as two digits, which then spells an "8". (A guard: the start of
	// this wave read the whole crop as one '?', and the fixes for small
	// digits above must not turn that into a confident wrong number.)
	t.Run("glyphs glowed into one blob are not a digit", func(t *testing.T) {
		for _, spacing := range []int{1, 2} {
			o := small
			o.Spacing, o.Dot = spacing, 5
			got, words := decode(t, boxBlur(render("1:03", o), 2))
			if got != "1:03" && confident(words) {
				t.Errorf("%d px apart: read a confident %q off 1:03 (%+v)", spacing, got, words)
			}
		}
	})

	// A minus sign whose centre rounds to a pixel above the row's middle
	// was dropped as a degree sign: -4.7 read a confident 4.7.
	t.Run("minus a pixel above the middle", func(t *testing.T) {
		if got, words := decode(t, render("-4.7", renderOpts{H: 40, Stroke: 6, Gap: 1, Seed: 1})); got != "-4.7" || !confident(words) {
			t.Errorf("read %q, want a confident -4.7 (%+v)", got, words)
		}
	})
	// Heavy sensor noise erodes the digits' bars at the tight threshold
	// until the measured stroke is half a solid minus sign's thickness,
	// and the minus was dropped as a fragment.
	t.Run("minus under sensor noise", func(t *testing.T) {
		for _, c := range []struct {
			text string
			h    int
			seed int64
		}{{"-4.7", 40, 1}, {"-0.5", 40, 2}, {"-12", 40, 3}, {"-12", 50, 1}, {"-12", 50, 2}, {"-0.5", 50, 4}, {"-0.5", 60, 2}, {"-4.7", 60, 1}} {
			o := renderOpts{H: c.h, Stroke: c.h * 14 / 100, Gap: 1, Blur: 1, Noise: 20, Seed: c.seed}
			if got, words := decode(t, render(c.text, o)); got != c.text && confident(words) {
				t.Errorf("%d px, seed %d: read a confident %q, want %s (%+v)", c.h, c.seed, got, c.text, words)
			}
		}
	})
	// On a small display a sliver of a gap splits a digit's middle bar
	// from its sides; laid out as a minus of its own it read the digit's
	// bars a second time: -4.7 read a confident -44.7.
	t.Run("a digit's middle bar is not a minus of its own", func(t *testing.T) {
		got, words := decode(t, render("-4.7", renderOpts{H: 16, Stroke: 2, Gap: 1, Seed: 1}))
		if got != "-4.7" && confident(words) {
			t.Errorf("read a confident %q, want -4.7 or a '?' (%+v)", got, words)
		}
	})

	// Blurred on a small display the point outgrows a point's size, or
	// fades at the threshold that reads the digits best. 0.00 read a
	// confident 000 and 90.1 a confident 901, a hundred and ten times
	// too big.
	t.Run("blurred point on a small display", func(t *testing.T) {
		if got, words := decode(t, render("0.00", renderOpts{H: 24, Stroke: 3, Gap: 1, Blur: 1, Seed: 1})); got != "0.00" {
			t.Errorf("read %q, want 0.00 (%+v)", got, words)
		}
		for seed := int64(1); seed <= 3; seed++ {
			got, words := decode(t, render("90.1", renderOpts{H: 24, Stroke: 3, Gap: 1, Blur: 1, Noise: 10, Seed: seed}))
			if got != "90.1" && confident(words) {
				t.Errorf("seed %d: read a confident %q, want 90.1 (%+v)", seed, got, words)
			}
		}
	})

	// Glowing, with the digits two pixels apart, the threshold that keeps
	// the digits apart is too tight for the point, and the one that keeps
	// the point runs the digits together. The point that only the loose
	// threshold saw goes in, marked unsure.
	t.Run("point only a looser threshold sees", func(t *testing.T) {
		o := renderOpts{H: 28, Stroke: 4, Gap: 0, Blur: 1, Noise: 8, Seed: 1, Spacing: 2}
		got, words := decode(t, render("90.1", o))
		if got != "90.1" {
			t.Errorf("read %q, want 90.1 (%+v)", got, words)
		}
		if confident(words) {
			t.Errorf("read %q as a sure reading; the point only one threshold saw must be marked unsure (%+v)", got, words)
		}
	})

	// Glow puts a bump on a bar's side that reaches into the sliver
	// between two small digits. The digits' facing sides still decide.
	t.Run("a bump of glow between two digits", func(t *testing.T) {
		o := small
		o.Spacing = 2
		img := render("00", o)
		x := max(2, o.H/5) + o.cellW() // the column just after the first "0"
		for y := 14; y < 16; y++ {
			img.Set(x, y, led)
		}
		if got, words := decode(t, img); got != "00" || !confident(words) {
			t.Errorf("read %q, want a confident 00 (%+v)", got, words)
		}
	})

	// A character LCD draws its digits in a 5x7 dot matrix. The dots are
	// not bars: no digit the display isn't showing may be read from them
	// with any confidence, and the reading as a whole is never a confident
	// number other than the one shown. (Blurred, the dots of a "1" run
	// into a bar and it may read as the "1" it is.)
	t.Run("dot-matrix characters are not digits", func(t *testing.T) {
		font := map[rune][7]string{
			'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
			'6': {"..##.", ".#...", "#....", "####.", "#...#", "#...#", ".###."},
			'.': {".....", ".....", ".....", ".....", ".....", ".##..", ".##.."},
		}
		ground, ink := color.RGBA{150, 170, 90, 255}, color.RGBA{40, 50, 40, 255}
		for _, pitch := range []int{7, 12} {
			img := image.NewRGBA(image.Rect(0, 0, 4*6*pitch+2*pitch, 9*pitch))
			rect(img, img.Bounds(), ground)
			for i, ch := range "1.61" {
				for row, line := range font[ch] {
					for col, c := range line {
						if c == '#' {
							x, y := pitch+(i*6+col)*pitch, pitch+row*pitch
							rect(img, image.Rect(x, y, x+pitch-1, y+pitch-1), ink)
						}
					}
				}
			}
			for _, blur := range []int{0, 1} {
				src := img
				if blur > 0 {
					src = boxBlur(img, blur)
				}
				got, words := decode(t, src)
				for _, w := range words {
					if w.Text >= "0" && w.Text <= "9" && w.Text != "1" && w.Text != "6" && w.Conf >= 60 {
						t.Errorf("pitch %d blur %d: read %q with a confident %q (%+v)", pitch, blur, got, w.Text, words)
						break
					}
				}
				if confident(words) && got != "1.61" {
					t.Errorf("pitch %d blur %d: read a confident %q off 1.61 (%+v)", pitch, blur, got, words)
				}
			}
		}
	})
}

// What two reviews of the forum-photo fixes found them breaking, each
// drawn synthetically: fonts and boxes the fixes had not been tried on.
// A case marked as a guard passed before the fixes too and pins what they
// must not start doing.
func TestSevenSegReviewedConditions(t *testing.T) {
	led := color.RGBA{230, 40, 40, 255}
	ledGround := color.RGBA{12, 10, 10, 255}
	housing := color.RGBA{230, 200, 60, 255}
	grey := color.RGBA{120, 120, 120, 255}
	rect := func(img *image.RGBA, r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	}
	// wrong reports a reading that is confident and not want: the one
	// outcome the decoder must never give. A '?' or a low score is honest.
	wrong := func(got, want string, words []Word) bool { return got != want && confident(words) }

	// The box takes in housing below the window only, and a glint on the
	// glass stands a few bars above the baseline just after the last
	// digit. The housing cut re-thresholds the window, where the glint
	// came out as a mark, and the mark passed for a point: "09." at 100.
	// A decimal point's foot is level with the digits' bottom bars.
	t.Run("a glint above the baseline is not a point", func(t *testing.T) {
		ib := inkBounds(render("09", clean), ledGround)
		x := ib.Max.X + clean.Stroke
		for _, below := range []int{12, 20} {
			for _, at := range []float64{0.6, 0.7, 0.75} {
				img := onHousing(render("09", clean), 0, 0, 0, below, housing)
				y := ib.Min.Y + int(at*float64(ib.Dy()))
				rect(img, image.Rect(x, y, x+4, y+7), led)
				got, words := decode(t, img)
				if got == "09." || wrong(got, "09", words) {
					t.Errorf("housing %d px below, glint at %.2f: read %q, want 09 or a '?' (%+v)", below, at, got, words)
				}
			}
		}
	})

	// A common LED font: the side bars run the full half height, beside
	// the top and bottom bars, with a small gap between. A fat horizontal
	// bar's end then faces the side bar next to it on as many rows as two
	// digits' sides do, and the clustering split every digit at that gap:
	// "9" read a confident "3", "907" a confident "1307", clean fat fonts
	// read '?'.
	t.Run("a font whose side bars stand beside its top and bottom bars", func(t *testing.T) {
		for _, want := range []string{"9", "8", "0", "2", "5", "908", "2468", "1234", "8888"} {
			for _, stroke := range []int{12, 14, 16, 18} {
				for _, ov := range []int{stroke/2 + 2, stroke + 2} { // beside half the bar, beside all of it
					o := renderOpts{H: 100, Stroke: stroke, Gap: 2, Seed: 1, Overlap: ov}
					got, words := decode(t, render(want, o))
					if got != want || !confident(words) {
						t.Errorf("%s at stroke %d, side bars %d px past the ends: read %q, want a confident %s (%+v)", want, stroke, ov, got, want, words)
					}
				}
			}
		}
		o := renderOpts{H: 140, Stroke: 14, Gap: 2, Seed: 5, Overlap: 16, Noise: 15, Blur: 1}
		if got, words := decode(t, render("907", o)); got != "907" || !confident(words) {
			t.Errorf("907 with noise: read %q, want a confident 907 (%+v)", got, words)
		}
	})

	// The same font, a clock with mild noise: the stacked ends of a
	// digit's bars are as square as a colon's dots, and were paired as a
	// second colon next to the real one (12::30), or as a colon between
	// two digits (9:07). A guard beside the colon-with-big-dots fix.
	t.Run("a fat font's bars are not a second colon", func(t *testing.T) {
		cases := []struct {
			want string
			o    renderOpts
		}{
			{"12:30", renderOpts{H: 140, Stroke: 22, Gap: 2, Seed: 5, Overlap: 24, Noise: 15, Blur: 1}},
			{"12:30", renderOpts{H: 140, Stroke: 22, Gap: 2, Seed: 5, Overlap: 24}},
			{"907", renderOpts{H: 80, Stroke: 13, Gap: 4, Seed: 5, Overlap: 6, Noise: 15, Blur: 1}},
		}
		for _, c := range cases {
			got, words := decode(t, render(c.want, c.o))
			if wrong(got, c.want, words) || (c.o.Noise == 0 && got != c.want) {
				t.Errorf("read %q, want %s (%+v)", got, c.want, words)
			}
		}
	})

	// Thin-stroke digits (every bar its own blob) in a few pixels of
	// housing, the box the docs call fine. After the housing cut the last
	// digit's side bars are as thin as a bevel hairline and as close to the
	// cut edge, and were erased as bevel: 1234 read a confident 123, 7.77
	// a confident 7.7. A bevel line is thinner than the digits' bars.
	t.Run("thin bars beside the housing cut are not bevel", func(t *testing.T) {
		thin := renderOpts{H: 80, Stroke: 4, Gap: 2, Seed: 4}
		lcd := thin
		lcd.Dark = true
		for _, want := range []string{"1234", "7.77", "-4.5"} {
			for _, px := range []int{4, 10} {
				cases := []struct {
					name string
					img  image.Image
				}{
					{"LCD in grey", onHousing(render(want, lcd), px, px, px, px, grey)},
					{"LED in yellow", onHousing(render(want, thin), px, px, px, px, housing)},
				}
				for _, c := range cases {
					got, words := decode(t, c.img)
					if wrong(got, want, words) {
						t.Errorf("%s, %s, %d px of housing: read a confident %q, want %s or a '?' (%+v)", want, c.name, px, got, want, words)
					}
				}
			}
			if got, words := decode(t, onHousing(render(want, lcd), 4, 4, 4, 4, grey)); got != want || !confident(words) {
				t.Errorf("%s, LCD in 4 px of grey: read %q, want a confident %s (%+v)", want, got, want, words)
			}
		}
	})

	// The box ends a little way into the last digit. The cut digit is
	// narrower than any whole one, and measured against it the whole
	// digits counted as glyphs run together: 56.7 read "??.7". A digit on
	// the crop's edge is not the measure.
	t.Run("a box cut into the last digit", func(t *testing.T) {
		img := render("56.7", renderOpts{H: 100, Stroke: 12, Gap: 2, Seed: 2})
		ib := inkBounds(img, ledGround)
		for _, into := range []int{5, 8} {
			cut := img.SubImage(image.Rect(ib.Min.X-3, ib.Min.Y-3, ib.Max.X-into, ib.Max.Y+3))
			got, words := decode(t, cut)
			if got != "56.7" && got != "56.?" {
				t.Errorf("%d px into the 7: read %q, want 56.7 or 56.? (%+v)", into, got, words)
			}
		}
	})

	// A point another threshold saw goes into a reading that has none,
	// flagged at 40. With noise on small digits that threshold sees a
	// fleck beside the minus sign or between two digits: 1234 read 123.4
	// and -1 read -.1, numbers ten times off, and a numeric trigger reads
	// the text alone. The fleck is no point: it stands clear of the
	// baseline, or beside a minus sign rather than between two numerals,
	// or at a level that broke the digits' glow into a colon too (111,
	// a guard: it read right before as well).
	t.Run("a fleck is not a missed point", func(t *testing.T) {
		cases := []struct {
			want string
			o    renderOpts
		}{
			{"1234", renderOpts{H: 30, Stroke: 3, Gap: 1, Noise: 18, Blur: 1, Seed: 3}},
			{"71", renderOpts{H: 30, Stroke: 3, Gap: 1, Noise: 18, Blur: 1, Seed: 3}},
			{"-1", renderOpts{H: 40, Stroke: 4, Gap: 1, Noise: 18, Blur: 1, Seed: 3}},
			{"-12", renderOpts{H: 40, Stroke: 4, Gap: 3, Blur: 1, Seed: 2}},
			{"2024", renderOpts{H: 40, Stroke: 4, Gap: 1, Noise: 18, Blur: 1, Seed: 3}},
			{"111", renderOpts{H: 40, Stroke: 2, Gap: 0, Noise: 18, Blur: 1, Seed: 3}},
		}
		for _, c := range cases {
			got, words := decode(t, render(c.want, c.o))
			if strings.Contains(got, ".") {
				t.Errorf("%s: read %q with a point the display has none of (%+v)", c.want, got, words)
			}
		}
	})

	// Blurred 30 px digits with a fat stroke and wide gaps: at one
	// threshold the digits dissolve into nothing and only the point stays,
	// and the reading was ".", at 92. A point with no digit is no reading.
	t.Run("a point with no digit is not a reading", func(t *testing.T) {
		for _, want := range []string{"25.4", "9.81", "88.8", "0.07"} {
			got, words := decode(t, render(want, renderOpts{H: 30, Stroke: 6, Gap: 3, Blur: 1, Seed: 2}))
			if got != "" && !strings.ContainsAny(got, "0123456789?") {
				t.Errorf("%s: read %q, a reading with no digit in it (%+v)", want, got, words)
			}
		}
	})

	// A sharp 30 px "7" with a 2 px stroke and a wide gap: its top bar
	// stands clear of its side bars and is clustered on its own. The side
	// bars alone spell "1", at 67. Ink over a cell that no probe reaches
	// belongs to the cell, and the cell is not a confident digit.
	t.Run("a thin 7 whose top bar stands clear is not a 1", func(t *testing.T) {
		for _, want := range []string{"7", "17"} {
			o := renderOpts{H: 30, Stroke: 2, Gap: 3, Shear: 0.12, Seed: 6}
			got, words := decode(t, render(want, o))
			if wrong(got, want, words) {
				t.Errorf("%s: read a confident %q, want %s or a '?' (%+v)", want, got, want, words)
			}
		}
	})

	// A small clock's glow fuses the colon to the digit after it and
	// leaves a pixel before it. That pixel was allowed as rounding, and
	// the same crop at preprocess upscale 2 or 3, where it is two or three
	// pixels, lost its colon: the dots went back to the bars, bridged the
	// digits round them, and a real 1:03 that read raw came back "?3".
	// What the camera shows reads the same at every upscale.
	t.Run("a colon fused to the digit after it reads at every upscale", func(t *testing.T) {
		// The digit after the colon is a "4", whose only left-hand bar
		// is the upper one, so a low colon's dots stand in the column
		// next to its ink without touching it.
		for _, want := range []string{"1:47", "0:47"} {
			o := renderOpts{H: 24, Stroke: 4, Gap: 0, Seed: 3, Spacing: 1, Dot: 5, ColonAt: [2]float64{0.54, 0.85}}
			img := render(want, o)
			// The colon's dots start a cell and a space in; move them a
			// pixel toward the digit after them.
			x0 := max(2, o.H/5) + o.cellW() + o.Spacing
			shiftRight(img, x0, x0+o.Dot-1, img.RGBAAt(0, 0))
			for _, k := range []int{1, 2, 3} {
				got, words := decode(t, upscale(img, k))
				if got != want || !confident(words) {
					t.Errorf("%s at upscale %d: read %q, want a confident %s (%+v)", want, k, got, want, words)
				}
			}
		}
	})

	// A strip of housing down one side of the box is cut only at a level
	// where the window inside it is as good as unlit (frameSideInnerMax).
	// Cut whenever the window is mostly dark, a thin "1" on the edge of a
	// tight box went as housing and 141 read a confident 4, a lone 1 with
	// housing beside it a confident 8. A guard: the best of the two
	// readings (the crop as drawn, the window) does not catch it, as the
	// cut reading scores the higher.
	t.Run("a thin 1 on the edge of a tight box is not housing", func(t *testing.T) {
		for _, text := range []string{"141", "14", "17", "11"} {
			for _, h := range []int{30, 50, 80} {
				for _, dark := range []bool{false, true} {
					o := renderOpts{H: h, Stroke: max(2, h*7/100), Gap: 0, Seed: 2, Dark: dark}
					img := render(text, o)
					got, words := decode(t, img.SubImage(inkBounds(img, img.RGBAAt(0, 0))))
					if wrong(got, text, words) {
						t.Errorf("%s at %d px, dark %v, in a tight box: read a confident %q (%+v)", text, h, dark, got, words)
					}
				}
			}
		}
		for _, stroke := range []int{6, 9} {
			img := render("1", renderOpts{H: 50, Stroke: stroke, Gap: 0, Seed: 2})
			ib := inkBounds(img, ledGround)
			win := img.SubImage(image.Rect(ib.Min.X-4, ib.Min.Y-4, ib.Max.X+4, ib.Max.Y+4))
			got, words := decode(t, onHousing(win, 0, 0, 8, 0, housing))
			if wrong(got, "1", words) {
				t.Errorf("1 at stroke %d with 8 px of housing on its right: read a confident %q (%+v)", stroke, got, words)
			}
		}
	})

	// The box ends a little above the rim of a red LED window, whose glow
	// leaves flat slivers along the crop's bottom edge, well under the
	// digits' feet: one between the digits, one under a digit. The one
	// under the digit was folded into it and moved its baseline down to
	// the edge, and the other, no wider than a point, passed for a point
	// the box had cut: 09 read 0.9 at 100, and with the slivers under and
	// after the last digit, 09. at 100. A point is only cut when the
	// digits' feet are; with their feet inside the crop, a sliver on the
	// bottom edge is rim or glow.
	t.Run("a glow sliver on the bottom edge is not a cut point", func(t *testing.T) {
		margin := max(2, clean.H/5)
		W, spacing := clean.cellW(), int(math.Round(float64(clean.cellW())*0.4))
		x0, x9 := margin, margin+W+spacing // the digits' cells
		bottom := 2*margin + clean.H
		cases := []struct {
			name    string
			slivers []image.Rectangle
		}{
			{"between the digits", []image.Rectangle{image.Rect(x0+W+2, bottom-3, x9, bottom)}},
			{"between the digits and under the 0", []image.Rectangle{image.Rect(x0+W+2, bottom-3, x9, bottom), image.Rect(x0+4, bottom-2, x0+14, bottom)}},
			{"under and after the 9", []image.Rectangle{image.Rect(x9+33, bottom-2, x9+43, bottom), image.Rect(x9+W+3, bottom-1, x9+W+15, bottom)}},
		}
		for _, c := range cases {
			for _, blur := range []int{0, 1} {
				img := render("09", clean)
				for _, r := range c.slivers {
					rect(img, r, led)
				}
				if blur > 0 {
					img = boxBlur(img, blur)
				}
				got, words := decode(t, img)
				if got != "09" || !confident(words) {
					t.Errorf("%s, blur %d: read %q, want a confident 09 (%+v)", c.name, blur, got, words)
				}
			}
		}
	})

	// A washing machine's 23 px "03", the digits glowing to within two
	// pixels of each other. The "3" has no left-hand bars, so its cell is
	// laid out from the right as wide as the "0"; laid over the 0's right
	// bar, it read that bar as its own e and f: "08" at 77. The widened
	// cell stops at the glyph before it.
	t.Run("a narrow 3 after a glowing 0 keeps to its own cell", func(t *testing.T) {
		for _, want := range []string{"03", "07", "83"} {
			for _, h := range []int{23, 24} {
				for _, fat := range []int{1, 2} {
					o := renderOpts{H: h, Stroke: 4, Gap: 0, Seed: 3, Spacing: 1}
					img := render(want, o)
					// The glow fattens the first digit's right bars by a
					// pixel or two, into the space before the next digit.
					margin, W := max(2, o.H/5), o.cellW()
					for y := margin; y < margin+o.H; y++ {
						if img.RGBAAt(margin+W-1, y) == led {
							rect(img, image.Rect(margin+W, y, margin+W+fat, y+1), led)
						}
					}
					got, words := decode(t, img)
					// Two pixels of glow make the first digit 1.6 times the
					// width of the second, which reads as glyphs run together
					// ('?'); the digit after it must still be right.
					if got != want && (fat == 1 || !strings.Contains(got, "?") || wrong(got, want, words)) {
						t.Errorf("%s at %d px, glow %d px: read %q, want %s (%+v)", want, h, fat, got, want, words)
					}
				}
			}
		}
	})

	// A box with a band of housing down one side and nothing readable in
	// the window it leaves: the window read as nothing (score 0), which
	// outscored the '?' reading of the crop as drawn and replaced it, so
	// "1:" with 14 px of housing on its left came back empty where the
	// glyphs it did read ("?1", a 1 at 78) had told the user something
	// was there. A window that reads as nothing says nothing.
	t.Run("an empty window does not replace a ? reading", func(t *testing.T) {
		h40 := renderOpts{H: 40, Stroke: 5, Gap: 1, Seed: 1}
		h24 := renderOpts{H: 24, Stroke: 3, Gap: 1, Seed: 1}
		cases := []struct {
			text, want string
			o          renderOpts
			l, b       int
		}{
			{"1:", "1", h24, 14, 0},
			{"-", "-", h40, 14, 0},
			{"8:88", "8:88", h24, 0, 14},
		}
		for _, c := range cases {
			got, words := decode(t, onHousing(render(c.text, c.o), c.l, 0, 0, c.b, housing))
			if len(words) == 0 {
				t.Errorf("%s with %d px of housing left, %d below: read nothing, want the glyphs of the crop as drawn", c.text, c.l, c.b)
			}
			if wrong(got, c.want, words) {
				t.Errorf("%s with %d px of housing left, %d below: read a confident %q (%+v)", c.text, c.l, c.b, got, words)
			}
		}
	})
}

// A decimal point that only another threshold saw goes into the reading
// flagged, and only where a point can be: between two numerals. The
// readings are built by hand so each rule is pinned on its own.
func TestSevenSegMissedPoint(t *testing.T) {
	g := func(ch byte, x float64) glyph { return glyph{ch: ch, conf: 100, x: x} }
	text := func(r reading) string {
		var b strings.Builder
		for _, gl := range r.glyphs {
			b.WriteByte(gl.ch)
		}
		return b.String()
	}
	twelve := reading{glyphs: []glyph{g('1', 10), g('2', 30)}}
	cases := []struct {
		name  string
		cur   reading
		level reading
		want  string
	}{
		{"point between two numerals", twelve, reading{glyphs: []glyph{g('1', 10), g('.', 20), g('2', 30)}}, "1.2"},
		{"a colon is not a point", twelve, reading{glyphs: []glyph{g('1', 10), g(':', 20), g('2', 30)}}, "12"},
		{"two points are ghosts", twelve, reading{glyphs: []glyph{g('1', 10), g('.', 20), g('2', 30), g('.', 40)}}, "12"},
		{"point before the first digit", twelve, reading{glyphs: []glyph{g('.', 5), g('1', 10), g('2', 30)}}, "12"},
		{"point after the last digit", twelve, reading{glyphs: []glyph{g('1', 10), g('2', 30), g('.', 40)}}, "12"},
		{"point after a minus sign", reading{glyphs: []glyph{g('-', 10), g('1', 30)}}, reading{glyphs: []glyph{g('-', 10), g('.', 20), g('1', 30)}}, "-1"},
		{"point after a '?'", reading{glyphs: []glyph{g('?', 10), g('2', 30)}}, reading{glyphs: []glyph{g('.', 20), g('2', 30)}}, "?2"},
		{"reading already has its point", reading{glyphs: []glyph{g('1', 10), g('.', 15), g('2', 30)}}, reading{glyphs: []glyph{g('1', 10), g('.', 20), g('2', 30)}}, "1.2"},
	}
	for _, c := range cases {
		got := withMissedPoint(c.cur, []reading{c.level})
		if text(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, text(got), c.want)
			continue
		}
		for _, gl := range got.glyphs {
			if gl.ch == '.' && c.name == "point between two numerals" && gl.conf > missedPointConf {
				t.Errorf("%s: the point went in at %.0f, want at most %d", c.name, gl.conf, missedPointConf)
			}
		}
	}
}

// upscale is preprocess upscale: each pixel becomes a k by k block.
func upscale(img image.Image, k int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := 0; y < b.Dy()*k; y++ {
		for x := 0; x < b.Dx()*k; x++ {
			out.Set(x, y, img.At(b.Min.X+x/k, b.Min.Y+y/k))
		}
	}
	return out
}

// shiftRight moves columns x0..x1 of img one pixel to the right, filling
// column x0 with bg.
func shiftRight(img *image.RGBA, x0, x1 int, bg color.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := x1; x >= x0; x-- {
			img.SetRGBA(x+1, y, img.RGBAAt(x, y))
		}
		img.SetRGBA(x0, y, bg)
	}
}

// bench400x150 is a 400x150 crop, about what a drawn box round a four-digit
// display comes to: the render centred on a canvas of the display's ground.
func bench400x150(text string, o renderOpts) *image.RGBA {
	src := render(text, o)
	dst := image.NewRGBA(image.Rect(0, 0, 400, 150))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(src.At(0, 0)), image.Point{}, draw.Src)
	off := image.Pt((400-src.Bounds().Dx())/2, (150-src.Bounds().Dy())/2)
	draw.Draw(dst, src.Bounds().Add(off), src, image.Point{}, draw.Src)
	return dst
}

// The decoder runs on every poll of a sevenseg watch, so it has to stay
// fast: one decode of a 400x150 crop, clean and as a camera would see it.
func BenchmarkSevenSeg400x150(b *testing.B) {
	cases := []struct {
		name string
		o    renderOpts
	}{
		{"clean", renderOpts{H: 100, Stroke: 12, Gap: 2, Seed: 1}},
		{"camera", renderOpts{H: 100, Stroke: 12, Gap: 2, Noise: 18, Blur: 1, Ghost: 0.25, Seed: 8}},
	}
	for _, c := range cases {
		img := bench400x150("123.4", c.o)
		eng := NewSevenSeg()
		if got, _ := eng.Recognize(context.Background(), img); got != "123.4" {
			b.Fatalf("%s: read %q, want 123.4", c.name, got)
		}
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, _, err := eng.RecognizeWords(context.Background(), img); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
