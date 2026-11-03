package ocr

import (
	"context"
	"image"
	"image/color"
	"math"
	"sort"
	"strings"
	"sync"
)

// SevenSeg reads seven-segment digit displays — bench scales, multimeters,
// thermometers, clocks — by geometry rather than by a font model: it
// binarizes the crop, groups the lit bars into glyph cells, and probes the
// seven segment positions of every cell. Pure Go, no external binary. It
// takes the crop as imgproc.Apply leaves it, preprocessed or raw; polarity
// is worked out here, so lit-LED-on-dark and dark-LCD-on-light displays
// both read with no preprocess settings.
//
// Recognize returns the digits joined ("23.5", "-8.0", "12:34");
// RecognizeWords adds one Word per glyph, its Conf saying how far every
// segment sat from the on/off decision. A glyph whose bars make no digit
// reads as '?'.
type SevenSeg struct{}

func NewSevenSeg() *SevenSeg { return &SevenSeg{} }

func (s *SevenSeg) Recognize(ctx context.Context, img image.Image) (string, error) {
	text, _, err := s.RecognizeWords(ctx, img)
	return text, err
}

func (s *SevenSeg) RecognizeWords(ctx context.Context, img image.Image) (string, []Word, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	best := s.read(img)
	if len(best.glyphs) == 0 {
		return "", nil, nil
	}
	var text strings.Builder
	words := make([]Word, 0, len(best.glyphs))
	for _, gl := range best.glyphs {
		ch := gl.ch
		if gl.unsure() {
			// The text, which a watch acts on and the pages show, says
			// the digit wasn't read; the Word keeps what the decoder saw
			// and how sure it was, so Test this region can show both.
			ch = '?'
		}
		text.WriteByte(ch)
		words = append(words, Word{Text: string(gl.ch), Conf: gl.conf})
	}
	return text.String(), words, nil
}

// HasUnsureDigit reports whether a '?' in text, as RecognizeWords returned
// it with words, is a digit the decoder saw and wasn't sure of (its Word
// holds the digit) rather than a glyph that spells no digit at all. A
// pattern written to catch a '?' means the second kind: the "E" of a
// boiler's E4, a washer's End. False for any other engine's words, which
// don't line up with the text glyph by glyph.
func HasUnsureDigit(text string, words []Word) bool {
	if len(words) != len(text) {
		return false
	}
	for i, w := range words {
		if text[i] == '?' && w.Text != "?" {
			return true
		}
	}
	return false
}

// unsure reports whether the glyph is a digit read below UnsureDigit that
// may be another digit (alt), which the text gives as '?'.
func (g glyph) unsure() bool {
	return g.numeral() && g.conf < UnsureDigit && g.alt != 0 && g.alt != g.ch
}

// read is the decode behind Recognize and RecognizeWords: the best reading
// of the crop, with no glyphs when nothing is lit or nothing reads.
func (s *SevenSeg) read(img image.Image) reading {
	g0 := toGray(img)
	g := enlargeSmall(g0)
	best := readCrop(g)
	if g.h == g0.h {
		best = withSmallDigits(g0, best)
	}
	return best
}

// readCrop reads one gray crop, the whole of it and the window inside a
// housing, and then takes the second opinions and edge checks.
func readCrop(g grayImg) reading {
	thr, primary, ok := thresholdLevels(g)
	if !ok {
		return reading{} // uniform (or empty) crop: nothing lit
	}
	best := readGray(g, thr, primary, frameCut{})
	// A box drawn round the housing is read as if drawn on the window:
	// the housing's own brightness would set the thresholds otherwise. The
	// crop as drawn is read too, and the better reading wins: the ground
	// round a lone fat digit, in the other polarity, is a ring just like a
	// housing, and its "window" is the digit's own bars. A window that
	// reads as nothing says nothing: a '?' reading of the crop as drawn
	// (which scores below an empty one) keeps the glyphs it did read.
	if win, cut := findWindow(g, thr); cut != (frameCut{}) {
		gw := g.sub(win)
		if thr, primary, ok := thresholdLevels(gw); ok {
			if r := readGray(gw, thr, primary, cut); len(r.glyphs) > 0 && (r.score >= best.score || len(best.glyphs) == 0) {
				r.offset(win.Min)
				best = r
			}
		}
	}
	return withCutEdges(g, readSpans(g, withFlatLight(g, best)))
}

// readGray reads one gray crop; cut says which of its edges findWindow cut
// a housing from.
func readGray(g grayImg, thr []int, primary int, cut frameCut) reading {
	d := newDecoder(g, thr, primary)
	d.cut = cut
	defer d.release()
	return d.read()
}

// The decoder's tuning ratios. Lengths are shares of inkH — the height of
// the row of ink — unless the comment says otherwise. The recipe's stated
// limits (dot size, slant) are these numbers.
const (
	// speckFrac: a blob smaller than (speckFrac*inkH)² px is sensor
	// speckle, never a bar.
	speckFrac = 0.03
	// minBlobArea is the speckle floor before inkH is known.
	minBlobArea = 4
	// dotMaxFrac: a decimal point, colon dot or degree ring is at most
	// this tall and wide. Tight on purpose: a fat font with wide gaps
	// leaves its lower vertical bars a fifth of the digit tall, and those
	// must stay bars.
	dotMaxFrac = 0.18
	// dotBaselineFrac: a lone dot's centre sits below this line to be a
	// decimal point rather than a stray mark.
	dotBaselineFrac = 0.6
	// dotSolidFrac: a dot fills at least this share of its bounding box.
	dotSolidFrac = 0.5
	// colonGapMin/Max: the two dots of a colon are this far apart. Some
	// clocks set the dots level with the top and bottom bars, most of the
	// row apart.
	colonGapMin = 0.1
	colonGapMax = 0.75
	// colonGapNear: dots further apart than this must also stand clear of
	// a digit's side bars (see splitBlobs' inDigit).
	colonGapNear = 0.6
	// colonLeanMax: the upper dot of a colon may stand this far right of
	// the lower one per row between them, as an italic font leans (the
	// most shearEstimate corrects).
	colonLeanMax = shearMax
	// colonGapMinBig: big dots, glowing on a small display, close the gap
	// between them to a pixel or two.
	colonGapMinBig = 0.05
	// colonMidSlack: a colon's upper dot may sit this far below the
	// row's middle (some clocks set the colon low); the lower dot is
	// below it.
	colonMidSlack = 0.1
	// barJoinFrac: bars closer than this in x belong to one glyph. Real
	// displays leave only a sliver between a horizontal bar's end and the
	// vertical bar beside it, and far more between digits.
	barJoinFrac = 0.08
	// reachFrac: a cell reaches past its ink by a stroke plus this share
	// of a stroke where the top or bottom bar is unlit.
	reachFrac = 0.2
	// wideCellFrac: a cluster at least this wide is a full-width digit;
	// narrower is a "1".
	wideCellFrac = 0.3
	// tallCellFrac: a cluster at least this tall is a digit; shorter is a
	// "-" (or a degree ring, see degreeTopFrac).
	tallCellFrac = 0.5
	// degreeTopFrac: a short, squarish cluster centred above this line is
	// a degree sign, which is dropped: it follows the reading and is not
	// part of it. A degree ring sits in the top half of the row, centred
	// about a quarter of the way down; a minus sign sits across the
	// middle, and on a small display rounding can put its centre a pixel
	// above it.
	degreeTopFrac = 0.4
	// cellWidthFrac: digit cell width when only "1"s are showing.
	cellWidthFrac = 0.55
	// cellWidthMin/Max clamp a measured cell width, as shares of the row
	// height (the ink plus the reach at either end).
	cellWidthMin = 0.5
	cellWidthMax = 0.9
	// oneMinStrokeFrac: a narrow cluster thinner than this share of the
	// stroke is a line, not a "1".
	oneMinStrokeFrac = 0.5
	// onePartnerRatio, oneFullFrac, oneSmallFrac: a "1" is a digit when
	// another digit in the row is as tall within this ratio; alone, when it
	// runs more than oneSmallFrac of the box's height, and not oneFullFrac
	// of it from the top or the bottom edge (see oneStands). A fifth: a "1"
	// with a digit's height of room above and below it in the box still
	// reads, and the hot pixels a threshold leaves of a dim display whose
	// digits it put out (a sixth of the box on a car's LED clock) don't.
	// oneBaseFrac:
	// smaller digits after a "1" stand on its baseline within this share of
	// its height, past where its cell reaches.
	onePartnerRatio = 1.25
	oneFullFrac     = 0.9
	oneSmallFrac    = 0.2
	oneBaseFrac     = 0.05
	// oneShiftFrac: a "1" after a full-width digit starts at least the
	// usual gap plus this share of what a digit is wider than the "1"
	// after it; nearer, it is a digit's left-hand bars (see layoutCells).
	oneShiftFrac = 0.5
	// oneMaskStrokes: a "1"'s cell leaves out the ink of other glyphs
	// before its bars only when its bars are at most this many strokes
	// wide, as a "1" is (see layoutCells).
	oneMaskStrokes = 2.0
	// sticksOutFrac: a full-width digit whose ink reaches this share of
	// the row's height above or below the other digits' carries ink that
	// isn't its own (see sticksOut).
	sticksOutFrac = 0.15
	// groundBorderFrac: a piece of ink spanning the box that lights this
	// share of the box's border is the ground round the digits, not a
	// digit (see groundCell).
	groundBorderFrac = 0.9
	// probeMinRunFrac: a probe needs a lit run this share of the row
	// height long to count.
	probeMinRunFrac = 0.015
	// strokeRunFloorFrac: a lit run shorter than this share of the digit
	// height is speckle or a ragged edge, not a bar crossing, and does not
	// vote for the stroke.
	strokeRunFloorFrac = 0.04
	// strokeMinFrac/strokeMaxFrac: the stroke a real display can have, as
	// shares of the digit height; the estimate is held inside this band.
	strokeMinFrac = 0.05
	strokeMaxFrac = 0.35
	// strokeFallbackFrac: bar thickness assumed when nothing measures.
	strokeFallbackFrac = 0.12
	// strokeRunCapFrac: a lit run longer than this is a bar seen along its
	// length, not across, and stays out of the stroke estimate.
	strokeRunCapFrac = 0.35
	// midBand0/1: the columns, as shares of the cell width, where the
	// horizontal bars a, d and g are probed and where barAcross looks for
	// one.
	midBand0 = 0.38
	midBand1 = 0.62
	// onFloor, onHalf: a zone is on above max(onFloor, onHalf*bestFill),
	// so dim or bloomed segments classify against the cell's own best.
	onFloor = 0.3
	onHalf  = 0.5
	// holeLit: a hole zone this full is a solid blob, not a digit.
	holeLit = 0.5
	// holePad: the hole zones stay this share of the cell width clear of
	// the vertical bars (on top of the measured stroke).
	holePad = 0.1
	// holeMinFrac: the hole zones are never narrower than this share of
	// the cell width, for the fattest fonts.
	holeMinFrac = 0.12
	// pointSizeFrac, pointConfSlope: a point scores 100 while it is small
	// against the row and falls off at pointConfSlope per unit of size over
	// pointSizeFrac*rowH.
	pointSizeFrac  = 0.25
	pointConfSlope = 2.5
	// unsureConf is the most a '?' scores.
	unsureConf = 30
	// UnsureDigit: a digit read below this confidence whose least certain
	// segment, read the other way, spells a different digit (glyph.alt)
	// comes back as '?': the decoder saw an 8 but it may be a 0. Its chip
	// on Test this region keeps the digit and its confidence. Chosen on
	// round 6's 75 real seven-segment photos and their box variants, where
	// a plain floor at any level dropped two exact reads for each wrong
	// value it removed; limited to ambiguous digits it removes a clock's
	// wrong 5:58 (the 8 at 11%, a 0 with its middle bar nearly lit) and
	// drops one exact read, a meter's 002.4 whose 4 at 11% may be a 9, and
	// which did read 002.9 once the photo was re-encoded. The decoder's
	// confidences sit at 11, 33, 55 and 78 (probes in ninths), so any
	// value from 12 to 55 gives the same result on those photos. Exported
	// so the Test page's note and the docs check quote the number the
	// decoder uses.
	UnsureDigit = 40
	// blurredPointSolid: a blurred, noisy point fills at least this share
	// of its bounding box (a clean one, dotSolidFrac).
	blurredPointSolid = 0.4
	// missedPointConf: the most a decimal point scores when only another
	// threshold saw it (see withMissedPoint).
	missedPointConf = 40
	// confOK: a glyph at or above this is trusted when the readings at two
	// thresholds disagree (the UI's ok line).
	confOK = 60
	// splitAboveFrac: Otsu's foreground class is two things (ghost segments
	// and lit ones, glow and bar cores, a bright background patch and the
	// digits) when its own Otsu split separates the sub-means by this share
	// of the distance from the background mean to the brighter sub-mean.
	splitAboveFrac = 0.4
	// splitBelowFrac: the same for the background class, which hides a dim
	// digit (a multiplexed display under a rolling shutter) when its upper
	// sub-mode sits this share of the way to the foreground mean.
	splitBelowFrac = 0.3
	// shearMax, shearStep, shearMinGain: the italic search range and step
	// (x per row), and how much sharper the column projection must get
	// before a lean is corrected at all.
	shearMax     = 0.3
	shearStep    = 0.02
	shearMinGain = 0.03
	// bezelThinFrac, bezelLongFrac: a blob lying along a crop edge, at most
	// bezelThinFrac of the crop thick and at least bezelLongFrac of it
	// long, is a bezel line or a reflection off the glass.
	bezelThinFrac = 0.025
	bezelLongFrac = 0.5
	// bezelNearFrac: such a line may also lie up to this share of the crop
	// in from the edge, with nothing between it and the edge.
	bezelNearFrac = 0.06
	// frameDebrisFrac, frameDebrisFill: what findWindow leaves of the
	// housing on a cut edge lies within frameDebrisFrac of the crop from
	// it, or fills under frameDebrisFill of its bounding box.
	frameDebrisFrac = 0.15
	frameDebrisFill = 0.15
	// bevelStrokeFrac: a bevel line is at most this share of the digits'
	// stroke thick.
	bevelStrokeFrac = 0.6
	// rimMaxStrokeFrac: ink along the crop's top or bottom edge thinner
	// than this share of a bar, lying flat and separated from the digits
	// by a blank row, is the rim of the display window, not a bar.
	rimMaxStrokeFrac = 0.6
	// cellMaxAspect: a cell wider than this many row heights is two
	// glyphs run together or the frame round the display, never a digit.
	cellMaxAspect = 1.0
	// runTogetherFrac: a digit cell this many times as wide as the
	// narrowest full-width one is glyphs run together. A "7" or "3",
	// with no left-hand bars, is the narrowest a real digit gets, about
	// two thirds of a "0" on a small display.
	runTogetherFrac = 1.6
	// minusMaxStrokes, minusBand0/1: a short cluster is a "-" only when
	// it is at most this many strokes tall and its centre lies in this
	// band of the row; anything else short is a fragment and dropped.
	minusMaxStrokes = 1.5
	// minusMaxFrac: or, lying flat, at most this share of the row tall.
	minusMaxFrac = 0.2
	minusBand0   = 0.3
	minusBand1   = 0.7
	// lineWideFrac, lineThinFrac: a blob wider than lineWideFrac*inkH and
	// shorter than lineThinFrac*inkH is a line across the row, never a bar.
	lineWideFrac = 1.5
	lineThinFrac = 0.25
	// frameFillFrac, frameMaxFrac: rows along the crop's top or bottom edge
	// lit across at least frameFillFrac of their width, at most
	// frameMaxFrac of the crop deep, are the housing round the display
	// window; so are such columns down the sides once a housing row is
	// found.
	frameFillFrac = 0.9
	frameMaxFrac  = 0.25
	// frameInnerMax: inside the bands, the window is lit at most this
	// share in the polarity the bands are found in.
	frameInnerMax = 0.5
	// frameSideInnerMax: a column band with no row band is the housing
	// down one side of the box only when, at that level, the window
	// inside it is as good as unlit (at most this share): the housing is
	// brighter than the digits and outlasts them as the threshold rises.
	// A "1" on the edge of a tight box is as bright as the digits beside
	// it, which are lit at every level it is.
	frameSideInnerMax = 0.05
	// frameGapFrac: a row or column of the window's margin, between the
	// housing and the digits, is lit at most this share of the way.
	frameGapFrac = 0.1
	// narrowCellFrac: a full-width digit's cluster narrower than this
	// share of the widest one has no bars down its left side.
	narrowCellFrac = 0.85
	// pointGapFrac: a decimal point starts at most this share of the row
	// height after the digit before it; a mark further out is something
	// else on the panel.
	pointGapFrac = 0.4
	// colonDotMaxFrac, colonDotAspect: a colon's dots may be bigger than a
	// lone point (on a small display they are as big as the bars are
	// thick), up to this share of the ink height, when they are this close
	// to square.
	colonDotMaxFrac = 0.3
	colonDotAspect  = 1.3
	// colonOpenFill: a colon's dot made of a few LEDs fills at least this
	// share of its bounding box (a solid dot, dotSolidFrac).
	colonOpenFill = 0.3
	// colonCentreRatio: the space before a colon of big dots is at most
	// this many times the space after it, and this many dots wide.
	colonCentreRatio = 1.6
	// colonSlackDots: both spaces are known to within this many dots (at
	// least a pixel): the glow of a small display fuses the colon to the
	// digit after it and leaves a pixel or two before it, which upscaling
	// the crop doubles or trebles like everything else.
	colonSlackDots = 0.5
	// sideBySideFrac: two bars a sliver apart whose facing edges are both
	// lit on more rows than this share of the ink height are the sides of
	// two digits, not two bars of one.
	sideBySideFrac = 0.15
	// sideBySideShare: and on at least this share of the rows either edge
	// is lit on. A horizontal bar's end faces a third of the vertical bar
	// beside it, two digits' sides face each other on most of theirs.
	sideBySideShare = 0.45
	// aboveMarkFrac: a mark this share of the row tall or less, standing
	// clear above the bars a sliver before it, is a glyph of its own (see
	// clusterBars).
	aboveMarkFrac = 0.4
	// probes per zone.
	probes = 9
)

// grayImg is the packed 8-bit luma the decoder works on.
type grayImg struct {
	w, h int
	pix  []uint8
}

func toGray(img image.Image) grayImg {
	b := img.Bounds()
	g := grayImg{w: b.Dx(), h: b.Dy()}
	if g.w <= 0 || g.h <= 0 {
		return grayImg{}
	}
	g.pix = make([]uint8, g.w*g.h)
	switch src := img.(type) {
	case *image.RGBA:
		for y := 0; y < g.h; y++ {
			i := src.PixOffset(b.Min.X, b.Min.Y+y)
			for x := 0; x < g.w; x++ {
				g.pix[y*g.w+x] = luma(src.Pix[i], src.Pix[i+1], src.Pix[i+2])
				i += 4
			}
		}
	case *image.Gray:
		for y := 0; y < g.h; y++ {
			copy(g.pix[y*g.w:(y+1)*g.w], src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):])
		}
	default:
		for y := 0; y < g.h; y++ {
			for x := 0; x < g.w; x++ {
				g.pix[y*g.w+x] = color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			}
		}
	}
	return g
}

func luma(r, g, b uint8) uint8 {
	return uint8((77*uint32(r) + 150*uint32(g) + 29*uint32(b)) >> 8)
}

// thresholdLevels is the set of thresholds worth reading the crop at, in
// ascending luminance, and which of them is Otsu's own. A real display's
// histogram is rarely two-valued: unlit "ghost" segments, LED glow and a
// digit caught dim by a rolling shutter each put a third mode between the
// background and the lit bars, and a single split lands on the wrong side
// of it whenever that mode is the big one. So Otsu's two classes are each
// split again, and a split that separates its halves strongly (see
// splitAboveFrac, splitBelowFrac) contributes a further threshold. A
// single-valued crop has nothing to split and reports !ok.
func thresholdLevels(g grayImg) (thr []int, primary int, ok bool) {
	var hist [256]int
	for _, v := range g.pix {
		hist[v]++
	}
	lo, hi := 0, 255
	for lo < 255 && hist[lo] == 0 {
		lo++
	}
	for hi > 0 && hist[hi] == 0 {
		hi--
	}
	if lo >= hi {
		return nil, 0, false
	}
	t1, mLo, mHi, _ := otsuRange(&hist, lo, hi)
	thr = []int{t1}
	if t2, ma, mb, ok := otsuRange(&hist, t1+1, hi); ok && mb-ma >= splitAboveFrac*(mb-mLo) {
		thr = append(thr, t2)
	}
	if t0, ma, mb, ok := otsuRange(&hist, lo, t1); ok && mb-ma >= splitBelowFrac*(mHi-ma) {
		thr = append([]int{t0}, thr...)
		primary = 1
	}
	return thr, primary, true
}

// otsuRange is Otsu's threshold over hist[lo..hi]: the split that
// maximises the between-class variance. Pixels at or below thr are the low
// class; mLo and mHi are the two class means. An already two-valued range
// splits exactly between its values; a single-valued one reports !ok.
func otsuRange(hist *[256]int, lo, hi int) (thr int, mLo, mHi float64, ok bool) {
	for lo < hi && hist[lo] == 0 {
		lo++
	}
	for hi > lo && hist[hi] == 0 {
		hi--
	}
	if lo >= hi {
		return 0, 0, 0, false
	}
	var total, sumAll float64
	for v := lo; v <= hi; v++ {
		total += float64(hist[v])
		sumAll += float64(v * hist[v])
	}
	var wB, sumB float64
	best := -1.0
	for t := lo; t < hi; t++ {
		wB += float64(hist[t])
		sumB += float64(t * hist[t])
		wF := total - wB
		if wB == 0 || wF == 0 {
			continue
		}
		mB, mF := sumB/wB, (sumAll-sumB)/wF
		if v := wB * wF * (mB - mF) * (mB - mF); v > best {
			best, thr, mLo, mHi = v, t, mB, mF
		}
	}
	return thr, mLo, mHi, best >= 0
}

// bitmap is a binarized crop; on[i] is a foreground (digit) pixel.
type bitmap struct {
	w, h int
	on   []bool
}

func (m *bitmap) at(x, y int) bool {
	return x >= 0 && y >= 0 && x < m.w && y < m.h && m.on[y*m.w+x]
}

// work is the scratch the decoder reuses across thresholds, polarities and
// polls: two crops' worth of buffers a poll would otherwise allocate six
// times over. Buffers are sized for the widest de-sheared bitmap.
type work struct {
	bufs   [3][]bool
	seen   []bool
	stack  []int
	lit    []int32
	counts []int
	shifts []int
	runs   []int
}

var workPool sync.Pool

func getWork(w, h int) *work {
	wk, _ := workPool.Get().(*work)
	if wk == nil {
		wk = &work{}
	}
	n := (w + 2*(int(math.Ceil(shearMax*float64(h)))+1)) * h
	for i := range wk.bufs {
		if cap(wk.bufs[i]) < n {
			wk.bufs[i] = make([]bool, n)
		}
	}
	if cap(wk.seen) < n {
		wk.seen = make([]bool, n)
	}
	return wk
}

// decoder holds one crop's reading state: its thresholds, and per polarity
// the lean it measured and the readings it has made, so a level is never
// decoded twice.
type decoder struct {
	g       grayImg
	thr     []int
	primary int
	cut     frameCut // the edges findWindow cut the housing from
	wk      *work
	shear   [2]float64
	sheared [2]bool
	cache   [3][2]*reading
}

func newDecoder(g grayImg, thr []int, primary int) *decoder {
	return &decoder{g: g, thr: thr, primary: primary, wk: getWork(g.w, g.h)}
}

func (d *decoder) release() { workPool.Put(d.wk) }

// reading is one decode of the crop. The score ranks readings against each
// other: each digit adds its confidence, each '?' costs 100, points and
// colons are neutral.
type reading struct {
	glyphs []glyph
	score  float64
	// dropped is the ink of the row that no glyph took: pieces too small
	// for a bar or a "-" (cells marked fragment), as rectangles in the
	// crop's own coordinates (see toCrop). readSpans looks at it.
	dropped []image.Rectangle
	// marks is the degree signs left out of the reading, likewise.
	// withCutEdges looks at both: what the box leaves of a digit it cuts
	// can look like either.
	marks []image.Rectangle
	// cutPoints is the decimal points left out of the reading because no
	// digit stands before them, with no room for one between them and the
	// box's left edge: the box cut their digit (see withCutEdges).
	cutPoints []image.Rectangle
	// bars is those of dropped and marks that are shaped like a bar of
	// the digits or a stub of one (see barPiece): what the box leaves of
	// a digit it cuts may be no more (see withCutEdges).
	bars []image.Rectangle
	// dark is the polarity read: dark digits on a light ground.
	dark bool
}

type glyph struct {
	ch     byte
	conf   float64
	x0, x1 int     // cell columns, to match a glyph across thresholds
	x      float64 // centre, for reading order
	// alt is the digit the cell would spell if its least certain segment
	// (the one conf measures) were read the other way, or 0 when that
	// spells no digit: a 0 whose middle bar is nearly lit has alt '8', a
	// 1 whose top is nearly lit has alt '7'. A 6 or 7 or 9, which the
	// table spells two ways, may have alt equal to ch: not ambiguous.
	// Set by classify; see unsure.
	alt byte
	// ink is the glyph's own ink (its cluster, or its dot or dots), in the
	// crop's coordinates once readAt and read have mapped it back (toCrop).
	ink image.Rectangle
}

// digit reports whether the glyph is a cell (a digit, '-' or '?') rather
// than a separator.
func (g glyph) digit() bool { return g.ch != '.' && g.ch != ':' }

// numeral reports whether the glyph is one of 0-9.
func (g glyph) numeral() bool { return g.ch >= '0' && g.ch <= '9' }

// read decides the polarity and the threshold. Polarity is settled at
// Otsu's own split by reading both ways and keeping the better score: the
// wrong way round the background is one blob with digit-shaped holes,
// which reads as '?' and loses to any real digit. The other levels then
// refine the winning polarity — both, when neither read anything there.
func (d *decoder) read() reading {
	best, bestBright := reading{score: math.Inf(-1)}, true
	for _, bright := range []bool{true, false} {
		if r := d.readAt(d.primary, bright); r.score > best.score {
			best, bestBright = r, bright
		}
	}
	if len(d.thr) == 1 {
		return best
	}
	polarities := []bool{bestBright}
	if best.score <= 0 {
		polarities = []bool{true, false}
	}
	best.score = math.Inf(-1)
	for _, bright := range polarities {
		if r := d.readLevels(bright); r.score > best.score {
			best = r
		}
	}
	return best
}

// readLevels reads one polarity at every level, loosest first (the one
// that lights the most pixels), and lets each tighter reading challenge
// the one before it.
func (d *decoder) readLevels(bright bool) reading {
	n := len(d.thr)
	level := func(i int) int {
		if bright {
			return i // bright foreground: a lower threshold lights more
		}
		return n - 1 - i
	}
	cur := d.readAt(level(0), bright)
	levels := []reading{cur}
	for i := 1; i < n; i++ {
		r := d.readAt(level(i), bright)
		levels = append(levels, r)
		cur = pick(cur, r)
	}
	return withMissedPoint(cur, levels)
}

// withMissedPoint adds the decimal point another threshold saw to a
// reading that has none. On a small, blurred display the point is the
// faintest thing in the crop: the tight threshold that reads the digits
// best can lose it, and 90.1 would read as a confident 901. A level that
// shows exactly one point, standing between two of the reading's numerals
// (not after its minus sign or a '?': -1 is never -.1), is evidence of it;
// the point goes in at no more than missedPointConf: Test this region
// shows it in red, and a trigger reads the text with the point in it, as
// with any glyph (nothing downstream looks at a glyph's confidence). A
// level showing a point at every
// digit is showing an LCD's unlit ghost points, and one showing a colon
// too is showing the glow of noisy digits broken into marks; both are
// passed over.
func withMissedPoint(cur reading, levels []reading) reading {
	for _, g := range cur.glyphs {
		if !g.digit() {
			return cur // a point or colon is already there
		}
	}
	for _, r := range levels {
		var p glyph
		n := 0
		for _, g := range r.glyphs {
			if !g.digit() {
				p, n = g, n+1
			}
		}
		if n != 1 || p.ch != '.' {
			continue
		}
		at := -1 // index of the first glyph after the point
		for i, g := range cur.glyphs {
			if g.x >= p.x {
				at = i
				break
			}
		}
		// Only between two numerals: a point after a minus sign or a
		// '?' is no number's point (-1 is not -.1).
		if at < 1 || !cur.glyphs[at-1].numeral() || !cur.glyphs[at].numeral() {
			continue
		}
		p.conf = math.Min(p.conf, missedPointConf)
		glyphs := make([]glyph, 0, len(cur.glyphs)+1)
		glyphs = append(glyphs, cur.glyphs[:at]...)
		glyphs = append(glyphs, p)
		cur.glyphs = append(glyphs, cur.glyphs[at:]...)
		return cur
	}
	return cur
}

// pick chooses between the readings at two thresholds of one polarity.
// The tight one wins on evidence of ghosts — see ghostEvidence — and
// otherwise whichever scores better: a dim digit shows only at the loose
// threshold and adds to its score, a bar eroded by the tight threshold
// costs confidence there. Ties go to the tight reading, which keeps ghost
// decimal points out.
func pick(loose, tight reading) reading {
	if ghostEvidence(loose, tight) || tight.score >= loose.score {
		return tight
	}
	return loose
}

// ghostEvidence reports whether the loose threshold lit segments that are
// not there: a cell that is a confident digit at the tight threshold but
// an '8' (every segment lit — the unlit segments were ghosts) or a '?'
// (bars bridged by glow or by a bright background patch) at the loose one.
// A display whose only digits are '8's shows ghosts as whole '8' cells that
// vanish at the tight threshold, so that pattern counts too. A digit the
// tight threshold merely erodes reads there at low confidence, or as a
// subset of its loose segments, and is not evidence.
func ghostEvidence(loose, tight reading) bool {
	all8, nLoose := true, 0
	for _, l := range loose.glyphs {
		if l.digit() {
			nLoose++
			if l.ch != '8' {
				all8 = false
			}
		}
	}
	nTight := 0
	for _, t := range tight.glyphs {
		if !t.digit() {
			continue
		}
		nTight++
		if t.ch == '?' || t.conf < confOK {
			continue
		}
		for _, l := range loose.glyphs {
			if l.digit() && (l.ch == '8' || l.ch == '?') && l.ch != t.ch && overlapX(l, t) {
				return true
			}
		}
	}
	return nLoose > 0 && all8 && nTight < nLoose
}

// overlapX reports whether two glyphs' cells share at least half of the
// narrower one.
func overlapX(a, b glyph) bool {
	o := min(a.x1, b.x1) - max(a.x0, b.x0) + 1
	return o > 0 && 2*o >= min(a.x1-a.x0+1, b.x1-b.x0+1)
}

// readAt decodes one threshold in one polarity. The lean of the display is
// measured on the first level read in that polarity and applied to all;
// when that level showed none, each other level is measured on its own.
func (d *decoder) readAt(level int, bright bool) reading {
	p := 0
	if !bright {
		p = 1
	}
	if r := d.cache[level][p]; r != nil {
		return *r
	}
	m := binarize(d.g, d.thr[level], bright, d.wk.bufs[0])
	m = denoise(m, d.wk.bufs[1])
	blobs := dropFrameDebris(m, components(m, d.wk), d.cut, d.wk)
	blobs = dropBezels(m, blobs)
	blobs = dropRims(m, blobs, d.wk)
	blobs = dropEdgeStrips(m, blobs, d.wk)
	s, first := d.shear[p], !d.sheared[p]
	if first {
		s = shearEstimate(m, d.wk)
		d.shear[p], d.sheared[p] = s, true
	}
	if s == 0 && !first {
		// The first level read showed no lean; this one may. Each level
		// is binarized on its own, and at the level that read upright the
		// digits can be too broken up (or run together) to show one.
		s = shearEstimate(m, d.wk)
	}
	if s != 0 {
		m = deshear(m, s, d.wk)
		blobs = components(m, d.wk)
	}
	r := decodeBitmap(m, blobs, d.wk)
	r.dark = !bright
	if s != 0 {
		r.toCrop(s, d.g.w, d.g.h)
	}
	d.cache[level][p] = &r
	return r
}

// toCrop maps the reading's ink rectangles from the de-sheared bitmap back
// to the crop: deshear moved row y right by its shift minus the smallest
// shift, so a rectangle's columns move back by that over its rows.
func (r *reading) toCrop(s float64, w, h int) {
	yc := h / 2
	shift := func(y int) int { return int(math.Round(s * float64(y-yc))) }
	minS := min(0, shift(0), shift(h-1))
	back := func(b image.Rectangle) image.Rectangle {
		a, z := shift(b.Min.Y), shift(b.Max.Y-1)
		x0 := b.Min.X + minS - max(a, z)
		x1 := b.Max.X + minS - min(a, z)
		return image.Rect(max(0, x0), b.Min.Y, min(w, max(x0+1, x1)), b.Max.Y)
	}
	for i := range r.glyphs {
		r.glyphs[i].ink = back(r.glyphs[i].ink)
	}
	for i := range r.dropped {
		r.dropped[i] = back(r.dropped[i])
	}
	for i := range r.marks {
		r.marks[i] = back(r.marks[i])
	}
	for i := range r.cutPoints {
		r.cutPoints[i] = back(r.cutPoints[i])
	}
	for i := range r.bars {
		r.bars[i] = back(r.bars[i])
	}
}

// offset moves the reading's ink rectangles by p: a reading of a window
// inside the crop, or of a span of it, put back in the crop's coordinates.
func (r *reading) offset(p image.Point) {
	for i := range r.glyphs {
		r.glyphs[i].ink = r.glyphs[i].ink.Add(p)
	}
	for i := range r.dropped {
		r.dropped[i] = r.dropped[i].Add(p)
	}
	for i := range r.marks {
		r.marks[i] = r.marks[i].Add(p)
	}
	for i := range r.cutPoints {
		r.cutPoints[i] = r.cutPoints[i].Add(p)
	}
	for i := range r.bars {
		r.bars[i] = r.bars[i].Add(p)
	}
}

// findWindow finds the display window inside a box drawn round the
// housing. A box drawn a little big takes in a band of the panel on every
// side, and where that panel is brighter than the digits (or, round an
// LCD, as dark as them) it binarizes as one ring that every digit
// overlaps, so the whole crop clusters into one glyph; its brightness
// skews the thresholds too. The ring's rows along the top and bottom edges
// are lit nearly all the way across, which a row of digits never is:
// digits leave gaps between them and their bars leave the cell's corners
// dark. Those rows are cut, then the columns down the sides that are lit
// nearly all the way down between them. A column band alone is lit the
// same way as a "1" on the edge of a box drawn tight round the digits,
// and counts only at a level where the window inside it is as good as
// unlit: a housing brighter than the digits is still lit when the
// threshold has put the digits out, a "1" goes out with the digits beside
// it. The bands count only in the polarity that leaves the window inside
// them mostly dark (the digits' own polarity: in the other one the
// window's ground is lit and its rows along the edge are bands too), and
// only when a dark margin separates each band from the digits.
// Every level is tried, and the deepest cut wins: the loosest threshold
// lights the most of the housing's ragged inner edge. What is left is the
// window, which reads as a box drawn on the window itself once
// dropFrameDebris has cleared what is left of the housing along the cut.
func findWindow(g grayImg, thr []int) (win image.Rectangle, cut frameCut) {
	full := image.Rect(0, 0, g.w, g.h)
	win = full
	if g.w < 3 || g.h < 3 {
		return win, cut
	}
	for _, t := range thr {
		for _, bright := range []bool{true, false} {
			on := func(x, y int) bool { return (int(g.pix[y*g.w+x]) > t) == bright }
			lit := func(x0, x1, y0, y1 int) bool { // frameFillFrac of the span is lit
				n := 0
				for y := y0; y <= y1; y++ {
					for x := x0; x <= x1; x++ {
						if on(x, y) {
							n++
						}
					}
				}
				return float64(n) >= frameFillFrac*float64((x1-x0+1)*(y1-y0+1))
			}
			maxY := int(frameMaxFrac * float64(g.h))
			top, bot := 0, 0
			for top < maxY && lit(0, g.w-1, top, top) {
				top++
			}
			for bot < maxY && lit(0, g.w-1, g.h-1-bot, g.h-1-bot) {
				bot++
			}
			y0, y1 := top, g.h-1-bot
			maxX := int(frameMaxFrac * float64(g.w))
			left, right := 0, 0
			for left < maxX && lit(left, left, y0, y1) {
				left++
			}
			for right < maxX && lit(g.w-1-right, g.w-1-right, y0, y1) {
				right++
			}
			if left == 0 && right == 0 && top == 0 && bot == 0 {
				continue
			}
			innerMax := frameInnerMax
			if top == 0 && bot == 0 {
				innerMax = frameSideInnerMax
			}
			r := image.Rect(left, y0, g.w-right, y1+1)
			// dark reports whether some row (or column) of the window
			// within a quarter of it from the cut edge is nearly unlit:
			// the window's own margin between the housing and the
			// digits. A digit's glow lit at a low threshold makes bands
			// too, but they run straight into the digits.
			dark := func(horiz bool, from, step int) bool {
				span, lines := r.Dx(), r.Dy()
				if !horiz {
					span, lines = r.Dy(), r.Dx()
				}
				for i, at := 0, from; i < max(1, lines/4); i, at = i+1, at+step {
					n := 0
					for j := 0; j < span; j++ {
						x, y := r.Min.X+j, at
						if !horiz {
							x, y = at, r.Min.Y+j
						}
						if on(x, y) {
							n++
						}
					}
					if float64(n) <= frameGapFrac*float64(span) {
						return true
					}
				}
				return false
			}
			if (top > 0 && !dark(true, r.Min.Y, 1)) || (bot > 0 && !dark(true, r.Max.Y-1, -1)) ||
				(left > 0 && !dark(false, r.Min.X, 1)) || (right > 0 && !dark(false, r.Max.X-1, -1)) {
				continue
			}
			n := 0
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					if on(x, y) {
						n++
					}
				}
			}
			if float64(n) > innerMax*float64(r.Dx()*r.Dy()) || r.Dx()*r.Dy() >= win.Dx()*win.Dy() {
				continue
			}
			win = r
			cut = frameCut{top: top > 0, bottom: bot > 0, left: left > 0, right: right > 0}
		}
	}
	return win, cut
}

// sub is the part of g inside r, copied.
func (g grayImg) sub(r image.Rectangle) grayImg {
	out := grayImg{w: r.Dx(), h: r.Dy(), pix: make([]uint8, r.Dx()*r.Dy())}
	for y := 0; y < out.h; y++ {
		copy(out.pix[y*out.w:(y+1)*out.w], g.pix[(r.Min.Y+y)*g.w+r.Min.X:])
	}
	return out
}

// frameCut records which edges findWindow cut the housing from.
type frameCut struct{ top, bottom, left, right bool }

// dropFrameDebris clears what findWindow leaves of the housing along the
// edges it cut: the ragged inner edge of the band, the corners a rounded
// window leaves, the bevel just inside. A piece that touches a cut edge
// goes when it is shallow (it lies within frameDebrisFrac of the crop from
// that edge, where no digit fits) or is a thin line or an L round the
// window (it fills under frameDebrisFill of its bounding box, where a bar
// fills nearly all of it and a digit about half). Digits sit well inside
// a window, so nothing a reading needs touches the cut. Pieces are cleared
// pixel by pixel: an L's bounding box takes in the corner of a digit.
//
// The bevel a few pixels in, broken by glare into pieces, goes too:
// hairlines (no thicker than dropBezels' lines) within that margin that are
// thinner than the digits' bars. A thin font's last digit has side bars
// as thin as dropBezels' lines and as close to the cut, but a bar thick;
// erased, they turned 1234 into a confident 123.
func dropFrameDebris(m bitmap, blobs []blob, cut frameCut, wk *work) []blob {
	if cut == (frameCut{}) {
		return blobs
	}
	dx := frameDebrisFrac * float64(m.w)
	dy := frameDebrisFrac * float64(m.h)
	thinX := max(2, int(bezelThinFrac*float64(m.w)))
	thinY := max(2, int(bezelThinFrac*float64(m.h)))
	kept := blobs[:0]
	for _, b := range blobs {
		top := cut.top && b.y0 == 0
		bottom := cut.bottom && b.y1 == m.h-1
		left := cut.left && b.x0 == 0
		right := cut.right && b.x1 == m.w-1
		shallow := (top && float64(b.y1+1) <= dy) || (bottom && float64(m.h-b.y0) <= dy) ||
			(left && float64(b.x1+1) <= dx) || (right && float64(m.w-b.x0) <= dx)
		sparse := float64(b.area) < frameDebrisFill*float64(b.w()*b.h())
		if (top || bottom || left || right) && (shallow || sparse) {
			eraseBlob(m, b, wk)
			continue
		}
		kept = append(kept, b)
	}
	// The bevel: hairlines along a cut edge, thinner than the digits' bars.
	inkTop, inkBot := rowExtent(kept, minBlobArea)
	if inkBot < inkTop {
		return kept
	}
	stroke := strokeEstimate(m, kept, inkBot-inkTop+1, wk)
	hair := func(t, limit int) bool { return t <= limit && float64(t) <= bevelStrokeFrac*float64(stroke) }
	out := kept[:0]
	for _, b := range kept {
		bevel := (hair(b.w(), thinX) && ((cut.left && float64(b.x1+1) <= dx) || (cut.right && float64(m.w-b.x0) <= dx))) ||
			(hair(b.h(), thinY) && ((cut.top && float64(b.y1+1) <= dy) || (cut.bottom && float64(m.h-b.y0) <= dy)))
		if bevel {
			eraseBlob(m, b, wk)
			continue
		}
		out = append(out, b)
	}
	return out
}

func binarize(g grayImg, thr int, bright bool, dst []bool) bitmap {
	m := bitmap{w: g.w, h: g.h, on: dst[:len(g.pix)]}
	for i, v := range g.pix {
		m.on[i] = (int(v) > thr) == bright
	}
	return m
}

// denoise drops pixels with at most one lit 8-neighbour: sensor speckle
// that made it through the threshold, which would otherwise seed stray
// blobs. A one-pixel stroke keeps its two along-stroke neighbours.
func denoise(m bitmap, dst []bool) bitmap {
	out := bitmap{w: m.w, h: m.h, on: dst[:len(m.on)]}
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			if !m.on[y*m.w+x] {
				out.on[y*m.w+x] = false
				continue
			}
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if (dx != 0 || dy != 0) && m.at(x+dx, y+dy) {
						n++
					}
				}
			}
			out.on[y*m.w+x] = n >= 2
		}
	}
	return out
}

// blob is one 8-connected run of foreground; bounds are inclusive.
type blob struct {
	x0, y0, x1, y1 int
	area           int
	seed           int // index of one of its pixels in the bitmap it was found in
}

func (b blob) w() int      { return b.x1 - b.x0 + 1 }
func (b blob) h() int      { return b.y1 - b.y0 + 1 }
func (b blob) cx() float64 { return float64(b.x0+b.x1) / 2 }
func (b blob) cy() float64 { return float64(b.y0+b.y1) / 2 }

// rect is the blob's bounds as an image.Rectangle (exclusive max).
func (b blob) rect() image.Rectangle { return image.Rect(b.x0, b.y0, b.x1+1, b.y1+1) }

func (b blob) merge(o blob) blob {
	return blob{
		x0: min(b.x0, o.x0), y0: min(b.y0, o.y0),
		x1: max(b.x1, o.x1), y1: max(b.y1, o.y1),
		area: b.area + o.area,
	}
}

func components(m bitmap, wk *work) []blob {
	seen := wk.seen[:len(m.on)]
	clear(seen)
	stack := wk.stack[:0]
	var out []blob
	for start := range m.on {
		if !m.on[start] || seen[start] {
			continue
		}
		b := blob{x0: start % m.w, y0: start / m.w, x1: start % m.w, y1: start / m.w, seed: start}
		seen[start] = true
		stack = append(stack[:0], start)
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := i%m.w, i/m.w
			b.area++
			b.x0, b.x1 = min(b.x0, x), max(b.x1, x)
			b.y0, b.y1 = min(b.y0, y), max(b.y1, y)
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= m.w || ny >= m.h {
						continue
					}
					if j := ny*m.w + nx; m.on[j] && !seen[j] {
						seen[j] = true
						stack = append(stack, j)
					}
				}
			}
		}
		out = append(out, b)
	}
	wk.stack = stack
	return out
}

// eraseBlob clears the pixels of one blob, leaving whatever else lies in
// its bounding box.
func eraseBlob(m bitmap, b blob, wk *work) {
	if !m.on[b.seed] {
		return
	}
	stack := append(wk.stack[:0], b.seed)
	m.on[b.seed] = false
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%m.w, i/m.w
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= m.w || ny >= m.h {
					continue
				}
				if j := ny*m.w + nx; m.on[j] {
					m.on[j] = false
					stack = append(stack, j)
				}
			}
		}
	}
	wk.stack = stack
}

// erase clears a blob's bounding box.
func erase(m bitmap, b blob) {
	for y := b.y0; y <= b.y1; y++ {
		clear(m.on[y*m.w+b.x0 : y*m.w+b.x1+1])
	}
}

// dropBezels removes the thin blobs lying along a crop edge — the bezel
// of a display window, the edge of the glass, a reflection — before the
// lean is measured: a bright line up the left of the crop would otherwise
// read as a leading "1". They are erased from m so no probe sees them. A
// line a few pixels in from the edge with nothing between it and the edge
// counts too: the bevel of a window, a little way inside the housing that
// findWindow cut away.
func dropBezels(m bitmap, blobs []blob) []blob {
	thinX := max(2, int(bezelThinFrac*float64(m.w)))
	thinY := max(2, int(bezelThinFrac*float64(m.h)))
	nearX := max(0, int(bezelNearFrac*float64(m.w)))
	nearY := max(0, int(bezelNearFrac*float64(m.h)))
	blank := func(x0, x1, y0, y1 int) bool {
		for y := max(0, y0); y <= min(m.h-1, y1); y++ {
			for x := max(0, x0); x <= min(m.w-1, x1); x++ {
				if m.on[y*m.w+x] {
					return false
				}
			}
		}
		return true
	}
	kept := blobs[:0]
	for _, b := range blobs {
		edgeX := (b.x0 <= nearX && blank(0, b.x0-1, b.y0, b.y1)) || (b.x1 >= m.w-1-nearX && blank(b.x1+1, m.w-1, b.y0, b.y1))
		edgeY := (b.y0 <= nearY && blank(b.x0, b.x1, 0, b.y0-1)) || (b.y1 >= m.h-1-nearY && blank(b.x0, b.x1, b.y1+1, m.h-1))
		onX := edgeX && b.w() <= thinX && float64(b.h()) >= bezelLongFrac*float64(m.h)
		onY := edgeY && b.h() <= thinY && float64(b.w()) >= bezelLongFrac*float64(m.w)
		if onX || onY {
			erase(m, b)
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

// dropRims removes the rim of the display window: the edge of a red LED
// filter or an LCD's bezel, caught along the top or bottom of the crop. It
// is a few pixels thick — thinner than a bar, but thicker than the
// hairline dropBezels looks for — and glare breaks it into pieces too
// short for that rule, yet it spans the digits, so clusterBars would fold
// every digit it overlaps into one glyph. A piece is a rim when it lies
// on the top or bottom edge, is flat, is thinner than rimMaxStrokeFrac of
// the stroke, and a blank row separates it from the rest of the ink; a
// digit's own top bar on a tight crop is a stroke thick and stays.
func dropRims(m bitmap, blobs []blob, wk *work) []blob {
	inkTop, inkBot := rowExtent(blobs, minBlobArea)
	if inkBot < inkTop {
		return blobs
	}
	stroke := strokeEstimate(m, blobs, inkBot-inkTop+1, wk)
	limit := max(1, int(rimMaxStrokeFrac*float64(stroke)))
	dotW := dotMaxFrac * float64(inkBot-inkTop+1)
	// blank reports whether rows y0..y1 carry no ink in columns x0..x1.
	blank := func(x0, x1, y0, y1 int) bool {
		for y := max(0, y0); y <= min(m.h-1, y1); y++ {
			for x := x0; x <= x1; x++ {
				if m.on[y*m.w+x] {
					return false
				}
			}
		}
		return true
	}
	// feetCut: the box shaved the bottom of the digits, so a bar of theirs
	// (taller than a rim) reaches the crop's bottom edge. Only then can
	// what is left of a decimal point lie on that edge too; with the
	// digits' feet inside the crop, every flat piece along the bottom is
	// rim or glow. Glow slivers on the bottom edge of a box drawn round
	// the window's rim, no wider than a point, passed for a cut point and
	// read 09 as 0.9 and 09.
	feetCut := false
	for _, b := range blobs {
		if b.area >= minBlobArea && b.h() > limit && b.y1 >= m.h-1-limit {
			feetCut = true
			break
		}
	}
	kept := blobs[:0]
	for _, b := range blobs {
		flat := b.h() <= limit && b.w() >= 2*b.h()
		// Sensor specks along the same edge, which the speckle floor
		// removes later, may sit between a rim and the edge; the rim
		// counts as on the edge when it is within its own thickness of
		// it. The digits lie on the other side, past a blank row or two.
		top := b.y0 <= limit && blank(b.x0, b.x1, b.y1+1, b.y1+2)
		// What is left of a decimal point when the box shaves the bottom
		// of the digits looks the same, but is no wider than a point: it
		// stays, and splitBlobs decides whether it is one.
		bottom := b.y1 >= m.h-1-limit && blank(b.x0, b.x1, b.y0-2, b.y0-1) && (float64(b.w()) > dotW || !feetCut)
		if flat && (top || bottom) {
			erase(m, b)
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

// dropLines removes any blob far wider than the row is tall and only a
// fraction of it high: a line across the crop, which is never a bar and
// would otherwise cluster every digit it overlaps into one glyph.
func dropLines(m bitmap, blobs []blob, inkH int) []blob {
	kept := blobs[:0]
	for _, b := range blobs {
		if float64(b.w()) > lineWideFrac*float64(inkH) && float64(b.h()) < lineThinFrac*float64(inkH) {
			erase(m, b)
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

// shearEstimate measures the lean of the vertical bars: the shear (x per
// row, positive when the top leans right, as an italic font does) whose
// removal makes the column projection of the ink sharpest. Vertical bars
// stack into few tall columns only when upright; horizontal bars
// contribute the same at any shear. The lean is corrected only when it
// buys a clear gain over upright, so hexagonal bar ends and noise never
// nudge a straight display.
func shearEstimate(m bitmap, wk *work) float64 {
	lit := wk.lit[:0]
	for i, on := range m.on {
		if on {
			lit = append(lit, int32(i))
		}
	}
	wk.lit = lit
	if len(lit) < 2 {
		return 0
	}
	span := int(math.Ceil(shearMax*float64(m.h))) + 1
	if n := m.w + 2*span; cap(wk.counts) < n {
		wk.counts = make([]int, n)
	}
	if cap(wk.shifts) < m.h {
		wk.shifts = make([]int, m.h)
	}
	counts, shifts := wk.counts[:m.w+2*span], wk.shifts[:m.h]
	yc := m.h / 2
	// The coarse search runs on a sample of the ink; the peak is broad.
	// Only rows ylo..yhi count, so the same search can run on half the
	// ink at a time.
	sharp := func(s float64, step, ylo, yhi int) float64 {
		clear(counts)
		for y := range shifts {
			// |s| <= shearMax and |y-yc| <= m.h/2 keep the shift inside
			// ±span; the clamp says so to the indexing below.
			shifts[y] = min(2*span, max(0, int(math.Round(s*float64(y-yc)))+span))
		}
		for i := 0; i < len(lit); i += step {
			idx := int(lit[i])
			if y := idx / m.w; y < ylo || y > yhi {
				continue
			}
			counts[idx%m.w+shifts[idx/m.w]]++
		}
		var sum float64
		for _, c := range counts {
			sum += float64(c) * float64(c)
		}
		return sum
	}
	best := func(ylo, yhi int) float64 {
		step := max(1, len(lit)/20000)
		base := sharp(0, step, ylo, yhi)
		bestS, bestV := 0.0, base
		for s := -shearMax; s <= shearMax+1e-9; s += shearStep {
			if math.Abs(s) < shearStep/2 {
				continue
			}
			if v := sharp(s, step, ylo, yhi); v > bestV {
				bestV, bestS = v, s
			}
		}
		if bestS == 0 {
			return 0
		}
		base, bestV = sharp(0, 1, ylo, yhi), sharp(bestS, 1, ylo, yhi)
		// The refinement walks up from just below the coarse best and goes
		// on while the lean keeps sharpening, but never past ±shearMax.
		// The cap is what was missing: on an angled clock photo, binarized,
		// the walk crept on past shearMax until a row's shift left counts
		// (index out of range). The upper bound still follows bestS: holding
		// it where the loop started changed 12 of 16,200 reads on round 6's
		// photos, one of them a flagged "?:17" into a confident "3:17". The
		// values tried start where they always did with the same step, so
		// a search that stays inside ±shearMax tries the same shears.
		for s := bestS - shearStep; s <= min(shearMax, bestS+shearStep)+1e-9; s += shearStep / 4 {
			if s < -shearMax-1e-9 {
				continue // the coarse best was -shearMax
			}
			if v := sharp(s, 1, ylo, yhi); v > bestV {
				bestV, bestS = v, s
			}
		}
		if bestV < base*(1+shearMinGain) {
			return 0
		}
		return bestS
	}
	whole := best(0, m.h-1)
	if whole == 0 {
		return 0
	}
	// A lean is real only when the upper and lower halves of the ink
	// agree on it. Stacking column projections rewards any shear that
	// lines bars up — including lining up an upright "2"'s upper-right
	// bar with its lower-left one, which is not a lean. A leaning
	// display leans in both halves; a single bar per half is sharpest
	// upright, and two halves that disagree are no evidence at all.
	top, bottom := best(0, yc), best(yc+1, m.h-1)
	if top == 0 || bottom == 0 || (top > 0) != (bottom > 0) || math.Abs(top-bottom) > 2*shearStep {
		return 0
	}
	return whole
}

// deshear shifts every row of m by its share of the lean so the vertical
// bars stand upright; the result is wider by the total shift.
func deshear(m bitmap, s float64, wk *work) bitmap {
	// The buffers are sized for a lean of at most shearMax (getWork).
	s = math.Max(-shearMax, math.Min(shearMax, s))
	shifts := wk.shifts[:m.h]
	yc := m.h / 2
	minS, maxS := 0, 0
	for y := range shifts {
		shifts[y] = int(math.Round(s * float64(y-yc)))
		minS, maxS = min(minS, shifts[y]), max(maxS, shifts[y])
	}
	w := m.w + maxS - minS
	out := bitmap{w: w, h: m.h, on: wk.bufs[2][:w*m.h]}
	clear(out.on)
	for y := 0; y < m.h; y++ {
		off := y*w + shifts[y] - minS
		copy(out.on[off:off+m.w], m.on[y*m.w:(y+1)*m.w])
	}
	return out
}

// decodeBitmap reads one upright, binarized crop: it sorts the blobs into
// bars, points and colons, clusters the bars into glyph cells, and probes
// every cell.
func decodeBitmap(m bitmap, blobs []blob, wk *work) reading {
	// The scale everything below is measured against is the height of the
	// row of ink — not of any one blob, since with real gaps between bars
	// every bar is its own blob and the tallest is a third of a digit.
	// Three passes: a rough extent over anything bigger than a speck, the
	// lines across the row that extent exposes, then the speckle floor the
	// extent implies and the extent again without the speckle.
	inkTop, inkBot := rowExtent(blobs, minBlobArea)
	if inkBot < inkTop {
		return reading{}
	}
	blobs = dropLines(m, blobs, inkBot-inkTop+1)
	inkTop, inkBot = rowExtent(blobs, minBlobArea)
	if inkBot < inkTop {
		return reading{}
	}
	speck := int(speckFrac * float64(inkBot-inkTop+1))
	minArea := max(minBlobArea, speck*speck)
	inkTop, inkBot = rowExtent(blobs, minArea)
	if inkBot < inkTop {
		return reading{}
	}
	inkH := inkBot - inkTop + 1

	bars, points, colons, leaning := splitBlobs(blobs, inkTop, inkH, minArea, m.h)
	if len(bars) == 0 {
		return reading{}
	}
	clusters := splitPairs(m, clusterBars(m, bars, inkH))
	clusters, units := splitStacked(m, clusters, bars, inkH, wk)
	clusters, points = attachDots(m, clusters, points, leaning)
	stroke := strokeEstimate(m, clusters, inkH, wk)
	clusters = dropSpecks(m, clusters, bars, stroke, wk)
	clusters, split := splitAttachedDots(m, clusters, stroke, inkH)
	points = append(points, split...)
	clusters, points, colons, promoted := smallOnesAfterPoint(clusters, points, colons, stroke, inkTop, inkH)
	cells, rowH := layoutCells(m, clusters, points, colons, promoted, stroke, inkTop, inkH)
	if cells == nil {
		return reading{}
	}
	points = append(points, blurredPoints(cells, inkH)...)
	points, cutPoints := pointsOnRow(points, cells, stroke, rowH, m.h)
	r := readCells(m, cells, stroke, rowH)
	if len(r.glyphs) == 0 {
		return reading{} // a point or colon with no digit is not a reading
	}
	for _, k := range cells {
		switch {
		case k.absorbed:
			continue
		case k.degree:
			r.marks = append(r.marks, k.c.rect())
		case k.fragment:
			r.dropped = append(r.dropped, k.c.rect())
		default:
			continue
		}
		if barPiece(k.c, stroke) {
			r.bars = append(r.bars, k.c.rect())
		}
	}
	for _, u := range units {
		r.marks = append(r.marks, u.rect())
	}
	for _, p := range cutPoints {
		r.cutPoints = append(r.cutPoints, p.rect())
	}
	for _, p := range points {
		g := separator('.', p, rowH)
		if cutPoint(p, m.h) {
			if p.x1 >= m.w-1 {
				// In the box's bottom corner, after the last digit: the
				// frame round the display ("12:25" read 12:25.).
				continue
			}
			g.conf = math.Min(g.conf, missedPointConf)
		}
		r.glyphs = append(r.glyphs, g)
	}
	for _, c := range colons {
		r.glyphs = append(r.glyphs, separator(':', c, rowH))
	}
	sort.SliceStable(r.glyphs, func(i, j int) bool { return r.glyphs[i].x < r.glyphs[j].x })
	dropLoneColons(&r)
	return r
}

// cutPoint reports whether a point is what the bottom of the box left of
// one: a sliver on the crop's bottom edge, flatter than half a square. A
// whole point on a tight crop touches that edge too, but is square. A cut
// point is weaker evidence than a whole one, so it goes into the reading
// flagged, at no more than missedPointConf: the number is right and Test
// this region shows the box wants redrawing.
func cutPoint(p blob, cropH int) bool {
	return p.y1 == cropH-1 && 2*p.h() < p.w()
}

// blurredPoints finds the decimal points that splitBlobs passed over: on
// a small display blur grows a point past dotMaxFrac (and often taller
// than wide), so it is clustered as a fragment and dropped, and 90.1
// would read as a confident 901. A fragment that is small, mostly solid,
// about square and stands between two digits is taken for a point;
// pointsOnRow still checks it sits on the baseline just after a digit. A
// digit's own lower bar is in its digit's cluster, not a fragment of its
// own.
func blurredPoints(cells []cell, inkH int) []blob {
	var out []blob
	for _, k := range cells {
		c := k.c
		if !k.fragment || float64(max(c.w(), c.h())) > colonDotMaxFrac*float64(inkH) ||
			2*c.w() < c.h() || 2*c.h() < c.w() || float64(c.area) < blurredPointSolid*float64(c.w()*c.h()) {
			continue
		}
		left, right := false, false
		for _, o := range cells {
			if o.degree || o.fragment {
				continue
			}
			left = left || o.c.x1 < c.x0
			right = right || o.c.x0 > c.x1
		}
		if left && right {
			out = append(out, c)
		}
	}
	return out
}

// pointsOnRow keeps the points that sit where a decimal point does: on
// the digits' baseline, just after a digit. A glare speck in the corner
// of the window, below the digits' feet, a glint on the glass a few bars
// above the baseline, or a lamp or screw on the baseline a digit's width
// clear of the reading is not this reading's point, and reporting it
// would turn "09" into "09.". The baseline is the bottom of the tallest
// cells, which already reaches past a "1", "4" or "7" to where its bottom
// bar would be; a point's foot is level with the digits' bottom bars, so
// it reaches to within a stroke of the lowest ink of the digits.
//
// A point on the baseline with no digit before it and no room for one
// between it and the box's left edge is returned apart, as cut: the box
// cut through the digit it follows.
//
// A mark after the last digit that the box's bottom edge cuts, its centre
// below the digits' feet, is the top of something under the display (a
// meter's bar-graph scale) and no point: 002.4 read 002.4. once the 4 read
// surely.
func pointsOnRow(points []blob, cells []cell, stroke int, rowH float64, cropH int) (kept, cut []blob) {
	base, feet := math.MinInt, math.MinInt
	var footRows []int // each digit's foot: their median is the baseline a mark under the row is below
	for _, k := range cells {
		if k.tall && !k.degree && !k.fragment {
			base, feet = max(base, k.y1), max(feet, k.c.y1)
			footRows = append(footRows, k.c.y1)
		}
	}
	// Two marks one over the other after the last digit are no point: the
	// pieces of a unit's letter beside the digits (the s of a clock's
	// seconds read 12:34..). Between digits, one may be the point and the
	// other a glint or a ghost over it.
	last := math.MinInt
	for _, k := range cells {
		if !k.degree && !k.fragment {
			last = max(last, k.c.x1)
		}
	}
	stacked := make([]bool, len(points))
	for i, p := range points {
		for j, q := range points {
			stacked[i] = stacked[i] || (i != j && p.x0 > last && q.x0 <= p.x1 && p.x0 <= q.x1)
		}
	}
	kept = points[:0]
	for i, p := range points {
		if stacked[i] || inSmall(p, cells) {
			continue
		}
		if p.cy() > float64(base)+float64(stroke)/2 {
			continue // below the digits' feet
		}
		if p.y1 < feet-stroke {
			continue // above the baseline
		}
		if p.x0 > last && p.y1 == cropH-1 && p.cy() > float64(median(footRows)) {
			continue // under the display, cut by the box
		}
		prev := math.MinInt // the right edge of the nearest digit before it
		for _, k := range cells {
			if !k.degree && !k.fragment && k.c.cx() < p.cx() {
				prev = max(prev, k.c.x1)
			}
		}
		if prev == math.MinInt && float64(p.x0) < cellWidthMax*rowH {
			cut = append(cut, p)
			continue
		}
		if prev == math.MinInt || float64(p.x0-prev-1) > pointGapFrac*rowH {
			continue
		}
		kept = append(kept, p)
	}
	return kept, cut
}

// dropLoneColons removes a colon with no glyph on one side of it. A
// clock's colon separates digits; two marks astride the row with nothing
// beyond them are the unlit colon of a display showing a word ("End"), or
// a pair of specks, and reporting them would turn the '?' such a display
// reads as into "?:". A colon with digits after it and nothing before is
// different: on a small, blurred display a leading "1" breaks into two
// dots just like one, and dropping it would read 1400 as a confident
// 400. It stays, as a '?'.
func dropLoneColons(r *reading) {
	kept := r.glyphs[:0]
	for i, g := range r.glyphs {
		if g.ch == ':' {
			left, right := false, false
			for _, o := range r.glyphs[:i] {
				left = left || o.digit()
			}
			for _, o := range r.glyphs[i+1:] {
				right = right || o.digit()
			}
			if !left && right {
				g.ch, g.conf = '?', unsureConf
				r.score -= 100
			} else if !left || !right {
				continue
			}
		}
		kept = append(kept, g)
	}
	r.glyphs = kept
}

// rowExtent is the vertical span of every blob at least minArea big;
// bot < top when there is none.
func rowExtent(blobs []blob, minArea int) (top, bot int) {
	top, bot = math.MaxInt, math.MinInt
	for _, b := range blobs {
		if b.area >= minArea {
			top, bot = min(top, b.y0), max(bot, b.y1)
		}
	}
	if top == math.MaxInt {
		return 0, -1
	}
	return top, bot
}

// splitBlobs sorts the blobs into bars, decimal points and colons. A
// point is a small, roughly square, solid blob sitting on the baseline; a
// colon is two such marks one above the other astride the row's middle.
// Both are held out of the bar clustering, since a real display puts a
// point closer to its digit than the bars of one digit are to the next. A
// small mark that is neither goes back to the bars.
//
// A colon's dots may be bigger than a lone point: on a small display they
// are as big as the bars are thick. Short, square bars stack the same way
// inside a digit (a fat font's side bars, a small digit's middle and bottom
// bars), but then the digit's own bars stand a sliver beside the gap
// between them, where the digits round a colon have their facing sides
// reaching past the gap. A point cut by the bottom of the box is as wide
// as a point and flatter.
func splitBlobs(blobs []blob, inkTop, inkH, minArea, cropH int) (bars, points, colons []blob, leaning bool) {
	fInk := float64(inkH)
	baseline := float64(inkTop) + dotBaselineFrac*fInk
	join := max(1, int(math.Round(barJoinFrac*fInk)))
	var marks, all []blob
	var big, loose []bool
	for _, b := range blobs {
		if b.area < minArea {
			continue
		}
		all = append(all, b)
		small := float64(b.w()) <= dotMaxFrac*fInk && float64(b.h()) <= dotMaxFrac*fInk
		squarish := 2*b.w() >= b.h() && 2*b.h() >= b.w()
		solid := float64(b.area) >= dotSolidFrac*float64(b.w()*b.h())
		cut := b.y1 == cropH-1 && float64(b.w()) <= dotMaxFrac*fInk && b.h() <= b.w()
		long, short := float64(max(b.w(), b.h())), float64(min(b.w(), b.h()))
		colonDot := !small && long <= colonDotMaxFrac*fInk && long <= colonDotAspect*short
		// A colon's dot that is not solid: on a dot-matrix display each
		// dot of the colon is a few LEDs, which a binarized crop shows
		// apart ("12:25" read 1225). It can only be a colon's, with a dot
		// like it below or above; alone it goes back to the bars.
		open := !solid && float64(b.area) >= colonOpenFill*float64(b.w()*b.h()) &&
			((small && squarish) || colonDot) && long <= colonDotAspect*short
		if (solid && ((small && squarish) || cut || colonDot)) || open {
			marks = append(marks, b)
			big = append(big, colonDot)
			loose = append(loose, open)
		} else {
			bars = append(bars, b)
		}
	}
	rowsMeet := func(a, b blob) bool { return a.y0 <= b.y1 && b.y0 <= a.y1 }
	// inDigit reports whether a pair of marks is two bars of one digit: a
	// blob a sliver beside the pair lies in the gap between the marks,
	// where a digit has its middle bar between its side bars, or its side
	// bars beside its middle and bottom bars. A colon has the facing
	// sides of the digits round it there, which reach past the gap.
	//
	// A leaning pair (see aligned below) is judged along its slant: the
	// column it stands in at a blob's rows (leanCol), not the box round both
	// dots, which on a slanted clock reaches over the digit before it.
	//
	// The same, for a pair further apart than colonGapNear, when a vertical
	// bar runs a sliver from the pair on both sides, down at least half the
	// gap: a fat font's top and bottom bars between its side bars, which run
	// on past them, as square as a big colon's dots and as far apart as a
	// clock's whose dots sit level with its top and bottom bars ("0" read
	// "1:1").
	inDigit := func(up, down blob) bool {
		pair := up.merge(down)
		var left, right bool
		g0, g1 := up.y1+1, down.y0-1
		for _, o := range all {
			if o == up || o == down {
				continue
			}
			x0, x1 := float64(pair.x0), float64(pair.x1)
			if c, half, ok := leanCol(up, down, o.cy()); ok {
				x0, x1 = c-half, c+half
			}
			near := float64(o.x0) <= x1+float64(join+1) && x0 <= float64(o.x1+join+1)
			if near && o.y0 > up.y1 && o.y1 < down.y0 {
				return true
			}
			if near && g1 >= g0 && 2*(min(o.y1, g1)-max(o.y0, g0)+1) >= g1-g0+1 {
				left = left || float64(o.x1) < x0
				right = right || float64(o.x0) > x1
			}
		}
		return left && right && float64(g1-g0+1) > colonGapNear*fInk
	}
	// placed reports whether a pair stands where a colon does: in a
	// column of its own, no bar above or below it. With ink on both
	// sides, the space before it is at most colonCentreRatio times the
	// space after: a digit's own bars stacked like a colon's dots (the
	// horizontal bars of a "3" with no left-hand bars, the left-hand bars
	// of a fat "0") stand far closer to the rest of their digit than to the
	// digit before. A pair of big dots must have ink on both sides, and
	// stand close after the digit before it: big dots are a small display's
	// glow, where a colon stands a dot's width or less from the digits
	// round it. A fat font's "1" is a pair of big square bars too, but it
	// sits at the right of its cell, its empty left part before it. The
	// spaces are known to within half a dot: that glow fuses the colon to
	// the digit after it and leaves a pixel or two before it, and the
	// pixel or two grows with the picture when the crop is upscaled.
	//
	// A pair of open dots (see open below) must stand clear of the digits
	// on both sides, about centred between them: the LEDs of a dot-matrix
	// digit, a few of them left at a threshold, stack the same way.
	placed := func(up, down blob, big, open bool) bool {
		pair := up.merge(down)
		before, after := math.MaxInt, math.MaxInt
		for _, o := range all {
			if o == up || o == down {
				continue
			}
			cx, x0, x1 := pair.cx(), pair.x0, pair.x1
			if c, half, ok := leanCol(up, down, o.cy()); ok {
				cx, x0, x1 = c, int(math.Floor(c-half)), int(math.Ceil(c+half))
			}
			if float64(o.x0) <= cx && cx <= float64(o.x1) {
				return false // under or over a bar of a digit: its middle and bottom bars
			}
			if rowsMeet(o, pair) {
				if o.x1 < x0 {
					before = min(before, x0-o.x1-1)
				}
				if o.x0 > x1 {
					after = min(after, o.x0-x1-1)
				}
			}
		}
		if before == math.MaxInt || after == math.MaxInt {
			return !big && !open
		}
		dot := float64(max(up.w(), down.w()))
		slack := math.Max(1, colonSlackDots*dot)
		if open && (before == 0 || float64(after) > colonCentreRatio*float64(before)+slack) {
			return false
		}
		return float64(before) <= colonCentreRatio*float64(after)+slack && (!big || float64(before) <= colonCentreRatio*dot+slack)
	}
	mid := float64(inkTop) + 0.5*fInk
	used := make([]bool, len(marks))
	var pairs [][2]int
	for i := range marks {
		for j := i + 1; j < len(marks) && !used[i]; j++ {
			if used[j] {
				continue
			}
			up, down := marks[i], marks[j]
			if up.cy() > down.cy() {
				up, down = down, up
			}
			gap := float64(down.y0 - up.y1)
			// One dot above the other, or the upper one further right by
			// up to an italic lean: a slanted clock's colon leans with
			// its digits, and the lean isn't always measured (one dot
			// and a few bars tell shearEstimate nothing).
			_, _, leaning := leanCol(up, down, mid)
			aligned := !leaning || leanPair(up, down)
			minGap := colonGapMin * fInk
			if big[i] || big[j] {
				minGap = max(1, colonGapMinBig*fInk)
			}
			// Open dots pair only with their like: as square, much the
			// same size, one above the other.
			twins := (!loose[i] && !loose[j]) || (!leaning && twinDots(up, down))
			if aligned && twins && gap >= minGap && gap <= colonGapMax*fInk && up.cy() < mid+colonMidSlack*fInk && down.cy() > mid &&
				!inDigit(up, down) && placed(up, down, big[i] || big[j], loose[i] || loose[j]) {
				pairs = append(pairs, [2]int{i, j})
				used[i], used[j] = true, true
			}
		}
	}
	// A font whose own bars come out as square as a colon's dots (a fat
	// font's "1" is a pair of them, stacked astride the middle) leaves
	// some such bar above the baseline that is no colon: the last
	// glyph's, which has no digit after it, or has a bar of its own beside
	// it. Then no big mark is trusted as a colon's dot or a point, and
	// they all go back to the bars.
	trust := true
	for i, mk := range marks {
		trust = trust && (!big[i] || used[i] || loose[i] || mk.cy() >= baseline)
	}
	for _, p := range pairs {
		i, j := p[0], p[1]
		if !trust && (big[i] || big[j]) {
			used[i], used[j] = false, false
			continue
		}
		c := marks[i].merge(marks[j])
		up, down := marks[i], marks[j]
		if up.cy() > down.cy() {
			up, down = down, up
		}
		if x, half, ok := leanCol(up, down, (up.cy()+down.cy())/2); ok {
			// A leaning colon stands where its column crosses the middle of
			// the row, one dot wide (its confidence judges a dot's width).
			c.x0, c.x1 = int(math.Round(x-half)), int(math.Round(x+half))-1
			leaning = true
		}
		colons = append(colons, c)
	}
	// A mark on the baseline is a point candidate even when it is as big
	// as a colon's dot (a blurred point grows past dotMaxFrac), unless the
	// font's own bars look like that; pointsOnRow drops one that isn't
	// just after a digit.
	for i, mk := range marks {
		if used[i] {
			continue
		}
		if !loose[i] && mk.cy() >= baseline && (trust || !big[i]) {
			points = append(points, mk)
		} else {
			bars = append(bars, mk)
		}
	}
	return bars, points, colons, leaning
}

// leanCol is where a leaning colon stands at row y: the centre of its
// column there, on the line through its two dots' centres, and half a
// dot's width. ok is false for a pair standing one dot above the other
// (within half a dot, or two pixels), which is judged by its bounding box
// as it always was.
func leanCol(up, down blob, y float64) (c, half float64, ok bool) {
	lean := up.cx() - down.cx()
	if math.Abs(lean) <= math.Max(2, 0.5*float64(max(up.w(), down.w()))) {
		return 0, 0, false
	}
	t := (down.cy() - math.Max(up.cy(), math.Min(down.cy(), y))) / math.Max(1, down.cy()-up.cy())
	return down.cx() + t*lean, float64(max(up.w(), down.w())) / 2, true
}

// twinDots reports whether two marks are two of a kind, as a colon's dots
// are: both about square, much the same size and area.
func twinDots(a, b blob) bool {
	square := func(b blob) bool {
		return float64(max(b.w(), b.h())) <= colonDotAspect*float64(min(b.w(), b.h()))
	}
	return square(a) && square(b) && max(a.area, b.area) <= 2*min(a.area, b.area) &&
		float64(max(a.w(), b.w())) <= colonDotAspect*float64(min(a.w(), b.w()))
}

// leanPair reports whether two marks that don't stand one above the other
// can still be a colon: the upper one is further right by no more than an
// italic lean (colonLeanMax), and they are two of a kind, as a colon's dots
// are: both about square and much the same size. Pieces of a dotted bar
// lean too, but are flat or ragged.
func leanPair(up, down blob) bool {
	lean := up.cx() - down.cx()
	slack := math.Max(2, 0.5*float64(max(up.w(), down.w())))
	if lean <= 0 || lean > slack+colonLeanMax*(down.cy()-up.cy()) {
		return false
	}
	square := func(b blob) bool {
		return float64(max(b.w(), b.h())) <= colonDotAspect*float64(min(b.w(), b.h()))
	}
	return square(up) && square(down) && max(up.area, down.area) <= 2*min(up.area, down.area)
}

// clusterBars groups bars that overlap or nearly touch in x into glyphs:
// every digit's horizontal bars span its cell, and real displays leave only
// a sliver between a horizontal bar's end and the vertical bar beside it.
// On a small, glowing display the gap between two digits is a sliver too.
// What tells them apart is which way the bars face: within a digit the
// sliver lies between a horizontal bar and a vertical one, which share a
// corner and hardly any rows, while two digits face each other with their
// side bars, which share most of their rows. Where a font leaves gaps
// between its bars, a fat horizontal bar's end faces the vertical bar
// beside it on as many rows as two digits' sides do (split there, the
// digit fell apart into a "3" and a "?"), but the vertical bar runs on
// above or below it: two digits' facing sides are lit on about the same
// rows, a bar's end and the bar beside it are not.
func clusterBars(m bitmap, bars []blob, inkH int) []blob {
	sort.Slice(bars, func(i, j int) bool { return bars[i].x0 < bars[j].x0 })
	join := max(1, int(math.Round(barJoinFrac*float64(inkH))))
	side := sideBySideFrac * float64(inkH)
	var clusters []blob
	var last []blob // the bars of the last cluster
	// facing counts the rows where b's leftmost columns and the rightmost
	// ones of a bar of the cluster a sliver before it are both lit: the
	// edges that face each other, not the bounding boxes (a bottom bar
	// merged with the vertical bar it touches spans that bar's rows). The
	// edge is a few columns deep, so a bump of glow on a bar's side does
	// not stand in for the bar.
	deep := max(2, join)
	lit := func(x0, x1, y int) bool {
		for x := x0; x <= x1; x++ {
			if m.at(x, y) {
				return true
			}
		}
		return false
	}
	facing := func(b blob) bool {
		for _, e := range last {
			if b.x0 <= e.x1 || b.x0 > e.x1+join+1 {
				continue
			}
			eEdge := func(y int) bool { return lit(max(e.x0, e.x1-deep+1), e.x1, y) }
			bEdge := func(y int) bool { return lit(b.x0, min(b.x1, b.x0+deep-1), y) }
			n, ne, nb := 0, 0, 0
			for y := min(e.y0, b.y0); y <= max(e.y1, b.y1); y++ {
				le := y >= e.y0 && y <= e.y1 && eEdge(y)
				lb := y >= b.y0 && y <= b.y1 && bEdge(y)
				if le {
					ne++
				}
				if lb {
					nb++
				}
				if le && lb {
					n++
				}
			}
			if float64(n) > side && float64(n) >= sideBySideShare*float64(max(ne, nb)) {
				return true
			}
		}
		return false
	}
	// above reports whether b is a small mark standing clear above every
	// bar of the cluster that ends a sliver before it, while the cluster
	// has its own bars at b's rows further left: the degree sign over the
	// foot of a slanted 5, which the lean, taken out, puts a sliver after
	// the 5's lower bars ("5°C" read '?'). Within a digit a bar a sliver
	// from another shares a corner with it, or is the first of the digit's
	// bars at its rows (the upper bar of a "1" leaning a little more than
	// the lean taken out).
	above := func(b blob) bool {
		if float64(b.h()) > aboveMarkFrac*float64(inkH) {
			return false
		}
		level := false
		for _, e := range last {
			if e.x1+join >= b.x0 && e.y0 <= b.y1+join {
				return false
			}
			level = level || (e.y0 <= b.y1 && b.y0 <= e.y1)
		}
		return level
	}
	for _, b := range bars {
		n := len(clusters)
		if n > 0 && (b.x0 <= clusters[n-1].x1 || (b.x0 <= clusters[n-1].x1+join && !facing(b) && !above(b))) {
			clusters[n-1] = clusters[n-1].merge(b)
			last = append(last, b)
		} else {
			clusters = append(clusters, b)
			last = append(last[:0], b)
		}
	}
	return clusters
}

// attachDots folds a "dot" inside a glyph's own columns back into that
// glyph: it is a short bar of the glyph (the bottom bar of a fat-stroke
// font), not a decimal point. The rest are returned as points.
//
// On a display whose colon leans (leaning, from splitBlobs), a digit
// leans the same way, and when the lean wasn't measured the point after
// it stands inside its columns too: its top leans out over the point. Then
// a dot past all of the digit's ink in the dot's own rows is the point; a
// bar of the digit has its ink on both sides, or none.
func attachDots(m bitmap, clusters, dots []blob, leaning bool) (_, points []blob) {
	// after reports whether, in d's rows and c's columns, there is ink to
	// the left of d and none to its right.
	after := func(c, d blob) bool {
		left := false
		for y := d.y0; y <= d.y1; y++ {
			for x := c.x0; x <= c.x1; x++ {
				if x >= d.x0 && x <= d.x1 || !m.at(x, y) {
					continue
				}
				if x > d.x1 {
					return false
				}
				left = true
			}
		}
		return left
	}
	for _, d := range dots {
		attached := false
		for i, c := range clusters {
			if d.cx() >= float64(c.x0) && d.cx() <= float64(c.x1) && !(leaning && after(c, d)) {
				clusters[i] = c.merge(d)
				attached = true
				break
			}
		}
		if !attached {
			points = append(points, d)
		}
	}
	return clusters, points
}

// strokeEstimate is the bar thickness: the median length of the lit runs
// along the rows and columns of the glyph clusters, leaving out runs long
// enough to be a bar seen lengthways rather than across. Falls back to a
// typical proportion of the digit height when nothing qualifies.
func strokeEstimate(m bitmap, clusters []blob, inkH int, wk *work) int {
	limit := max(1, int(strokeRunCapFrac*float64(inkH)))
	runs := wk.runs[:0]
	add := func(run int) {
		if run > 0 && run <= limit {
			runs = append(runs, run)
		}
	}
	for _, c := range clusters {
		for y := c.y0; y <= c.y1; y++ {
			run := 0
			for x := c.x0; x <= c.x1; x++ {
				if m.at(x, y) {
					run++
				} else {
					add(run)
					run = 0
				}
			}
			add(run)
		}
		for x := c.x0; x <= c.x1; x++ {
			run := 0
			for y := c.y0; y <= c.y1; y++ {
				if m.at(x, y) {
					run++
				} else {
					add(run)
					run = 0
				}
			}
			add(run)
		}
	}
	wk.runs = runs
	// Sensor speckle and ragged bar edges that survive the threshold each
	// leave a run a pixel or two long, and on a noisy frame those outnumber
	// the runs across real bars; they carry no stroke information, so the
	// median is taken over runs at least a few percent of the digit tall.
	// The result is held to the band real displays occupy.
	floor := max(2, int(math.Round(strokeRunFloorFrac*float64(inkH))))
	kept := runs[:0]
	for _, r := range runs {
		if r >= floor {
			kept = append(kept, r)
		}
	}
	lo := max(1, int(math.Round(strokeMinFrac*float64(inkH))))
	hi := max(lo, int(math.Round(strokeMaxFrac*float64(inkH))))
	if len(kept) == 0 {
		return min(hi, max(lo, int(math.Round(strokeFallbackFrac*float64(inkH)))))
	}
	sort.Ints(kept)
	return min(hi, max(lo, kept[len(kept)/2]))
}

// splitAttachedDots finds a decimal point that touched its digit's lower
// right bar — a camera's blur closes the few pixels of physical gap on a
// small or distant display — and came through as part of the digit's
// cluster. Its signature is ink in the bottom rows of the cell that juts
// out past the digit's right edge as measured in the rows above, by
// about a stroke and no more than a point's width plus a stroke. The jut
// becomes a point and the cell shrinks back to the digit.
func splitAttachedDots(m bitmap, clusters []blob, stroke, inkH int) (_, points []blob) {
	band := max(1, int(math.Round(dotMaxFrac*float64(inkH))))
	minJut := max(2, stroke/2)
	rightmost := func(c blob, y0, y1 int) int {
		r := c.x0 - 1
		for y := y0; y <= y1; y++ {
			for x := c.x1; x > r; x-- {
				if m.at(x, y) {
					r = x
					break
				}
			}
		}
		return r
	}
	for i, c := range clusters {
		if float64(c.h()) < tallCellFrac*float64(inkH) || c.h() <= band+1 {
			continue
		}
		yb := c.y1 - band + 1
		xTop := rightmost(c, c.y0, yb-1)
		xBot := rightmost(c, yb, c.y1)
		jut := xBot - xTop
		if jut < minJut || jut > band+stroke {
			continue
		}
		d := blob{x0: xTop + 1, y0: c.y1, x1: xBot, y1: c.y1}
		for y := yb; y <= c.y1; y++ {
			for x := xTop + 1; x <= xBot; x++ {
				if m.at(x, y) {
					d.area++
					d.y0 = min(d.y0, y)
				}
			}
		}
		for x := d.x0; x < d.x1 && !m.at(x, d.y1) && !m.at(x, d.y0); x++ {
			d.x0 = x + 1 // skip the gap between bar and point
		}
		if float64(d.area) < dotSolidFrac*float64(d.w()*d.h()) {
			continue
		}
		points = append(points, d)
		clusters[i].x1 = xTop
	}
	return clusters, points
}

// cell is one glyph's probing box, laid out from its cluster.
type cell struct {
	c          blob
	wide, tall bool
	x0, y0     int
	x1, y1     int
	fromRow    bool    // y0/y1 still to be filled from the row's extent
	offCrop    bool    // a "1" cut through by the crop's left edge
	thin       bool    // a line thinner than half a stroke, not a "1"
	degree     bool    // a degree sign: dropped from the reading
	fragment   bool    // too small for a bar or a "-": dropped
	skip       int     // columns at the left of the cell the probes leave out: another glyph's ink
	leftBars   bool    // a narrow cluster where a digit's left-hand bars stand, not a "1"
	afterMark  bool    // a "1" with a point or colon between it and the glyph before it
	centre     float64 // reading order
	sizeRef    float64 // the height its width is judged against (see sizeRef)
	small      bool    // a small digit after the point or a clock's minutes (see smallAfterPoint)
	absorbed   bool    // a piece of such a small digit, merged into its cell
	unread     bool    // a small digit with another one in pieces merged in: read as '?'
}

// layoutCells turns clusters into cells. A glyph's ink only reaches the top
// of its cell when the top bar is lit ("1", "4" and "7" stop a stroke
// short), so each tall cluster's cell is extended by a stroke where its
// ink shows no bar there. Short clusters (a lone "-") take the row's
// extent. rowH is the row's height with those extensions; cells is nil
// when nothing tall is showing. points and colons are the row's marks,
// which a "1"'s cell leaves out of its probes.
func layoutCells(m bitmap, clusters, points, colons, promoted []blob, stroke, inkTop, inkH int) (cells []cell, rowH float64) {
	fInk := float64(inkH)
	marks := append(append([]blob(nil), points...), colons...)
	reach := stroke + max(1, int(math.Round(reachFrac*float64(stroke))))
	cells = make([]cell, 0, len(clusters))
	rowY0, rowY1 := math.MaxInt, math.MinInt
	maxWide := 0
	full := fullDigits(clusters, fInk)
	for _, c := range clusters {
		k := cell{c: c, centre: c.cx(), y0: c.y0, y1: c.y1, sizeRef: fInk}
		if full {
			k.sizeRef = sizeRef(m, c, fInk, stroke, reach)
		}
		k.wide = float64(c.w()) >= wideCellFrac*k.sizeRef
		k.tall = float64(c.h()) >= tallCellFrac*fInk
		if !k.tall {
			k.fromRow = true
			squarish := 2*c.w() <= 3*c.h() && 2*c.h() <= 3*c.w()
			k.degree = squarish && c.cy() < float64(inkTop)+degreeTopFrac*fInk
			// A "-" is one bar thick and sits across the middle of the
			// row. A short cluster that is taller than a bar or sits
			// elsewhere is the edge of a unit symbol or a mark beside the
			// digits caught in the box; laid out as a cell it would
			// overlap the digit next to it and read that digit's bars as
			// its own.
			// Noise can erode the digits' bars at a tight threshold so the
			// stroke measures well under a solid minus sign's thickness; a
			// bar no taller than minusMaxFrac of the row and lying flat is
			// a minus all the same. The side stroke of a unit symbol stands
			// upright.
			thin := float64(c.h()) <= minusMaxStrokes*float64(stroke) || (float64(c.h()) <= minusMaxFrac*fInk && c.w() >= c.h())
			barLike := thin && c.cy() >= float64(inkTop)+minusBand0*fInk && c.cy() <= float64(inkTop)+minusBand1*fInk
			if k.degree && barLike && c.w() > c.h() {
				// A degree sign follows the number. A flat bar before
				// every digit is its minus sign, high in the row when
				// the digits have no top bar: -4.2 read a confident 4.2.
				lead := true
				for _, o := range clusters {
					lead = lead && !(o.x1 < c.x0 && float64(o.h()) >= tallCellFrac*fInk)
				}
				k.degree = !lead
			}
			k.fragment = !k.degree && !barLike
		} else {
			if !k.wide || !barAcross(m, c, c.y0, min(c.y1, c.y0+max(1, stroke/2)-1)) {
				k.y0 -= reach
			}
			if !k.wide || !barAcross(m, c, max(c.y0, c.y1-max(1, stroke/2)+1), c.y1) {
				k.y1 += reach
			}
			rowY0, rowY1 = min(rowY0, k.y0), max(rowY1, k.y1)
			if k.wide {
				maxWide = max(maxWide, c.w())
			}
		}
		cells = append(cells, k)
	}
	smallAfterPoint(m, cells, points, colons, promoted, stroke, reach, inkTop, fInk)
	if rowY0 == math.MaxInt {
		return nil, 0
	}
	rowH = float64(rowY1 - rowY0 + 1)
	W := cellWidth(cells, maxWide, rowH, inkH, stroke)
	gap := digitGap(cells, marks, fInk)
	for i := range cells {
		k := &cells[i]
		if k.fromRow {
			k.y0, k.y1 = rowY0, rowY1
		}
		switch {
		case k.wide:
			k.x0, k.x1 = k.c.x0, k.c.x1
			// A "7" or "3" has no bars down its left side, so its ink
			// starts a stroke into its cell. On a small display that
			// narrower cell puts the columns where the top, middle and
			// bottom bars are probed onto the right-hand bars; then the
			// cell is laid out from the right, as wide as the widest
			// digit, but never over the ink of the glyph before it: a "3"
			// a pixel after a "0" took the 0's right bar for its own left
			// bars and read a confident 8. A digit the box cuts on the
			// right is narrow too, but it is on the crop's edge and has
			// its left-hand bars.
			// A smaller digit (the tenths) is laid out as wide as the
			// widest digit drawn its size.
			full := int(math.Round(float64(maxWide) * k.sizeRef / fInk))
			if k.small {
				// Its own height is known (smallAfterPoint), and sizeRef
				// errs on the large side.
				full = int(math.Round(float64(maxWide) * float64(k.y1-k.y0+1) / fInk))
			}
			narrow := float64(k.c.w()) < narrowCellFrac*float64(full) && float64(k.c.w())*(1-midBand1) <= float64(stroke)
			if narrow && k.c.x1 < m.w-1 && !leftBar(m, k.c, stroke) {
				k.x0 = k.x1 - full + 1
				for _, o := range clusters {
					if o.x1 < k.c.x0 {
						k.x0 = max(k.x0, o.x1+1)
					}
				}
			}
		case k.tall: // a "1": its two bars sit at the right of the cell
			k.x1 = k.c.x1
			k.x0 = k.x1 - W + 1
			if k.small {
				// A smaller "1" has a cell its own size: laid out a
				// full-size digit wide, a clock's second small 1 took
				// in the first one's bars (12:34 11 read 12:341?).
				k.x0 = k.x1 - max(1, int(math.Round(float64(W)*float64(k.y1-k.y0+1)/fInk))) + 1
			}
			// A leading "1" is the leftmost thing on most displays and
			// its cell always begins before its bars, so the cell may
			// start left of the crop. The bars themselves must not: ink
			// on the crop's edge is a bezel, a reflection or a digit the
			// box cut through.
			k.offCrop = k.x0 < 0 && k.c.x0 < max(2, stroke/2)
			k.thin = float64(k.c.w()) < oneMinStrokeFrac*float64(stroke)
			// A "1" stands at the right of its cell, so after a full-width
			// digit it starts the usual gap plus most of a digit's width
			// later. Bars that start right after the usual gap are the
			// left-hand bars of the next digit, whose right-hand bars the
			// threshold lost or the box cut off: read as a "1", "0" became
			// "1" (or, with the digit before kept out of the cell, a
			// confident one). A smaller "1" (a clock's seconds) stands
			// that much closer, by its own size (12:34 01 read 12:340?).
			scale := 1.0
			if k.small {
				scale = math.Min(1, k.sizeRef/fInk)
			}
			for _, o := range clusters {
				if o.x1 < k.c.x0 && float64(o.w()) >= wideCellFrac*fInk && float64(o.h()) >= tallCellFrac*fInk {
					prev := o.x1
					for _, q := range clusters {
						if q.x1 < k.c.x0 && q.x1 > prev {
							prev = -1 // something stands between: not the digit before
						}
					}
					if prev >= 0 && float64(k.c.x0-prev-1) < scale*(gap+oneShiftFrac*float64(maxWide-k.c.w())) {
						k.leftBars = true
						k.fragment = k.fragment || windowEdge(k.c, o, clusters, gap)
					}
				}
			}
			before := math.MinInt // the right edge of the glyph before it
			for _, o := range clusters {
				if o.x1 < k.c.x0 {
					before = max(before, o.x1)
				}
			}
			for _, o := range marks {
				k.afterMark = k.afterMark || (before != math.MinInt && o.x0 > before && o.x1 < k.c.x0)
			}
		default: // a "-": centred
			k.x0 = int(k.centre) - W/2
			k.x1 = k.x0 + W - 1
		}
	}
	// A "-" stands in a cell of its own. A short bar whose centre falls
	// inside a digit's cell is a piece of that digit the clustering split
	// off (on a small display a sliver of a gap separates a digit's middle
	// bar from its sides), and as a cell of its own it would read the
	// digit's bars a second time.
	for i := range cells {
		k := &cells[i]
		if k.tall || k.degree || k.fragment {
			continue
		}
		for _, o := range cells {
			if o.tall && k.centre >= float64(o.x0) && k.centre <= float64(o.x1) {
				k.fragment = true
				break
			}
		}
	}
	// A "1"'s cell leaves out the ink of other glyphs before its bars,
	// once the pieces of digits are known (just above).
	for i := range cells {
		k := &cells[i]
		if !k.tall || k.wide || k.degree || k.fragment {
			continue
		}
		// The cell is laid out by the "1"'s own bars, so it may take in
		// what stands before them: a colon or point a dot's width to
		// the left, the digit before on a tight display, the housing.
		// None of that is the "1"'s, and probed as its top or middle
		// bar it read '?'. The probes leave out everything up to the
		// last of it. Not a narrow cluster: that may be the left-hand
		// bars of this same digit, split off from its right-hand ones
		// (a "0" read as "1" is worse than a '?'). And only for bars
		// as thin as a "1"'s: a wider narrow cluster is something else
		// run together (a unit sign over a small digit), and the ink
		// beside it is the evidence that it is no "1".
		from := k.x0
		if float64(k.c.w()) > oneMaskStrokes*float64(stroke) {
			from = math.MinInt
		}
		for _, o := range cells {
			// A full-width digit or a "-" is another glyph; a piece
			// too small for either (a fragment, a "degree" mark) may be
			// a bar of this one, split off on a small display.
			other := (o.wide && o.tall) || (!o.tall && !o.fragment && !o.degree)
			if from != math.MinInt && other && o.c.x1 < k.c.x0 && o.c.x1 >= from {
				from = o.c.x1 + 1
			}
		}
		for _, o := range marks {
			if from != math.MinInt && o.x1 < k.c.x0 && o.x1 >= from {
				from = o.x1 + 1
			}
		}
		if from != math.MinInt {
			k.skip = max(0, from-k.x0)
		}
	}
	return cells, rowH
}

// leftBar reports whether cluster c has a vertical bar down its left side:
// its first columns are lit on at least two thirds of its rows. A "7"
// lights only its top bar there, a "3" its three horizontal bars, which
// on a fat font (a stroke a fifth of the height) reach over half the rows
// between them.
func leftBar(m bitmap, c blob, stroke int) bool {
	n := 0
	for y := c.y0; y <= c.y1; y++ {
		for x := c.x0; x <= min(c.x1, c.x0+max(1, stroke/2)); x++ {
			if m.at(x, y) {
				n++
				break
			}
		}
	}
	return 3*n >= 2*c.h()
}

// digitGap is the usual space between two full-width digits side by side
// in the row, with no colon or point between them: the median of those
// spaces, or 0 when the row shows no such pair (some displays leave next
// to none).
func digitGap(cells []cell, marks []blob, fInk float64) float64 {
	var gaps []int
	var prev *cell
	for i := range cells {
		k := &cells[i]
		if !k.tall || k.degree || k.fragment {
			continue
		}
		// The gap to or from a smaller digit (a clock's seconds) is the
		// tail's own spacing, not the row's: counted, it had a small
		// seconds 1 taken for the left-hand bars of a digit (12:34 01
		// read 12:340?).
		if k.wide && prev != nil && prev.wide && k.sizeRef >= fInk && prev.sizeRef >= fInk {
			between := false
			for _, mk := range marks {
				between = between || (mk.x0 > prev.c.x1 && mk.x1 < k.c.x0)
			}
			if !between {
				gaps = append(gaps, k.c.x0-prev.c.x1-1)
			}
		}
		prev = k
	}
	if len(gaps) == 0 {
		return 0
	}
	sort.Ints(gaps)
	return float64(max(0, gaps[len(gaps)/2]))
}

// clusterTop is the highest ink among the clusters.
func clusterTop(clusters []blob) int {
	top := math.MaxInt
	for _, c := range clusters {
		top = min(top, c.y0)
	}
	return top
}

// cellWidth is what the full-width digits measure, or the usual proportion
// of the row's height when only "1"s are showing — capped, then, so a cell
// never reaches into the neighbouring "1"'s bars. The row's height, not
// the ink's: a "1" stops a stroke short of where a "0" would reach at
// either end, and on a fat font a cell laid out from its own ink put its
// bars under the probes for the top, middle and bottom bars ("3").
func cellWidth(cells []cell, maxWide int, rowH float64, inkH, stroke int) int {
	if maxWide > 0 {
		return max(1, int(math.Round(math.Min(cellWidthMax*rowH, math.Max(cellWidthMin*rowH, float64(maxWide))))))
	}
	W := int(math.Round(cellWidthFrac * rowH))
	pitch := math.MaxInt
	prev := -1
	for _, k := range cells {
		if !k.tall {
			continue
		}
		if prev >= 0 {
			pitch = min(pitch, k.c.x1-prev)
		}
		prev = k.c.x1
	}
	if pitch != math.MaxInt {
		W = min(W, pitch-stroke)
	}
	return max(W, 1)
}

// barAcross reports whether rows y0..y1 of cluster c carry ink across its
// middle columns — a horizontal bar rather than the tips of two verticals.
func barAcross(m bitmap, c blob, y0, y1 int) bool {
	x0 := c.x0 + int(math.Round(midBand0*float64(c.w()-1)))
	x1 := c.x0 + int(math.Round(midBand1*float64(c.w()-1)))
	lit, total := 0, 0
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			total++
			if m.at(x, y) {
				lit++
			}
		}
	}
	return total > 0 && lit*2 >= total
}

// readCells classifies every cell and scores the reading.
func readCells(m bitmap, cells []cell, stroke int, rowH float64) reading {
	minRun := max(1, int(math.Round(probeMinRunFrac*rowH)))
	// The narrowest full-width digit: one cell far wider than it is two
	// glyphs the glow ran together, which can spell a digit too. A digit
	// on the crop's edge may be one the box cut through, narrower than any
	// digit the display draws, and is not the measure.
	// Widths are judged against each cell's own height, so a smaller digit
	// (the tenths of a thermometer) is not the measure of the full-size ones,
	// and a smaller digit is no measure at all: its sizeRef errs on the
	// large side, and against a small 3 at 0.7 of the height the 8 of 8.3
	// looked like two digits run together.
	narrowest, wide := math.Inf(1), 0
	fullRef := 0.0 // the full-size digits' sizeRef: the row's
	for _, k := range cells {
		fullRef = math.Max(fullRef, k.sizeRef)
	}
	for _, k := range cells {
		if k.wide && k.tall && !k.fragment {
			wide++
			if k.c.x0 > 0 && k.c.x1 < m.w-1 && !k.small && k.sizeRef >= fullRef {
				narrowest = math.Min(narrowest, cellAspect(k))
			}
		}
	}
	var r reading
	for _, k := range cells {
		if k.degree || k.fragment {
			continue
		}
		var (
			ch    byte
			conf  float64
			alt   byte
			blank bool
		)
		if nb, ok := sticksOut(k, cells); k.wide && k.tall && ok {
			// More than a digit's ink: a unit sign or a mark on the glass
			// over or under it was taken into its cluster, where it lit a
			// bar ("4" with a "g" above it read a confident "9"). The cell
			// is probed over the rows the digit beside it fills, and the
			// ink past them is left out.
			tol := max(2, int(sticksOutFrac*float64(nb.c.h())))
			k.y0, k.y1 = max(k.y0, nb.c.y0-tol), min(k.y1, nb.c.y1+tol)
		}
		if k.offCrop || k.thin || k.unread {
			// A tall narrow blob on the crop's left edge, or thinner than
			// half a stroke, is a bezel, a reflection or a sliver of a
			// digit the crop cut through — never a "1" to be read at full
			// confidence.
			ch, conf = '?', unsureConf
		} else if k.tall && !k.wide && (k.leftBars || !oneStands(m, k, cells)) {
			// A bar that only looks like a "1": the edge of the frame or
			// the housing, a reflection, a speck. Not read as a digit.
			ch, conf = '?', unsureConf
		} else if float64(k.x1-k.x0+1) > cellMaxAspect*rowH || (k.wide && wide > 1 && !math.IsInf(narrowest, 1) && cellAspect(k) >= runTogetherFrac*narrowest) {
			// Wider than the row is tall: digits run together, or the
			// frame round the display, whose ring of edges spells "0".
			// Or far wider than the other digits: on a small display a
			// "1", a colon and a "0" glowing into one blob.
			ch, conf = '?', unsureConf
		} else if groundCell(m, k.c) {
			// The ground round the digits, read the wrong way round: one
			// piece from edge to edge of the box with the digits as holes.
			// Its probes find every bar lit, and a lone "1" or a "10" in a
			// box drawn close round it read a confident 8.
			ch, conf = '?', unsureConf
		} else {
			ch, conf, alt, blank = classify(m, k, stroke, minRun)
			if !blank && ch != '?' && strayInk(k, cells) {
				// Ink over or under the cell that no probe reaches: a
				// bar the clustering left out of its digit (a thin "7"
				// whose top bar stands a wide gap above its side bars
				// spells "1" without it), or a mark on the glass.
				ch, conf, alt = '?', unsureConf, 0
			}
		}
		if blank && k.small {
			// Ink smallAfterPoint took for a small digit that no probe
			// finds lit: something is there, and dropped the reading
			// would be a confident shorter number (8.3 read 8.).
			ch, conf, blank = '?', unsureConf, false
		}
		if blank {
			continue
		}
		r.glyphs = append(r.glyphs, glyph{ch: ch, conf: conf, alt: alt, x0: k.x0, x1: k.x1, x: k.centre, ink: k.c.rect()})
		if ch == '?' {
			r.score -= 100
		} else {
			r.score += conf
		}
	}
	return r
}

// groundCell reports whether cluster c is the ground of the display seen
// in the wrong polarity: it spans the box from edge to edge and lights at
// least groundBorderFrac of the box's border. A digit drawn to the very
// edges of a box leaves the corners and the gaps between its bars unlit
// there.
func groundCell(m bitmap, c blob) bool {
	if c.x0 > 0 || c.y0 > 0 || c.x1 < m.w-1 || c.y1 < m.h-1 {
		return false
	}
	lit, n := 0, 0
	for x := 0; x < m.w; x++ {
		for _, y := range []int{0, m.h - 1} {
			n++
			if m.at(x, y) {
				lit++
			}
		}
	}
	for y := 1; y < m.h-1; y++ {
		for _, x := range []int{0, m.w - 1} {
			n++
			if m.at(x, y) {
				lit++
			}
		}
	}
	return float64(lit) >= groundBorderFrac*float64(n)
}

// oneStands reports whether a "1" (a tall, narrow cell k) is a digit: it
// has a partner, another digit in the row as tall as it is (within
// onePartnerRatio either way), or, alone in the row, it stands clear of
// the box. A lone bar running nearly the box's height from its top or
// bottom edge, or standing on its left or right edge, is the edge of the
// frame, the housing or a reflection seen through a box drawn a little
// wide; one far shorter than the box is a speck at a threshold that put
// the digits out. A bar much taller than the digits before it, or beside
// it, is the frame too. Either way the decoder can't tell it from a "1",
// and a "1" read at 100% where there is none is the worst answer it can
// give: it reads as '?'. A box drawn a few pixels clear of a real "1"
// touches neither edge, and it reads 1.
//
// Three things still count for a bar with no partner. Smaller digits after
// it, standing on its baseline, are the tenths of a thermometer (1.5); so
// is the bar itself when it stands smaller after a point or colon, on the
// baseline of the digit before (20.1 with small tenths read 20.?). A
// minus sign before it makes it a number (-1), even in a box drawn tight
// to its top and bottom.
func oneStands(m bitmap, k cell, cells []cell) bool {
	// Not much taller than the partner's ink, and not much shorter once
	// its cell reaches past its bars to where a top and bottom bar would
	// be: on most fonts a "1" stops a stroke short of a "0" at either end,
	// on some it runs the full height.
	h, cell := float64(k.c.h()), float64(k.y1-k.y0+1)
	base := (k.y1 - k.c.y1) + max(2, int(oneBaseFrac*h))
	smallAfter, minus := true, false
	for _, o := range cells {
		if o.c == k.c || o.degree || o.fragment {
			continue
		}
		if !o.tall {
			minus = minus || o.c.x1 < k.c.x0
			continue
		}
		oh := float64(o.c.h())
		if h <= onePartnerRatio*oh && oh <= onePartnerRatio*cell {
			return true
		}
		if k.afterMark && o.c.x1 < k.c.x0 && oh > h && k.y1 >= o.c.y1-base && k.c.y1 <= o.c.y1+base {
			return true // the smaller tenths after a full-size digit and its point
		}
		smallAfter = smallAfter && oh < h && o.c.x0 > k.c.x1 && o.c.y1 >= k.c.y1-base && o.c.y1 <= k.c.y1+base
	}
	// smallAfter holds, too, when the bar is alone in the row.
	if !smallAfter {
		return false
	}
	full := h >= oneFullFrac*float64(m.h) && (k.c.y0 <= 1 || k.c.y1 >= m.h-2) && !minus
	edge := k.c.x0 <= 1 || k.c.x1 >= m.w-2
	small := h < oneSmallFrac*float64(m.h)
	return !full && !edge && !small
}

// sticksOut reports whether a full-width digit's ink is much taller than
// the other full-width digits' (their median, by more than sticksOutFrac
// of it), and if so the nearest of them. The digits of a display are all
// one height; one that is taller has someone else's ink. Heights, not
// rows: a photo taken a little turned puts each digit a little higher
// than the one before.
//
// Only when a shorter digit comes before it. Smaller digits after a big
// one are the display's own: the tenths on many thermometers, a clock's
// seconds (21.5 read ?1.5, 8.5 read ?.5). A unit sign sits over the last
// digits, after full-size ones.
func sticksOut(k cell, cells []cell) (nb cell, ok bool) {
	var hs []int
	dist := math.MaxFloat64
	shorterBefore := false
	for _, o := range cells {
		if o.c == k.c || !o.wide || !o.tall || o.degree || o.fragment {
			continue
		}
		hs = append(hs, o.c.h())
		if d := math.Abs(o.centre - k.centre); d < dist {
			nb, dist = o, d
		}
		shorterBefore = shorterBefore || (o.c.x1 < k.c.x0 && float64(k.c.h()) > (1+sticksOutFrac)*float64(o.c.h()))
	}
	if len(hs) == 0 || !shorterBefore {
		return cell{}, false
	}
	return nb, float64(k.c.h()) > (1+sticksOutFrac)*float64(median(hs))
}

// strayInk reports whether a fragment stands over or under cell k, within
// its columns: ink of the row that the cell's probes never see. A
// fragment inside the cell's rows is a bar of the digit the clustering
// split off, and the probes read it where it lies.
func strayInk(k cell, cells []cell) bool {
	for _, f := range cells {
		if f.fragment && !f.degree && f.centre >= float64(k.x0) && f.centre <= float64(k.x1) &&
			(f.c.y1 < k.y0 || f.c.y0 > k.y1) {
			return true
		}
	}
	return false
}

// separator is the glyph for a decimal point or colon: confident while it
// is small against the row. A colon's blob is its two dots merged, so its
// height spans the gap between them; its width is one dot's size, and that
// is what is judged (judging the height scored every colon 0, which Test
// this region showed as a low-confidence glyph on a clean read), against
// the bigger size a colon's dot may have.
func separator(ch byte, b blob, rowH float64) glyph {
	extent := max(b.w(), b.h())
	limit := pointSizeFrac * rowH
	if ch == ':' {
		// Its dots may be as big as colonDotMaxFrac allows; splitBlobs has
		// already checked the pair stands where a colon does.
		extent, limit = b.w(), colonDotMaxFrac*rowH
	}
	size := float64(extent) / limit
	conf := math.Round(100 * math.Min(1, math.Max(0, pointConfSlope*(1-size))))
	return glyph{ch: ch, conf: conf, x0: b.x0, x1: b.x1, x: b.cx(), ink: b.rect()}
}

// A zone is where one segment (or one of the two holes an "8" has) is
// looked for, in cell-relative coordinates: probes are laid along p0..p1
// across the segment and each scans s0..s1 along it. Vertical probes are
// columns (for the horizontal bars a, d, g); the rest are rows.
type zone struct {
	vertical bool
	p0, p1   float64
	s0, s1   float64
}

// Segment order is a b c d e f g; the two holes follow in classify.
var zones = [7]zone{
	{true, midBand0, midBand1, 0.00, 0.30}, // a: top bar
	{false, 0.27, 0.37, 0.68, 1.00},        // b: upper right
	{false, 0.63, 0.73, 0.68, 1.00},        // c: lower right
	{true, midBand0, midBand1, 0.70, 1.00}, // d: bottom bar
	{false, 0.63, 0.73, 0.00, 0.32},        // e: lower left
	{false, 0.27, 0.37, 0.00, 0.32},        // f: upper left
	{true, midBand0, midBand1, 0.35, 0.65}, // g: middle bar
}

// patterns maps a lit-segment set (bit 6 = a ... bit 0 = g) to its glyph.
// 6, 7 and 9 have two spellings in the wild.
var patterns = func() map[uint8]byte {
	p := map[uint8]byte{}
	for _, e := range []struct {
		segs string
		ch   byte
	}{
		{"abcdef", '0'}, {"bc", '1'}, {"abdeg", '2'}, {"abcdg", '3'}, {"bcfg", '4'},
		{"acdfg", '5'}, {"acdefg", '6'}, {"cdefg", '6'}, {"abc", '7'}, {"abcf", '7'},
		{"abcdefg", '8'}, {"abcdfg", '9'}, {"abcfg", '9'}, {"g", '-'},
	} {
		var bits uint8
		for _, s := range e.segs {
			bits |= 1 << uint(6-(s-'a'))
		}
		p[bits] = e.ch
	}
	return p
}()

// classify probes one cell. Each zone's fill is the share of its probes
// that cross a lit run; a zone is on above an adaptive threshold (half the
// cell's best fill, floored so a uniformly weak glyph still reads) and the
// confidence is how far the closest zone sat from that line, so a clean
// render scores 100 and a half-lit bar scores near 0. The two hole zones
// sit between the vertical bars, a measured stroke plus a margin in from
// either side; a lit hole or a pattern that spells no digit is '?', capped
// low. alt is the digit the closest zone read the other way would spell
// (glyph.alt), 0 when none.
func classify(m bitmap, k cell, stroke, minRun int) (ch byte, conf float64, alt byte, blank bool) {
	W, H := k.x1-k.x0+1, k.y1-k.y0+1
	if W <= 0 || H <= 0 {
		return 0, 0, 0, true
	}
	in := math.Min((float64(stroke)+holePad*float64(W))/float64(W), 0.5-holeMinFrac/2)
	holes := [2]zone{
		{false, 0.27, 0.33, in, 1 - in}, // upper hole: lit here is a blob, not a digit
		{false, 0.67, 0.73, in, 1 - in}, // lower hole
	}
	var fill [9]float64
	for i := range fill {
		z := holes[max(0, i-7)]
		if i < 7 {
			z = zones[i]
		}
		fill[i] = zoneFill(m, k, z, minRun, i >= 7)
	}
	maxFill := 0.0
	for i := 0; i < 7; i++ {
		maxFill = math.Max(maxFill, fill[i])
	}
	if maxFill == 0 {
		return 0, 0, 0, true
	}
	thr := math.Max(onFloor, onHalf*maxFill)
	var bits uint8
	margin, nearest := 1.0, -1
	for i := 0; i < 7; i++ {
		if fill[i] >= thr {
			bits |= 1 << uint(6-i)
		}
		if d := 2 * math.Abs(fill[i]-thr); d < margin {
			margin, nearest = d, i
		}
	}
	conf = math.Round(1000*math.Min(1, margin)) / 10
	ch, ok := patterns[bits]
	if !ok || fill[7] >= holeLit || fill[8] >= holeLit {
		return '?', math.Min(conf, unsureConf), 0, false
	}
	if nearest >= 0 {
		alt = patterns[bits^(1<<uint(6-nearest))]
	}
	return ch, conf, alt, false
}

// zoneFill is the share of a zone's probes that cross a lit run.
//
// In a hole zone a probe counts ink that stands inside the hole, or fills
// most of it, but not the sliver that only reaches in from one side: on a
// small, glowing display the bars come out a pixel or two fatter than the
// measured stroke and their edge reaches into the hole zone.
func zoneFill(m bitmap, k cell, z zone, minRun int, hole bool) float64 {
	W, H := k.x1-k.x0+1, k.y1-k.y0+1
	lo := k.x0 + k.skip // the probes leave out another glyph's ink before it
	hits := 0
	for p := 0; p < probes; p++ {
		t := z.p0 + (z.p1-z.p0)*float64(p)/float64(probes-1)
		if z.vertical {
			x := k.x0 + int(math.Round(t*float64(W-1)))
			from := k.y0 + int(math.Round(z.s0*float64(H-1)))
			to := k.y0 + int(math.Round(z.s1*float64(H-1)))
			if x >= lo && litRun(m, x, from, to, true, minRun) {
				hits++
			}
		} else {
			y := k.y0 + int(math.Round(t*float64(H-1)))
			from := max(lo, k.x0+int(math.Round(z.s0*float64(W-1))))
			to := k.x0 + int(math.Round(z.s1*float64(W-1)))
			if hole && holeRun(m, y, from, to, minRun) || !hole && litRun(m, y, from, to, false, minRun) {
				hits++
			}
		}
	}
	return float64(hits) / probes
}

// holeRun reports whether row y carries, between from and to, a lit run of
// at least minRun pixels that touches neither end, or one that reaches in
// from an end over half the way: more than a fattened bar's edge, as the
// ground round a digit-shaped hole does when the polarity is the wrong way
// round.
func holeRun(m bitmap, y, from, to, minRun int) bool {
	start := -1
	for x := from; x <= to+1; x++ {
		if x <= to && m.at(x, y) {
			if start < 0 {
				start = x
			}
			continue
		}
		if start >= 0 {
			end := x - 1
			if n := end - start + 1; n >= minRun && (start > from && end < to || 2*n > to-from+1) {
				return true
			}
			start = -1
		}
	}
	return false
}

// litRun reports whether column x (vertical) or row y (horizontal) `fixed`
// carries a lit run of at least minRun pixels between from and to.
func litRun(m bitmap, fixed, from, to int, vertical bool, minRun int) bool {
	run := 0
	for i := from; i <= to; i++ {
		var lit bool
		if vertical {
			lit = m.at(fixed, i)
		} else {
			lit = m.at(i, fixed)
		}
		if lit {
			if run++; run >= minRun {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}
