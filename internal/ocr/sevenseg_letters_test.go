package ocr

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"os"
	"strings"
	"testing"

	"github.com/darrenhuai/watchglass/internal/config"
	"github.com/darrenhuai/watchglass/internal/imgproc"
)

// Letters on a seven-segment display (SevenSeg.Letters): fault codes and
// status words. Off, every cell reads as it always did; on, the letters
// of letterPatterns read as letters, never into a number (that is the
// trigger's job), and only when they are sure.

// readWith decodes img with the letter table on or off.
func readWith(t *testing.T, img image.Image, letters bool) (string, []Word) {
	t.Helper()
	text, words, err := NewSevenSeg().WithLetters(letters).RecognizeWords(context.Background(), img)
	if err != nil {
		t.Fatalf("RecognizeWords: %v", err)
	}
	return text, words
}

// hasLetter reports whether a reading holds a letter.
func hasLetter(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' })
}

// The codes and words the recipes mention, on a clean render and a noisy
// LCD render. O reads 0: the digit wins where the bars are the same.
var letterCases = []struct{ text, want string }{
	{"E4", "E4"}, {"F3", "F3"}, {"End", "End"}, {"A1", "A1"}, {"H2", "H2"}, {"LO", "L0"},
	{"PASS", "PA55"}, {"Err", "Err"}, {"dEF", "dEF"}, {"hELP", "hELP"},
}

var lcdNoisy = renderOpts{H: 60, Stroke: 8, Gap: 2, Dark: true, Noise: 6, Blur: 1}

func TestSevenSegLettersOn(t *testing.T) {
	for _, c := range letterCases {
		for name, o := range map[string]renderOpts{"clean": clean, "lcd": lcdNoisy} {
			got, words := readWith(t, render(c.text, o), true)
			if got != c.want {
				t.Errorf("%s (%s): read %q, want %q (%v)", c.text, name, got, c.want, words)
			}
		}
	}
	// A display standing by shows dashes and nothing else: two or more
	// read as dashes. One dash alone stays '?', both ways: a reading
	// eroded to a digit's middle bar looks the same (a gauge's -7 at
	// Binarize 128 read a confident '-').
	for name, o := range map[string]renderOpts{"clean": clean, "lcd": lcdNoisy} {
		for _, c := range []struct{ text, want string }{{"--", "--"}, {"---", "---"}, {"-", "?"}} {
			if got, _ := readWith(t, render(c.text, o), true); got != c.want {
				t.Errorf("%s (%s): read %q, want %q", c.text, name, got, c.want)
			}
		}
		if got, _ := readWith(t, render("-", o), false); got != "?" {
			t.Errorf("- (%s) with the table off: read %q, want ?", name, got)
		}
	}
}

// A lower-case letter reads only within a digit's width of a capital
// letter, or of a lower-case letter before it that is (End, Err, nCr):
// the lower loop of a digit too pale for the threshold spells a u or a c,
// and an odometer's pale last 6 read "10561u1" beside its 1. Beside digits
// alone, or far from everything, it is no letter.
func TestSevenSegLowerCaseBesideCapitals(t *testing.T) {
	for name, o := range map[string]renderOpts{"clean": clean, "lcd": lcdNoisy} {
		for _, text := range []string{"1056 u", "1056u", "1u", "u1", "ou"} {
			if got, _ := readWith(t, render(text, o), true); hasLetter(got) {
				t.Errorf("%s (%s): read %q, a lower-case letter with no capital beside it", text, name, got)
			}
		}
		for _, c := range []struct{ text, want string }{{"nCr", "nCr"}, {"Err", "Err"}, {"End", "End"}} {
			if got, _ := readWith(t, render(c.text, o), true); got != c.want {
				t.Errorf("%s (%s): read %q, want %q", c.text, name, got, c.want)
			}
		}
	}
	// The real thing: a VW odometer whose pale last 6 came apart at the
	// one threshold that read it (testdata/photos/odo-vw.jpg): never a
	// letter, with the table on or off.
	crop := imgproc.Apply(imgproc.Crop(loadPhoto(t, vwBox.file), region(loadPhoto(t, vwBox.file), vwBox.box)), config.Preprocess{})
	for _, on := range []bool{true, false} {
		got, _ := readWith(t, crop, on)
		if hasLetter(got) || !strings.HasPrefix(got, "1056") {
			t.Errorf("odometer 105616 (letters %v): read %q, want 1056 and no letter", on, got)
		}
	}
}

// b, y and U are a 6, a 9 and a 0 without the top bar. b shares its bars
// with the 6 drawn that way, so the digit wins: it reads 6 as with the
// table off, and b only beside another letter (bE). y and U read when a
// second threshold level agrees, or another letter stands in the row; a
// clean render has one level, so alone with digits they are '?'. The
// real thing: a Jeep
// odometer through Binarize 192 (one level), whose first 0 loses its
// right bars and spells C beside a sliver (testdata/photos/odo-jeep.jpg);
// and the VW odometer, whose 6 spelled b at the one level that read it,
// is in TestSevenSegLowerCaseBesideCapitals.
func TestSevenSegToplessLettersNeedASecondOpinion(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"bE", "bE"}, {"b1", "61"}, {"U1", "?1"}, {"y2", "?2"}, {"b", "6"},
		{"105b", "1056"}, {"1056", "1056"}, {"PLUS", "PLU5"}, {"dEF", "dEF"},
		{"bAt", "bAt"}, {"Prob", "Prob"},
	} {
		if got, _ := readWith(t, render(c.text, clean), true); got != c.want {
			t.Errorf("%s: read %q, want %q", c.text, got, c.want)
		}
	}
	jeep := photoBox{file: "odo-jeep.jpg", box: image.Rect(16, 16, 298, 79)}
	img := loadPhoto(t, jeep.file)
	crop := imgproc.Apply(imgproc.Crop(img, region(img, jeep.box)), config.Preprocess{Threshold: 192})
	if got, _ := readWith(t, crop, true); hasLetter(got) || !strings.HasSuffix(got, "91308") {
		t.Errorf("Jeep odometer 091308 at Binarize 192 with letters: read %q, want no letter and the digits 91308", got)
	}
	if got, _ := readWith(t, crop, false); got != "091308" {
		t.Errorf("Jeep odometer 091308 at Binarize 192 without letters: read %q", got)
	}
	plain := imgproc.Apply(imgproc.Crop(img, region(img, jeep.box)), config.Preprocess{})
	if got, _ := readWith(t, plain, true); got != "091308" {
		t.Errorf("Jeep odometer 091308 with letters: read %q", got)
	}
}

// A letter is read from a glyph as tall as the digits, and never on the
// crop's edge, judged in the crop's own coordinates: a VCR clock's 4, cut
// by a box shrunk by 6% and moved left (testdata/photos/clock-vcr-1234.jpg),
// spelled L over half the row on the crop's right edge once the italic
// digits were de-sheared.
func TestSevenSegCutGlyphIsNoLetter(t *testing.T) {
	vcr := photoBox{file: "clock-vcr-1234.jpg", box: image.Rect(16, 16, 393, 177)}
	img := loadPhoto(t, vcr.file)
	if got, _ := readWith(t, imgproc.Apply(imgproc.Crop(img, region(img, vcr.box)), config.Preprocess{}), true); got != "12:34" {
		t.Errorf("VCR 12:34 with letters: read %q", got)
	}
	// Boxes about 12% smaller and moved left, cutting the 4: with neither
	// rule, the second read a confident 12:3L, the third and fourth 1?:?L
	// and 1?:3L.
	for _, shrunk := range []image.Rectangle{
		image.Rect(24, 26, 24+332, 26+142),
		image.Rect(23, 26, 353, 170),
		image.Rect(23, 25, 351, 163),
		image.Rect(23, 27, 353, 169),
	} {
		for _, on := range []bool{true, false} {
			if got, _ := readWith(t, imgproc.Apply(imgproc.Crop(img, region(img, shrunk)), config.Preprocess{}), on); hasLetter(got) {
				t.Errorf("VCR 12:34 in the box %v (letters %v): read %q, a letter for a cut digit", shrunk, on, got)
			}
		}
	}
}

// With the table off nothing changes: a letter is a '?' (and End, whose
// n is dropped as a fragment, one '?'), and no reading ever holds a letter.
func TestSevenSegLettersOff(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"E4", "?4"}, {"F3", "?3"}, {"End", "?"}, {"A1", "?1"}, {"H2", "?2"}, {"--", "?"},
	} {
		if got, _ := readWith(t, render(c.text, clean), false); got != c.want {
			t.Errorf("%s: read %q, want %q", c.text, got, c.want)
		}
	}
	for _, c := range letterCases {
		for _, o := range []renderOpts{clean, lcdNoisy} {
			if got, _ := readWith(t, render(c.text, o), false); hasLetter(got) {
				t.Errorf("%s: read %q with the letter table off", c.text, got)
			}
		}
	}
	// Each letter alone: never a letter with the table off. (Alone, with
	// no digit-height glyph beside it, a lower-case letter or an L has
	// nothing to be laid out against and may read as bars, both ways.)
	for _, ch := range letterPatterns {
		s := string(rune(ch))
		for _, o := range []renderOpts{clean, lcdNoisy} {
			if got, _ := readWith(t, render(s, o), false); hasLetter(got) {
				t.Errorf("%s alone: read %q with the letter table off", s, got)
			}
		}
	}
}

// Letters and digits in one reading keep their confidences, one Word per
// glyph, so Test this region can show each letter's chip.
func TestSevenSegLetterWordsMatchGlyphs(t *testing.T) {
	text, words := readWith(t, render("E4", clean), true)
	if text != "E4" || len(words) != 2 || words[0].Text != "E" || words[1].Text != "4" || words[0].Conf < 90 || words[1].Conf < 90 {
		t.Errorf("E4: text %q, words %+v", text, words)
	}
}

// A 6 without its top bar is a b. Half a top bar is neither: the cell is
// unsure, the text says '?' and the Word keeps the glyph seen, exactly as
// an unsure digit is reported. With the table off it is a 6 or '?', never
// a letter.
func TestSevenSegDamagedSixIsUnsure(t *testing.T) {
	for _, o := range []renderOpts{clean, lcdNoisy} {
		// bE, with half of the b's top bar lit, over the left of its
		// cell: beside the E the b would read (see
		// TestSevenSegToplessLettersNeedASecondOpinion), but half a bar
		// is neither a b nor a 6.
		img := render("bE", o)
		W := o.cellW()
		margin := max(2, o.H/5)
		fg := color.RGBA{230, 40, 40, 255}
		if o.Dark {
			fg = color.RGBA{30, 30, 34, 255}
		}
		draw.Draw(img, image.Rect(margin+o.Stroke+o.Gap, margin, margin+W/2, margin+o.Stroke), image.NewUniform(fg), image.Point{}, draw.Src)
		got, words := readWith(t, img, true)
		if got != "?E" || len(words) != 2 || !HasUnsureDigit(got, words) || words[0].Text == "?" {
			t.Errorf("damaged 6 before an E (dark=%v): read %q, words %+v; want ?E with the glyph seen in its Word", o.Dark, got, words)
		}
		if got, _ := readWith(t, img, false); hasLetter(got) {
			t.Errorf("damaged 6 before an E (dark=%v) with the table off: read %q", o.Dark, got)
		}
	}
	// A whole b beside another letter reads b; alone it is the 6 drawn
	// without its top bar, and the digit wins (see
	// TestSevenSegToplessLettersNeedASecondOpinion); a whole 6 reads 6,
	// both ways. Off, the E is a '?'.
	for _, c := range []struct {
		text, on, off string
	}{{"bE", "bE", "6?"}, {"b", "6", "6"}, {"6", "6", "6"}} {
		if got, _ := readWith(t, render(c.text, clean), true); got != c.on {
			t.Errorf("%s with the table on: read %q, want %q", c.text, got, c.on)
		}
		if got, _ := readWith(t, render(c.text, clean), false); got != c.off {
			t.Errorf("%s with the table off: read %q, want %q", c.text, got, c.off)
		}
	}
}

// A 6 whose top bar has faded, or that a display draws without it, in
// numbers on noisy, blurred and LCD renders, where several threshold
// levels agree on what they see: never a b, with the table on it reads
// what it reads with the table off. A numeric watch would skip a b, but a
// text watch would see 16 flicker to 1b.
func TestSevenSegFadedSixIsNoLetter(t *testing.T) {
	blur := renderOpts{H: 50, Stroke: 7, Gap: 2, Blur: 3, Noise: 12, Seed: 7}
	opts := map[string]renderOpts{"clean": clean, "lcd": lcdNoisy, "blur": blur,
		"ghost": {H: 60, Stroke: 8, Gap: 2, Dark: true, Ghost: 0.35, Noise: 4, Seed: 3},
		"dim45": {H: 60, Stroke: 8, Gap: 2, DimIndex: 0, DimLevel: 0.45, Noise: 3, Seed: 5}}
	for _, p := range [][2]string{{"16", "1b"}, {"65", "b5"}, {"26.5", "2b.5"}, {"106", "10b"}, {"1264", "12b4"}} {
		for name, o := range opts {
			six, topless := render(p[0], o), render(p[1], o)
			for _, fade := range []float64{0.5, 0.65, 0.8, 1} {
				img := image.NewRGBA(six.Bounds())
				for i := range six.Pix {
					img.Pix[i] = uint8(float64(six.Pix[i])*(1-fade) + float64(topless.Pix[i])*fade + 0.5)
				}
				on, words := readWith(t, img, true)
				off, _ := readWith(t, img, false)
				if hasLetter(on) || on != off {
					t.Errorf("%s (%s) with its 6's top bar %.0f%% faded: read %q with letters, %q without (%v)", p[0], name, 100*fade, on, off, words)
				}
			}
		}
	}
}

// Digits stay digits with the table on. A row of 4s and 1s has no bar
// across its top or bottom, so its ink is shorter than its digits; the
// right bars of a 4 a sliver after its left bar and middle bar are the
// 4's, not a lower-case letter's start (clusterBars' tooWide): "14" read
// 1??, and dim renders a confident 1. Every render, and every segment
// font from thin to fat, gapped or with the side bars running past the
// others, reads as it does with the table off.
func TestSevenSegLettersKeepDigits(t *testing.T) {
	texts := []string{"4", "14", "41", "44", "144", "-14", "1.4", "74", "47", "11.4", "12:41"}
	opts := map[string]renderOpts{"clean": clean, "lcd": lcdNoisy,
		"dim45":   {H: 60, Stroke: 8, Gap: 2, DimIndex: 0, DimLevel: 0.45, Noise: 3, Seed: 5},
		"dim30i1": {H: 60, Stroke: 8, Gap: 2, DimIndex: 1, DimLevel: 0.3, Noise: 3, Seed: 6},
		"ghost":   {H: 60, Stroke: 8, Gap: 2, Dark: true, Ghost: 0.35, Noise: 4, Seed: 3},
		"ghostL":  {H: 60, Stroke: 8, Gap: 2, Ghost: 0.3, Noise: 4, Seed: 4},
		"shear":   {H: 60, Stroke: 8, Gap: 2, Shear: 0.15, Noise: 3, Seed: 9},
	}
	for name, o := range opts {
		for _, s := range texts {
			img := render(s, o)
			on, words := readWith(t, img, true)
			if off, _ := readWith(t, img, false); on != off {
				t.Errorf("%s (%s): read %q with letters, %q without (%v)", s, name, on, off, words)
			}
		}
	}
	for _, s := range []string{"4", "14", "41", "44", "-14"} {
		for name, o := range map[string]renderOpts{"clean": clean, "lcd": lcdNoisy} {
			if got, words := readWith(t, render(s, o), true); got != s {
				t.Errorf("%s (%s) with letters: read %q (%v)", s, name, got, words)
			}
		}
	}
	for stroke := 5; stroke <= 12; stroke++ {
		for gap := 1; gap <= 3; gap++ {
			for _, overlap := range []int{0, stroke / 2} {
				o := renderOpts{H: 60, Stroke: stroke, Gap: gap, Overlap: overlap, Noise: 3, Seed: int64(10*stroke + gap)}
				for _, s := range []string{"14", "41", "44", "-14"} {
					img := render(s, o)
					on, _ := readWith(t, img, true)
					if off, _ := readWith(t, img, false); on != off {
						t.Errorf("%s (stroke %d, gap %d, overlap %d): read %q with letters, %q without", s, stroke, gap, overlap, on, off)
					}
				}
			}
		}
	}
}

// A thermostat's 22.5 with a degree sign a sliver after the 5
// (testdata/photos/therm-vaillant-225.jpg): at one threshold the sign's
// left arc stands alone, an upright piece. The upper-right bar of a U is
// upright too, and with the table on such a bar is no mark, but only with
// the U's lower bar under it: the arc has nothing under it and stays a
// mark, so the 5 isn't run into the sign (on the original photo a box
// grown by 6% read 22.?; on this crop, the boxes reaching furthest past
// the sign do). Letters on, every box reads as with them off.
func TestSevenSegDegreeSignStaysApart(t *testing.T) {
	img := loadPhoto(t, "therm-vaillant-225.jpg")
	for _, box := range []image.Rectangle{
		image.Rect(34, 30, 204, 132), // the corpus's box
		image.Rect(24, 24, 214, 138), // grown by 6%
		image.Rect(24, 20, 230, 142),
		image.Rect(26, 20, 230, 142),
		image.Rect(28, 20, 230, 142),
	} {
		crop := imgproc.Apply(imgproc.Crop(img, region(img, box)), config.Preprocess{})
		for _, on := range []bool{true, false} {
			if got, words := readWith(t, crop, on); got != "22.5" {
				t.Errorf("box %v (letters %v): read %q (%v), want 22.5", box, on, got, words)
			}
		}
	}
}

// A letter is read only where the box leaves it whole and clear: a digit
// the box or the housing cuts a bar off spells a letter (0s cut along the
// top spell U, a 6 cut on the right spells E), so a letter on the box's
// edge reads '?'.
func TestSevenSegLetterOnTheEdgeIsUnsure(t *testing.T) {
	zeros := render("00", clean)
	b := zeros.Bounds()
	// The box cuts the top bar off: what is left of each 0 is a U.
	cut := zeros.SubImage(image.Rect(b.Min.X, b.Min.Y+clean.H/5+clean.Stroke+2, b.Max.X, b.Max.Y)).(*image.RGBA)
	if got, _ := readWith(t, cut, true); hasLetter(got) {
		t.Errorf("00 cut along the top: read %q, a letter for a cut digit", got)
	}
	// E4 with the box drawn on the E's left bars: the E is cut.
	e4 := render("E4", clean)
	b = e4.Bounds()
	tight := e4.SubImage(image.Rect(b.Min.X+clean.H/5+1, b.Min.Y, b.Max.X, b.Max.Y)).(*image.RGBA)
	if got, _ := readWith(t, tight, true); got != "?4" {
		t.Errorf("E4 with the box on the E's edge: read %q, want ?4", got)
	}
}

// A VFD oven showing End (testdata/photos/oven-end-vfd.jpg, see the README
// there): the glow runs the E into the n, and the n stands in the lower
// half of the row. Off, it read one '?'.
func TestSevenSegLetterPhoto(t *testing.T) {
	img := loadPhoto(t, "oven-end-vfd.jpg")
	box := image.Rect(16, 16, 487, 292)
	crop := imgproc.Apply(imgproc.Crop(img, region(img, box)), config.Preprocess{})
	if got, _ := readWith(t, crop, true); got != "End" {
		t.Errorf("oven End with letters: read %q", got)
	}
	if got, _ := readWith(t, crop, false); got != "?" {
		t.Errorf("oven End without letters: read %q, want ?", got)
	}
}

// The recipes' samples, read as their watches read them (the boxes are the
// recipes' regions on the 800x450 frames).
func TestSevenSegLetterSamples(t *testing.T) {
	for _, c := range []struct {
		file           string
		box            image.Rectangle
		withL, without string
	}{
		{"boiler-seven-segment-fault.jpg", image.Rect(310, 140, 490, 280), "E4", "?4"},
		{"boiler-seven-segment-temp.jpg", image.Rect(310, 140, 490, 280), "52", "52"},
		{"washer-end.jpg", image.Rect(344, 145, 632, 272), "End", "?"},
		{"washer-0-04.jpg", image.Rect(344, 145, 632, 272), "0:04", "0:04"},
	} {
		img := loadSample(t, c.file)
		crop := imgproc.Apply(imgproc.Crop(img, region(img, c.box)), config.Preprocess{})
		if got, _ := readWith(t, crop, true); got != c.withL {
			t.Errorf("%s with letters: read %q, want %q", c.file, got, c.withL)
		}
		if got, _ := readWith(t, crop, false); got != c.without {
			t.Errorf("%s without letters: read %q, want %q", c.file, got, c.without)
		}
	}
}

// loadSample decodes one of the recipes' sample frames.
func loadSample(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open("../../docs/recipes/samples/" + name)
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

// Engines.ForWatch hands a watch the decoder with its letter table set,
// and leaves the other engines alone.
func TestEnginesForWatchLetters(t *testing.T) {
	e := Engines{SevenSeg: NewSevenSeg()}
	on, err := e.ForWatch("sevenseg", true)
	if err != nil || !on.(*SevenSeg).Letters {
		t.Fatalf("ForWatch(sevenseg, true) = %#v, %v", on, err)
	}
	off, err := e.ForWatch("sevenseg", false)
	if err != nil || off.(*SevenSeg).Letters {
		t.Fatalf("ForWatch(sevenseg, false) = %#v, %v", off, err)
	}
	if e.SevenSeg.(*SevenSeg).Letters {
		t.Error("ForWatch changed the shared decoder")
	}
	if _, err := e.ForWatch("tesseract", true); err == nil {
		t.Error("ForWatch(tesseract) with no tesseract should fail like For")
	}
}
