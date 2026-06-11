package capture

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/config"
	imgutil "github.com/dburman/nightsky/internal/image"
)

// DarkFrameManager handles capture, storage, and subtraction of dark frames.
// Dark frames are captured with the lens cap on and subtracted from light frames
// to remove fixed-pattern noise and hot pixels.
type DarkFrameManager struct {
	// Directory where dark frames are stored.
	Dir string
	// Count is the number of dark frames to average when capturing.
	Count int

	cam    camera.Camera
	logger *slog.Logger

	// cache holds decoded master darks by filename so per-frame selection
	// doesn't re-read and re-decode from disk every capture.
	cache        map[string]image.Image
	lastSelected string // filename of the most recently selected dark, for log de-dup
}

// NewDarkFrameManager creates a new dark frame manager.
func NewDarkFrameManager(dir string, count int, cam camera.Camera, logger *slog.Logger) *DarkFrameManager {
	return &DarkFrameManager{
		Dir:    dir,
		Count:  count,
		cam:    cam,
		logger: logger,
	}
}

// DarkGrid returns the set of (exposure, gain, binning) points to capture
// master darks for, covering the trajectory the auto-exposure controller
// actually follows in this mode. The controller maximizes exposure before
// raising gain (see exposure.go decompose), so the grid is L-shaped: an
// exposure ramp (doubling from the base up to max) at the base gain, then a
// gain ramp (doubling from base to max) at the max exposure. This is far
// cheaper than a full cross product while still landing a dark within ~one
// stop of any frame SelectDark will see.
//
// For fixed-exposure modes (auto_exposure off) it returns the single base
// point. Points are deduplicated.
func DarkGrid(m config.ModeConfig) []DarkFrameKey {
	binning := m.Binning
	if binning == 0 {
		binning = 1
	}

	base := DarkFrameKey{Exposure: m.Exposure, Gain: m.Gain, Binning: binning}
	if !m.AutoExposure {
		return []DarkFrameKey{base}
	}

	maxExp := m.MaxExposure
	if maxExp < m.Exposure {
		maxExp = m.Exposure
	}
	maxGain := m.MaxGain
	if maxGain < m.Gain {
		maxGain = m.Gain
	}

	seen := map[DarkFrameKey]bool{}
	var grid []DarkFrameKey
	add := func(k DarkFrameKey) {
		if k.Binning == 0 {
			k.Binning = 1
		}
		if !seen[k] {
			seen[k] = true
			grid = append(grid, k)
		}
	}

	// Exposure ramp at base gain: base, 2x, 4x, ... up to max.
	for e := m.Exposure; e < maxExp; e *= 2 {
		add(DarkFrameKey{Exposure: e, Gain: m.Gain, Binning: binning})
	}
	add(DarkFrameKey{Exposure: maxExp, Gain: m.Gain, Binning: binning})

	// Gain ramp at max exposure: base, 2x, 4x, ... up to max.
	for g := m.Gain; g < maxGain; g *= 2 {
		if g < 1 {
			g = 1
		}
		add(DarkFrameKey{Exposure: maxExp, Gain: g, Binning: binning})
	}
	add(DarkFrameKey{Exposure: maxExp, Gain: maxGain, Binning: binning})

	return grid
}

// DarkFrameKey uniquely identifies a dark frame by its capture parameters.
type DarkFrameKey struct {
	Exposure time.Duration
	Gain     float64
	Binning  int
}

func (k DarkFrameKey) filename() string {
	return fmt.Sprintf("dark_%dms_gain%.0f_bin%d.png",
		k.Exposure.Milliseconds(), k.Gain, k.Binning)
}

// CaptureDarks captures and averages multiple dark frames, saving the result.
// The camera lens must be covered before calling this.
func (dm *DarkFrameManager) CaptureDarks(ctx context.Context, settings camera.CaptureSettings) error {
	if err := os.MkdirAll(dm.Dir, 0755); err != nil {
		return fmt.Errorf("create dark dir: %w", err)
	}

	key := DarkFrameKey{
		Exposure: settings.Exposure,
		Gain:     settings.Gain,
		Binning:  settings.Binning,
	}

	dm.logger.Info("capturing dark frames",
		"count", dm.Count,
		"exposure", settings.Exposure,
		"gain", settings.Gain,
	)

	var frames []image.Image

	for i := 0; i < dm.Count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dm.logger.Info("dark frame", "frame", i+1, "of", dm.Count)

		result, err := dm.cam.Capture(ctx, settings)
		if err != nil {
			return fmt.Errorf("capture dark frame %d: %w", i+1, err)
		}
		frames = append(frames, result.Image)

		if len(result.DNGData) > 0 {
			dngName := fmt.Sprintf("dark_%dms_gain%.0f_bin%d_%04d.dng",
				key.Exposure.Milliseconds(), key.Gain, key.Binning, i+1)
			dngPath := filepath.Join(dm.Dir, dngName)
			if err := os.WriteFile(dngPath, result.DNGData, 0644); err != nil {
				dm.logger.Warn("failed to save dark DNG", "path", dngPath, "error", err)
			} else {
				dm.logger.Info("dark DNG saved", "path", dngPath)
			}
		}
	}

	// Stack all dark frames into the master dark.
	master := stackFrames(frames)

	// Save the master dark frame.
	path := filepath.Join(dm.Dir, key.filename())
	data, err := imgutil.EncodeImage(master, "png", 95)
	if err != nil {
		return fmt.Errorf("encode dark frame: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("save dark frame: %w", err)
	}

	dm.logger.Info("dark frame saved", "path", path)
	return nil
}

// LoadDark loads a previously captured dark frame matching the given parameters
// exactly. Returns nil if no matching dark frame exists.
func (dm *DarkFrameManager) LoadDark(key DarkFrameKey) image.Image {
	return dm.load(key.filename())
}

// darkMatchTolerance is the maximum exposure-level distance (in stops, where
// level = log2(exposureUs × gain)) between a frame's actual settings and an
// available dark for that dark to be considered a usable match. One stop keeps
// dark-current scaling error small while tolerating the auto-exposure grid.
const darkMatchTolerance = 1.0

// SelectDark returns the available master dark whose capture settings are
// closest to the actual settings of the frame being calibrated. Binning must
// match exactly; exposure and gain are matched in log2(exposureUs × gain)
// space, since dark current scales with both. Returns nil (and logs once) when
// no dark is within darkMatchTolerance, so a mismatched dark is never
// subtracted silently.
func (dm *DarkFrameManager) SelectDark(actual DarkFrameKey) image.Image {
	entries, err := os.ReadDir(dm.Dir)
	if err != nil {
		return nil
	}

	want := exposureLevelOf(actual)
	var bestKey DarkFrameKey
	var bestFile string
	bestDist := -1.0

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		key, ok := parseDarkFilename(e.Name())
		if !ok || key.Binning != actual.Binning {
			continue
		}
		dist := math.Abs(exposureLevelOf(key) - want)
		if bestDist < 0 || dist < bestDist {
			bestDist, bestKey, bestFile = dist, key, e.Name()
		}
	}

	if bestFile == "" || bestDist > darkMatchTolerance {
		if dm.lastSelected != "" {
			dm.logger.Warn("no dark frame within tolerance for current settings; skipping subtraction",
				"exposure", actual.Exposure, "gain", actual.Gain, "binning", actual.Binning)
			dm.lastSelected = ""
		}
		return nil
	}

	if bestFile != dm.lastSelected {
		dm.logger.Info("selected dark frame",
			"file", bestFile,
			"dark_exposure", bestKey.Exposure, "dark_gain", bestKey.Gain,
			"frame_exposure", actual.Exposure, "frame_gain", actual.Gain,
			"distance_stops", bestDist)
		dm.lastSelected = bestFile
	}
	return dm.load(bestFile)
}

// load reads and decodes a master dark by filename, caching the result.
func (dm *DarkFrameManager) load(name string) image.Image {
	if dm.cache == nil {
		dm.cache = make(map[string]image.Image)
	}
	if img, ok := dm.cache[name]; ok {
		return img
	}

	f, err := os.Open(filepath.Join(dm.Dir, name))
	if err != nil {
		return nil
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		dm.logger.Warn("failed to decode dark frame", "file", name, "error", err)
		return nil
	}
	dm.cache[name] = img
	return img
}

// exposureLevelOf returns log2(exposureUs × gain) for a dark key, the unified
// scale used to match darks to frames. Inputs are floored to 1 to stay finite.
func exposureLevelOf(k DarkFrameKey) float64 {
	us := float64(k.Exposure.Microseconds())
	if us < 1 {
		us = 1
	}
	g := k.Gain
	if g < 1 {
		g = 1
	}
	return math.Log2(us * g)
}

// parseDarkFilename reverses DarkFrameKey.filename, recovering the key from a
// "dark_<ms>ms_gain<g>_bin<n>.png" name. ok is false for non-matching names.
func parseDarkFilename(name string) (DarkFrameKey, bool) {
	var ms, gain, bin int
	if n, err := fmt.Sscanf(name, "dark_%dms_gain%d_bin%d.png", &ms, &gain, &bin); n != 3 || err != nil {
		return DarkFrameKey{}, false
	}
	return DarkFrameKey{
		Exposure: time.Duration(ms) * time.Millisecond,
		Gain:     float64(gain),
		Binning:  bin,
	}, true
}

// SubtractDark subtracts a dark frame from a light frame pixel by pixel.
// Values are clamped to 0 (no negative values).
func SubtractDark(light, dark image.Image) image.Image {
	bounds := light.Bounds()
	darkBounds := dark.Bounds()

	// Ensure same dimensions.
	if bounds.Dx() != darkBounds.Dx() || bounds.Dy() != darkBounds.Dy() {
		return light // dimensions don't match, skip subtraction
	}

	result := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			lr, lg, lb, la := light.At(x, y).RGBA()
			dr, dg, db, _ := dark.At(x, y).RGBA()

			// Subtract with clamping (values are 16-bit, shift to 8-bit).
			r := clampSub(lr>>8, dr>>8)
			g := clampSub(lg>>8, dg>>8)
			b := clampSub(lb>>8, db>>8)

			result.SetRGBA(x, y, color.RGBA{
				R: uint8(r),
				G: uint8(g),
				B: uint8(b),
				A: uint8(la >> 8),
			})
		}
	}

	return result
}

func clampSub(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return 0
}

// stackFrames combines dark frames into a master dark. With three or more
// frames a per-pixel median is used: a mean lets a single bright outlier
// (satellite, plane, cosmic-ray hit) in any one dark contaminate the master,
// while a median rejects it. Fewer than three frames fall back to a mean.
func stackFrames(frames []image.Image) image.Image {
	if len(frames) >= 3 {
		return medianFrames(frames)
	}
	return averageFrames(frames)
}

// medianFrames computes the pixel-wise per-channel median of multiple frames.
func medianFrames(frames []image.Image) image.Image {
	bounds := frames[0].Bounds()
	n := len(frames)

	rs := make([]uint8, n)
	gs := make([]uint8, n)
	bs := make([]uint8, n)

	result := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			for i, f := range frames {
				r, g, b, _ := f.At(x, y).RGBA()
				rs[i] = uint8(r >> 8)
				gs[i] = uint8(g >> 8)
				bs[i] = uint8(b >> 8)
			}
			result.SetRGBA(x, y, color.RGBA{
				R: medianU8(rs),
				G: medianU8(gs),
				B: medianU8(bs),
				A: 255,
			})
		}
	}
	return result
}

// medianU8 returns the median of vals, sorting in place.
func medianU8(vals []uint8) uint8 {
	slices.Sort(vals)
	return vals[len(vals)/2]
}

// averageFrames computes the pixel-wise average of multiple frames.
func averageFrames(frames []image.Image) image.Image {
	if len(frames) == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if len(frames) == 1 {
		return frames[0]
	}

	bounds := frames[0].Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// Accumulate in 32-bit to avoid overflow.
	accumR := make([]uint32, w*h)
	accumG := make([]uint32, w*h)
	accumB := make([]uint32, w*h)

	for _, frame := range frames {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, b, _ := frame.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
				idx := y*w + x
				accumR[idx] += r >> 8
				accumG[idx] += g >> 8
				accumB[idx] += b >> 8
			}
		}
	}

	n := uint32(len(frames))
	result := image.NewRGBA(bounds)
	draw.Draw(result, bounds, image.Transparent, image.Point{}, draw.Src)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			result.SetRGBA(bounds.Min.X+x, bounds.Min.Y+y, color.RGBA{
				R: uint8(accumR[idx] / n),
				G: uint8(accumG[idx] / n),
				B: uint8(accumB[idx] / n),
				A: 255,
			})
		}
	}

	return result
}
