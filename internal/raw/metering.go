package raw

import "math"

// MeanBrightness computes the mean brightness (0–255) of the linear CFA data
// within the named zone, for driving auto-exposure from the raw signal rather
// than the gamma-encoded display image. Each sample is black-subtracted and
// normalized by (white − black) so the result is a physically linear measure
// of scene flux mapped onto the same 0–255 range the controller targets.
//
// Zones match capture.ZoneMean: "center" (inner 50%-radius circle, zenith),
// "top" (top third), and "full" (default / fallback).
//
// Note: because this is linear, a given scene reads darker than the gamma
// display mean — target_brightness should be retuned when switching to raw
// metering.
func (im *Image) MeanBrightness(zone string) float64 {
	black := im.blackFloor()
	span := float64(im.WhiteLevel) - black
	if span <= 0 {
		span = 65535
	}

	cx := float64(im.Width) / 2
	cy := float64(im.Height) / 2
	radius := float64(min(im.Width, im.Height)) * 0.5
	yMax := im.Height
	if zone == "top" {
		yMax = im.Height / 3
	}

	var sum float64
	var count int
	for y := 0; y < yMax; y += 4 {
		for x := 0; x < im.Width; x += 4 {
			if zone == "center" {
				dx, dy := float64(x)-cx, float64(y)-cy
				if math.Sqrt(dx*dx+dy*dy) > radius {
					continue
				}
			}
			v := (float64(im.Pix[y*im.Width+x]) - black) / span
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			sum += v * 255
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// blackFloor returns the smallest per-channel black level, or 0 if none is set.
// Using the minimum avoids over-subtracting any channel before normalization.
func (im *Image) blackFloor() float64 {
	if len(im.BlackLevel) == 0 {
		return 0
	}
	b := im.BlackLevel[0]
	for _, v := range im.BlackLevel[1:] {
		if v < b {
			b = v
		}
	}
	return b
}
