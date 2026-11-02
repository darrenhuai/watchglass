package ocr

import (
	"math"
	"sort"
)

// Small tenths. Many thermometers draw the digits after the point smaller
// than the rest, at a half to two thirds of their height and standing on
// the same baseline, and some clocks draw the seconds smaller after the
// minutes. Under tallCellFrac of the row such a digit was taken for a
// fragment, a speck of a unit or a label, and left out: 20.1 with its 1 at
// 0.6 of the height read a confident 20., 25.4 read 25., and 12:34 with
// small seconds 45 read 12:35. A cluster that stands on the full-size
// digits' baseline right after a decimal point (or after another such
// digit), or after the last full-size digit of a row with a colon in it,
// and is at least smallTenthsMinFrac of the row tall, is read as a digit
// judged on its own height. Anything shorter is left out as before: a
// unit's letters sit higher or lower, or stand after no point.
//
// A small digit's bars are no bigger than the point, and noise or the gap
// between the bars breaks it into pieces: a small 4 came apart into its
// upper left bar, its middle bar (point-sized, so taken for a mark) and
// its right-hand bars, which alone read a confident 1 (25.4 read 25.1).
// The pieces between the point and the promoted cluster belong to it and
// are merged into it. A small 1's two bars, a gap apart and each no wider
// than a point, look like a colon; a "colon" standing on the baseline
// right after a decimal point is no colon (smallOnesAfterPoint).

// smallTenthsMinFrac: the shortest a digit after the point may be, as a
// share of the row's ink. A tenths digit at half the height is 0.4 of the
// row once a 4 or 7 leaves out its top or bottom bar.
const smallTenthsMinFrac = 0.35

// smallWideFrac: a small digit at least this wide for its ink's height is
// a full-width digit; a "1" is a bar or two thick, about a fifth.
const smallWideFrac = 0.35

// smallSpanFrac: a small digit with the pieces merged into it spans at
// most this share of its height. A digit is about half as wide as it is
// tall, and a piece split off it may stand a little way off its side.
const smallSpanFrac = 0.8

// smallHalvesFrac: two narrow pieces that span at most this share of the
// small digit's height are the two halves of one digit. A 0 is about
// half as wide as it is tall; two small 1s side by side span more.
const smallHalvesFrac = 0.65

// secondsGapFrac: a small seconds digit stands no further from the ink
// before it than this share of the row's height, and no further from the
// digit before it than that and a digit's width. Digits stand about a
// quarter of their height apart; a mark much further off is not part of
// the number.
const secondsGapFrac = 0.75

// smallTopStrokes: a digit whose top stands more than this many strokes
// under the row's is a smaller digit, whatever it is laid out as. A
// full-size 1 or 4, which have no top bar, stop short by a stroke at most.
const smallTopStrokes = 1.5

// smallAfterPoint turns the fragments that are small digits after the
// decimal point, or after a clock's minutes, into digit cells.
func smallAfterPoint(m bitmap, cells []cell, points, colons, promoted []blob, stroke, reach, inkTop int, fInk float64) {
	var feet []int
	for _, k := range cells {
		if k.tall && !k.degree && !k.fragment {
			feet = append(feet, k.c.y1)
		}
	}
	if len(feet) == 0 {
		return
	}
	foot := median(feet)
	// fullSize: a full-size digit, the measure the small ones stand after:
	// its ink fills most of the row, or its bars imply it does. A seconds
	// digit at two thirds of the row implies 0.8 of it (sizeRef), so the
	// test is on the ink.
	fullSize := func(o cell) bool {
		return o.tall && !o.degree && !o.fragment && !o.small && (float64(o.c.h()) >= smallDigitFrac*fInk || o.sizeRef >= fInk)
	}
	order := make([]int, len(cells))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return cells[order[a]].c.x0 < cells[order[b]].c.x0 })
	// stacked: a mark with another one over it. Marks stacked in a column
	// are the bars of a small digit broken into dots, not a point. A row
	// binarized at a poor level can hold thousands of specks, so the
	// marks are compared only with those that start before they end.
	stacked := make([]bool, len(points))
	byX := make([]int, len(points))
	for i := range byX {
		byX[i] = i
	}
	sort.Slice(byX, func(a, b int) bool { return points[byX[a]].x0 < points[byX[b]].x0 })
	for a, i := range byX {
		for _, j := range byX[a+1:] {
			p, o := points[i], points[j]
			if o.x0 > p.x1 {
				break
			}
			stacked[i] = stacked[i] || o.y1 < p.y0
			stacked[j] = stacked[j] || p.y1 < o.y0
		}
	}
	for _, i := range order {
		k := &cells[i]
		c := k.c
		if k.absorbed {
			continue
		}
		// A fragment tall enough for a small digit, or a smaller digit
		// laid out as a "1": judged against a height its top bar implies,
		// a small 7, its left side bare, is narrow, and read as a "1" it
		// read a confident 1 (19.7 read 19.1).
		// With no full-size digit wide enough to be the measure (1.7),
		// a smaller digit is one whose top stands well under the row's.
		frag := !k.tall && !k.degree && k.fragment
		lower := float64(c.y0-inkTop) > smallTopStrokes*float64(stroke) && float64(c.h()) < smallDigitFrac*fInk
		narrow := k.tall && !k.wide && (k.sizeRef < fInk || lower)
		// A small digit read whole may have another one in pieces before
		// it (see below).
		smaller := k.tall && k.wide && !k.degree && !k.fragment && (k.sizeRef < fInk || lower)
		if !frag && !narrow && !smaller || c.y1 < foot-stroke || c.y1 > foot+stroke {
			continue
		}
		// The glyph before it: a full-size digit with a point between, or
		// a small digit promoted already, next to it.
		prev, prevSmall, prevH := math.MinInt, false, 0
		for j, o := range cells {
			if j != i && o.c.x1 < c.x0 && (o.tall && !o.degree && !o.fragment || o.small && !o.fragment) && o.c.x1 > prev {
				prev, prevSmall, prevH = o.c.x1, o.small, o.c.h()
			}
		}
		if prev == math.MinInt {
			continue
		}
		// A fragment is a small digit when it is tall enough for one, as
		// tall as the small digit just before it (noise erodes a small 1
		// a little under the share: 12:34 41 at 0.55 read 12:344, the 1
		// left out), or a pair smallOnesAfterPoint moved in.
		if frag && float64(c.h()) < smallTenthsMinFrac*fInk && !(prevSmall && c.h() >= prevH-stroke) && !inPromoted(c, promoted) {
			continue
		}
		// The decimal point between them: the first mark on the baseline
		// with no other mark over it.
		left := prev
		point := false
		for pi, mk := range points {
			if !stacked[pi] && mk.x0 > prev && mk.x1 < c.x0 && mk.y1 >= foot-stroke && (!point || mk.x1 < left) {
				point, left = true, mk.x1
			}
		}
		// A clock's seconds: after the last full-size digit of a row with
		// a colon before it.
		seconds := false
		if !point && !prevSmall {
			for _, cl := range colons {
				seconds = seconds || cl.x1 < c.x0
			}
			for j, o := range cells {
				seconds = seconds && (j == i || !fullSize(o) || o.c.x0 < c.x0)
			}
		}
		if !point && !prevSmall && !seconds {
			continue
		}
		// The pieces of the same digit between the point and this
		// cluster, standing over or under it: fragments, short bars, and
		// marks off the baseline.
		lo, hi, x0 := c.y0-stroke, c.y1+stroke, c.x0
		var took []int
		pc := blob{x0: math.MaxInt, y0: math.MaxInt, x1: math.MinInt, y1: math.MinInt}
		for j, o := range cells {
			if j == i || o.tall || o.absorbed || o.c.x0 <= left || o.c.x1 >= x0 || o.c.y1 < lo || o.c.y0 > hi {
				continue
			}
			pc = pc.merge(o.c)
			took = append(took, j)
		}
		for _, mk := range points {
			if mk.x0 > left && mk.x1 < x0 && mk.y1 >= lo && mk.y0 <= hi {
				pc = pc.merge(mk)
			}
		}
		if smaller {
			// A small digit laid out whole takes in the pieces before it
			// that fit within a digit's width of its right side: its own
			// lower left bar, split off as a mark, left a small 2 without
			// it, a '?'. Pieces as tall as a small digit themselves are
			// another digit: a small 4 in five dots before a small 5
			// (12:3|45 read 12:35). The two can't be told apart, and the
			// cell is a '?', never a shorter number. With no piece before
			// it, only the piece after it (below) can change it.
			switch {
			case pc.x0 == math.MaxInt:
			case float64(pc.h()) >= smallTenthsMinFrac*fInk:
				k.unread = true
			case float64(c.x1-pc.x0) > smallSpanFrac*float64(c.h()):
				continue
			}
		}
		for _, j := range took {
			cells[j].absorbed, cells[j].fragment, cells[j].degree = true, true, false
		}
		if pc.x0 != math.MaxInt {
			c = c.merge(pc)
		}
		halved := false
		if smaller || narrow || float64(c.w()) < smallWideFrac*float64(c.h()) {
			// A narrow piece may be half a small digit: a 0 whose left
			// and right bars came apart read as two 1s (18.0 read a
			// confident 18.11). The narrow piece just after it, close
			// enough that the two span less than a digit's width, is the
			// other half. The same piece after a small digit read whole
			// is its right-hand bars, split off (36.8 at half the
			// height read 36.?1), and a smaller digit read whole that
			// touches a narrow piece is the rest of it: digits never
			// touch (72.0 read a confident 72.10). The two may share a
			// column: a split runs down a slant.
			for j := range cells {
				o := &cells[j]
				whole := o.tall && o.wide && (o.sizeRef >= fInk || o.c.x0 > c.x1+1)
				if j == i || o.absorbed || whole || o.degree || o.c.x0 <= c.x0 || o.c.x1 <= c.x1 || o.c.y1 < lo || o.c.y0 > hi ||
					float64(o.c.x1-c.x0) > smallHalvesFrac*float64(max(c.h(), o.c.h())) {
					continue
				}
				c = c.merge(o.c)
				o.absorbed, o.fragment, o.degree, o.tall = true, true, false, false
				halved = true
			}
		}
		if smaller && pc.x0 == math.MaxInt && !halved {
			continue
		}
		k.c = c
		k.small = true
		k.tall, k.fragment, k.degree, k.fromRow = true, false, false, false
		k.wide = float64(c.w()) >= smallWideFrac*float64(c.h())
		// The cell reaches past the ink as a full-size digit's does
		// (layoutCells): where no bar runs across, and always for a "1",
		// which stops short of the row at either end. By a small digit's
		// own stroke, not the row's: a small 4, which has neither bar,
		// laid out a full-size reach either way probed its middle bar as
		// its top one.
		share := math.Min(1, float64(c.h())/(smallDigitFrac*fInk))
		r := max(1, int(math.Round(float64(reach)*share)))
		band := max(1, int(math.Round(float64(stroke)*share/2)))
		k.y0, k.y1 = c.y0, c.y1
		h := c.h()
		if !k.wide || !barAcross(m, c, c.y0, min(c.y1, c.y0+band-1)) {
			k.y0 -= r
			h += r
		}
		if !k.wide || !barAcross(m, c, max(c.y0, c.y1-band+1), c.y1) {
			k.y1 += r
			h += r
		}
		k.sizeRef = math.Min(fInk, float64(h)/smallDigitFrac)
	}
}

// inSmall reports whether mark p lies inside the ink of a small digit
// smallAfterPoint promoted: a piece of that digit, not a point.
func inSmall(p blob, cells []cell) bool {
	for _, k := range cells {
		if k.small && !k.fragment && p.x0 >= k.c.x0 && p.x1 <= k.c.x1 && p.y0 >= k.c.y0 && p.y1 <= k.c.y1 {
			return true
		}
	}
	return false
}

// smallOnesAfterPoint moves a "colon" that is a small 1 into the clusters:
// a pair of marks right after a decimal point, with no digit between, that
// reaches down to the digits' baseline and stands at least
// smallTenthsMinFrac of the row tall. A clock's colon stands between
// digits, its lower dot well above the baseline, and never after a point.
//
// A clock's small seconds end in a 1 as often as in anything else, and
// that 1 looks the same: a pair of bars on the baseline after the last
// full-size digit of a row that has a colon between its digits, or after
// a smaller digit there (12:34 41 read a confident 12:344, the 1 dropped
// as a colon with nothing after it). A colon drawn low enough to reach
// the baseline stands between full-size digits, so a pair with one after
// it is left alone.
//
// Two marks one over the other that were not paired as a colon (too far
// apart for one, or too small) are the same bars (1:0 41 read 1:04), and
// a pair standing a digit's width after a narrow piece that is in the
// same place is the other side of a small 0 whose bars came apart (72.0
// read 72.11, 12:5 01 read 12:51); both go in with it. The marks of a
// promoted stack are no longer points. The pairs moved in are returned
// too: smallAfterPoint lays them out as digits however short noise has
// left them.
func smallOnesAfterPoint(clusters, points, colons []blob, stroke, inkTop, inkH int) (_, _, _, promoted []blob) {
	inkBot := inkTop + inkH - 1
	fInk := float64(inkH)
	// fullSize: a full-size digit's cluster. A 1, 4 or 7 has no top bar
	// and stops a stroke short of the row's top; a smaller digit's top
	// stands well under it.
	fullSize := func(c blob) bool {
		return float64(c.h()) >= tallCellFrac*fInk && float64(c.y0-inkTop) <= smallTopStrokes*float64(stroke)
	}
	isPromoted := func(b blob) bool { return inPromoted(b, promoted) }
	// digitSized: a cluster that is a digit, full-size or small, or a
	// pair already moved in.
	digitSized := func(c blob) bool {
		return float64(c.h()) >= smallTenthsMinFrac*fInk || isPromoted(c)
	}
	// Tenths and seconds stand after the last full-size digit, so only
	// pairs there are looked at; and a clock has a colon between two
	// full-size digits. A row binarized at a poor level holds thousands
	// of marks, so each mark is compared only with those that start
	// before it ends.
	lastFull, firstFull := math.MinInt, math.MaxInt
	for _, c := range clusters {
		if fullSize(c) {
			lastFull, firstFull = max(lastFull, c.x1), min(firstFull, c.x0)
		}
	}
	clockColon := false
	for _, o := range colons {
		clockColon = clockColon || (o.x0 > firstFull && o.x1 < lastFull)
	}
	pairs := append([]blob(nil), colons...)
	isColon := make([]bool, len(colons))
	for i := range isColon {
		isColon[i] = true
	}
	stacked := make([]bool, len(points))
	byX := make([]int, len(points))
	maxW := 0
	for i := range byX {
		byX[i] = i
		maxW = max(maxW, points[i].w())
	}
	sort.Slice(byX, func(a, b int) bool { return points[byX[a]].x0 < points[byX[b]].x0 })
	for a, i := range byX {
		p := points[i]
		if stacked[i] || p.y1 < inkBot-stroke || p.x0 <= lastFull {
			continue
		}
		up, found := -1, false
		for b := a - 1; b >= 0 && points[byX[b]].x0 >= p.x0-maxW; b-- {
			j := byX[b]
			if q := points[j]; !stacked[j] && q.y1 < p.y0 && q.x1 >= p.x0 && (!found || q.y1 > points[up].y1) {
				up, found = j, true
			}
		}
		for b := a + 1; b < len(byX) && points[byX[b]].x0 <= p.x1; b++ {
			j := byX[b]
			if q := points[j]; !stacked[j] && q.y1 < p.y0 && (!found || q.y1 > points[up].y1) {
				up, found = j, true
			}
		}
		if !found {
			continue
		}
		m := p.merge(points[up])
		if float64(m.w()) >= smallWideFrac*float64(m.h()) {
			continue // a 1 is a bar or two thick
		}
		stacked[i], stacked[up] = true, true
		pairs = append(pairs, m)
		isColon = append(isColon, false)
	}
	var loose []blob // the marks that may be a decimal point
	for i, p := range points {
		if !stacked[i] {
			loose = append(loose, p)
		}
	}
	// seconds reports whether a pair, or a narrow cluster, stands where a
	// clock's small seconds digit does.
	seconds := func(cl blob) bool {
		if !clockColon || cl.x0 <= lastFull {
			return false
		}
		// The digit before it, within a digit's width and a gap, and the
		// last ink before it (that digit, or a piece of this one) no
		// further off than the digits stand apart.
		prev, found, last := blob{}, false, math.MinInt
		for _, c := range clusters {
			if c == cl {
				continue
			}
			if c.x1 < cl.x0 {
				last = max(last, c.x1)
				if digitSized(c) && (!found || c.x1 > prev.x1) {
					prev, found = c, true
				}
			}
		}
		for _, p := range points {
			if p.x1 < cl.x0 {
				last = max(last, p.x1)
			}
		}
		if !found || prev.y1 < inkBot-stroke || float64(cl.x0-prev.x1-1) > (secondsGapFrac+smallSpanFrac)*fInk ||
			float64(cl.x0-last-1) > secondsGapFrac*fInk {
			return false
		}
		// As tall as a small digit, or as the smaller digit it follows:
		// noise erodes a small 1's bars a little under that.
		return float64(cl.h()) >= smallTenthsMinFrac*fInk || (!fullSize(prev) && cl.h() >= prev.h()-stroke)
	}
	// afterPoint reports whether a pair stands right after a decimal
	// point, with nothing digit-sized between; a pair moved in between
	// counts as a digit unless through says to look past it (the narrow
	// first side of a split digit stands after such a pair: 72.0 read
	// 72.11).
	afterPoint := func(cl blob, through bool) bool {
		if float64(cl.h()) < smallTenthsMinFrac*fInk || cl.x0 <= lastFull {
			return false
		}
		// The point nearest before it: if any point will do, that one
		// does, with the least room for a digit between.
		p, found := blob{}, false
		for _, q := range loose {
			if q.x1 < cl.x0 && q.y1 >= inkBot-stroke && (!found || q.x1 > p.x1) {
				p, found = q, true
			}
		}
		if !found {
			return false
		}
		before, between := false, false
		for _, c := range clusters {
			before = before || (c.x1 < p.x0 && float64(c.h()) >= tallCellFrac*fInk)
			// A piece shorter than a small digit between them is more of
			// it: a small 7's top bar.
			between = between || (c != cl && c.x0 > p.x1 && c.x1 < cl.x0 && float64(c.h()) >= smallTenthsMinFrac*fInk && !(through && isPromoted(c)))
		}
		return before && !between
	}
	// halfOf reports whether a pair is the other side of a small digit
	// whose narrow first side stands a digit's width before it: a piece
	// in the same place, over the same rows.
	halfOf := func(cl blob) bool {
		if cl.x0 <= lastFull {
			return false
		}
		for _, c := range clusters {
			if c == cl || c.x1 >= cl.x0 || fullSize(c) || float64(c.w()) >= smallWideFrac*float64(c.h()) ||
				c.y1 < inkBot-stroke || float64(cl.x1-c.x0) > smallHalvesFrac*float64(c.h()) || cl.y0 > c.y1 || c.y0 > cl.y1 {
				continue
			}
			if isPromoted(c) || seconds(c) || afterPoint(c, true) {
				return true
			}
		}
		return false
	}
	// Left to right, so a pair moved in is the digit before the next.
	order := make([]int, len(pairs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return pairs[order[a]].x0 < pairs[order[b]].x0 })
	var kept []blob
	for _, i := range order {
		cl := pairs[i]
		if cl.y1 < inkBot-stroke || cl.x0 <= lastFull || !(afterPoint(cl, false) || seconds(cl) || halfOf(cl)) {
			if isColon[i] {
				kept = append(kept, cl)
			}
			continue
		}
		at := sort.Search(len(clusters), func(i int) bool { return clusters[i].x0 > cl.x0 })
		clusters = append(clusters[:at:at], append([]blob{cl}, clusters[at:]...)...)
		promoted = append(promoted, cl)
	}
	// The marks of a stack that is not a 1 stay points; those of a
	// promoted stack are its bars.
	var keptPoints []blob
	for _, p := range points {
		inOne := false
		for _, cl := range promoted {
			inOne = inOne || (p.x0 >= cl.x0 && p.x1 <= cl.x1 && p.y0 >= cl.y0 && p.y1 <= cl.y1)
		}
		if !inOne {
			keptPoints = append(keptPoints, p)
		}
	}
	return clusters, keptPoints, kept, promoted
}

// inPromoted reports whether cluster c is one smallOnesAfterPoint moved in.
func inPromoted(c blob, promoted []blob) bool {
	for _, o := range promoted {
		if o == c {
			return true
		}
	}
	return false
}
