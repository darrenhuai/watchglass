package ocr

import (
	"image"
	"math"
)

// Small digits. Under about 20 px tall a digit's bars are two or three
// pixels thick and the gaps between them one or two, and a threshold on
// whole pixels either joins the bars or breaks them: a 1234 rendered 12 px
// tall, shrunk the way a camera shrinks it, read right 7 times in 30 and a
// confident wrong number 6 times. Read at a bilinear upscale, where each
// pixel's grey becomes a slope the threshold can cut between pixels, the
// same crops read 29-30 of 30 with no wrong number (S2's probe, digits 10
// to 36 px tall, light and dark, sharp and soft). A nearest-neighbour
// upscale (preprocess upscale) helps less: it only repeats the pixels.
//
// So a crop shorter than tinyCropH is read at the smallest whole factor
// that makes it at least smallCropH tall, at most smallMaxScale. A taller
// crop is read as it is first, and again enlarged when the digits found in
// it are small (withSmallDigits). Enlarging every crop under 48 px, or
// under 90, cost exact reads: a meter's last digit (the decoder's floors
// for specks and slivers are in pixels, and at 2x a sliver the box cut is
// twice as wide), and on small glowing LED digits the point only a looser
// threshold sees, which bilinear smoothing hands to every threshold. A
// crop of two grey levels, which Binarize makes, is left alone: the
// threshold has already been drawn, and enlarged a hot pixel or two in a
// car's display grew into a pair of "1"s.
const (
	// tinyCropH: a crop shorter than this is always read enlarged, to at
	// least smallCropH; a taller one by the size of the digits found in it
	// (see withSmallDigits).
	tinyCropH     = 24
	smallCropH    = 48
	smallMaxScale = 4
	// smallDigitH, tinyDigitH: digits shorter than these are read again
	// at the factor that makes them smallDigitTarget tall (see
	// withSmallDigits).
	smallDigitH      = 22
	tinyDigitH       = 16
	smallDigitTarget = 30
	// smallMaxPixels: the most an enlarged read covers, about a 1080p
	// frame.
	smallMaxPixels = 2_000_000
)

// enlargeSmall returns g at a bilinear upscale when it is shorter than
// tinyCropH, and g itself otherwise.
func enlargeSmall(g grayImg) grayImg {
	if g.h <= 0 || g.w <= 0 || g.h >= tinyCropH || twoLevels(g) {
		return g
	}
	return enlarge(g, min(smallMaxScale, int(math.Ceil(float64(smallCropH)/float64(g.h)))))
}

// withSmallDigits reads g again enlarged when the digits best found in it
// are small: under tinyDigitH the enlarged reading is taken unless it
// scores worse (read as they are, digits that small came back a
// confident wrong number 6-16 times in 30 on the probe, and a wrong
// reading scores as well as a right one); under smallDigitH only when
// best isn't clean and the enlarged reading scores better. A blank or
// unreadable crop under smallCropH is tried enlarged too.
func withSmallDigits(g grayImg, best reading) reading {
	if twoLevels(g) || g.h <= 0 {
		return best
	}
	var hs []int
	for _, gl := range best.glyphs {
		if gl.digit() && gl.ch != '-' && gl.ch != '?' {
			hs = append(hs, gl.ink.Dy())
		}
	}
	h := 0
	switch {
	case len(hs) > 0:
		h = median(hs)
	case g.h < smallCropH:
		h = g.h / 2 // nothing read: judge by the crop
	}
	if h <= 0 || h >= smallDigitH || (h >= tinyDigitH && readClean(best)) {
		return best
	}
	k := min(smallMaxScale, int(math.Ceil(float64(smallDigitTarget)/float64(h))))
	// No more than smallMaxPixels are read enlarged: past that, only the
	// digits found and a digit's height round them (a loose box is mostly
	// ground: a 1280x720 one with 14 px digits, enlarged whole, took 2 s
	// and 240 MB to read), at a smaller factor if need be.
	win := image.Rect(0, 0, g.w, g.h)
	if w, ok := flatRegion(g, best); ok && win.Dx()*win.Dy()*k*k > smallMaxPixels {
		// The digits found, a digit's height round them each way.
		m := w.Dy() / 3
		x0, x1 := g.w, 0
		for _, gl := range best.glyphs {
			x0, x1 = min(x0, gl.ink.Min.X), max(x1, gl.ink.Max.X)
		}
		if x0 < x1 {
			w.Min.X, w.Max.X = max(0, x0-m), min(g.w, x1+m)
		}
		win = w
	}
	for k >= 2 && win.Dx()*win.Dy()*k*k > smallMaxPixels {
		k--
	}
	if k < 2 {
		return best
	}
	r := readCrop(enlarge(g.sub(win), k))
	if len(r.glyphs) == 0 {
		return best
	}
	if r.score > best.score || (h < tinyDigitH && r.score >= best.score) {
		return r
	}
	return best
}

// enlarge returns g at a bilinear upscale of k.
func enlarge(g grayImg, k int) grayImg {
	if k < 2 {
		return g
	}
	out := grayImg{w: g.w * k, h: g.h * k, pix: make([]uint8, g.w*k*g.h*k)}
	at := func(x, y int) float64 {
		x = max(0, min(g.w-1, x))
		y = max(0, min(g.h-1, y))
		return float64(g.pix[y*g.w+x])
	}
	for y := 0; y < out.h; y++ {
		fy := (float64(y)+0.5)/float64(k) - 0.5
		y0 := int(math.Floor(fy))
		ty := fy - float64(y0)
		for x := 0; x < out.w; x++ {
			fx := (float64(x)+0.5)/float64(k) - 0.5
			x0 := int(math.Floor(fx))
			tx := fx - float64(x0)
			v := (at(x0, y0)*(1-tx)+at(x0+1, y0)*tx)*(1-ty) + (at(x0, y0+1)*(1-tx)+at(x0+1, y0+1)*tx)*ty
			out.pix[y*out.w+x] = uint8(math.Round(v))
		}
	}
	return out
}

// twoLevels reports whether g holds at most two grey levels.
func twoLevels(g grayImg) bool {
	a, b := -1, -1
	for _, v := range g.pix {
		switch {
		case int(v) == a || int(v) == b:
		case a < 0:
			a = int(v)
		case b < 0:
			b = int(v)
		default:
			return false
		}
	}
	return true
}
