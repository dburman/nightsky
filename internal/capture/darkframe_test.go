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

func TestMedianU8(t *testing.T) {
	if got := medianU8([]uint8{255, 10, 10}); got != 10 {
		t.Errorf("median of {255,10,10} = %d, want 10", got)
	}
	if got := medianU8([]uint8{1, 2, 3, 4, 5}); got != 3 {
		t.Errorf("median of 1..5 = %d, want 3", got)
	}
}
