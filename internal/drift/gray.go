package drift

import (
	"image"
	"math"
)

// grayGrid is img's luma averaged over scale×scale blocks, w by h of them,
// the first block's corner at origin (frame pixels, the frame's corner at
// 0,0). A block that isn't wholly inside the frame is NaN. inside counts
// the blocks that are.
//
// The common frame types are read straight from their pixel arrays: a
// JPEG snapshot decodes to YCbCr, whose Y plane is the luma already.
func grayGrid(img image.Image, origin image.Point, scale, w, h int) (g []float32, inside int) {
	fb := img.Bounds()
	fw, fh := fb.Dx(), fb.Dy()
	g = make([]float32, w*h)
	var at func(x, y int) uint32 // luma of frame pixel x,y, scaled to 0-255
	switch m := img.(type) {
	case *image.YCbCr:
		at = func(x, y int) uint32 { return uint32(m.Y[m.YOffset(fb.Min.X+x, fb.Min.Y+y)]) }
	case *image.Gray:
		at = func(x, y int) uint32 { return uint32(m.Pix[m.PixOffset(fb.Min.X+x, fb.Min.Y+y)]) }
	case *image.RGBA:
		at = func(x, y int) uint32 {
			p := m.Pix[m.PixOffset(fb.Min.X+x, fb.Min.Y+y):]
			return (299*uint32(p[0]) + 587*uint32(p[1]) + 114*uint32(p[2])) / 1000
		}
	case *image.NRGBA:
		at = func(x, y int) uint32 {
			p := m.Pix[m.PixOffset(fb.Min.X+x, fb.Min.Y+y):]
			return (299*uint32(p[0]) + 587*uint32(p[1]) + 114*uint32(p[2])) / 1000
		}
	default:
		at = func(x, y int) uint32 {
			r, gg, b, _ := img.At(fb.Min.X+x, fb.Min.Y+y).RGBA()
			return (299*r + 587*gg + 114*b) / 1000 >> 8
		}
	}
	// A large block is sampled on a sparser grid: the average of a 6×6
	// block barely changes for reading every other pixel, and the cost of
	// a check stays about the same whatever the camera's resolution.
	step := 1
	if scale >= 4 {
		step = scale / 3
	}
	// A frame with one byte of luma per pixel (YCbCr, Gray) is read row
	// by row without a call per pixel: plane, its stride and where pixel
	// 0,0 is.
	var plane []uint8
	var stride, base int
	switch m := img.(type) {
	case *image.YCbCr:
		plane, stride, base = m.Y, m.YStride, m.YOffset(fb.Min.X, fb.Min.Y)
	case *image.Gray:
		plane, stride, base = m.Pix, m.Stride, m.PixOffset(fb.Min.X, fb.Min.Y)
	}
	nan := float32(math.NaN())
	for j := 0; j < h; j++ {
		y0 := origin.Y + j*scale
		for i := 0; i < w; i++ {
			x0 := origin.X + i*scale
			if x0 < 0 || y0 < 0 || x0+scale > fw || y0+scale > fh {
				g[j*w+i] = nan
				continue
			}
			var sum, n uint32
			for y := y0; y < y0+scale; y += step {
				if plane != nil {
					row := plane[base+y*stride+x0 : base+y*stride+x0+scale]
					for x := 0; x < scale; x += step {
						sum += uint32(row[x])
						n++
					}
					continue
				}
				for x := x0; x < x0+scale; x += step {
					sum += at(x, y)
					n++
				}
			}
			g[j*w+i] = float32(sum) / float32(n)
			inside++
		}
	}
	return g, inside
}
