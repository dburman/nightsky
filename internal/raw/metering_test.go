package raw

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.5 }

// A uniform mid-level frame should meter to the linear fraction of the
// black-to-white span, scaled to 0–255.
func TestMeanBrightness_LinearNormalization(t *testing.T) {
	im := &Image{Width: 8, Height: 8, WhiteLevel: 1023}
	im.BlackLevel = []float64{23}
	im.Pix = make([]uint16, 64)
	for i := range im.Pix {
		im.Pix[i] = 523 // (523-23)/(1023-23) = 0.5 → 127.5
	}
	if got := im.MeanBrightness("full"); !approx(got, 127.5) {
		t.Errorf("full mean = %v, want ~127.5", got)
	}
}

// "top" must only sample the upper third, so a bright top / dark bottom reads
// brighter than the whole-frame mean.
func TestMeanBrightness_TopZone(t *testing.T) {
	im := &Image{Width: 8, Height: 9, WhiteLevel: 1000}
	im.Pix = make([]uint16, 8*9)
	for y := 0; y < 9; y++ {
		val := uint16(100)
		if y < 3 {
			val = 1000 // bright top third
		}
		for x := 0; x < 8; x++ {
			im.Pix[y*8+x] = val
		}
	}
	top := im.MeanBrightness("top")
	full := im.MeanBrightness("full")
	if top <= full {
		t.Errorf("top zone (%v) should exceed full-frame mean (%v)", top, full)
	}
	if !approx(top, 255) {
		t.Errorf("top zone all-bright should read ~255, got %v", top)
	}
}

func TestMeanBrightness_ClampsAndBlack(t *testing.T) {
	// Values below black floor clamp to 0; above white clamp to 255.
	im := &Image{Width: 2, Height: 1, WhiteLevel: 500, BlackLevel: []float64{100}}
	im.Pix = []uint16{50, 9999} // below black, above white
	// Step is 4, so only x=0 is sampled in this 2-wide row.
	if got := im.MeanBrightness("full"); !approx(got, 0) {
		t.Errorf("sub-black sample should meter ~0, got %v", got)
	}
}
