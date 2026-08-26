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
