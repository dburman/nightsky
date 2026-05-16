package capture

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dburman/nightsky/internal/astro"
	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/cloud"
	"github.com/dburman/nightsky/internal/config"
	"github.com/dburman/nightsky/internal/flat"
	imgutil "github.com/dburman/nightsky/internal/image"
	"github.com/dburman/nightsky/internal/metrics"
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
	mode            Mode
	nightSessionDir string // set when night begins, cleared at dawn
	exposureCtrl    *ExposureController
	darkMgr         *DarkFrameManager
	currentDark     image.Image
	flatMgr         *flat.Manager
	frameCount      int64
	skipRemaining   int
	cloudMetrics    []cloud.Metric // accumulated per-frame cloud metrics for the current night

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

	// Initialize flat field manager if enabled.
	if cfg.Flat.Enabled {
		l.flatMgr = flat.NewManager(cfg.Flat.Directory, cfg.Flat.Count, cam, logger)
		if err := l.flatMgr.Load(); err != nil {
			logger.Warn("flat frame load failed", "error", err)
		}
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

			// End-of-night processing: fire before clearing nightSessionDir.
			if l.mode == ModeNight && newMode == ModeDay {
				nightDir := filepath.Join(l.cfg.Output.Directory, l.nightSessionDir)
				if l.nightSessionDir != "" {
					// Flush cloud metrics before handing off to OnNightEnd.
					if len(l.cloudMetrics) > 0 {
						if err := cloud.WriteReport(nightDir, l.cloudMetrics); err != nil {
							l.logger.Error("cloud report write failed", "error", err)
						} else {
							l.logger.Info("cloud coverage report written",
								"dir", nightDir,
								"frames", len(l.cloudMetrics),
								"summary", cloud.SummaryLine(l.cloudMetrics),
							)
						}
						l.cloudMetrics = nil
					}
					if l.OnNightEnd != nil {
						l.OnNightEnd(nightDir)
					}
				}
				l.nightSessionDir = ""
			}

			l.mode = newMode
			l.initMode()
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

		// Capture frame. Grace period covers rpicam-still startup (3-10s on a Pi),
		// the shutter open time, and image encoding before the context fires.
		captureCtx, cancel := context.WithTimeout(ctx, settings.Exposure+120*time.Second)
		result, err := l.cam.Capture(captureCtx, settings)
		cancel()

		if err != nil {
			l.logger.Error("capture failed", "error", err)
			time.Sleep(time.Second)
			continue
		}

		l.frameCount++

		// Compute metered brightness for auto-exposure using the configured zone.
		meteredMean := ZoneMean(result.Image, modeCfg.MeteringZone)

		// Skip frames after mode transition.
		if l.skipRemaining > 0 {
			l.skipRemaining--
			l.logger.Debug("skipping frame", "remaining", l.skipRemaining)
			if modeCfg.AutoExposure {
				l.exposureCtrl.Adjust(meteredMean)
			}
			continue
		}

		// Dark frame subtraction.
		processedImg := result.Image
		if l.currentDark != nil {
			processedImg = SubtractDark(processedImg, l.currentDark)
		}

		// Flat field correction.
		if l.flatMgr != nil && l.flatMgr.Ready() {
			processedImg = l.flatMgr.Apply(processedImg)
		}

		// Histogram stretch.
		if l.cfg.Output.Stretch.Enabled {
			sc := l.cfg.Output.Stretch
			processedImg = imgutil.Stretch(processedImg, sc.Mode, sc.BlackPoint, sc.WhitePoint, sc.AutoBlackPercentile, sc.AutoWhitePercentile)
		}

		// Cloud coverage metric — night frames only.
		if l.mode == ModeNight {
			m := cloud.Estimate(processedImg, result.Meta.Timestamp)
			l.cloudMetrics = append(l.cloudMetrics, m)
		}

		// Apply overlay.
		overlayCfg := imgutil.DefaultOverlayConfig()
		overlayCfg.Enabled = l.cfg.Output.Overlay
		overlayCfg.FontSize = l.cfg.Output.OverlayFontSize
		processedImg = imgutil.ApplyOverlay(processedImg, result.Meta, overlayCfg)

		// Save image. Night images all go into the folder named after the
		// night's start date so midnight crossings don't split the dataset.
		dateLabel := time.Now().Format("2006-01-02")
		if l.mode == ModeNight {
			if l.nightSessionDir == "" {
				l.nightSessionDir = dateLabel
			}
			dateLabel = l.nightSessionDir
		}
		outputDir := filepath.Join(l.cfg.Output.Directory, dateLabel)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			l.logger.Error("create output dir", "error", err)
			continue
		}

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

		// Generate thumbnail from the already-decoded image so the web UI
		// serves pre-built thumbnails without re-decoding from disk.
		go func(img image.Image, path string) {
			if err := imgutil.CacheThumb(img, path); err != nil {
				l.logger.Debug("thumbnail generation failed", "error", err)
			}
		}(processedImg, outputPath)

		// Notify upload handler.
		if l.OnImageSaved != nil {
			l.OnImageSaved(outputPath, result.Meta)
		}

		// Auto-exposure adjustment using the metered zone mean.
		if modeCfg.AutoExposure {
			l.exposureCtrl.Adjust(meteredMean)
		}

		// Write live metrics for the web server's /api/metrics endpoint.
		cloudCov := 0.0
		if len(l.cloudMetrics) > 0 {
			cloudCov = l.cloudMetrics[len(l.cloudMetrics)-1].Coverage
		}
		snap := metrics.Snapshot{
			Mode:           l.mode.String(),
			ExposureNs:     l.exposureCtrl.Exposure.Nanoseconds(),
			Exposure:       imgutil.FormatExposure(l.exposureCtrl.Exposure),
			Gain:           l.exposureCtrl.Gain,
			FrameCount:     l.frameCount,
			MeanBrightness: meteredMean,
			CloudCoverage:  cloudCov,
			SensorTempC:    result.Meta.Temperature,
			LastCapture:    result.Meta.Timestamp,
		}
		if err := metrics.Write(l.cfg.Output.Directory, snap); err != nil {
			l.logger.Debug("metrics write failed", "error", err)
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
	if l.mode == ModeNight && l.nightSessionDir == "" {
		l.nightSessionDir = time.Now().Format("2006-01-02")
	}

	modeCfg := l.modeConfig()

	// Holy Grail: seed the new controller with the current exposure and gain
	// rather than the configured initial values. This prevents a hard jump at
	// the day↔night transition — the auto-exposure algorithm ramps naturally
	// from wherever it was to the new mode's target brightness.
	initExposure := modeCfg.Exposure
	initGain := modeCfg.Gain
	if l.exposureCtrl != nil {
		initExposure = l.exposureCtrl.Exposure
		initGain = l.exposureCtrl.Gain
	}

	// Initialize auto-exposure controller.
	minExposure := 100 * time.Microsecond
	l.exposureCtrl = NewExposureController(
		modeCfg.TargetBrightness,
		initExposure,
		initGain,
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

// PruneRawImages removes raw captured image files from night directories older
// than afterDays, while keeping synthesized outputs (timelapse, keogram, star
// trails, WB analysis report). The .thumbs cache is also removed since its
// source images will be gone. Today's directory is never touched.
func PruneRawImages(outputDir string, afterDays int, logger *slog.Logger) error {
	if afterDays <= 0 {
		return nil
	}

	today := time.Now().Format("2006-01-02")
	cutoff := time.Now().AddDate(0, 0, -afterDays)

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == today {
			continue
		}
		dirDate, err := time.Parse("2006-01-02", entry.Name())
		if err != nil {
			continue
		}
		if !dirDate.Before(cutoff) {
			continue
		}

		dirPath := filepath.Join(outputDir, entry.Name())
		pruned, err := pruneRawInDir(dirPath)
		if err != nil {
			logger.Error("failed to prune raw images", "dir", dirPath, "error", err)
			continue
		}
		if pruned > 0 {
			logger.Info("pruned raw images, kept synthesized outputs",
				"dir", dirPath,
				"removed", pruned,
			)
		}
	}

	return nil
}

// pruneRawInDir deletes all files and subdirectories in dir except the
// synthesized night outputs: timelapse-*, keogram-*, startrails-*, wb-analysis-*.
// Returns the number of items removed.
func pruneRawInDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read dir: %w", err)
	}

	var removed int
	for _, e := range entries {
		lower := strings.ToLower(e.Name())

		// Keep synthesized outputs regardless of extension.
		if strings.HasPrefix(lower, "timelapse-") ||
			strings.HasPrefix(lower, "keogram-") ||
			strings.HasPrefix(lower, "startrails-") ||
			strings.HasPrefix(lower, "wb-analysis-") ||
			strings.HasPrefix(lower, "cloud-") {
			continue
		}

		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return removed, err
			}
		} else {
			if err := os.Remove(path); err != nil {
				return removed, err
			}
		}
		removed++
	}

	return removed, nil
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

	slices.Sort(dirs)
	slices.Reverse(dirs)
	return dirs, nil
}
