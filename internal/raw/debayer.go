package raw

import (
	"image"
	"math"
)

// DebayerOptions controls CFA → RGB conversion.
type DebayerOptions struct {
	// WBRed and WBBlue are white-balance gain multipliers applied to the red
	// and blue channels in linear space (the same awbgains values used by
	// libcamera). Values <= 0 mean no gain (1.0).
	WBRed  float64
	WBBlue float64
	// Gamma is the display encoding gamma (e.g. 2.2). Values <= 0 or == 1
	// leave the data linear.
	Gamma float64
}

// Debayer converts the linear CFA mosaic to an 8-bit RGB image using bilinear
// interpolation: each missing colour at a photosite is the average of the
// nearest neighbours of that colour. Samples are black-subtracted and
// normalized by the black-to-white span, white-balance gains are applied in
// linear space, then the result is gamma-encoded to 8 bits.
//
// This is a simple high-quality-enough demosaic for an all-sky camera; it does
// not apply a colour-correction matrix, so colours are close to but not
// identical to the ISP's output.
func (im *Image) Debayer(opts DebayerOptions) *image.RGBA {
	w, h := im.Width, im.Height
	out := image.NewRGBA(image.Rect(0, 0, w, h))

	black := im.blackFloor()
	span := float64(im.WhiteLevel) - black
	if span <= 0 {
		span = 65535
	}

	gainR := opts.WBRed
	if gainR <= 0 {
		gainR = 1
	}
	gainB := opts.WBBlue
	if gainB <= 0 {
		gainB = 1
	}

	invGamma := 1.0
	if opts.Gamma > 0 && opts.Gamma != 1 {
		invGamma = 1.0 / opts.Gamma
	}

	// Gamma lookup table over normalized 0..1 in 1/4096 steps.
	const lutSize = 4096
	var lut [lutSize + 1]uint8
	for i := 0; i <= lutSize; i++ {
		v := float64(i) / lutSize
		if invGamma != 1.0 {
			v = math.Pow(v, invGamma)
		}
		lut[i] = uint8(v*255 + 0.5)
	}

	encode := func(v float64) uint8 {
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		return lut[int(v*lutSize)]
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Average the in-bounds 3x3 neighbourhood per colour plane. The
			// photosite's own colour uses its own sample; the other planes use
			// the mean of same-coloured neighbours. For 2x2 CFA patterns any
			// in-bounds 2x2 window contains all colours, so every plane gets
			// at least one sample even at corners.
			var sum [3]float64
			var cnt [3]int
			for dy := -1; dy <= 1; dy++ {
				ny := y + dy
				if ny < 0 || ny >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					nx := x + dx
					if nx < 0 || nx >= w {
						continue
					}
					c := im.ColorAt(nx, ny)
					if c > CFABlue {
						continue
					}
					sum[c] += float64(im.Pix[ny*w+nx])
					cnt[c]++
				}
			}
			own := im.ColorAt(x, y)
			var rgb [3]float64
			for c := 0; c < 3; c++ {
				switch {
				case uint8(c) == own:
					rgb[c] = float64(im.Pix[y*w+x])
				case cnt[c] > 0:
					rgb[c] = sum[c] / float64(cnt[c])
				}
			}

			r := (rgb[CFARed] - black) / span * gainR
			g := (rgb[CFAGreen] - black) / span
			b := (rgb[CFABlue] - black) / span * gainB

			i := out.PixOffset(x, y)
			out.Pix[i] = encode(r)
			out.Pix[i+1] = encode(g)
			out.Pix[i+2] = encode(b)
			out.Pix[i+3] = 255
		}
	}

	return out
}
