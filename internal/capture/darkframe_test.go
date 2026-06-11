package capture

import (
	"image"
	"image/color"
	"testing"
)

// uniformFrame returns a 4x4 RGBA image filled with the given gray value.
func uniformFrame(v uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

// A single bright outlier (satellite trail / cosmic ray) in one of the darks
// must be rejected by the median stack; a mean would drag the pixel up.
func TestStackFrames_MedianRejectsOutlier(t *testing.T) {
	contaminated := uniformFrame(10)
	contaminated.SetRGBA(2, 2, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	master := stackFrames([]image.Image{uniformFrame(10), uniformFrame(10), contaminated})

	r, g, b, _ := master.At(2, 2).RGBA()
	if uint8(r>>8) != 10 || uint8(g>>8) != 10 || uint8(b>>8) != 10 {
		t.Errorf("outlier pixel survived median stack: got (%d,%d,%d), want (10,10,10)",
			uint8(r>>8), uint8(g>>8), uint8(b>>8))
	}
	// Non-outlier pixel unchanged.
	r, _, _, _ = master.At(0, 0).RGBA()
	if uint8(r>>8) != 10 {
		t.Errorf("clean pixel changed: got %d, want 10", uint8(r>>8))
	}
}

// With fewer than three frames a median is meaningless; the stack must fall
// back to the mean.
func TestStackFrames_MeanFallbackBelowThree(t *testing.T) {
	master := stackFrames([]image.Image{uniformFrame(10), uniformFrame(30)})
	r, _, _, _ := master.At(1, 1).RGBA()
	if uint8(r>>8) != 20 {
		t.Errorf("two-frame stack: got %d, want mean 20", uint8(r>>8))
	}
}

func TestMedianU8(t *testing.T) {
	if got := medianU8([]uint8{255, 10, 10}); got != 10 {
		t.Errorf("median of {255,10,10} = %d, want 10", got)
	}
	if got := medianU8([]uint8{1, 2, 3, 4, 5}); got != 3 {
		t.Errorf("median of 1..5 = %d, want 3", got)
	}
}
