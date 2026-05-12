package capture

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dburman/nightsky/internal/astro"
	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/config"
	imgutil "github.com/dburman/nightsky/internal/image"
)

// Mode represents the current capture mode.
type Mode int

const (
	ModeDay Mode = iota
	ModeNight
)

func (m Mode) String() string {
	if m == ModeDay {
		return "day"
	}
	return "night"
}

// Loop is the main capture orchestrator. It runs continuously, switching between
// day and night modes based on sun position, capturing images, applying overlays,
// and saving them to disk.
type Loop struct {
	cam     camera.Camera
	cfg     *config.Config
	logger  *slog.Logger

	// State.
	mode         Mode
	exposureCtrl *ExposureController
	darkMgr      *DarkFrameManager
	currentDark  image.Image
	frameCount   int64
	skipRemaining int

	// Callbacks for upload integration.
	OnImageSaved func(path string, meta camera.CaptureMeta)
	OnNightEnd   func(dateDir string)
}

// NewLoop creates a new capture loop.
func NewLoop(cam camera.Camera, cfg *config.Config, logger *slog.Logger) *Loop {
	l := &Loop{
		cam:    cam,
		cfg:    cfg,
		logger: logger,
	}

	// Initialize dark frame manager if enabled.
	if cfg.Dark.Enabled {
		l.darkMgr = NewDarkFrameManager(cfg.Dark.Directory, cfg.Dark.Count, cam, logger)
	}

	return l
}

// Run starts the capture loop. It blocks until the context is cancelled.
func (l *Loop) Run(ctx context.Context) error {
	l.logger.Info("starting capture loop",
		"camera", l.cam.Info().Name,
		"lat", l.cfg.Location.Latitude,
		"lon", l.cfg.Location.Longitude,
		"angle", l.cfg.Location.Angle,
	)

	// Determine initial mode.
	l.mode = l.currentMode()
	l.logger.Info("initial mode", "mode", l.mode)
	l.initMode()

	lastMode := l.mode
	var lastDateDir string

	for {
		select {
		case <-ctx.Done():
			l.logger.Info("capture loop stopping")
			return ctx.Err()
		default:
		}

		// Check for mode transition.
		newMode := l.currentMode()
		if newMode != l.mode {
			l.logger.Info("mode transition", "from", l.mode, "to", newMode)

			// End-of-night processing.
			if l.mode == ModeNight && newMode == ModeDay {
				if lastDateDir != "" && l.OnNightEnd != nil {
					l.OnNightEnd(lastDateDir)
				}
			}

			lastMode = l.mode
			l.mode = newMode
			l.initMode()
			_ = lastMode
		}

		// Get mode settings.
		modeCfg := l.modeConfig()

		// Build capture settings.
		settings := camera.CaptureSettings{
			Exposure: l.exposureCtrl.Exposure,
			Gain:     l.exposureCtrl.Gain,
			Binning:  modeCfg.Binning,
			WBRed:    modeCfg.WBRed,
			WBBlue:   modeCfg.WBBlue,
			AWB:      modeCfg.AWB,
			Flip:     l.cfg.Camera.Flip,
			Format:   camera.FormatRGB24,
			Denoise:  modeCfg.Denoise,
		}

		// Apply cooler settings if applicable.
		if modeCfg.CoolerEnabled {
			l.cam.SetCooler(camera.CoolerSettings{
				Enabled:    true,
				TargetTemp: modeCfg.CoolerTarget,
			})
		}

		// Capture frame.
		captureCtx, cancel := context.WithTimeout(ctx, settings.Exposure+60*time.Second)
		result, err := l.cam.Capture(captureCtx, settings)
		cancel()

		if err != nil {
			l.logger.Error("capture failed", "error", err)
			time.Sleep(time.Second)
			continue
		}

		l.frameCount++

		// Skip frames after mode transition.
		if l.skipRemaining > 0 {
			l.skipRemaining--
			l.logger.Debug("skipping frame", "remaining", l.skipRemaining)
			if modeCfg.AutoExposure {
				l.exposureCtrl.Adjust(result.Meta.MeanBrightness)
			}
			continue
		}

		// Dark frame subtraction.
		processedImg := result.Image
		if l.currentDark != nil {
			processedImg = SubtractDark(processedImg, l.currentDark)
		}

		// Apply overlay.
		overlayCfg := imgutil.DefaultOverlayConfig()
		overlayCfg.Enabled = l.cfg.Output.Overlay
		overlayCfg.FontSize = l.cfg.Output.OverlayFontSize
		processedImg = imgutil.ApplyOverlay(processedImg, result.Meta, overlayCfg)

		// Save image.
		dateDir := time.Now().Format("2006-01-02")
		outputDir := filepath.Join(l.cfg.Output.Directory, dateDir)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			l.logger.Error("create output dir", "error", err)
			continue
		}
		lastDateDir = outputDir

		filename := fmt.Sprintf("%s-%s.%s",
			l.cfg.Output.FilenamePrefix,
			result.Meta.Timestamp.Format("20060102150405"),
			modeCfg.ImageType,
		)
		outputPath := filepath.Join(outputDir, filename)

		data, err := imgutil.EncodeImage(processedImg, modeCfg.ImageType, modeCfg.Quality)
		if err != nil {
			l.logger.Error("encode image", "error", err)
			continue
		}

		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			l.logger.Error("save image", "error", err)
			continue
		}

		l.logger.Info("saved",
			"path", outputPath,
			"size_kb", len(data)/1024,
			"frame", l.frameCount,
		)

		// Notify upload handler.
		if l.OnImageSaved != nil {
			l.OnImageSaved(outputPath, result.Meta)
		}

		// Auto-exposure adjustment.
		if modeCfg.AutoExposure {
			l.exposureCtrl.Adjust(result.Meta.MeanBrightness)
		}

		// Delay between captures.
		if modeCfg.Delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(modeCfg.Delay):
			}
		}
	}
}

// currentMode determines whether it's day or night based on sun position.
func (l *Loop) currentMode() Mode {
	if astro.IsNight(time.Now(), l.cfg.Location.Latitude, l.cfg.Location.Longitude, l.cfg.Location.Angle) {
		return ModeNight
	}
	return ModeDay
}

// modeConfig returns the configuration for the current mode.
func (l *Loop) modeConfig() config.ModeConfig {
	if l.mode == ModeDay {
		return l.cfg.Day
	}
	return l.cfg.Night
}

// initMode initializes state for a new mode.
func (l *Loop) initMode() {
	modeCfg := l.modeConfig()

	// Initialize auto-exposure controller.
	minExposure := 100 * time.Microsecond
	l.exposureCtrl = NewExposureController(
		modeCfg.TargetBrightness,
		modeCfg.Exposure,
		modeCfg.Gain,
		minExposure,
		modeCfg.MaxExposure,
		0,
		modeCfg.MaxGain,
		l.logger,
	)

	l.skipRemaining = modeCfg.SkipFrames

	// Load dark frame for this mode's settings.
	if l.darkMgr != nil {
		key := DarkFrameKey{
			Exposure: modeCfg.Exposure,
			Gain:     modeCfg.Gain,
			Binning:  modeCfg.Binning,
		}
		l.currentDark = l.darkMgr.LoadDark(key)
		if l.currentDark != nil {
			l.logger.Info("loaded dark frame for mode", "mode", l.mode)
		}
	}
}

// CleanOldData removes output directories older than DaysToKeep.
func CleanOldData(outputDir string, daysToKeep int, logger *slog.Logger) error {
	if daysToKeep <= 0 {
		return nil
	}

	cutoff := time.Now().AddDate(0, 0, -daysToKeep)

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Parse directory name as date.
		dirDate, err := time.Parse("2006-01-02", entry.Name())
		if err != nil {
			continue // not a date directory
		}

		if dirDate.Before(cutoff) {
			dirPath := filepath.Join(outputDir, entry.Name())
			logger.Info("removing old data", "dir", dirPath, "age_days", int(time.Since(dirDate).Hours()/24))
			if err := os.RemoveAll(dirPath); err != nil {
				logger.Error("failed to remove old data", "dir", dirPath, "error", err)
			}
		}
	}

	return nil
}

// ListDateDirs returns all date directories in the output directory, sorted newest first.
func ListDateDirs(outputDir string) ([]string, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, err
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := time.Parse("2006-01-02", entry.Name()); err == nil {
			dirs = append(dirs, filepath.Join(outputDir, entry.Name()))
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	return dirs, nil
}
