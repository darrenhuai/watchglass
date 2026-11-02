// Package drift notices when the picture around a watch's box has moved:
// a camera that was bumped, a screen that slid on its stand, an IR switch
// that shifts the image. The box then sits partly off the digits, and the
// reader produces partial or wrong readings with nothing to say why.
//
// When a watch starts reading well it keeps a small reference of the area
// around its box: the box padded by a margin, in grayscale, scaled down.
// Only the part outside the box counts (the bezel, the housing, labels),
// so digits that change don't look like movement. Every check finds the
// shift that best lines the current picture's edges up with the
// reference's. Edges, compared by their direction and normalised over
// the whole area, don't change when the camera's exposure or white
// balance drifts, so neither looks like movement either. A shift that
// stays put over several checks is a move; a picture where nothing lines
// up is "can't find the reference".
package drift

import (
	"image"
	"math"
)

const (
	// maxRefSide caps the reference's longer side, in reference pixels.
	// The reference is the padded area scaled down by a whole factor
	// until it fits, so it costs at most maxRefSide² bytes per watch
	// whatever the camera's resolution.
	maxRefSide = 192
	// padFrac is the margin around the box, as a fraction of the box's
	// longer side, and minPad the least margin in frame pixels. A move
	// up to the margin can be measured.
	padFrac = 0.6
	minPad  = 24
	// coarseRange is how far, in pixels of the coarsest level, the
	// exhaustive search looks; levels are added until the margin fits.
	coarseRange = 12
	// minOverlap is the share of the reference's surroundings that must
	// still be inside the frame at a shift for the shift to count.
	minOverlap = 0.5
)

// Reference is what a watch's surroundings looked like when it was
// reading well.
type Reference struct {
	// FrameW and FrameH are the size of the frame it was taken from.
	FrameW, FrameH int
	// Box is the watch's box and Area the box with its margin, clipped to
	// the frame, both in frame pixels with the frame's corner at 0,0.
	Box, Area image.Rectangle
	// Scale is frame pixels per reference pixel; W and H the reference's
	// size, Gray its pixels, row by row.
	Scale int
	W, H  int
	Gray  []uint8
}

// Result is one check: how far the picture around the box has moved
// since the reference (DX right, DY down, in frame pixels), and how well
// the edges line up there (Score, 1 for a perfect match, around 0 for
// none). Found is false when no shift could be scored at all (a frame of
// another size, or one where the surroundings are off its edge).
type Result struct {
	DX, DY float64
	Score  float64
	Found  bool
	// Local is the score at the best turn and shift (turn): a camera
	// that turned a little scores low unturned, but well turned.
	Local float64
}

// Magnitude is the length of the shift in frame pixels.
func (r Result) Magnitude() float64 { return math.Hypot(r.DX, r.DY) }

// BoxRect is a region's box in frame pixels, rounded the way the runner
// crops it (imgproc.Crop), for a frame of size w×h.
func BoxRect(w, h int, x, y, bw, bh float64) image.Rectangle {
	fw, fh := float64(w), float64(h)
	r := image.Rect(int(math.Round(x*fw)), int(math.Round(y*fh)),
		int(math.Round((x+bw)*fw)), int(math.Round((y+bh)*fh)))
	return r.Intersect(image.Rect(0, 0, w, h))
}

// padFor is the margin around a box in frame pixels.
func padFor(box image.Rectangle) int {
	side := box.Dx()
	if box.Dy() > side {
		side = box.Dy()
	}
	p := int(math.Round(padFrac * float64(side)))
	if p < minPad {
		p = minPad
	}
	return p
}

// Take makes a reference of the surroundings of box in img. It returns
// nil when the box is empty or leaves no surroundings to go by (a box
// that fills the frame).
func Take(img image.Image, box image.Rectangle) *Reference {
	fb := img.Bounds()
	fw, fh := fb.Dx(), fb.Dy()
	box = box.Intersect(image.Rect(0, 0, fw, fh))
	if box.Empty() {
		return nil
	}
	pad := padFor(box)
	area := box.Inset(-pad).Intersect(image.Rect(0, 0, fw, fh))
	side := area.Dx()
	if area.Dy() > side {
		side = area.Dy()
	}
	scale := (side + maxRefSide - 1) / maxRefSide
	if scale < 1 {
		scale = 1
	}
	w, h := area.Dx()/scale, area.Dy()/scale
	if w < 8 || h < 8 {
		return nil
	}
	area = image.Rect(area.Min.X, area.Min.Y, area.Min.X+w*scale, area.Min.Y+h*scale)
	ref := &Reference{FrameW: fw, FrameH: fh, Box: box, Area: area, Scale: scale, W: w, H: h}
	g, _ := grayGrid(img, area.Min, scale, w, h)
	ref.Gray = make([]uint8, w*h)
	for i, v := range g {
		ref.Gray[i] = uint8(v + 0.5)
	}
	if ref.ringPixels() < 64 {
		return nil
	}
	return ref
}

// mask says which reference pixels count: those clear of the box (by one
// reference pixel, so the digits' anti-aliased edges stay out) and not on
// the reference's own border, where there is no gradient.
func (r *Reference) mask() []bool {
	m := make([]bool, r.W*r.H)
	keep := r.Box.Inset(-r.Scale)
	for j := 1; j < r.H-1; j++ {
		for i := 1; i < r.W-1; i++ {
			px := image.Rect(r.Area.Min.X+i*r.Scale, r.Area.Min.Y+j*r.Scale,
				r.Area.Min.X+(i+1)*r.Scale, r.Area.Min.Y+(j+1)*r.Scale)
			m[j*r.W+i] = !px.Overlaps(keep)
		}
	}
	return m
}

func (r *Reference) ringPixels() int {
	n := 0
	for _, k := range r.mask() {
		if k {
			n++
		}
	}
	return n
}

// searchRange is how far a move can be measured, in reference pixels:
// the margin, at most.
func (r *Reference) searchRange() int {
	rr := (padFor(r.Box) + r.Scale - 1) / r.Scale
	if rr < 2 {
		rr = 2
	}
	return rr
}

// level is one step of the image pyramid: gradients of a grid of gray
// values, and which of them count (mask for the reference, valid for the
// current picture).
type level struct {
	w, h   int
	gx, gy []float32
	ok     []bool
}

// gradients makes a level from gray values (NaN where the frame ends):
// a [1 2 1] blur each way, then central differences.
func gradients(g []float32, w, h int, keep []bool) level {
	b := blur(g, w, h)
	l := level{w: w, h: h, gx: make([]float32, w*h), gy: make([]float32, w*h), ok: make([]bool, w*h)}
	for j := 1; j < h-1; j++ {
		for i := 1; i < w-1; i++ {
			k := j*w + i
			if keep != nil && !keep[k] {
				continue
			}
			a, c, u, d := b[k-1], b[k+1], b[k-w], b[k+w]
			if isNaN(a) || isNaN(c) || isNaN(u) || isNaN(d) {
				continue
			}
			l.gx[k], l.gy[k], l.ok[k] = c-a, d-u, true
		}
	}
	return l
}

func isNaN(f float32) bool { return f != f }

func blur(g []float32, w, h int) []float32 {
	t := make([]float32, w*h)
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			k := j*w + i
			if i == 0 || i == w-1 {
				t[k] = g[k]
				continue
			}
			t[k] = (g[k-1] + 2*g[k] + g[k+1]) / 4
		}
	}
	out := make([]float32, w*h)
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			k := j*w + i
			if j == 0 || j == h-1 {
				out[k] = t[k]
				continue
			}
			out[k] = (t[k-w] + 2*t[k] + t[k+w]) / 4
		}
	}
	return out
}

// half halves a gray grid, averaging 2×2 blocks (NaN if any is NaN), and
// a mask with it (kept only where all four were).
func half(g []float32, keep []bool, w, h int) ([]float32, []bool, int, int) {
	w2, h2 := w/2, h/2
	out := make([]float32, w2*h2)
	var k2 []bool
	if keep != nil {
		k2 = make([]bool, w2*h2)
	}
	for j := 0; j < h2; j++ {
		for i := 0; i < w2; i++ {
			a, b := (2*j)*w+2*i, (2*j+1)*w+2*i
			out[j*w2+i] = (g[a] + g[a+1] + g[b] + g[b+1]) / 4
			if keep != nil {
				k2[j*w2+i] = keep[a] && keep[a+1] && keep[b] && keep[b+1]
			}
		}
	}
	return out, k2, w2, h2
}

// pair is one pyramid level: the reference's gradients, the current
// picture's (off pixels bigger on every side, the search range), and the
// reference's counted pixels as a list: where each sits in cur at shift
// 0 (at), and its gradient.
type pair struct {
	ref, cur level
	off      int
	need     int
	at       []int32
	gx, gy   []float32
}

func newPair(ref, cur level, off int) pair {
	p := pair{ref: ref, cur: cur, off: off}
	for k, ok := range ref.ok {
		if ok {
			i, j := k%ref.w, k/ref.w
			p.at = append(p.at, int32((j+off)*cur.w+i+off))
			p.gx = append(p.gx, ref.gx[k])
			p.gy = append(p.gy, ref.gy[k])
		}
	}
	p.need = int(minOverlap * float64(len(p.at)))
	return p
}

// score is how well cur's edges line up with ref's when the picture has
// moved by (u, v) pixels of this level: the cosine between the two
// gradient fields over the reference's surroundings. ok is false when too
// little of the surroundings is still in the frame at that shift, or the
// shift is past the search range.
func (p *pair) score(u, v int) (float64, bool) {
	if u < -p.off || u > p.off || v < -p.off || v > p.off {
		return 0, false
	}
	d := v*p.cur.w + u
	cok, cgx, cgy := p.cur.ok, p.cur.gx, p.cur.gy
	var dot, nr, nc float32
	var sdot, snr, snc float64
	n := 0
	for m, a := range p.at {
		c := int(a) + d
		if !cok[c] {
			continue
		}
		rx, ry, cx, cy := p.gx[m], p.gy[m], cgx[c], cgy[c]
		dot += rx*cx + ry*cy
		nr += rx*rx + ry*ry
		nc += cx*cx + cy*cy
		n++
		if n&255 == 0 {
			// Sums of a few hundred float32 products keep their
			// precision; the running totals are float64.
			sdot, snr, snc = sdot+float64(dot), snr+float64(nr), snc+float64(nc)
			dot, nr, nc = 0, 0, 0
		}
	}
	sdot, snr, snc = sdot+float64(dot), snr+float64(nr), snc+float64(nc)
	if n < p.need || snr == 0 || snc == 0 {
		return 0, false
	}
	return sdot / math.Sqrt(snr*snc), true
}

// Measure finds how far the surroundings of ref's box have moved in img.
// It searches the coarsest level of a small pyramid exhaustively over the
// margin and refines the best shift level by level. Then it tries small
// turns about the box's centre near that shift (turn), and answers with
// the shift of the box's centre, to a fraction of a pixel.
func Measure(ref *Reference, img image.Image) Result { return measure(ref, img, 0, 0) }

// measure is Measure for a picture that sits at (bx, by) when it hasn't
// moved: the shift a check settled on (Tracker). The turn search only
// runs for a shift that far from it or a poor match.
func measure(ref *Reference, img image.Image, bx, by float64) Result {
	fb := img.Bounds()
	if ref == nil || fb.Dx() != ref.FrameW || fb.Dy() != ref.FrameH {
		return Result{}
	}
	rr := ref.searchRange()
	// The current picture: the reference's area grown by the search range
	// on every side, on the same grid.
	origin := ref.Area.Min.Sub(image.Pt(rr*ref.Scale, rr*ref.Scale))
	cw, ch := ref.W+2*rr, ref.H+2*rr
	cg, _ := grayGrid(img, origin, ref.Scale, cw, ch)
	rg := make([]float32, len(ref.Gray))
	for i, v := range ref.Gray {
		rg[i] = float32(v)
	}
	rmask := ref.mask()

	// Levels, finest first, until the search range fits coarseRange or
	// a coarser level would keep too little of the surroundings.
	var levels []pair
	rw, rh, cW, cH := ref.W, ref.H, cw, ch
	off := rr
	for {
		rl := gradients(rg, rw, rh, rmask)
		n := count(rl.ok)
		if len(levels) > 0 && n < minLevelPixels {
			break
		}
		levels = append(levels, newPair(rl, gradients(cg, cW, cH, nil), off))
		if off <= coarseRange || rw/2 < 12 || rh/2 < 12 {
			break
		}
		rg, rmask, rw, rh = half(rg, rmask, rw, rh)
		// cur's grid starts off pixels before ref's; keep that whole by
		// dropping a pixel when off is odd.
		if off%2 == 1 {
			cg, cW, cH = crop1(cg, cW, cH)
			off--
		}
		cg, _, cW, cH = half(cg, nil, cW, cH)
		off /= 2
	}

	// Exhaustive at the coarsest level.
	top := levels[len(levels)-1]
	bu, bv, best, found := 0, 0, -2.0, false
	for v := -top.off; v <= top.off; v++ {
		for u := -top.off; u <= top.off; u++ {
			s, ok := top.score(u, v)
			if ok && (s > best || (s == best && u*u+v*v < bu*bu+bv*bv)) {
				bu, bv, best, found = u, v, s, true
			}
		}
	}
	if !found {
		return Result{}
	}
	// Refine down the levels.
	for li := len(levels) - 2; li >= 0; li-- {
		l := levels[li]
		cu, cv := 2*bu, 2*bv
		best = -2
		for v := cv - 2; v <= cv+2; v++ {
			for u := cu - 2; u <= cu+2; u++ {
				if u < -l.off || u > l.off || v < -l.off || v > l.off {
					continue
				}
				s, ok := l.score(u, v)
				if ok && s > best {
					bu, bv, best = u, v, s
				}
			}
		}
		if best < -1 {
			return Result{}
		}
	}
	l := &levels[0]
	sc := float64(ref.Scale)
	res := Result{Score: best, Found: true, Local: best}
	fx, fy := subPixel(l.score, bu, bv, best)
	res.DX, res.DY = (float64(bu)+fx)*sc, (float64(bv)+fy)*sc
	// A small shift that lines up well is no move and no turn: the turn
	// search, which costs more than the rest, is left out.
	if math.Hypot(res.DX-bx, res.DY-by) >= turnFrom || best < turnBelow {
		if dx, dy, local, ok := ref.turn(l.ref, l.cur, bu, bv, l.off); ok {
			res.DX, res.DY, res.Local = dx, dy, local
		}
	}
	return res
}

// minLevelPixels is the least of the surroundings a pyramid level may
// keep: a box that fills most of the frame leaves a thin ring, which
// halving would wipe out.
const minLevelPixels = 120

func count(b []bool) int {
	n := 0
	for _, k := range b {
		if k {
			n++
		}
	}
	return n
}

// subPixel is the fraction of a pixel to add to the best shift (bu, bv),
// from a parabola through its score and its neighbours', each way.
func subPixel(score func(u, v int) (float64, bool), bu, bv int, best float64) (float64, float64) {
	sub := func(du, dv int) float64 {
		a, oka := score(bu-du, bv-dv)
		c, okc := score(bu+du, bv+dv)
		if !oka || !okc {
			return 0
		}
		den := a - 2*best + c
		if den >= 0 {
			return 0
		}
		f := 0.5 * (a - c) / den
		return math.Max(-0.5, math.Min(0.5, f))
	}
	return sub(1, 0), sub(0, 1)
}

// turnRange is how far, in reference pixels, the search for a turn
// strays from the overall best shift: the overall shift of a turned
// picture is the shift of its most detailed part, not of the box.
const turnRange = 2

// The turn search runs when the shift is at least turnFrom frame pixels
// from where the picture sits unmoved, or the score below turnBelow: a
// camera turned by 2 degrees scored under 0.75 on round 6's photos.
const (
	turnFrom  = 3.5
	turnBelow = 0.8
)

// turnSteps are the turns tried, in degrees, before the best is refined
// by half a degree each way.
var turnSteps = []float64{-4, -3, -2, -1, 0, 1, 2, 3, 4}

// turn finds the turn about the box's centre and the shift of that centre
// that line the surroundings up best, near the overall best shift
// (bu, bv). A camera that was knocked often turns a little too, which
// moves each side of the box by a different amount; the box itself moves
// by the shift of its centre. It returns that shift in frame pixels and
// its score. Gradients are compared unturned: a few degrees change their
// directions too little to matter.
func (r *Reference) turn(ref, cur level, bu, bv, off int) (dx, dy, local float64, ok bool) {
	sc := float64(r.Scale)
	ci := (float64(r.Box.Min.X+r.Box.Max.X)/2-float64(r.Area.Min.X))/sc - 0.5
	cj := (float64(r.Box.Min.Y+r.Box.Max.Y)/2-float64(r.Area.Min.Y))/sc - 0.5
	var idx []int
	for k, k2 := range ref.ok {
		if k2 {
			idx = append(idx, k)
		}
	}
	// The turns are tried on every third pixel, and the best is refined
	// on all of them.
	var sparse []int
	for n := 0; n < len(idx); n += 3 {
		sparse = append(sparse, idx[n])
	}
	use := sparse
	// Where each pixel in use lands in cur at shift 0, turned: lin, and
	// the extremes of its column and row, so a shift that keeps every
	// pixel inside cur needs no bounds check per pixel.
	lin, qi, qj := make([]int, len(idx)), make([]int, len(idx)), make([]int, len(idx))
	var loI, hiI, loJ, hiJ int
	place := func(deg float64) {
		s, c := math.Sincos(deg * math.Pi / 180)
		loI, hiI, loJ, hiJ = math.MaxInt, math.MinInt, math.MaxInt, math.MinInt
		for n, k := range use {
			pi, pj := float64(k%ref.w)-ci, float64(k/ref.w)-cj
			x := int(math.Round(c*pi-s*pj+ci)) + off
			y := int(math.Round(s*pi+c*pj+cj)) + off
			qi[n], qj[n], lin[n] = x, y, y*cur.w+x
			loI, hiI, loJ, hiJ = min(loI, x), max(hiI, x), min(loJ, y), max(hiJ, y)
		}
	}
	at := func(u, v int) (float64, bool) {
		inside := loI+u >= 0 && hiI+u < cur.w && loJ+v >= 0 && hiJ+v < cur.h
		d := v*cur.w + u
		var dot, nr, nc float64
		m := 0
		for n, k := range use {
			if !inside {
				x, y := qi[n]+u, qj[n]+v
				if x < 0 || y < 0 || x >= cur.w || y >= cur.h {
					continue
				}
			}
			c := lin[n] + d
			if !cur.ok[c] {
				continue
			}
			rx, ry, cx, cy := ref.gx[k], ref.gy[k], cur.gx[c], cur.gy[c]
			dot += float64(rx*cx + ry*cy)
			nr += float64(rx*rx + ry*ry)
			nc += float64(cx*cx + cy*cy)
			m++
		}
		if m < int(minOverlap*float64(len(use))) || nr == 0 || nc == 0 {
			return 0, false
		}
		return dot / math.Sqrt(nr*nc), true
	}
	type cand struct {
		deg     float64
		u, v    int
		s       float64
		visited bool
	}
	best := cand{s: -2}
	search := func(deg float64, u0, v0, rng int) {
		place(deg)
		for v := v0 - rng; v <= v0+rng; v++ {
			for u := u0 - rng; u <= u0+rng; u++ {
				if s, ok := at(u, v); ok && s > best.s {
					best = cand{deg: deg, u: u, v: v, s: s, visited: true}
				}
			}
		}
	}
	for _, deg := range turnSteps {
		search(deg, bu, bv, turnRange)
	}
	if !best.visited {
		return 0, 0, 0, false
	}
	use = idx
	d0, u0, v0 := best.deg, best.u, best.v
	best = cand{s: -2}
	search(d0, u0, v0, 1)
	search(d0-0.5, u0, v0, 1)
	search(d0+0.5, u0, v0, 1)
	// A fraction of a pixel at the best turn.
	place(best.deg)
	frac := func(du, dv int) float64 {
		a, oka := at(best.u-du, best.v-dv)
		c, okc := at(best.u+du, best.v+dv)
		if !oka || !okc {
			return 0
		}
		den := a - 2*best.s + c
		if den >= 0 {
			return 0
		}
		return math.Max(-0.5, math.Min(0.5, 0.5*(a-c)/den))
	}
	fx, fy := frac(1, 0), frac(0, 1)
	return (float64(best.u) + fx) * sc, (float64(best.v) + fy) * sc, best.s, true
}

// crop1 drops a grid's first row and column.
func crop1(g []float32, w, h int) ([]float32, int, int) {
	out := make([]float32, (w-1)*(h-1))
	for j := 1; j < h; j++ {
		copy(out[(j-1)*(w-1):j*(w-1)], g[j*w+1:j*w+w])
	}
	return out, w - 1, h - 1
}
