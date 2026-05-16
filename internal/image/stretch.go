package image

import (
	"image"
	"image/draw"
	"sort"
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
		pix[i+0] = clampStretch((float32(pix[i+0])-bp)*scale)
		pix[i+1] = clampStretch((float32(pix[i+1])-bp)*scale)
		pix[i+2] = clampStretch((float32(pix[i+2])-bp)*scale)
		// alpha unchanged
	}
	return out
}

// autoLevels returns the blackPct and whitePct percentile luminance values as
// black and white points. Sampling every 4th pixel keeps it fast on large images.
func autoLevels(img *image.RGBA, blackPct, whitePct float64) (black, white int) {
	b := img.Bounds()
	pix := img.Pix
	stride := img.Stride

	var lums []int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			i := (y-b.Min.Y)*stride + (x-b.Min.X)*4
			if i+2 >= len(pix) {
				continue
			}
			lum := int(0.299*float64(pix[i]) + 0.587*float64(pix[i+1]) + 0.114*float64(pix[i+2]))
			lums = append(lums, lum)
		}
	}
	if len(lums) == 0 {
		return 0, 255
	}

	sort.Ints(lums)
	n := len(lums)
	blackIdx := int(float64(n) * blackPct / 100.0)
	whiteIdx := int(float64(n) * whitePct / 100.0)
	black = lums[max(0, min(n-1, blackIdx))]
	white = lums[max(0, min(n-1, whiteIdx))]
	if black >= white {
		return 0, 255
	}
	return black, white
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
