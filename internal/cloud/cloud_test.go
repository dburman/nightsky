package cloud

import (
	"image"
	"image/color"
	"testing"
	"time"
)

func tinted(r, g, b uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	return img
}

func TestEstimate_GreenRatio(t *testing.T) {
	// Neutral gray sky → ratio ~1.
	m := Estimate(tinted(60, 60, 60), time.Now())
	if m.GreenRatio < 0.95 || m.GreenRatio > 1.05 {
		t.Errorf("neutral GreenRatio = %.3f, want ~1.0", m.GreenRatio)
	}

	// Aurora-green sky → ratio well above 1.
	m = Estimate(tinted(30, 90, 30), time.Now())
	if m.GreenRatio < 2.5 || m.GreenRatio > 3.5 {
		t.Errorf("green GreenRatio = %.3f, want ~3.0", m.GreenRatio)
	}

	// Black frame → neutral fallback, not division blowup.
	m = Estimate(tinted(0, 0, 0), time.Now())
	if m.GreenRatio != 1 {
		t.Errorf("black-frame GreenRatio = %.3f, want 1.0 fallback", m.GreenRatio)
	}
}
