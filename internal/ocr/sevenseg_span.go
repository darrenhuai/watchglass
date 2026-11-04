package ocr

import (
	"image"
	"math"
	"sort"
)

// Reading past a gap. One threshold serves the whole crop, and a display
// lit unevenly (a reflective LCD under a lamp on one side, a vignetting
// lens) can be dark enough to read on one side and too pale on the other.
// When the digits on the pale side sit apart from the rest, behind a wider
// gap or a blank cell, the threshold that reads the rest leaves only
// pieces of them, too small to be bars, and the reading came back short: a
// VW odometer's 105616 read a confident 1056. readSpans notices those
// pieces past the end of the reading and reads that part of the crop on its
// own, at its own thresholds. What it reads there joins the reading when it
// looks like more of the same row; anything else there is a '?', so the
// reading is never a shorter number that looks whole.
const (
	// spanPieceFrac: the dropped pieces past the reading's end add up to
	// at least this share of a digit's ink before the span is read.
	spanPieceFrac = 0.15
	// spanHeightRatio: a digit read in the span is as tall as the
	// reading's digits within this ratio either way.
	spanHeightRatio = 1.25
	// spanContrastFrac: the span's ink stands out from its ground at
	// least this share as much as the reading's does. Unlit ghost
	// segments, read at a threshold of their own, look like digits too,
	// but faintly.
	spanContrastFrac = 0.5
	// spanSmallFrac: what the span reads is left out when all of it is
	// under this share of a digit's height: smaller than any digit.
	spanSmallFrac = 0.55
)

// readSpans returns best with what lies past its ends read too (see the
// comment above): unchanged when no dropped piece of the row lies there.
func readSpans(g grayImg, best reading) reading {
	var digits []glyph
	for _, gl := range best.glyphs {
		if gl.digit() {
			digits = append(digits, gl)
		}
	}
	if len(digits) == 0 {
		return best
	}
	// The row: the median digit's height, the median top and bottom.
	var hs, tops, bots, areas []int
	left, right := g.w, 0
	for _, gl := range best.glyphs {
		left, right = min(left, gl.ink.Min.X), max(right, gl.ink.Max.X)
	}
	for _, gl := range digits {
		hs = append(hs, gl.ink.Dy())
		tops = append(tops, gl.ink.Min.Y)
		bots = append(bots, gl.ink.Max.Y)
		areas = append(areas, gl.ink.Dx()*gl.ink.Dy())
	}
	h, top, bot, area := median(hs), median(tops), median(bots), median(areas)
	if h <= 0 {
		return best
	}
	// pieces is the area of dropped ink in the row, clear of the crop's
	// edges (what the box cuts is another matter), within columns x0..x1.
	pieces := func(x0, x1 int) int {
		n := 0
		for _, d := range best.dropped {
			if d.Min.X < x0 || d.Max.X > x1 || d.Max.Y <= top || d.Min.Y >= bot {
				continue
			}
			if d.Min.X == 0 || d.Min.Y == 0 || d.Max.X >= g.w || d.Max.Y >= g.h {
				continue
			}
			n += d.Dx() * d.Dy()
		}
		return n
	}
	margin := max(1, int(math.Round(0.05*float64(h))))
	need := spanPieceFrac * float64(area)
	if x0 := right + margin; g.w-x0 >= h/2 && float64(pieces(x0, g.w)) >= need {
		best = withSpan(g, best, image.Rect(x0, 0, g.w, g.h), digits, h, top, bot, true)
	}
	if x1 := left - margin; x1 >= h/2 && float64(pieces(0, x1)) >= need {
		best = withSpan(g, best, image.Rect(0, 0, x1, g.h), digits, h, top, bot, false)
	}
	return best
}

// withSpan reads span (a part of g past the reading's right end, or its
// left) and adds what it reads to best: its digits when every glyph it
// reads in the row is a digit or '?' as tall as the reading's (h, within
// spanHeightRatio) and stands out as well; nothing when everything it
// reads there is far smaller than a digit (spanSmallFrac: the lower-case
// letters of a washer's "End", a unit); else one '?'.
func withSpan(g grayImg, best reading, span image.Rectangle, digits []glyph, h, top, bot int, after bool) reading {
	var add []glyph
	unread := glyph{ch: '?', conf: unsureConf, ink: span}
	sub := g.sub(span)
	if thr, primary, ok := thresholdLevels(sub); ok {
		// In the reading's own polarity: a pale span's ground can win the
		// other way round as one '?'.
		d := newDecoder(sub, thr, primary)
		r := d.readLevels(!best.dark)
		d.release()
		r.offset(span.Min)
		ok := len(r.glyphs) > 0
		small := ok
		var inks []image.Rectangle
		for _, gl := range r.glyphs {
			cy := (gl.ink.Min.Y + gl.ink.Max.Y) / 2
			if cy < top || cy > bot {
				continue // over or under the row: not part of this reading
			}
			small = small && float64(gl.ink.Dy()) < spanSmallFrac*float64(h)
			if gl.digit() {
				dh := float64(gl.ink.Dy())
				// A letter read in the span (the housing's corner, a
				// unit) never joins the reading: letters are read in
				// the row, where the box leaves them whole.
				if dh > spanHeightRatio*float64(h) || float64(h) > spanHeightRatio*dh || gl.ch == '-' || gl.letter() {
					ok = false
				}
				inks = append(inks, gl.ink)
			}
			add = append(add, gl)
		}
		if len(inks) == 0 {
			ok = false
		}
		if ok {
			var mine []image.Rectangle
			for _, gl := range digits {
				mine = append(mine, gl.ink)
			}
			ok = contrast(g, inks) >= spanContrastFrac*contrast(g, mine)
		}
		switch {
		case small:
			add = nil
		case !ok:
			add = []glyph{unread}
		}
	} else {
		add = []glyph{unread}
	}
	for _, gl := range add {
		if gl.ch == '?' {
			best.score -= 100
		} else if gl.digit() {
			best.score += gl.conf
		}
	}
	if after {
		best.glyphs = append(best.glyphs, add...)
	} else {
		best.glyphs = append(add, best.glyphs...)
	}
	return best
}

// contrast is how far the bright tenth of the pixels inside rects stands
// from the dark tenth: a glyph's box holds its ink and the ground round it.
func contrast(g grayImg, rects []image.Rectangle) float64 {
	var hist [256]int
	n := 0
	for _, r := range rects {
		r = r.Intersect(image.Rect(0, 0, g.w, g.h))
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				hist[g.pix[y*g.w+x]]++
				n++
			}
		}
	}
	if n == 0 {
		return 0
	}
	at := func(q float64) int {
		k := int(q * float64(n))
		for v, c := range hist {
			if k < c {
				return v
			}
			k -= c
		}
		return 255
	}
	return float64(at(0.9) - at(0.1))
}

func median(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}
