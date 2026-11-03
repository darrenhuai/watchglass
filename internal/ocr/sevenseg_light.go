package ocr

import "image"

// Uneven light. A shadow across the glass, a glare gradient or a lamp to
// one side lights one end of a reflective LCD more than the other, and the
// thresholds, which are global, lose the digits at one end: at the level
// that reads the dark end the light end's ground is lit too, at the level
// that reads the light end the dark end's digits are gone (an 852 read 5?).
// flatten divides the light out: each pixel over the mean of a window
// round it about as wide as the box is tall, so ink and ground keep their
// contrast wherever they lie. The flattened crop is read as a second
// opinion when the crop as it is doesn't read cleanly (see readCrop).
//
// Round 6's ghost-segment photos (a clock at 852, a VFD at 23:39, a
// microwave at 24:00) read this way too: their unlit segments are faint
// where the light falls, and divided out, the lit ones stand clear of
// them. But flattening can lift the unlit segments as well, most of all
// on a light-on-dark display, where a lit segment and an unlit one both
// stand at twice the mean or more and clip together: an LED showing 12:47
// with its unlit segments at a third of the lit ones read 88:88. So a
// flattened reading that lights segments the crop as it is shows unlit is
// passed over (flatGhosts). Measuring each segment against the deepest
// one in its digit did not hold: in real photos one lit bar of a digit
// stood 0.44-0.47 as far from the ground as the next, as shallow as a
// ghost, and near a shadow's edge the ground itself is mismeasured.
//
// Flattening is not cheap on a big box: there the flattened read covers
// only the rows of the digits found and a digit's height round them, and a
// big crop the decoder found no ink in at all is not flattened.

// flattenWinFrac: the window is this share of the crop's height either
// side of the pixel: wider than a digit's bars and holes, so the mean is
// the light there and not the ink.
const flattenWinFrac = 0.5

// flatten divides each pixel by the mean of the window round it, the mean
// mapping to 128. What stands at twice the mean or more clips to white:
// the ground of a reflective LCD in a patch of glare, or a lit segment of
// an LED, whose unlit segments may clip with it (see flatGhosts).
func flatten(g grayImg) grayImg {
	r := max(2, int(flattenWinFrac*float64(g.h)))
	// The sums run to 255 times the pixel count: int64, so a big crop
	// doesn't overflow a 32-bit int (an ARM build: a uniform 800x400 crop
	// came out 128 to 251 where all of it is 128).
	w := g.w + 1
	sum := make([]int64, w*(g.h+1))
	for y := 0; y < g.h; y++ {
		var row int64
		for x := 0; x < g.w; x++ {
			row += int64(g.pix[y*g.w+x])
			sum[(y+1)*w+x+1] = sum[y*w+x+1] + row
		}
	}
	out := grayImg{w: g.w, h: g.h, pix: make([]uint8, len(g.pix)), letters: g.letters}
	for y := 0; y < g.h; y++ {
		y0, y1 := max(0, y-r), min(g.h, y+r+1)
		for x := 0; x < g.w; x++ {
			x0, x1 := max(0, x-r), min(g.w, x+r+1)
			s := sum[y1*w+x1] - sum[y0*w+x1] - sum[y1*w+x0] + sum[y0*w+x0]
			n := int64((y1 - y0) * (x1 - x0))
			v := int64(g.pix[y*g.w+x]) * 128 * n / max(1, s)
			out.pix[y*g.w+x] = uint8(min(255, v))
		}
	}
	return out
}

// withFlatLight reads the flattened crop when best has a glyph it couldn't
// read, or none at all, and keeps that reading when it scores better. A
// flattened reading that spells what best spells found nothing new: it
// only took best's doubt away, and the doubt stays (a 5 with a degree sign
// over it read 9 unsure, and flattened, 9 sure).
func withFlatLight(g grayImg, best reading) reading {
	if readClean(best) {
		return best
	}
	// A small crop is flattened whole: the end of the row the light lost
	// is what the flattened read is for, and the reading as it is may
	// have found no ink there, or none at all. A big one only round the
	// ink found.
	win := image.Rect(0, 0, g.w, g.h)
	if g.w*g.h > flatMaxPixels {
		w, ok := flatRegion(g, best)
		if !ok {
			return best
		}
		win = w
	}
	f := flatten(g.sub(win))
	thr, primary, ok := thresholdLevels(f)
	if !ok {
		return best
	}
	r := readGray(f, thr, primary, frameCut{})
	r.offset(win.Min)
	r = dropFlatTail(r, best)
	if len(r.glyphs) == 0 || spelled(r) == spelled(best) || dashesLose(r, best) || flatGhosts(r, best) || lostEdge(f, win, thr[primary], r, best) {
		return best
	}
	if r.score > best.score || len(best.glyphs) == 0 {
		return r
	}
	return best
}

// dropFlatTail drops a point after the last glyph of a flattened reading
// that the crop as it is doesn't show: flattening lifts whatever is faint
// in a flat patch of the ground, and a fleck of glare past a microwave's
// clock read 24:00. as a point.
func dropFlatTail(r, best reading) reading {
	n := len(r.glyphs)
	if n < 2 || r.glyphs[n-1].ch != '.' {
		return r
	}
	p := r.glyphs[n-1].ink
	for _, gl := range best.glyphs {
		if gl.ch == '.' && gl.ink.Overlaps(p) {
			return r
		}
	}
	r.glyphs = r.glyphs[:n-1]
	return r
}

// spelled is the reading's text.
func spelled(r reading) string {
	b := make([]byte, len(r.glyphs))
	for i, gl := range r.glyphs {
		b[i] = gl.ch
	}
	return string(b)
}

// readClean reports whether a reading read every glyph it found, surely.
func readClean(r reading) bool {
	for _, gl := range r.glyphs {
		if gl.ch == '?' || gl.unsure() {
			return false
		}
	}
	return len(r.glyphs) > 0
}

// flatGhosts reports whether the flattened reading r lit the unlit
// segments best left dark: an 8 (or a '?') where best read another digit
// surely, as ghostEvidence judges two thresholds, or nothing but 8s where
// best read something else. Flattening evens out the light, but it lifts
// the faint unlit segments of a display with them, and an LED showing
// 12:47 read a confident 88:88.
func flatGhosts(r, best reading) bool {
	if ghostEvidence(r, best) {
		return true
	}
	eights := func(x reading) bool {
		all, n := true, 0
		for _, gl := range x.glyphs {
			if gl.digit() {
				n++
				all = all && gl.ch == '8'
			}
		}
		return all && n > 0
	}
	return eights(r) && !eights(best)
}

// flatMaxPixels: a crop larger than this is flattened only in the band of
// rows round the ink found (see flatRegion), and not at all with none.
const flatMaxPixels = 100_000

// lostEdge reports whether best has a '?' on the box's left or right edge
// and the flattened crop f (win of the crop, thresholded at thr) still
// shows ink between that edge and the glyph r reads there, in at least
// cutSpanFrac of the glyph's rows (as withCutEdges asks of a cut digit),
// which r left out: what the box cut of the
// digit beyond, which the flattened read took for the housing. A third of
// a clock's leading 1, cut by the box, ran into the 5 after it and read
// '?' as it was, and flattened it vanished: 15:45:16 read a confident
// 5:45:16. Where the '?' was a dark corner, flattened it is ground.
func lostEdge(f grayImg, win image.Rectangle, thr int, r, best reading) bool {
	ink := func(x, y int) bool {
		x, y = x-win.Min.X, y-win.Min.Y
		if x < 0 || y < 0 || x >= f.w || y >= f.h {
			return false
		}
		return (int(f.pix[y*f.w+x]) > thr) != r.dark
	}
	for _, b := range best.glyphs {
		left, right := b.ink.Min.X <= win.Min.X, b.ink.Max.X >= win.Max.X
		if b.ch != '?' || !left && !right {
			continue
		}
		// The glyph r reads nearest that edge.
		var near *glyph
		for i := range r.glyphs {
			gl := &r.glyphs[i]
			if gl.numeral() && (near == nil || left && gl.ink.Min.X < near.ink.Min.X || !left && gl.ink.Max.X > near.ink.Max.X) {
				near = gl
			}
		}
		if near == nil {
			continue
		}
		x0, x1 := win.Min.X, near.ink.Min.X
		if !left {
			x0, x1 = near.ink.Max.X, win.Max.X
		}
		// A row counts when it has a run of ink wider than a sliver, but
		// not ink all the way across: a shadow's edge, flattened, leaves
		// the ground beside it dark from the box's edge to the digit.
		thick := cutSliverPx + 1
		row := func(y int) (lit, all bool) {
			run := 0
			all = true
			for x := x0; x < x1; x++ {
				if !ink(x, y) {
					all = false
					if run < thick {
						run = 0
					}
				} else if run < thick {
					run++
				}
			}
			return run >= thick, all
		}
		rows, solid := 0, 0
		for y := near.ink.Min.Y; y < near.ink.Max.Y; y++ {
			if lit, all := row(y); lit {
				rows++
				if all {
					solid++
				}
			}
		}
		// Nor ink that runs on past the digits' row, as the housing's
		// edge does and no bar of a digit (dropEdgeStrips).
		past := max(2, int(edgeStripPastFrac*float64(near.ink.Dy())))
		strip := false
		for _, y := range []int{near.ink.Min.Y - past, near.ink.Max.Y - 1 + past} {
			lit, _ := row(y)
			strip = strip || lit
		}
		if float64(rows) >= cutSpanFrac*float64(near.ink.Dy()) && 2*solid < rows && !strip {
			return true
		}
	}
	return false
}

// flatRegion is the part of g worth reading again: the rows of the ink
// best found, read or not, and a digit's height above and below, across
// the whole width, since the digits the light lost stand in the same row.
// A box drawn loose round small digits is mostly ground, and flattened
// whole a 1920x1080 one took twice as long and twice the memory to read.
// With no ink found at all there is nothing to read.
func flatRegion(g grayImg, best reading) (image.Rectangle, bool) {
	var u image.Rectangle
	var hs []int
	for _, gl := range best.glyphs {
		u = u.Union(gl.ink)
		if gl.digit() {
			hs = append(hs, gl.ink.Dy())
		}
	}
	for _, rs := range [][]image.Rectangle{best.dropped, best.marks, best.bars, best.cutPoints} {
		for _, q := range rs {
			u = u.Union(q)
		}
	}
	if u.Empty() {
		return image.Rectangle{}, false
	}
	m := u.Dy()
	if len(hs) > 0 {
		m = median(hs)
	}
	// Specks read as glyphs (a crop binarized at a poor level) are no
	// measure of the digits: never less than a quarter of the crop.
	m = max(m, g.h/4)
	return image.Rect(0, u.Min.Y-m, g.w, u.Max.Y+m).Intersect(image.Rect(0, 0, g.w, g.h)), true
}
