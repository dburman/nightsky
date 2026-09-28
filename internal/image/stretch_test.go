package image

import (
	"image"
	"image/color"
	"math/rand"
	"sort"
	"testing"
)

// referenceAutoLevels is the original collect-and-sort implementation, kept as
// the oracle for the histogram version.
func referenceAutoLevels(img *image.RGBA, blackPct, whitePct float64) (black, white int) {
	b := img.Bounds()
	pix := img.Pix
	stride := img.Stride

	var lums []int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			i := (y-b.Min.Y)*stride + (x-b.Min.X)*4
			if i+2 >= len(pix) {
				continue
			}
			lum := int(0.299*float64(pix[i]) + 0.587*float64(pix[i+1]) + 0.114*float64(pix[i+2]))
			lums = append(lums, lum)
		}
	}
	if len(lums) == 0 {
		return 0, 255
	}
	sort.Ints(lums)
	n := len(lums)
	blackIdx := int(float64(n) * blackPct / 100.0)
	whiteIdx := int(float64(n) * whitePct / 100.0)
	black = lums[max(0, min(n-1, blackIdx))]
	white = lums[max(0, min(n-1, whiteIdx))]
	if black >= white {
		return 0, 255
	}
	return black, white
}

func gradientImg(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	return img
}

// darkSkyImg approximates a night frame: a low, noisy background with a
// scattering of bright stars — the distribution the percentiles must handle.
func darkSkyImg(w, h int, seed int64) *image.RGBA {
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(rng.Intn(12) + 2)
			if rng.Float64() < 0.001 {
				v = uint8(180 + rng.Intn(75))
			}
			img.SetRGBA(x, y, color.RGBA{v, v, uint8(int(v) + rng.Intn(3)), 255})
		}
	}
	return img
}

// The histogram must return exactly what the sort returned, or it is not a
// drop-in replacement.
func TestAutoLevels_MatchesReference(t *testing.T) {
	imgs := map[string]*image.RGBA{
		"gradient/64x48":   gradientImg(64, 48),
		"gradient/321x241": gradientImg(321, 241),
		"gradient/640x480": gradientImg(640, 480),
		"darksky/640x480":  darkSkyImg(640, 480, 1),
		"darksky/321x241":  darkSkyImg(321, 241, 2),
		"flat/16x16":       image.NewRGBA(image.Rect(0, 0, 16, 16)), // all zero
	}
	pcts := [][2]float64{{10, 99.9}, {0.5, 99.5}, {1, 99}, {0, 100}, {25, 75}, {50, 50}}

	for name, img := range imgs {
		for _, p := range pcts {
			wb, ww := referenceAutoLevels(img, p[0], p[1])
			gb, gw := autoLevels(img, p[0], p[1])
			if wb != gb || ww != gw {
				t.Errorf("%s pct=%v: got (%d,%d), want (%d,%d)", name, p, gb, gw, wb, ww)
			}
		}
	}
}

// autoLevels runs on every night frame when stretch is enabled; the sort-based
// version allocated ~33 MB per call at HQ-camera resolution.
func TestAutoLevels_DoesNotAllocate(t *testing.T) {
	img := darkSkyImg(640, 480, 3)
	if allocs := testing.AllocsPerRun(5, func() { autoLevels(img, 10, 99.9) }); allocs > 0 {
		t.Errorf("autoLevels allocated %.0f times per call, want 0", allocs)
	}
}

// Manual mode is fully determined, so the mapping can be checked exactly:
// black -> 0, white -> 255, midpoint -> centre, and values outside the range
// clamp rather than wrap.
func TestStretch_ManualMapping(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5, 1))
	for i, v := range []uint8{0, 50, 100, 150, 255} {
		img.SetRGBA(i, 0, color.RGBA{v, v, v, 255})
	}

	out := Stretch(img, "manual", 50, 150, 0, 0)
	want := []uint8{0, 0, 127, 255, 255}
	for i, w := range want {
		r, _, _, _ := out.At(i, 0).RGBA()
		if got := uint8(r >> 8); got != w {
			t.Errorf("pixel %d: got %d, want %d", i, got, w)
		}
	}
}

// Auto mode on a dark frame pushes the sky background toward black while
// keeping stars bright - that is the documented purpose of the black
// percentile, not an overall brightening.
func TestStretch_AutoDarkensSkyBackground(t *testing.T) {
	img := darkSkyImg(128, 96, 4)
	out := Stretch(img, "auto", 0, 255, 10, 99.9)

	if out.Bounds() != img.Bounds() {
		t.Fatalf("bounds changed: %v -> %v", img.Bounds(), out.Bounds())
	}

	// Median approximates the sky background; max tracks the stars.
	stats := func(im image.Image) (median, maxV int) {
		b := im.Bounds()
		var hist [256]int
		n := 0
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, _, _, _ := im.At(x, y).RGBA()
				v := int(r >> 8)
				hist[v]++
				n++
				if v > maxV {
					maxV = v
				}
			}
		}
		cum := 0
		for v := 0; v < 256; v++ {
			cum += hist[v]
			if cum > n/2 {
				return v, maxV
			}
		}
		return 255, maxV
	}

	inMed, inMax := stats(img)
	outMed, outMax := stats(out)

	if outMed >= inMed {
		t.Errorf("sky background did not darken: median %d -> %d", inMed, outMed)
	}
	if outMax < inMax {
		t.Errorf("stars dimmed: max %d -> %d", inMax, outMax)
	}
	if outMax-outMed <= inMax-inMed {
		t.Errorf("contrast did not increase: range %d -> %d", inMax-inMed, outMax-outMed)
	}
}

func BenchmarkAutoLevels(b *testing.B) {
	for _, s := range []struct {
		name string
		w, h int
	}{{"1920x1080", 1920, 1080}, {"4056x3040", 4056, 3040}} {
		img := gradientImg(s.w, s.h)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				autoLevels(img, 10, 99.9)
			}
		})
	}
}

func BenchmarkAutoLevels_Reference(b *testing.B) {
	img := gradientImg(1920, 1080)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		referenceAutoLevels(img, 10, 99.9)
	}
}
