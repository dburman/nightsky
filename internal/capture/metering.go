package capture

import (
	"image"
	"image/color"
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

// lumSampler reads pixel luminance without allocating.
//
// image.Image.At returns a boxed color.Color, so the obvious loop costs one
// allocation per sampled pixel — tens of thousands per frame, on every frame,
// since auto-exposure meters continuously. The concrete types here cover
// everything the capture path actually produces: rpicam-still's JPEG decodes
// to *image.YCbCr (day mode) and its PNG to *image.RGBA (night mode), with
// NRGBA covering PNGs that carry an alpha channel. Anything else falls back
// to At, so correctness never depends on the fast paths existing.
//
// Each branch reproduces the arithmetic of the corresponding color type's
// RGBA() method exactly, so results are identical to the At-based version
// rather than merely close.
type lumSampler struct {
	rgba  *image.RGBA
	nrgba *image.NRGBA
	ycbcr *image.YCbCr
	img   image.Image
}

func newLumSampler(img image.Image) lumSampler {
	switch t := img.(type) {
	case *image.RGBA:
		return lumSampler{rgba: t}
	case *image.NRGBA:
		return lumSampler{nrgba: t}
	case *image.YCbCr:
		return lumSampler{ycbcr: t}
	default:
		return lumSampler{img: img}
	}
}

func lum(r, g, b float64) float64 {
	return 0.299*r + 0.587*g + 0.114*b
}

func (s lumSampler) at(x, y int) float64 {
	switch {
	case s.rgba != nil:
		// Pix is already premultiplied, and RGBA() returns pix*0x101, so
		// >>8 recovers the stored byte exactly.
		i := s.rgba.PixOffset(x, y)
		p := s.rgba.Pix
		return lum(float64(p[i]), float64(p[i+1]), float64(p[i+2]))
	case s.nrgba != nil:
		i := s.nrgba.PixOffset(x, y)
		p := s.nrgba.Pix
		a := uint32(p[i+3])
		// Mirror NRGBA.RGBA(): widen to 16-bit, premultiply, then take the
		// high byte. For the opaque frames a camera produces this is the
		// stored byte, but the general form keeps translucent input exact.
		conv := func(v uint8) float64 {
			c := uint32(v)
			c |= c << 8
			c = c * a / 0xff
			return float64(c >> 8)
		}
		return lum(conv(p[i]), conv(p[i+1]), conv(p[i+2]))
	case s.ycbcr != nil:
		yi := s.ycbcr.YOffset(x, y)
		ci := s.ycbcr.COffset(x, y)
		r, g, b := color.YCbCrToRGB(s.ycbcr.Y[yi], s.ycbcr.Cb[ci], s.ycbcr.Cr[ci])
		return lum(float64(r), float64(g), float64(b))
	default:
		r, g, b, _ := s.img.At(x, y).RGBA()
		return lum(float64(r>>8), float64(g>>8), float64(b>>8))
	}
}

// meteringStep is the sampling stride. Every 4th pixel in each axis is ample
// for a brightness average and keeps the cost off the capture cadence.
const meteringStep = 4

func fullMean(img image.Image) float64 {
	b := img.Bounds()
	s := newLumSampler(img)

	var sum float64
	var count int
	for y := b.Min.Y; y < b.Max.Y; y += meteringStep {
		for x := b.Min.X; x < b.Max.X; x += meteringStep {
			sum += s.at(x, y)
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
	s := newLumSampler(img)

	cx := float64(b.Min.X+b.Max.X) / 2
	cy := float64(b.Min.Y+b.Max.Y) / 2
	r := float64(min(b.Dx(), b.Dy())) * 0.5 // 50% radius
	// Compare squared distances so the per-pixel math needs no square root.
	r2 := r * r

	var sum float64
	var count int
	for y := b.Min.Y; y < b.Max.Y; y += meteringStep {
		dy := float64(y) - cy
		for x := b.Min.X; x < b.Max.X; x += meteringStep {
			dx := float64(x) - cx
			if dx*dx+dy*dy > r2 {
				continue
			}
			sum += s.at(x, y)
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
	s := newLumSampler(img)
	yMax := b.Min.Y + b.Dy()/3

	var sum float64
	var count int
	for y := b.Min.Y; y < yMax; y += meteringStep {
		for x := b.Min.X; x < b.Max.X; x += meteringStep {
			sum += s.at(x, y)
			count++
		}
	}
	if count == 0 {
		return fullMean(img)
	}
	return sum / float64(count)
}
