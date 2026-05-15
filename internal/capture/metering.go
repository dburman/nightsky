package capture

import (
	"image"
	"math"
)

// ZoneMean computes the mean luminance (0–255) of img within the named zone.
//
// Zones:
//   - "full"   — entire frame (default)
//   - "center" — inner circle with radius = 50% of min(width, height); covers
//     the zenith region of an upward-pointing fisheye all-sky camera
//   - "top"    — top third of frame (sky region for a horizon-facing camera)
//
// Falls back to full-frame if the zone name is unrecognised.
func ZoneMean(img image.Image, zone string) float64 {
	switch zone {
	case "center":
		return centerCircleMean(img)
	case "top":
		return topThirdMean(img)
	default:
		return fullMean(img)
	}
}

func fullMean(img image.Image) float64 {
	b := img.Bounds()
	var sum float64
	var count int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

func centerCircleMean(img image.Image) float64 {
	b := img.Bounds()
	cx := float64(b.Min.X+b.Max.X) / 2
	cy := float64(b.Min.Y+b.Max.Y) / 2
	r := float64(min(b.Dx(), b.Dy())) * 0.5 // 50% radius

	var sum float64
	var count int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		dy := float64(y) - cy
		for x := b.Min.X; x < b.Max.X; x += 4 {
			dx := float64(x) - cx
			if math.Sqrt(dx*dx+dy*dy) > r {
				continue
			}
			rv, g, bl, _ := img.At(x, y).RGBA()
			sum += 0.299*float64(rv>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			count++
		}
	}
	if count == 0 {
		return fullMean(img)
	}
	return sum / float64(count)
}

func topThirdMean(img image.Image) float64 {
	b := img.Bounds()
	yMax := b.Min.Y + b.Dy()/3

	var sum float64
	var count int
	for y := b.Min.Y; y < yMax; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			count++
		}
	}
	if count == 0 {
		return fullMean(img)
	}
	return sum / float64(count)
}
