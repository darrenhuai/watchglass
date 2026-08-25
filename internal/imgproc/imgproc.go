// Package imgproc holds pure image math: cropping and frame diffing.
package imgproc

import (
	"image"
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
