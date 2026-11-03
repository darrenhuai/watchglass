package ocr

import (
	"image"
	"math"
)

// Letters on a seven-segment display (SevenSeg.Letters). The letter table
// itself is letterPatterns; what else a letter needs is below. The capital
// letters and b, d, h, t and y stand as tall as a digit and are laid out
// and probed like one. The lower-case c, n, o, r and u stand in the lower
// half of the row (their top is the middle bar), which laid out as a cell
// of their own would put their middle bar under the probes for the top
// bar; and under half the row they are what the decoder otherwise drops as
// a fragment. lowerCase finds them and layoutCells gives such a cell the
// row's full height, so the probes find the bars where the letter has them.
const (
	// lowerTopFrac: a lower-case letter's ink starts at least this far
	// down the row (its middle bar is the row's). A digit, even a smaller
	// one, starts higher.
	lowerTopFrac = 0.38
	// lowerBaseFrac: and reaches to within this share of the row's bottom
	// (an n or r ends with the tips of its side bars, a stroke and a gap
	// above the bottom bar's foot).
	lowerBaseFrac = 0.3
	// lowerMinFrac: and is at least this tall. A lower-case letter runs
	// from the middle bar to the bottom, about half the row (a washer's n
	// measures 0.44 of it, an oven's 0.49); the lower loop of a digit too
	// pale for the threshold is eroded to less (an odometer's 6, 0.31).
	lowerMinFrac = 0.4
	// lowerWideFrac: and at least this wide. An r is two bars wide, less
	// than a full-width digit (wideCellFrac).
	lowerWideFrac = 0.2
	// lowerWidthShare: and at least this share as wide as the widest
	// full-height glyph in the row.
	lowerWidthShare = 0.6
	// letterTallFrac: a capital letter (and b, d, h, t, y) spans the row
	// like a digit, so a cell under this share of the row's height that
	// spells one is a piece of something: a lower-case shape over half
	// the row counts as tall (tallCellFrac) and laid out at its own
	// height a c spells C, a u spells U. An H or h, whose ink runs only
	// between the unlit top and bottom bars, stands 0.72 of the row; a
	// lower-case letter, from the middle bar down, 0.55 to 0.6.
	letterTallFrac = 0.65
	// flatMarkRatio: with the letter table on, a mark this much wider
	// than it is tall is a bar (the foot of an L), never a point.
	flatMarkRatio = 1.5
	// dashHeightRatio: a row of nothing but dashes is laid out this many
	// times the widest dash tall (a middle bar is about a third of the
	// digit's height long).
	dashHeightRatio = 3
	// dashAspect: a dash is at least this many times as wide as it is tall
	// (a thick-stroked LCD's middle bar is short: 1.6 to 1.8 times).
	dashAspect = 1.5
	// glyphMaxWideFrac: no glyph is wider than this share of the row's
	// height; a bar a sliver after a cluster that would make it wider is
	// the next glyph's (clusterBars).
	glyphMaxWideFrac = 0.7
	// bandShare: a band beside a letter (besideBand) runs along at least
	// this share of that side of the letter's ink.
	bandShare = 0.5
	// bandReachFrac: and stands within this share of the row's height of
	// it (a stroke at least): the rim of a meter's window stands a few
	// pixels after its last digit.
	bandReachFrac = 0.25
)

// dashesOnly reports whether the row shows nothing but dashes ("--", a
// display standing by): two or more clusters, every one a flat bar, at
// least dashAspect times as wide as it is tall and at least two pixels
// thick, standing clear of the box's edges (a band of housing or the
// ground in the wrong polarity runs into them), no wider than the box is
// tall (a dash is a third of a digit), and every dash level with the
// others within a dash's thickness. Alone in the row, such bars set the
// row's height themselves and were laid out as digits of their own
// height, which read '?'. One dash alone stays '?': a reading eroded to a
// digit's middle bar, or to the minus before a digit a threshold lost,
// looks the same (a gauge's -7 at Binarize 128 read a confident '-').
func dashesOnly(m bitmap, clusters []blob) bool {
	if len(clusters) < 2 {
		return false
	}
	thick, lo, hi := 0, math.MaxInt, math.MinInt
	for _, c := range clusters {
		if c.h() < 2 || float64(c.w()) < dashAspect*float64(c.h()) || c.w() > m.h ||
			c.x0 <= 0 || c.y0 <= 0 || c.x1 >= m.w-1 || c.y1 >= m.h-1 {
			return false
		}
		thick = max(thick, c.h())
		cy := (c.y0 + c.y1) / 2
		lo, hi = min(lo, cy), max(hi, cy)
	}
	return hi-lo <= thick
}

// dashRow is the row extent for a row of dashes: with no digit to measure
// by, the cells are laid out dashHeightRatio times the widest dash tall,
// centred on it, so each bar is probed where a digit's middle bar is and
// reads '-'.
func dashRow(cells []cell) (y0, y1 int) {
	widest := cell{}
	for _, k := range cells {
		if k.c.w() > widest.c.w() {
			widest = k
		}
	}
	h := dashHeightRatio * widest.c.w()
	cy := (widest.c.y0 + widest.c.y1) / 2
	return cy - h/2, cy - h/2 + h - 1
}

// clockColon reports whether a colon stands between two full-height
// glyphs, as a clock's does. Then a shorter cluster after the last digit
// is its smaller seconds, not a lower-case letter (see lowerCase). A pair
// of marks on the box's edge with nothing after them is a glyph the box
// cut, not a clock.
func clockColon(colons, clusters []blob, fInk float64) bool {
	for _, c := range colons {
		left, right := false, false
		for _, o := range clusters {
			if float64(o.h()) < tallCellFrac*fInk {
				continue
			}
			left = left || o.x1 < c.x0
			right = right || o.x0 > c.x1
		}
		if left && right {
			return true
		}
	}
	return false
}

// lowerCase reports whether cluster c is a lower-case letter: it stands in
// the lower half of the row, on its baseline, as wide as a letter, within
// a digit's width of a full-height glyph on either side (or of the
// lower-case letter just before it, prevLower's right edge: the second r
// of Err), and no point stands between it and the cluster before it. The
// tenths of a thermometer stand on the baseline too, drawn smaller after
// the point; they keep reading as digits (see smallAfterPoint). The lower
// loop of a digit too pale for the threshold spells a u or a c too, so a
// letter standing further from the digits than a digit is wide is none (an
// odometer's pale last 6, 1.5 digits past the rest, read u). The caller
// only asks for a row that has a full-size digit and no colon: a clock
// draws its seconds smaller after the last digit, in the same half of the
// row.
func lowerCase(c blob, clusters, marks []blob, inkTop, inkH, prevLower int) bool {
	fInk := float64(inkH)
	if float64(c.y0) < float64(inkTop)+lowerTopFrac*fInk || float64(c.y1) < float64(inkTop)+fInk-1-lowerBaseFrac*fInk {
		return false
	}
	if float64(c.h()) < lowerMinFrac*fInk || float64(c.w()) < lowerWideFrac*fInk {
		return false
	}
	// As wide as the row's full-size glyphs, near enough: the corner of a
	// window's rim in the lower half of the row is narrower.
	prev, widest := math.MinInt, 0
	for _, o := range clusters {
		if o.x1 < c.x0 {
			prev = max(prev, o.x1)
		}
		if float64(o.h()) >= tallCellFrac*fInk {
			widest = max(widest, o.w())
		}
	}
	if float64(c.w()) < lowerWidthShare*float64(widest) {
		return false
	}
	// Beside a full-height glyph, or the lower-case letter before it.
	beside := prevLower != math.MinInt && c.x0-prevLower-1 <= widest
	for _, o := range clusters {
		if float64(o.h()) < tallCellFrac*fInk {
			continue
		}
		if (o.x1 < c.x0 && c.x0-o.x1-1 <= widest) || (o.x0 > c.x1 && o.x0-c.x1-1 <= widest) {
			beside = true
		}
	}
	if !beside {
		return false
	}
	for _, mk := range marks {
		if mk.x1 < c.x0 && mk.x0 > prev {
			return false
		}
	}
	return true
}

// lowerBesideCapitals turns a lower-case letter into '?' unless it stands
// within a digit's width of a capital letter (or of a lower-case letter
// before it that does): the words a display spells with lower-case
// letters stand them beside capitals (End, Err, nCr), while the lower
// loop of a digit too pale for the threshold spells a u or a c beside the
// digits (an odometer's pale 16 read ?u1). lowerCase laid the cell out;
// this is the check once the row is read.
func lowerBesideCapitals(r *reading, cells []cell) {
	widest := 0
	for _, k := range cells {
		if k.tall && !k.fragment {
			widest = max(widest, k.c.w())
		}
	}
	lower := func(ch byte) bool { return ch == 'c' || ch == 'n' || ch == 'o' || ch == 'r' || ch == 'u' }
	capital := func(ch byte) bool { return isLetter(ch) && !lower(ch) }
	prevOK := math.MinInt // the right edge of the last lower-case letter kept
	for i, g := range r.glyphs {
		if !lower(g.ch) {
			continue
		}
		ok := prevOK != math.MinInt && g.ink.Min.X-prevOK <= widest
		for _, o := range r.glyphs {
			if !capital(o.ch) {
				continue
			}
			if (o.ink.Max.X <= g.ink.Min.X && g.ink.Min.X-o.ink.Max.X <= widest) || (o.ink.Min.X >= g.ink.Max.X && o.ink.Min.X-g.ink.Max.X <= widest) {
				ok = true
			}
		}
		if ok {
			prevOK = g.ink.Max.X
			continue
		}
		r.glyphs[i].ch, r.glyphs[i].conf, r.glyphs[i].alt = '?', unsureConf, 0
		r.score -= 100 + g.conf
	}
}

// onEdge reports whether cluster c touches an edge of the box: a glyph
// the box cut through, which with a bar gone may spell a letter (a 6 cut
// on the right spells E, 0s cut along the top spell U).
func onEdge(m bitmap, c blob) bool {
	return c.x0 <= 1 || c.y0 <= 1 || c.x1 >= m.w-2 || c.y1 >= m.h-2
}

// lettersOffEdges is onEdge for a reading mapped back to the crop (w by
// h): a letter whose ink touches the crop's edge reads '?'. The cells
// were judged in the de-sheared bitmap, where a slanted display's rows
// are shifted and the crop's edge is not the bitmap's (a VCR clock's 4,
// cut by a shrunk box, spelled L on the crop's right edge).
func lettersOffEdges(r *reading, w, h int) {
	for i, g := range r.glyphs {
		if !g.letter() {
			continue
		}
		if g.ink.Min.X <= 1 || g.ink.Min.Y <= 1 || g.ink.Max.X >= w-1 || g.ink.Max.Y >= h-1 {
			r.glyphs[i].ch, r.glyphs[i].conf, r.glyphs[i].alt = '?', unsureConf, 0
			r.score -= 100 + g.conf
		}
	}
}

// besideBand reports whether something that is no glyph stands against
// cell k's ink, within reach px of it on any side, along at least
// bandShare of that side: a strip of housing or a window's rim the decoder
// erased before clustering (erased), or lit pixels there that lie in no
// other cell's ink. A digit with a bar lost to such a band spells a
// letter (a 0 whose top bar went with the housing is a U, one whose right
// bars went with the rim is an L), so a letter there reads '?'.
func besideBand(m bitmap, k cell, cells []cell, reach int, erased []image.Rectangle) bool {
	ink := k.c.rect()
	crop := image.Rect(0, 0, m.w, m.h)
	bands := [4]image.Rectangle{
		image.Rect(ink.Min.X, ink.Min.Y-reach, ink.Max.X, ink.Min.Y),
		image.Rect(ink.Min.X, ink.Max.Y, ink.Max.X, ink.Max.Y+reach),
		image.Rect(ink.Min.X-reach, ink.Min.Y, ink.Min.X, ink.Max.Y),
		image.Rect(ink.Max.X, ink.Min.Y, ink.Max.X+reach, ink.Max.Y),
	}
	for i, band := range bands {
		band = band.Intersect(crop)
		if band.Empty() {
			continue
		}
		flat := i < 2 // the band runs along the top or bottom
		length := band.Dx()
		if !flat {
			length = band.Dy()
		}
		need := int(math.Ceil(bandShare * float64(length)))
		for _, e := range erased {
			if o := e.Intersect(band); !o.Empty() && (flat && o.Dx() >= need || !flat && o.Dy() >= need) {
				return true
			}
		}
		// Lit pixels in the band that belong to no cell: the rim of the
		// window that is still in the bitmap.
		n := 0
		for t := 0; t < length; t++ {
			x0, y0, x1, y1 := band.Min.X+t, band.Min.Y, band.Min.X+t, band.Max.Y-1
			if !flat {
				x0, y0, x1, y1 = band.Min.X, band.Min.Y+t, band.Max.X-1, band.Min.Y+t
			}
			if litOutsideCells(m, x0, y0, x1, y1, cells, k) {
				n++
			}
		}
		if n >= need {
			return true
		}
	}
	return false
}

// litOutsideCells reports whether a lit pixel lies in the line x0,y0..x1,y1
// (one column or one row) outside every cell's ink but k's.
func litOutsideCells(m bitmap, x0, y0, x1, y1 int, cells []cell, k cell) bool {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if !m.at(x, y) {
				continue
			}
			inCell := false
			for _, o := range cells {
				// A fragment or a mark is no glyph: a bar of a digit the
				// threshold broke off is what the letter is missing.
				if o.c != k.c && !o.fragment && !o.degree && image.Pt(x, y).In(o.c.rect()) {
					inCell = true
					break
				}
			}
			if !inCell {
				return true
			}
		}
	}
	return false
}

// flatMark reports whether a mark is too flat to be a point: with the
// letter table on, the foot of an L or the bottom bar of a c that the
// clustering sees on its own. A point is about square; a point the box
// cuts is flat too, but stands on the box's bottom edge (see splitBlobs).
func flatMark(b blob) bool {
	return float64(b.w()) >= flatMarkRatio*float64(b.h())
}
