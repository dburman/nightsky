package raw

import "fmt"

// SubtractDark subtracts a master dark's samples from a frame in linear sensor
// space, clamping at zero. Calibrating here — on the raw CFA data, before
// debayering and gamma encoding — is where dark-current and fixed-pattern
// removal is physically valid, unlike subtraction on the 8-bit display image.
// The frame is modified in place.
func (im *Image) SubtractDark(dark *Image) error {
	if dark.Width != im.Width || dark.Height != im.Height {
		return fmt.Errorf("raw: dark size %dx%d != frame %dx%d",
			dark.Width, dark.Height, im.Width, im.Height)
	}
	for i, d := range dark.Pix {
		if im.Pix[i] > d {
			im.Pix[i] -= d
		} else {
			im.Pix[i] = 0
		}
	}
	return nil
}

// DivideFlat applies flat-field correction in linear space: each photosite is
// scaled by its CFA colour's mean flat level divided by the local flat value,
// which flattens lens vignetting and dust shadows without shifting the colour
// balance (normalization is per CFA colour, not global). The frame is modified
// in place; samples are clamped to WhiteLevel (or 65535 if unset).
func (im *Image) DivideFlat(flat *Image) error {
	if flat.Width != im.Width || flat.Height != im.Height {
		return fmt.Errorf("raw: flat size %dx%d != frame %dx%d",
			flat.Width, flat.Height, im.Width, im.Height)
	}

	// Per-CFA-colour mean of the flat.
	var sum [3]float64
	var count [3]int
	for y := 0; y < im.Height; y++ {
		for x := 0; x < im.Width; x++ {
			c := flat.ColorAt(x, y)
			if c > CFABlue {
				continue
			}
			sum[c] += float64(flat.Pix[y*im.Width+x])
			count[c]++
		}
	}
	var mean [3]float64
	for c := 0; c < 3; c++ {
		if count[c] > 0 {
			mean[c] = sum[c] / float64(count[c])
		}
	}

	clamp := float64(im.WhiteLevel)
	if clamp == 0 {
		clamp = 65535
	}

	for y := 0; y < im.Height; y++ {
		for x := 0; x < im.Width; x++ {
			i := y*im.Width + x
			fv := float64(flat.Pix[i])
			if fv <= 0 {
				continue // can't correct a black flat pixel; leave as-is
			}
			c := flat.ColorAt(x, y)
			if c > CFABlue {
				continue
			}
			v := float64(im.Pix[i]) * mean[c] / fv
			if v > clamp {
				v = clamp
			}
			im.Pix[i] = uint16(v)
		}
	}
	return nil
}
