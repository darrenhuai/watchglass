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
	DotGap    int     // gap before a decimal point; -1 = the normal spacing, 0 = touching
	Bezel     int     // bright line this many px tall along the crop's top edge
	EdgeLine  int     // bright line this many px wide down the crop's left edge
	Shear     float64 // italic lean, x per row (positive: top leans right)
	dotGapSet bool    // DotGap was chosen; the zero value keeps the normal spacing
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
	dot := max(2, int(math.Round(float64(o.H)*0.12)))
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
			for _, cy := range []int{y + o.H*3/10, y + o.H*7/10} {
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
		s, g, H := o.Stroke, o.Gap, o.H
		type bar struct {
			name byte
			rect func(t int) image.Rectangle
		}
		bars := []bar{ // a fixed order so the jitter sequence is reproducible
			{'a', func(t int) image.Rectangle { return image.Rect(x+s+g, y, x+W-s-g, y+t) }},
			{'g', func(t int) image.Rectangle { return image.Rect(x+s+g, y+(H-t)/2, x+W-s-g, y+(H-t)/2+t) }},
			{'d', func(t int) image.Rectangle { return image.Rect(x+s+g, y+H-t, x+W-s-g, y+H) }},
			{'f', func(t int) image.Rectangle { return image.Rect(x, y+s+g, x+t, y+(H-s)/2-g) }},
			{'b', func(t int) image.Rectangle { return image.Rect(x+W-t, y+s+g, x+W, y+(H-s)/2-g) }},
			{'e', func(t int) image.Rectangle { return image.Rect(x, y+(H+s)/2+g, x+t, y+H-s-g) }},
			{'c', func(t int) image.Rectangle { return image.Rect(x+W-t, y+(H+s)/2+g, x+W, y+H-s-g) }},
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
}
