package ocr

import (
	"math"
	"sort"
)

// A unit sign over a small digit. Many thermometers and scales print the
// tenths smaller than the rest, standing on the same baseline, with the
// unit ("°C", "g") above them in the room the smaller digit leaves. The
// sign and the digit share columns, so clusterBars, which groups ink by
// columns, folds them into one glyph as tall as the others, and its probes
// read the sign as the digit's top bars: 28.8 read 28.?. splitStacked
// takes such a sign off its digit. A cluster is split at a band of empty
// rows across it when
//
//   - what lies below is a digit of its own: at least tallCellFrac of the
//     row tall, and shaped like the row's full-size digits, no wider for
//     its height than stackAspectRatio times theirs (their median). The lower half of a
//     digit is twice as wide for its height, and read alone it can spell
//     another digit (the lower half of a 4 is a 7, of an 8 a 0);
//   - what lies above is small, at most aboveMarkFrac of the row, and ends
//     clear above the digit;
//   - what lies above is not the digit's own top bar standing a gap
//     above its side bars, as on a thin "7": the digit below has its own
//     bar across its top, or the piece above is not one flat bar over it;
//   - and the cut is what makes a digit of the cluster: read whole it is
//     no digit (or not a sure one), read alone, at its own height and
//     stroke, what lies below is. An italic font's segments leave a band
//     of empty rows across a "3" or a "2" between its upper and lower
//     halves, and those read whole.
//
// The sign goes into the reading's marks (left out, as a degree sign is)
// and the digit is read in a cell of its own height.
func splitStacked(m bitmap, clusters, bars []blob, inkH int, wk *work) (_, units []blob) {
	fInk := float64(inkH)
	var aspects []float64 // the full-size digits' width over height
	for _, c := range clusters {
		if float64(c.h()) >= smallDigitFrac*fInk && float64(c.w()) >= wideCellFrac*fInk {
			aspects = append(aspects, float64(c.w())/float64(c.h()))
		}
	}
	if len(aspects) == 0 {
		return clusters, nil // nothing to tell a smaller digit by
	}
	// Digits run together (a pair the glow joined) are no measure: the
	// median is taken over those no wider than runTogetherFrac times the
	// narrowest.
	sort.Float64s(aspects)
	n := 1
	for n < len(aspects) && aspects[n] < runTogetherFrac*aspects[0] {
		n++
	}
	maxAspect := stackAspectRatio * aspects[n/2]
	out := clusters[:0:0]
	stroke := -1
	for _, c := range clusters {
		var mem []blob
		for _, b := range bars {
			if b.x0 >= c.x0 && b.x1 <= c.x1 && b.y0 >= c.y0 && b.y1 <= c.y1 {
				mem = append(mem, b)
			}
		}
		up, down, ok := stackCut(m, mem, fInk)
		ok = ok && float64(down.w()) <= maxAspect*float64(down.h())
		if ok {
			if stroke < 0 {
				stroke = strokeEstimate(m, clusters, inkH, wk)
			}
			if ch, conf := trialRead(m, c, stroke); ch >= '0' && ch <= '9' && conf >= confOK {
				ok = false // a digit as it stands
			}
		}
		if ok {
			own := strokeEstimate(m, []blob{down}, down.h(), wk)
			if ch, conf := trialRead(m, down, own); ch < '0' || ch > '9' || conf < confOK {
				ok = false // nor a digit cut
			}
		}
		if !ok {
			out = append(out, c)
			continue
		}
		out = append(out, down)
		units = append(units, up)
	}
	return out, units
}

// trialRead classifies cluster c in a cell of its own, laid out as
// layoutCells lays out a full-width digit: reaching a stroke past its ink
// where it shows no bar across its top or bottom.
func trialRead(m bitmap, c blob, stroke int) (ch byte, conf float64) {
	reach := stroke + max(1, int(math.Round(reachFrac*float64(stroke))))
	k := cell{c: c, x0: c.x0, x1: c.x1, y0: c.y0, y1: c.y1, wide: true, tall: true}
	if !barAcross(m, c, c.y0, min(c.y1, c.y0+max(1, stroke/2)-1)) {
		k.y0 -= reach
	}
	if !barAcross(m, c, max(c.y0, c.y1-max(1, stroke/2)+1), c.y1) {
		k.y1 += reach
	}
	minRun := max(1, int(math.Round(probeMinRunFrac*float64(k.y1-k.y0+1))))
	ch, conf, _, _, blank := classify(m, k, stroke, minRun)
	if blank {
		return '?', 0
	}
	return ch, conf
}

// stackCut finds the lowest band of empty rows across a cluster's bars
// that leaves a small sign above and a digit below (see splitStacked).
func stackCut(m bitmap, mem []blob, fInk float64) (up, down blob, ok bool) {
	if len(mem) < 2 {
		return blob{}, blob{}, false
	}
	sort.Slice(mem, func(i, j int) bool { return mem[i].y0 < mem[j].y0 })
	// Walk down the bars by their tops; a cut lies after bar i when every
	// bar so far ends above the next one's top.
	reach := mem[0].y1
	for i := 1; i < len(mem); i++ {
		if mem[i].y0 <= reach {
			reach = max(reach, mem[i].y1)
			continue
		}
		u, d := mem[0], mem[i]
		for _, b := range mem[1:i] {
			u = u.merge(b)
		}
		for _, b := range mem[i+1:] {
			d = d.merge(b)
		}
		reach = max(reach, mem[i].y1)
		if float64(u.h()) > aboveMarkFrac*fInk || float64(d.h()) < tallCellFrac*fInk ||
			float64(d.w()) < wideCellFrac*min(fInk, float64(d.h())/smallDigitFrac) {
			continue
		}
		// The digit's own top bar, a gap above its side bars: one flat
		// piece over the digit's columns, with no bar across the top of
		// what is below.
		topBar := i == 1 && u.w() >= 2*u.h() && 2*(min(u.x1, d.x1)-max(u.x0, d.x0)+1) >= d.w()
		if topBar && !barAcross(m, d, d.y0, min(d.y1, d.y0+max(1, u.h())-1)) {
			continue
		}
		up, down, ok = u, d, true
	}
	return up, down, ok
}

// smallDigitFrac: a cluster shorter than this share of the row's ink is a
// smaller digit (a thermometer's tenths, a superscript unit digit), judged
// wide or narrow against its own height rather than the row's. A "1" of a
// fat font, a stroke short of the row at either end, is still taller.
const smallDigitFrac = 0.8

// stackAspectRatio: a smaller digit under a unit sign is at most this many
// times as wide for its height as the row's full-size digits (the median of
// them). Drawn smaller, a digit keeps its shape.
const stackAspectRatio = 1.25

// sizeRef is the height cluster c's width is judged against: the row's ink
// height, or a smaller digit's own height over smallDigitFrac. A cluster
// at least wideCellFrac of it wide is a full-width digit, narrower a "1".
// The digit's height is its ink's plus reach where it shows no bar across
// its top or bottom, as its cell is laid out: a "4" or a "7" of a fat font
// stops a stroke short of the row and is no smaller digit. A cluster too
// short for a digit (a minus, a speck) is judged against the row.
func sizeRef(m bitmap, c blob, fInk float64, stroke, reach int) float64 {
	if float64(c.h()) < tallCellFrac*fInk {
		return fInk
	}
	h := c.h()
	if !barAcross(m, c, c.y0, min(c.y1, c.y0+max(1, stroke/2)-1)) {
		h += reach
	}
	if !barAcross(m, c, max(c.y0, c.y1-max(1, stroke/2)+1), c.y1) {
		h += reach
	}
	return min(fInk, float64(h)/smallDigitFrac)
}

// fullDigits reports whether the row shows a full-size digit: a cluster at
// least smallDigitFrac of the row tall and wideCellFrac of it wide. Only
// then is a shorter one a smaller digit; with none, the row's height is set
// by whatever is in the box, and a speck as tall as half of it is no digit.
func fullDigits(clusters []blob, fInk float64) bool {
	for _, c := range clusters {
		if float64(c.h()) >= smallDigitFrac*fInk && float64(c.w()) >= wideCellFrac*fInk {
			return true
		}
	}
	return false
}

// cellAspect is a cell's ink width against its size (see sizeRef): what
// tells digits run together from one digit, whatever size the digit is
// drawn. For full-size digits it is their width against the row's.
func cellAspect(k cell) float64 {
	return float64(k.c.w()) / math.Max(1, k.sizeRef)
}

// Two digits a sliver apart. clusterBars keeps two digits apart by their
// facing sides, but a "2" faces the digit after it with its upper right bar
// and that digit faces back with its lower left one ("22" on a small
// thermometer): no rows in common, so the pair, a sliver apart, was one
// glyph, read '?'. splitPairs splits a cluster that is as wide for its
// height as two digits at an empty column that leaves a digit's width on
// either side of it.
const (
	// pairMinAspect: a cluster at least this wide for its height may be
	// two digits; one digit, even a fat "0", is narrower.
	pairMinAspect = 0.85
	// pairPartMin, pairPartMax: each side of the cut is a full-width
	// digit, this wide for the cluster's height (a "1" is narrower, and
	// "10" run together stays one '?').
	pairPartMin = 0.35
	pairPartMax = 0.75
)

func splitPairs(m bitmap, clusters []blob) []blob {
	out := clusters[:0:0]
	for _, c := range clusters {
		h := float64(c.h())
		if float64(c.w()) < pairMinAspect*h {
			out = append(out, c)
			continue
		}
		// The empty column nearest the middle that leaves a digit on
		// either side.
		cut, best := -1, math.Inf(1)
		for x := c.x0 + 1; x < c.x1; x++ {
			empty := true
			for y := c.y0; y <= c.y1 && empty; y++ {
				empty = !m.at(x, y)
			}
			l, r := float64(x-c.x0), float64(c.x1-x)
			if !empty || l < pairPartMin*h || r < pairPartMin*h || l > pairPartMax*h || r > pairPartMax*h {
				continue
			}
			if d := math.Abs(float64(x) - c.cx()); d < best {
				cut, best = x, d
			}
		}
		if cut < 0 {
			out = append(out, c)
			continue
		}
		// Both sides as tall as the pair: two digits of the row. A piece
		// shorter than that is a bar or a mark run into one digit.
		// (An empty side has no height: its bounds are still the
		// sentinels inkIn starts from, which h() would wrap round.)
		l, r := inkIn(m, c, c.x0, cut-1), inkIn(m, c, cut+1, c.x1)
		if l.area == 0 || r.area == 0 || float64(l.h()) < smallDigitFrac*h || float64(r.h()) < smallDigitFrac*h {
			out = append(out, c)
			continue
		}
		out = append(out, l, r)
	}
	return out
}

// inkIn is the ink of cluster c between columns x0 and x1, as a blob.
func inkIn(m bitmap, c blob, x0, x1 int) blob {
	b := blob{x0: math.MaxInt, y0: math.MaxInt, x1: math.MinInt, y1: math.MinInt}
	for y := c.y0; y <= c.y1; y++ {
		for x := x0; x <= x1; x++ {
			if m.at(x, y) {
				if b.area == 0 {
					b.seed = y*m.w + x
				}
				b.x0, b.y0, b.x1, b.y1 = min(b.x0, x), min(b.y0, y), max(b.x1, x), max(b.y1, y)
				b.area++
			}
		}
	}
	return b
}
