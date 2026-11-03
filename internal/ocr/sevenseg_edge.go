package ocr

import (
	"image"
	"slices"
)

// Ink on the edge of the box. A box drawn a little wide takes in a strip
// of the housing or the frame round the window along one side, thicker
// than the hairline dropBezels removes; touching the digits it folds them
// into one glyph ('?'), and a strip down a side alone reads as a "1".
// dropEdgeStrips removes such a strip when it is clearly not a digit's:
//
//   - down the left or right edge, as long as the digits' row and running
//     past it above or below: no bar of a digit reaches past the row, and
//     a "1" the box cut through stays (layoutCells reads that as '?');
//   - along the top or bottom edge, longer than twice the widest piece of
//     ink inside the box: a digit's bar cut by the box is no longer than
//     the digit is wide.
//
// Anything else on an edge stays, and is read or flagged as it was.
const (
	// edgeStripAspect: a strip is at least this many times as long as it
	// is thick.
	edgeStripAspect = 4
	// edgeStripPastFrac: a strip down a side runs past the digits' row by
	// at least this share of the row's height (and two pixels).
	edgeStripPastFrac = 0.05
	// edgeStripLongRatio: a strip along the top or bottom is longer than
	// this many times the widest piece of ink inside the box.
	edgeStripLongRatio = 2
)

func dropEdgeStrips(m bitmap, blobs []blob, wk *work) []blob {
	// The row is measured on the ink clear of the left and right edges
	// (a tight box puts the digits' top and bottom bars on the other
	// two), the widest piece on the ink clear of the top and bottom.
	var sides, flats []blob
	maxW := 0
	for _, b := range blobs {
		if b.area < minBlobArea {
			continue
		}
		if b.x0 > 0 && b.x1 < m.w-1 {
			sides = append(sides, b)
		}
		if b.y0 > 0 && b.y1 < m.h-1 {
			flats = append(flats, b)
			maxW = max(maxW, b.w())
		}
	}
	top, bot := rowExtent(sides, minBlobArea)
	past := max(2, int(edgeStripPastFrac*float64(bot-top+1)))
	kept := blobs[:0]
	for _, b := range blobs {
		// Down a side: as long as the row at least, and past it above or
		// below. A digit's bar the box cut is no longer than the row.
		side := bot >= top && (b.x0 == 0 || b.x1 == m.w-1) && b.h() >= edgeStripAspect*b.w() &&
			b.h() >= bot-top+1 && (b.y0 <= top-past || b.y1 >= bot+past)
		flat := maxW > 0 && (b.y0 == 0 || b.y1 == m.h-1) && b.w() >= edgeStripAspect*b.h() && b.w() > edgeStripLongRatio*maxW
		if side || flat {
			eraseBlob(m, b, wk)
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

// A digit the box cuts through. What the box leaves of a digit on its
// left or right edge is often less than a digit: a side bar and the stubs
// of the bars across, which the clustering drops as too short for a digit
// or takes for a degree sign. Dropped silently, 1234 with the box cut into
// the 4 read a confident 123. withCutEdges puts a '?' at that end of the
// reading instead, for what lies on the box's edge past every glyph read,
// in the digits' row: pieces wider than a sliver that together run at
// least cutSpanFrac of a digit's height and reach to within cutReachFrac
// of the row's top or bottom, as a digit's side bars and the bars across
// do (the left half of a 4 runs from its top bar to its middle one, four
// tenths of the digit). A sliver, a speck, a degree sign or the letter of
// a unit beside the digits (the P of PM, the F of °F, cut by the box) is
// shorter and stays out.
//
// Less than that can be left of a digit, too: one bar, or a stub of one.
// A 7 cut down the middle leaves its top bar (9087 read 908), a small 4
// cut a third of the way in its upper left bar. One piece on the edge
// shaped like the digits' bars (barPiece: about a stroke thick, a stroke
// long or more, solid) at the top or the bottom of the row is enough. A
// unit's letters are thinner or hollower than the digits' bars, and a
// minus or a middle bar's stub alone sits in the middle of the row.
//
// Two more signs of a digit the box left out. A decimal point before
// every digit, with no room for its digit between it and the box's left
// edge (see pointsOnRow): 9.8696044 with the 9 cut read a confident
// 8696044. And a reading that ends one digit after a colon: a clock shows
// two there, and 12:21 whose last digit was too pale read 12:2.
const (
	cutSpanFrac  = 0.37
	cutReachFrac = 0.2
	// cutSliverPx: a piece this narrow or narrower is a sliver.
	cutSliverPx = 2
	// cutBarThinFrac, cutBarThickStrokes: a bar the box cut is at least
	// this share of the digits' stroke thick (the box may cut it
	// lengthways) and at most this many strokes; cutBarFill: it fills at
	// least this share of its bounding box, as a bar does and the letters
	// of a unit (the s of a clock's seconds, the r of rpm) don't.
	cutBarThinFrac     = 0.4
	cutBarThickStrokes = 1.6
	cutBarFill         = 0.7
)

// barPiece reports whether piece c of the row, in the upright bitmap the
// stroke was measured on, is shaped like one of the digits' bars or a stub
// of one: wider than a sliver, about a stroke thick, at least a stroke
// long and solid.
func barPiece(c blob, stroke int) bool {
	s := float64(stroke)
	thick, long := float64(min(c.w(), c.h())), float64(max(c.w(), c.h()))
	return thick > cutSliverPx && thick >= cutBarThinFrac*s && thick <= cutBarThickStrokes*s+1 && long >= s &&
		float64(c.area) >= cutBarFill*float64(c.w()*c.h())
}

func withCutEdges(g grayImg, best reading) reading {
	var hs, tops, bots []int
	left, right := g.w, 0
	for _, gl := range best.glyphs {
		left, right = min(left, gl.ink.Min.X), max(right, gl.ink.Max.X)
		if gl.digit() && gl.ch != '-' {
			hs = append(hs, gl.ink.Dy())
			tops = append(tops, gl.ink.Min.Y)
			bots = append(bots, gl.ink.Max.Y)
		}
	}
	if len(hs) == 0 {
		return best
	}
	h, top, bot := median(hs), median(tops), median(bots)
	reach := int(cutReachFrac * float64(h))
	atTopOrBottom := func(d image.Rectangle) bool { return d.Min.Y <= top+reach || d.Max.Y >= bot-reach }
	// The row a bar lies in runs from the highest digit's top to the
	// lowest one's foot: a 4 or a 7 stops a bar short of one or the other.
	rowTop, rowBot := slices.Min(tops), slices.Max(bots)
	rowReach := int(cutReachFrac * float64(rowBot-rowTop))
	var atLeft, atRight image.Rectangle
	var barLeft, barRight bool
	for _, d := range append(append([]image.Rectangle(nil), best.dropped...), best.marks...) {
		if d.Max.Y <= top || d.Min.Y >= bot || d.Dx() <= cutSliverPx {
			continue
		}
		if d.Min.X <= 0 && d.Max.X <= left {
			atLeft = atLeft.Union(d)
		}
		if d.Max.X >= g.w-1 && d.Min.X >= right {
			atRight = atRight.Union(d)
		}
	}
	// One bar of a digit, or a stub of one, at the top or the bottom of
	// the row where the bars across and the side bars run, is enough: the
	// top bar is all a 7 cut down the middle leaves. A minus, or the stub
	// of a middle bar alone, stays out.
	for _, d := range best.bars {
		if d.Max.Y <= rowTop || d.Min.Y >= rowBot || d.Dx() <= cutSliverPx ||
			(d.Min.Y > rowTop+rowReach && d.Max.Y < rowBot-rowReach) {
			continue
		}
		if d.Min.X <= cutSliverPx && d.Max.X <= left {
			atLeft, barLeft = atLeft.Union(d), true
		}
		if d.Max.X >= g.w-1-cutSliverPx && d.Min.X >= right {
			atRight, barRight = atRight.Union(d), true
		}
	}
	cut := func(d image.Rectangle) bool {
		return float64(d.Dy()) >= cutSpanFrac*float64(h) && atTopOrBottom(d)
	}
	cutLeft, cutRight := barLeft || cut(atLeft), barRight || cut(atRight)
	for _, p := range best.cutPoints {
		if p.Max.X <= left {
			atLeft, cutLeft = atLeft.Union(p), true
		}
	}
	if n := len(best.glyphs); n >= 3 && best.glyphs[n-1].digit() && best.glyphs[n-1].ch != '-' &&
		best.glyphs[n-2].ch == ':' && best.glyphs[n-3].digit() {
		cutRight = true
	}
	if cutLeft {
		best.glyphs = append([]glyph{{ch: '?', conf: unsureConf, x: -1, ink: atLeft}}, best.glyphs...)
		best.score -= 100
	}
	if cutRight {
		best.glyphs = append(best.glyphs, glyph{ch: '?', conf: unsureConf, x: float64(g.w), ink: atRight})
		best.score -= 100
	}
	return best
}
