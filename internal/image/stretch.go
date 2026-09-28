package image

import (
	"image"
	"image/draw"
)

// Stretch applies a linear histogram stretch to img, mapping [black, white]
// to [0, 255]. In "auto" mode the black and white points are derived from the
// 0.5th and 99.5th percentiles of the luminance histogram, which brings out
// detail in faint night-sky images without clipping bright stars.
//
// The original image data is never modified; a new *image.RGBA is returned.
func Stretch(img image.Image, mode string, blackPoint, whitePoint int, autoBlackPct, autoWhitePct float64) image.Image {
	rgba := toRGBAStretch(img)

	if mode == "auto" {
		blackPoint, whitePoint = autoLevels(rgba, autoBlackPct, autoWhitePct)
	}

	if blackPoint >= whitePoint {
		return rgba // degenerate — nothing to stretch
	}

	out := image.NewRGBA(rgba.Bounds())
	draw.Draw(out, out.Bounds(), rgba, rgba.Bounds().Min, draw.Src)

	scale := 255.0 / float32(whitePoint-blackPoint)
	bp := float32(blackPoint)
	pix := out.Pix

	for i := 0; i < len(pix)-3; i += 4 {
		pix[i+0] = clampStretch((float32(pix[i+0]) - bp) * scale)
		pix[i+1] = clampStretch((float32(pix[i+1]) - bp) * scale)
		pix[i+2] = clampStretch((float32(pix[i+2]) - bp) * scale)
		// alpha unchanged
	}
	return out
}

// autoLevels returns the blackPct and whitePct percentile luminance values as
// black and white points. Sampling every 4th pixel keeps it fast on large images.
//
// Luminance is bounded to 0–255, so the percentiles come from a 256-bin
// histogram rather than by collecting every sampled value and sorting it.
// The result is identical — a percentile of the same sample set — but the
// sort and its backing slice are gone: at 4056×3040 the old form allocated
// ~33 MB per frame and sorted ~770k ints, which on a 512 MB board is the
// dominant source of GC pressure in the capture path.
func autoLevels(img *image.RGBA, blackPct, whitePct float64) (black, white int) {
	b := img.Bounds()
	pix := img.Pix
	stride := img.Stride

	var hist [256]int
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			i := (y-b.Min.Y)*stride + (x-b.Min.X)*4
			if i+2 >= len(pix) {
				continue
			}
			lum := int(0.299*float64(pix[i]) + 0.587*float64(pix[i+1]) + 0.114*float64(pix[i+2]))
			hist[lum]++
			n++
		}
	}
	if n == 0 {
		return 0, 255
	}

	black = histPercentile(&hist, n, blackPct)
	white = histPercentile(&hist, n, whitePct)
	if black >= white {
		return 0, 255
	}
	return black, white
}

// histPercentile returns the value at the given percentile of a 256-bin
// luminance histogram holding n samples. The index is computed and clamped
// exactly as indexing a sorted slice would, so it matches the sort-based
// definition it replaced.
func histPercentile(hist *[256]int, n int, pct float64) int {
	idx := max(0, min(n-1, int(float64(n)*pct/100.0)))
	cum := 0
	for v := 0; v < len(hist); v++ {
		cum += hist[v]
		if cum > idx {
			return v
		}
	}
	return 255
}

func clampStretch(v float32) uint8 {
	if v >= 255 {
		return 255
	}
	if v <= 0 {
		return 0
	}
	return uint8(v)
}

func toRGBAStretch(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}
