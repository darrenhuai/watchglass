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
	g := toGray(img)
	thr, primary, ok := thresholdLevels(g)
	if !ok {
		return "", nil, nil // uniform (or empty) crop: nothing lit
	}
	d := newDecoder(g, thr, primary)
	defer d.release()
	best := d.read()
	if len(best.glyphs) == 0 {
		return "", nil, nil
	}
	var text strings.Builder
	words := make([]Word, 0, len(best.glyphs))
	for _, gl := range best.glyphs {
		text.WriteByte(gl.ch)
		words = append(words, Word{Text: string(gl.ch), Conf: gl.conf})
	}
	return text.String(), words, nil
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
	// colonGapMin/Max: the two dots of a colon are this far apart.
	colonGapMin = 0.1
	colonGapMax = 0.6
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
	// part of it.
	degreeTopFrac = 0.5
	// cellWidthFrac: digit cell width when only "1"s are showing.
	cellWidthFrac = 0.55
	// cellWidthMin/Max clamp a measured cell width, as shares of the row
	// height (the ink plus the reach at either end).
	cellWidthMin = 0.5
	cellWidthMax = 0.9
	// oneMinStrokeFrac: a narrow cluster thinner than this share of the
	// stroke is a line, not a "1".
	oneMinStrokeFrac = 0.5
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
	// lineWideFrac, lineThinFrac: a blob wider than lineWideFrac*inkH and
	// shorter than lineThinFrac*inkH is a line across the row, never a bar.
	lineWideFrac = 1.5
	lineThinFrac = 0.25
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
}

type glyph struct {
	ch     byte
	conf   float64
	x0, x1 int     // cell columns, to match a glyph across thresholds
	x      float64 // centre, for reading order
}

// digit reports whether the glyph is a cell (a digit, '-' or '?') rather
// than a separator.
func (g glyph) digit() bool { return g.ch != '.' && g.ch != ':' }

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
	for i := 1; i < n; i++ {
		cur = pick(cur, d.readAt(level(i), bright))
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
// measured on the first level read in that polarity and applied to all.
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
	blobs := dropBezels(m, components(m, d.wk))
	if !d.sheared[p] {
		d.shear[p] = shearEstimate(m, d.wk)
		d.sheared[p] = true
	}
	if d.shear[p] != 0 {
		m = deshear(m, d.shear[p], d.wk)
		blobs = components(m, d.wk)
	}
	r := decodeBitmap(m, blobs, d.wk)
	d.cache[level][p] = &r
	return r
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
}

func (b blob) w() int      { return b.x1 - b.x0 + 1 }
func (b blob) h() int      { return b.y1 - b.y0 + 1 }
func (b blob) cx() float64 { return float64(b.x0+b.x1) / 2 }
func (b blob) cy() float64 { return float64(b.y0+b.y1) / 2 }

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
		b := blob{x0: start % m.w, y0: start / m.w, x1: start % m.w, y1: start / m.w}
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

// erase clears a blob's bounding box.
func erase(m bitmap, b blob) {
	for y := b.y0; y <= b.y1; y++ {
		clear(m.on[y*m.w+b.x0 : y*m.w+b.x1+1])
	}
}

// dropBezels removes the thin blobs lying along a crop edge — the bezel
// of a display window, the edge of the glass, a reflection — before the
// lean is measured: a bright line up the left of the crop would otherwise
// read as a leading "1". They are erased from m so no probe sees them.
func dropBezels(m bitmap, blobs []blob) []blob {
	thinX := max(2, int(bezelThinFrac*float64(m.w)))
	thinY := max(2, int(bezelThinFrac*float64(m.h)))
	kept := blobs[:0]
	for _, b := range blobs {
		onX := (b.x0 == 0 || b.x1 == m.w-1) && b.w() <= thinX && float64(b.h()) >= bezelLongFrac*float64(m.h)
		onY := (b.y0 == 0 || b.y1 == m.h-1) && b.h() <= thinY && float64(b.w()) >= bezelLongFrac*float64(m.w)
		if onX || onY {
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
			shifts[y] = int(math.Round(s*float64(y-yc))) + span
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
		for s := bestS - shearStep; s <= bestS+shearStep+1e-9; s += shearStep / 4 {
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

	bars, points, colons := splitBlobs(blobs, inkTop, inkH, minArea)
	if len(bars) == 0 {
		return reading{}
	}
	clusters := clusterBars(bars, inkH)
	clusters, points = attachDots(clusters, points)
	stroke := strokeEstimate(m, clusters, inkH, wk)
	clusters, split := splitAttachedDots(m, clusters, stroke, inkH)
	points = append(points, split...)
	cells, rowH := layoutCells(m, clusters, stroke, inkTop, inkH)
	if cells == nil {
		return reading{}
	}
	r := readCells(m, cells, stroke, rowH)
	for _, p := range points {
		r.glyphs = append(r.glyphs, separator('.', p, rowH))
	}
	for _, c := range colons {
		r.glyphs = append(r.glyphs, separator(':', c, rowH))
	}
	sort.SliceStable(r.glyphs, func(i, j int) bool { return r.glyphs[i].x < r.glyphs[j].x })
	return r
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
func splitBlobs(blobs []blob, inkTop, inkH, minArea int) (bars, points, colons []blob) {
	fInk := float64(inkH)
	var marks []blob
	for _, b := range blobs {
		if b.area < minArea {
			continue
		}
		small := float64(b.w()) <= dotMaxFrac*fInk && float64(b.h()) <= dotMaxFrac*fInk
		squarish := 2*b.w() >= b.h() && 2*b.h() >= b.w()
		solid := float64(b.area) >= dotSolidFrac*float64(b.w()*b.h())
		if small && squarish && solid {
			marks = append(marks, b)
		} else {
			bars = append(bars, b)
		}
	}
	mid := float64(inkTop) + 0.5*fInk
	used := make([]bool, len(marks))
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
			aligned := math.Abs(up.cx()-down.cx()) <= math.Max(2, 0.5*float64(max(up.w(), down.w())))
			if aligned && gap >= colonGapMin*fInk && gap <= colonGapMax*fInk && up.cy() < mid && down.cy() > mid {
				colons = append(colons, up.merge(down))
				used[i], used[j] = true, true
			}
		}
	}
	for i, mk := range marks {
		if used[i] {
			continue
		}
		if mk.cy() >= float64(inkTop)+dotBaselineFrac*fInk {
			points = append(points, mk)
		} else {
			bars = append(bars, mk)
		}
	}
	return bars, points, colons
}

// clusterBars groups bars that overlap or nearly touch in x into glyphs:
// every digit's horizontal bars span its cell, and real displays leave only
// a sliver between a horizontal bar's end and the vertical bar beside it.
func clusterBars(bars []blob, inkH int) []blob {
	sort.Slice(bars, func(i, j int) bool { return bars[i].x0 < bars[j].x0 })
	join := max(1, int(math.Round(barJoinFrac*float64(inkH))))
	var clusters []blob
	for _, b := range bars {
		if n := len(clusters); n > 0 && b.x0 <= clusters[n-1].x1+join {
			clusters[n-1] = clusters[n-1].merge(b)
		} else {
			clusters = append(clusters, b)
		}
	}
	return clusters
}

// attachDots folds a "dot" inside a glyph's own columns back into that
// glyph: it is a short bar of the glyph (the bottom bar of a fat-stroke
// font), not a decimal point. The rest are returned as points.
func attachDots(clusters, dots []blob) (_, points []blob) {
	for _, d := range dots {
		attached := false
		for i, c := range clusters {
			if d.cx() >= float64(c.x0) && d.cx() <= float64(c.x1) {
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
	offCrop    bool    // a "1" whose cell would start left of the crop
	thin       bool    // a line thinner than half a stroke, not a "1"
	degree     bool    // a degree sign: dropped from the reading
	centre     float64 // reading order
}

// layoutCells turns clusters into cells. A glyph's ink only reaches the top
// of its cell when the top bar is lit ("1", "4" and "7" stop a stroke
// short), so each tall cluster's cell is extended by a stroke where its
// ink shows no bar there. Short clusters (a lone "-") take the row's
// extent. rowH is the row's height with those extensions; cells is nil
// when nothing tall is showing.
func layoutCells(m bitmap, clusters []blob, stroke, inkTop, inkH int) (cells []cell, rowH float64) {
	fInk := float64(inkH)
	reach := stroke + max(1, int(math.Round(reachFrac*float64(stroke))))
	cells = make([]cell, 0, len(clusters))
	rowY0, rowY1 := math.MaxInt, math.MinInt
	maxWide := 0
	for _, c := range clusters {
		k := cell{c: c, centre: c.cx(), y0: c.y0, y1: c.y1}
		k.wide = float64(c.w()) >= wideCellFrac*fInk
		k.tall = float64(c.h()) >= tallCellFrac*fInk
		if !k.tall {
			k.fromRow = true
			squarish := 2*c.w() <= 3*c.h() && 2*c.h() <= 3*c.w()
			k.degree = squarish && c.cy() < float64(inkTop)+degreeTopFrac*fInk
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
	if rowY0 == math.MaxInt {
		return nil, 0
	}
	rowH = float64(rowY1 - rowY0 + 1)
	W := cellWidth(cells, maxWide, rowH, inkH, stroke)
	for i := range cells {
		k := &cells[i]
		if k.fromRow {
			k.y0, k.y1 = rowY0, rowY1
		}
		switch {
		case k.wide:
			k.x0, k.x1 = k.c.x0, k.c.x1
		case k.tall: // a "1": its two bars sit at the right of the cell
			k.x1 = k.c.x1
			k.x0 = k.x1 - W + 1
			k.offCrop = k.x0 < 0
			k.thin = float64(k.c.w()) < oneMinStrokeFrac*float64(stroke)
		default: // a "-": centred
			k.x0 = int(k.centre) - W/2
			k.x1 = k.x0 + W - 1
		}
	}
	return cells, rowH
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
// of the ink height when only "1"s are showing — capped, then, so a cell
// never reaches into the neighbouring "1"'s bars.
func cellWidth(cells []cell, maxWide int, rowH float64, inkH, stroke int) int {
	if maxWide > 0 {
		return max(1, int(math.Round(math.Min(cellWidthMax*rowH, math.Max(cellWidthMin*rowH, float64(maxWide))))))
	}
	W := int(math.Round(cellWidthFrac * float64(inkH)))
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
	var r reading
	for _, k := range cells {
		if k.degree {
			continue
		}
		var (
			ch    byte
			conf  float64
			blank bool
		)
		if k.offCrop || k.thin {
			// A tall narrow blob at the crop's left edge with no room for
			// a cell, or thinner than half a stroke, is a bezel, a
			// reflection or a sliver of a digit the crop cut through —
			// never a "1" to be read at full confidence.
			ch, conf = '?', unsureConf
		} else {
			ch, conf, blank = classify(m, k, stroke, minRun)
		}
		if blank {
			continue
		}
		r.glyphs = append(r.glyphs, glyph{ch: ch, conf: conf, x0: k.x0, x1: k.x1, x: k.centre})
		if ch == '?' {
			r.score -= 100
		} else {
			r.score += conf
		}
	}
	return r
}

// separator is the glyph for a decimal point or colon: confident while it
// is small against the row.
func separator(ch byte, b blob, rowH float64) glyph {
	size := float64(max(b.w(), b.h())) / (pointSizeFrac * rowH)
	conf := math.Round(100 * math.Min(1, math.Max(0, pointConfSlope*(1-size))))
	return glyph{ch: ch, conf: conf, x0: b.x0, x1: b.x1, x: b.cx()}
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
// low.
func classify(m bitmap, k cell, stroke, minRun int) (ch byte, conf float64, blank bool) {
	W, H := k.x1-k.x0+1, k.y1-k.y0+1
	if W <= 0 || H <= 0 {
		return 0, 0, true
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
		fill[i] = zoneFill(m, k, z, minRun)
	}
	maxFill := 0.0
	for i := 0; i < 7; i++ {
		maxFill = math.Max(maxFill, fill[i])
	}
	if maxFill == 0 {
		return 0, 0, true
	}
	thr := math.Max(onFloor, onHalf*maxFill)
	var bits uint8
	margin := 1.0
	for i := 0; i < 7; i++ {
		if fill[i] >= thr {
			bits |= 1 << uint(6-i)
		}
		margin = math.Min(margin, 2*math.Abs(fill[i]-thr))
	}
	conf = math.Round(1000*math.Min(1, margin)) / 10
	ch, ok := patterns[bits]
	if !ok || fill[7] >= holeLit || fill[8] >= holeLit {
		return '?', math.Min(conf, unsureConf), false
	}
	return ch, conf, false
}

// zoneFill is the share of a zone's probes that cross a lit run.
func zoneFill(m bitmap, k cell, z zone, minRun int) float64 {
	W, H := k.x1-k.x0+1, k.y1-k.y0+1
	hits := 0
	for p := 0; p < probes; p++ {
		t := z.p0 + (z.p1-z.p0)*float64(p)/float64(probes-1)
		if z.vertical {
			x := k.x0 + int(math.Round(t*float64(W-1)))
			from := k.y0 + int(math.Round(z.s0*float64(H-1)))
			to := k.y0 + int(math.Round(z.s1*float64(H-1)))
			if litRun(m, x, from, to, true, minRun) {
				hits++
			}
		} else {
			y := k.y0 + int(math.Round(t*float64(H-1)))
			from := k.x0 + int(math.Round(z.s0*float64(W-1)))
			to := k.x0 + int(math.Round(z.s1*float64(W-1)))
			if litRun(m, y, from, to, false, minRun) {
				hits++
			}
		}
	}
	return float64(hits) / probes
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
