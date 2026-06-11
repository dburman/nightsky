package capture

import (
	"context"
	"fmt"
	"image"
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

// SubtractDark subtracts a dark frame from a light frame pixel by pixel,
// clamping at 0. Both inputs are converted to *image.RGBA once and the
// subtraction runs over the packed pixel buffers, avoiding millions of
// per-pixel interface calls on a full-resolution frame.
func SubtractDark(light, dark image.Image) image.Image {
	bounds := light.Bounds()
	if bounds.Dx() != dark.Bounds().Dx() || bounds.Dy() != dark.Bounds().Dy() {
		return light // dimensions don't match, skip subtraction
	}

	l := imgutil.ToRGBA(light)
	d := imgutil.ToRGBA(dark)
	result := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))

	lp, dp, rp := l.Pix, d.Pix, result.Pix
	n := len(rp)
	for i := 0; i < n; i += 4 {
		rp[i] = clampSub8(lp[i], dp[i])
		rp[i+1] = clampSub8(lp[i+1], dp[i+1])
		rp[i+2] = clampSub8(lp[i+2], dp[i+2])
		rp[i+3] = lp[i+3]
	}
	return result
}

func clampSub8(a, b uint8) uint8 {
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

// medianFrames computes the pixel-wise per-channel median of multiple frames,
// operating over packed RGBA buffers.
func medianFrames(frames []image.Image) image.Image {
	n := len(frames)
	pix := make([][]uint8, n)
	for i, f := range frames {
		pix[i] = imgutil.ToRGBA(f).Pix
	}

	result := image.NewRGBA(image.Rect(0, 0, frames[0].Bounds().Dx(), frames[0].Bounds().Dy()))
	rp := result.Pix
	samples := make([]uint8, n)

	for i := 0; i < len(rp); i += 4 {
		for c := 0; c < 3; c++ {
			for f := 0; f < n; f++ {
				samples[f] = pix[f][i+c]
			}
			rp[i+c] = medianU8(samples)
		}
		rp[i+3] = 255
	}
	return result
}

// medianU8 returns the median of vals, sorting in place.
func medianU8(vals []uint8) uint8 {
	slices.Sort(vals)
	return vals[len(vals)/2]
}

// averageFrames computes the pixel-wise average of multiple frames over packed
// RGBA buffers, accumulating per channel in 32-bit to avoid overflow.
func averageFrames(frames []image.Image) image.Image {
	if len(frames) == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if len(frames) == 1 {
		return frames[0]
	}

	w := frames[0].Bounds().Dx()
	h := frames[0].Bounds().Dy()
	size := w * h * 4

	accum := make([]uint32, size)
	for _, frame := range frames {
		p := imgutil.ToRGBA(frame).Pix
		for i := 0; i < size; i++ {
			accum[i] += uint32(p[i])
		}
	}

	n := uint32(len(frames))
	result := image.NewRGBA(image.Rect(0, 0, w, h))
	rp := result.Pix
	for i := 0; i < size; i += 4 {
		rp[i] = uint8(accum[i] / n)
		rp[i+1] = uint8(accum[i+1] / n)
		rp[i+2] = uint8(accum[i+2] / n)
		rp[i+3] = 255
	}
	return result
}
