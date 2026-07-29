package stars

import (
	"image"
	"image/color"
	"testing"
)

// skyFrame builds a dark frame with optional noise-free background level.
func skyFrame(w, h int, background uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{background, background, background, 255})
		}
	}
	return img
}

// drawStar paints a small gaussian-ish point source centred at (cx, cy).
func drawStar(img *image.RGBA, cx, cy int, peak uint8) {
	levels := []struct {
		r int
		v uint8
	}{{0, peak}, {1, peak / 2}}
	for _, l := range levels {
		for dy := -l.r; dy <= l.r; dy++ {
			for dx := -l.r; dx <= l.r; dx++ {
				if dx == 0 && dy == 0 && l.r > 0 {
					continue // keep the brighter centre
				}
				img.SetRGBA(cx+dx, cy+dy, color.RGBA{l.v, l.v, l.v, 255})
			}
		}
	}
}

func TestDetect_CountsStars(t *testing.T) {
	img := skyFrame(200, 200, 10)
	// Stars inside the centre circle (radius 100 around 100,100).
	positions := [][2]int{{100, 100}, {80, 90}, {120, 110}, {95, 130}, {60, 80}}
	for _, p := range positions {
		drawStar(img, p[0], p[1], 200)
	}
	// One "star" outside the sky circle must not count.
	drawStar(img, 5, 5, 200)

	res := Detect(img)
	if res.Count != len(positions) {
		t.Errorf("Count = %d, want %d", res.Count, len(positions))
	}
	if res.MeanFWHM <= 0 || res.MeanFWHM > 6 {
		t.Errorf("MeanFWHM = %.2f, want small positive", res.MeanFWHM)
	}
}

// A uniform frame (flat daylight, lens cap) must yield zero stars.
func TestDetect_UniformFrameNoStars(t *testing.T) {
	if res := Detect(skyFrame(200, 200, 180)); res.Count != 0 {
		t.Errorf("uniform frame Count = %d, want 0", res.Count)
	}
	if res := Detect(skyFrame(200, 200, 0)); res.Count != 0 {
		t.Errorf("black frame Count = %d, want 0", res.Count)
	}
}

// A broad bright blob (moon, cloud edge) is wider than a star and must be
// rejected by the FWHM cut.
func TestDetect_RejectsBroadBlobs(t *testing.T) {
	img := skyFrame(200, 200, 10)
	for dy := -15; dy <= 15; dy++ {
		for dx := -15; dx <= 15; dx++ {
			img.SetRGBA(100+dx, 100+dy, color.RGBA{220, 220, 220, 255})
		}
	}
	if res := Detect(img); res.Count != 0 {
		t.Errorf("broad blob counted as %d stars, want 0", res.Count)
	}
}

// Two maxima closer than the separation limit collapse into one detection.
func TestDetect_MinSeparation(t *testing.T) {
	img := skyFrame(200, 200, 10)
	drawStar(img, 100, 100, 200)
	drawStar(img, 102, 100, 190) // within minSeparation of the first

	if res := Detect(img); res.Count != 1 {
		t.Errorf("Count = %d, want 1 (close pair suppressed)", res.Count)
	}
}
