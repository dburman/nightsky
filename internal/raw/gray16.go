package raw

import (
	"fmt"
	"image"
	"slices"
)

// ToGray16 packs the CFA samples into an *image.Gray16, a lossless 16-bit
// container that encodes to standard PNG — used to persist raw master darks.
// CFA metadata is not carried; masters only need dimensions and samples.
func (im *Image) ToGray16() *image.Gray16 {
	g := image.NewGray16(image.Rect(0, 0, im.Width, im.Height))
	for i, v := range im.Pix {
		g.Pix[i*2] = uint8(v >> 8) // Gray16 stores big-endian
		g.Pix[i*2+1] = uint8(v)
	}
	return g
}

// FromGray16 unpacks a Gray16 (e.g. a decoded master-dark PNG) into an Image
// holding only dimensions and samples; CFA/black/white metadata is zero.
func FromGray16(g *image.Gray16) *Image {
	b := g.Bounds()
	im := &Image{
		Width:  b.Dx(),
		Height: b.Dy(),
		Pix:    make([]uint16, b.Dx()*b.Dy()),
	}
	for i := range im.Pix {
		im.Pix[i] = uint16(g.Pix[i*2])<<8 | uint16(g.Pix[i*2+1])
	}
	return im
}

// StackMedian combines raw frames into a master by per-photosite median
// (mean when fewer than three frames), rejecting transient outliers like
// satellite trails the same way the RGB dark stacker does. Metadata (CFA,
// black/white levels) is copied from the first frame.
func StackMedian(frames []*Image) (*Image, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("raw: no frames to stack")
	}
	first := frames[0]
	for i, f := range frames[1:] {
		if f.Width != first.Width || f.Height != first.Height {
			return nil, fmt.Errorf("raw: frame %d size %dx%d != %dx%d",
				i+1, f.Width, f.Height, first.Width, first.Height)
		}
	}

	out := &Image{
		Width: first.Width, Height: first.Height,
		Pix:        make([]uint16, len(first.Pix)),
		CFACols:    first.CFACols,
		CFARows:    first.CFARows,
		CFAPattern: slices.Clone(first.CFAPattern),
		BlackLevel: slices.Clone(first.BlackLevel),
		WhiteLevel: first.WhiteLevel,
	}

	n := len(frames)
	if n < 3 {
		for i := range out.Pix {
			var sum uint32
			for _, f := range frames {
				sum += uint32(f.Pix[i])
			}
			out.Pix[i] = uint16(sum / uint32(n))
		}
		return out, nil
	}

	samples := make([]uint16, n)
	for i := range out.Pix {
		for f := 0; f < n; f++ {
			samples[f] = frames[f].Pix[i]
		}
		slices.Sort(samples)
		out.Pix[i] = samples[n/2]
	}
	return out, nil
}
