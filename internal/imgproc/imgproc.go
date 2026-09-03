// Package imgproc holds pure image math: cropping and frame diffing.
package imgproc

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"watchglass/internal/config"
)

// Crop extracts a normalized region from img. The result's bounds start at (0,0).
func Crop(img image.Image, r config.Region) *image.RGBA {
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	x0 := b.Min.X + int(math.Round(r.X*w))
	y0 := b.Min.Y + int(math.Round(r.Y*h))
	x1 := b.Min.X + int(math.Round((r.X+r.W)*w))
	y1 := b.Min.Y + int(math.Round((r.Y+r.H)*h))
	rect := image.Rect(x0, y0, x1, y1).Intersect(b)
	out := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(out, out.Bounds(), img, rect.Min, draw.Src)
	return out
}

// PercentChanged reports the percentage (0-100) of pixels whose grayscale
// value differs by more than tol between a and b. Filmed screens flicker;
// tol absorbs sensor noise so only real change counts. Dimension mismatch
// returns 100 so callers re-baseline instead of comparing garbage.
func PercentChanged(a, b image.Image, tol uint8) float64 {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return 100
	}
	if ra, ok := a.(*image.RGBA); ok {
		if rb, ok := b.(*image.RGBA); ok {
			return percentChangedRGBA(ra, rb, tol)
		}
	}
	total := ab.Dx() * ab.Dy()
	if total == 0 {
		return 0
	}
	changed := 0
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ga := grayAt(a, ab.Min.X+x, ab.Min.Y+y)
			gb := grayAt(b, bb.Min.X+x, bb.Min.Y+y)
			d := int(ga) - int(gb)
			if d < 0 {
				d = -d
			}
			if d > int(tol) {
				changed++
			}
		}
	}
	return float64(changed) / float64(total) * 100
}

func grayAt(img image.Image, x, y int) uint8 {
	r, g, b, _ := img.At(x, y).RGBA()
	// standard luma weights on 16-bit channel values, scaled to 8-bit
	return uint8((299*r + 587*g + 114*b) / 1000 >> 8)
}

// percentChangedRGBA is PercentChanged specialized for the *image.RGBA pair
// every runner produces (Crop always returns RGBA). Direct Pix access runs
// an order of magnitude faster than the generic At() path on large regions,
// which is what keeps big-region pixel watches cheap on small hardware.
//
// The luma arithmetic below must replicate grayAt's exact 16-bit path
// (image/color.RGBA.RGBA() expands each 8-bit channel c8 to 16-bit via
// c8*0x101 before the standard-weights divide-and-shift). A naive 8-bit
// reformulation is not always bit-identical to the 16-bit one because of
// division-boundary rounding, so we scale up by 0x101 here too before
// dividing — see TestPercentChangedFastPathMatchesGeneric, which pins this.
func percentChangedRGBA(a, b *image.RGBA, tol uint8) float64 {
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	total := w * h
	if total == 0 {
		return 0
	}
	changed := 0
	for y := 0; y < h; y++ {
		ra := a.Pix[a.PixOffset(a.Bounds().Min.X, a.Bounds().Min.Y+y):]
		rb := b.Pix[b.PixOffset(b.Bounds().Min.X, b.Bounds().Min.Y+y):]
		for x := 0; x < w; x++ {
			o := x * 4
			ga := int((299*uint32(ra[o])*0x101 + 587*uint32(ra[o+1])*0x101 + 114*uint32(ra[o+2])*0x101) / 1000 >> 8)
			gb := int((299*uint32(rb[o])*0x101 + 587*uint32(rb[o+1])*0x101 + 114*uint32(rb[o+2])*0x101) / 1000 >> 8)
			d := ga - gb
			if d < 0 {
				d = -d
			}
			if d > int(tol) {
				changed++
			}
		}
	}
	return float64(changed) / float64(total) * 100
}

// Apply runs the watch's preprocessing chain for OCR legibility:
// upscale -> grayscale -> invert -> binarize. A zero-value Preprocess
// returns img unchanged. Threshold implies grayscale.
func Apply(img *image.RGBA, p config.Preprocess) *image.RGBA {
	out := img
	if p.Upscale > 1 {
		out = upscale(out, p.Upscale)
	}
	if p.Grayscale || p.Threshold > 0 {
		out = grayscale(out)
	}
	if p.Invert {
		out = invert(out)
	}
	if p.Threshold > 0 {
		out = binarize(out, uint8(p.Threshold))
	}
	return out
}

func upscale(img *image.RGBA, n int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*n, b.Dy()*n))
	for y := 0; y < b.Dy()*n; y++ {
		for x := 0; x < b.Dx()*n; x++ {
			out.SetRGBA(x, y, img.RGBAAt(b.Min.X+x/n, b.Min.Y+y/n))
		}
	}
	return out
}

func grayscale(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			g := grayAt(img, b.Min.X+x, b.Min.Y+y)
			out.SetRGBA(x, y, color.RGBA{R: g, G: g, B: g, A: 255})
		}
	}
	return out
}

func invert(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := img.RGBAAt(b.Min.X+x, b.Min.Y+y)
			out.SetRGBA(x, y, color.RGBA{R: 255 - c.R, G: 255 - c.G, B: 255 - c.B, A: c.A})
		}
	}
	return out
}

func binarize(img *image.RGBA, level uint8) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			v := uint8(0)
			if grayAt(img, b.Min.X+x, b.Min.Y+y) >= level {
				v = 255
			}
			out.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return out
}
