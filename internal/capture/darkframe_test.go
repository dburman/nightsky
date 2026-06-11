package capture

import (
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dburman/nightsky/internal/config"
	"github.com/dburman/nightsky/internal/raw"
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

func writeDark(t *testing.T, dir string, key DarkFrameKey, v uint8) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, key.filename()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, uniformFrame(v)); err != nil {
		t.Fatal(err)
	}
}

func darkTestManager(dir string) *DarkFrameManager {
	return &DarkFrameManager{
		Dir:    dir,
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func TestParseDarkFilename(t *testing.T) {
	key := DarkFrameKey{Exposure: 8000 * time.Millisecond, Gain: 200, Binning: 2}
	got, ok := parseDarkFilename(key.filename())
	if !ok {
		t.Fatalf("failed to parse %q", key.filename())
	}
	if got != key {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, key)
	}
	if _, ok := parseDarkFilename("allsky-20260315.png"); ok {
		t.Error("non-dark filename parsed as a dark")
	}
}

// SelectDark must pick the nearest available dark in exposure×gain space and
// match binning exactly.
func TestSelectDark_NearestMatch(t *testing.T) {
	dir := t.TempDir()
	writeDark(t, dir, DarkFrameKey{Exposure: 2000 * time.Millisecond, Gain: 200, Binning: 1}, 5)
	writeDark(t, dir, DarkFrameKey{Exposure: 8000 * time.Millisecond, Gain: 200, Binning: 1}, 20)
	dm := darkTestManager(dir)

	// Actual frame at 7s/gain200 is closest to the 8s dark (value 20).
	got := dm.SelectDark(DarkFrameKey{Exposure: 7000 * time.Millisecond, Gain: 200, Binning: 1})
	if got == nil {
		t.Fatal("expected a dark within tolerance")
	}
	if r, _, _, _ := got.At(0, 0).RGBA(); uint8(r>>8) != 20 {
		t.Errorf("selected wrong dark: pixel %d, want 20 (the 8s dark)", uint8(r>>8))
	}
}

// No dark within one stop, and binning mismatch, must both yield nil.
func TestSelectDark_OutOfToleranceAndBinning(t *testing.T) {
	dir := t.TempDir()
	writeDark(t, dir, DarkFrameKey{Exposure: 1000 * time.Millisecond, Gain: 100, Binning: 1}, 5)
	dm := darkTestManager(dir)

	// 15.5s/gain400 is many stops from the only 1s/gain100 dark.
	if got := dm.SelectDark(DarkFrameKey{Exposure: 15500 * time.Millisecond, Gain: 400, Binning: 1}); got != nil {
		t.Error("expected nil when no dark is within tolerance")
	}
	// Exact level but wrong binning.
	if got := dm.SelectDark(DarkFrameKey{Exposure: 1000 * time.Millisecond, Gain: 100, Binning: 2}); got != nil {
		t.Error("expected nil when only a different-binning dark exists")
	}
}

func TestDarkGrid_FixedExposureSinglePoint(t *testing.T) {
	m := config.ModeConfig{Exposure: time.Second, Gain: 100, Binning: 1, AutoExposure: false}
	grid := DarkGrid(m)
	if len(grid) != 1 || grid[0].Exposure != time.Second || grid[0].Gain != 100 {
		t.Fatalf("fixed-exposure grid = %+v, want single base point", grid)
	}
}

func TestDarkGrid_AutoExposureLShape(t *testing.T) {
	m := config.ModeConfig{
		Exposure:     4 * time.Second,
		MaxExposure:  16 * time.Second,
		Gain:         2,
		MaxGain:      8,
		Binning:      1,
		AutoExposure: true,
	}
	grid := DarkGrid(m)

	// Exposure ramp at base gain: 4,8,16 s @ gain 2.
	// Gain ramp at max exposure: 2,4,8 @ 16 s. (16s@gain2 shared.)
	wantExpRamp := []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second}
	for _, e := range wantExpRamp {
		if !containsKey(grid, DarkFrameKey{Exposure: e, Gain: 2, Binning: 1}) {
			t.Errorf("missing exposure-ramp point %v@gain2", e)
		}
	}
	for _, g := range []float64{2, 4, 8} {
		if !containsKey(grid, DarkFrameKey{Exposure: 16 * time.Second, Gain: g, Binning: 1}) {
			t.Errorf("missing gain-ramp point 16s@gain%v", g)
		}
	}

	// No duplicates.
	seen := map[DarkFrameKey]bool{}
	for _, k := range grid {
		if seen[k] {
			t.Errorf("duplicate grid point %+v", k)
		}
		seen[k] = true
	}
}

func containsKey(grid []DarkFrameKey, k DarkFrameKey) bool {
	for _, g := range grid {
		if g == k {
			return true
		}
	}
	return false
}

// gradientFrame returns a 16x12 image whose channels vary per pixel, so the
// fast .Pix path is exercised on non-uniform data.
func gradientFrame(off uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 16, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(x*4) + off,
				G: uint8(y*8) + off,
				B: uint8(x+y) + off,
				A: 255,
			})
		}
	}
	return img
}

// SubtractDark over packed buffers must match a straightforward At()-based
// reference, including clamping at zero.
func TestSubtractDark_MatchesReference(t *testing.T) {
	light := gradientFrame(40)
	dark := gradientFrame(10)

	got := SubtractDark(light, dark)

	b := light.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			lr, lg, lb, _ := light.At(x, y).RGBA()
			dr, dg, db, _ := dark.At(x, y).RGBA()
			wantR := clampSub8(uint8(lr>>8), uint8(dr>>8))
			wantG := clampSub8(uint8(lg>>8), uint8(dg>>8))
			wantB := clampSub8(uint8(lb>>8), uint8(db>>8))
			gr, gg, gb, _ := got.At(x, y).RGBA()
			if uint8(gr>>8) != wantR || uint8(gg>>8) != wantG || uint8(gb>>8) != wantB {
				t.Fatalf("pixel (%d,%d): got (%d,%d,%d), want (%d,%d,%d)",
					x, y, uint8(gr>>8), uint8(gg>>8), uint8(gb>>8), wantR, wantG, wantB)
			}
		}
	}
}

func TestSubtractDark_DimensionMismatchSkips(t *testing.T) {
	light := gradientFrame(0)
	dark := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if got := SubtractDark(light, dark); got != image.Image(light) {
		t.Error("expected original light frame returned on dimension mismatch")
	}
}

func writeRawDark(t *testing.T, dir string, key DarkFrameKey, v uint16) {
	t.Helper()
	im := &raw.Image{Width: 4, Height: 4, Pix: make([]uint16, 16)}
	for i := range im.Pix {
		im.Pix[i] = v
	}
	f, err := os.Create(filepath.Join(dir, key.rawFilename()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, im.ToGray16()); err != nil {
		t.Fatal(err)
	}
}

// SelectRawDark must pick the nearest raw master by the same rules as
// SelectDark, and ignore the RGB dark files (and vice versa).
func TestSelectRawDark_NearestAndIsolated(t *testing.T) {
	dir := t.TempDir()
	writeRawDark(t, dir, DarkFrameKey{Exposure: 2000 * time.Millisecond, Gain: 200, Binning: 1}, 7)
	writeRawDark(t, dir, DarkFrameKey{Exposure: 8000 * time.Millisecond, Gain: 200, Binning: 1}, 42)
	// An RGB dark at the exact target settings must NOT shadow the raw masters.
	writeDark(t, dir, DarkFrameKey{Exposure: 7000 * time.Millisecond, Gain: 200, Binning: 1}, 99)
	dm := darkTestManager(dir)

	got := dm.SelectRawDark(DarkFrameKey{Exposure: 7000 * time.Millisecond, Gain: 200, Binning: 1})
	if got == nil {
		t.Fatal("expected a raw master within tolerance")
	}
	if got.Pix[0] != 42 {
		t.Errorf("selected wrong raw master: Pix[0] = %d, want 42 (the 8s master)", got.Pix[0])
	}

	// And the RGB selector must not pick up darkraw_ files.
	rgb := dm.SelectDark(DarkFrameKey{Exposure: 7000 * time.Millisecond, Gain: 200, Binning: 1})
	if rgb == nil {
		t.Fatal("expected the RGB dark")
	}
	if r, _, _, _ := rgb.At(0, 0).RGBA(); uint8(r>>8) != 99 {
		t.Errorf("RGB selector picked wrong file: %d, want 99", uint8(r>>8))
	}
}

func TestSelectRawDark_NoneAvailable(t *testing.T) {
	dm := darkTestManager(t.TempDir())
	if got := dm.SelectRawDark(DarkFrameKey{Exposure: time.Second, Gain: 100, Binning: 1}); got != nil {
		t.Error("expected nil with no raw masters")
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
