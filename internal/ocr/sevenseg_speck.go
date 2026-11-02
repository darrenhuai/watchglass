package ocr

// Specks beside a digit. A reflective LCD's glass carries marks a little
// darker than the ground: the faint bar of an unused leading digit, a
// scratch, a fleck in the polariser. One that stands close enough to a
// digit joins its cluster, and in the digit's upper-left zone it lights
// that bar: a thermometer's 2 with a thin mark beside it read '?' (22.3
// read ?2.3). A digit's own pieces are bars, at least a stroke long; a
// piece of the cluster smaller than a stroke every way, apart from the
// rest, is no part of the digit and is left out of it when it is a
// sliver (twice as long as it is wide): a square one may be a colon's dot
// or one of the dots a calculator draws its bars with.

// dropSpecks erases from each cluster of at least three pieces the
// slivers no longer than a stroke either way and under half a stroke's
// square in area, and shrinks the cluster to the ink left.
func dropSpecks(m bitmap, clusters, bars []blob, stroke int, wk *work) []blob {
	if stroke < 2 {
		return clusters
	}
	for ci, c := range clusters {
		var in []blob
		for _, b := range bars {
			if b.x0 >= c.x0 && b.x1 <= c.x1 && b.y0 >= c.y0 && b.y1 <= c.y1 {
				in = append(in, b)
			}
		}
		if len(in) < 3 {
			continue
		}
		dropped := false
		for _, b := range in {
			sliver := max(b.w(), b.h()) >= 2*min(b.w(), b.h())
			if sliver && b.w() < stroke && b.h() < stroke && 2*b.area < stroke*stroke && m.on[b.seed] {
				eraseBlob(m, b, wk)
				c.area -= b.area
				dropped = true
			}
		}
		if dropped {
			clusters[ci] = shrinkToInk(m, c)
		}
	}
	return clusters
}

// shrinkToInk shrinks c's box to the lit pixels in it.
func shrinkToInk(m bitmap, c blob) blob {
	out := blob{x0: c.x1, y0: c.y1, x1: c.x0, y1: c.y0, area: c.area, seed: c.seed}
	for y := c.y0; y <= c.y1; y++ {
		for x := c.x0; x <= c.x1; x++ {
			if m.on[y*m.w+x] {
				out.x0, out.x1 = min(out.x0, x), max(out.x1, x)
				out.y0, out.y1 = min(out.y0, y), max(out.y1, y)
				if !m.on[out.seed] {
					out.seed = y*m.w + x
				}
			}
		}
	}
	if out.x1 < out.x0 {
		return c
	}
	return out
}

// windowEdge reports whether bar c, standing after digit d, is the edge of
// the display's window rather than a digit: the last thing in the row,
// nearer the digit than half the usual gap between digits, and at least
// as tall as the digit. The glass's rim a few pixels after a meter's last
// 0 read 74590?. A digit's bars stand a gap after the digit before.
func windowEdge(c, d blob, clusters []blob, gap float64) bool {
	if gap <= 0 || float64(c.x0-d.x1-1) >= windowEdgeGapFrac*gap || c.h() < d.h() {
		return false
	}
	for _, o := range clusters {
		if o.x0 > c.x1 {
			return false
		}
	}
	return true
}

// windowEdgeGapFrac: see windowEdge.
const windowEdgeGapFrac = 0.5
