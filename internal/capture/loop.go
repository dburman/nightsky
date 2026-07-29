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
	"sync"
	"time"

	"github.com/dburman/nightsky/internal/alerts"
	"github.com/dburman/nightsky/internal/astro"
	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/cloud"
	"github.com/dburman/nightsky/internal/config"
	"github.com/dburman/nightsky/internal/flat"
	imgutil "github.com/dburman/nightsky/internal/image"
	"github.com/dburman/nightsky/internal/metrics"
	"github.com/dburman/nightsky/internal/raw"
	"github.com/dburman/nightsky/internal/sdnotify"
	"github.com/dburman/nightsky/internal/stars"
	"github.com/dburman/nightsky/internal/timelapse"
)

// Mode represents the current capture mode.
type Mode int

const (
	ModeDay Mode = iota
	ModeNight
)

// diskCheckInterval is how many frames pass between mid-session free-space
// checks. At typical night cadence (~5-15s/frame) this checks every few
// minutes — fast enough to react before a nearly-full card runs out.
const diskCheckInterval = 50

// starDetectEvery is how many night frames pass between star-detection runs;
// detection walks the full sky region, so it's sampled rather than per-frame.
const starDetectEvery = 5

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
	flatMgr         *flat.Manager
	frameCount      int64
	skipRemaining   int
	failureStreak   int  // consecutive capture failures, for backoff and reinit
	rawWarned       bool // de-dups raw-calibration fallback warnings
	lastCaptured    time.Time // when the camera last delivered a frame, for the staleness tripwire
	cloudMetrics    []cloud.Metric // accumulated per-frame cloud metrics for the current night

	// Iterative timelapse state (used when SegmentFrames > 0). segmentWg is
	// per night session — end-of-night processing waits on the finished
	// night's group while a new session gets a fresh one.
	nightFrameCount int
	segmentWg       *sync.WaitGroup

	// nightEndWg tracks in-flight end-of-night processing goroutines so Run
	// drains them before returning.
	nightEndWg sync.WaitGroup

	// thumbSem bounds concurrent thumbnail encodes; when full, the thumbnail
	// is skipped and the web server generates it on demand instead.
	thumbSem chan struct{}

	// Callbacks for upload integration.
	OnImageSaved func(path string, meta camera.CaptureMeta)
	OnNightEnd   func(dateDir string)

	// Notifier, when set by the caller, receives aurora alerts and night
	// summaries. A nil notifier disables both.
	Notifier *alerts.Notifier

	auroraDet *alerts.AuroraDetector // nil unless aurora alerting is enabled
	lastStars stars.Result           // most recent star detection, for metrics
}

// NewLoop creates a new capture loop.
func NewLoop(cam camera.Camera, cfg *config.Config, logger *slog.Logger) *Loop {
	l := &Loop{
		cam:       cam,
		cfg:       cfg,
		logger:    logger,
		thumbSem:  make(chan struct{}, 2),
		segmentWg: &sync.WaitGroup{},
	}

	// Initialize dark frame manager if enabled.
	if cfg.Dark.Enabled {
		l.darkMgr = NewDarkFrameManager(cfg.Dark.Directory, cfg.Dark.Count, cam, logger)
	}

	if cfg.Alerts.Aurora.Enabled {
		l.auroraDet = alerts.NewAuroraDetector(alerts.AuroraConfig{
			RatioThreshold: cfg.Alerts.Aurora.RatioThreshold,
			MinFrames:      cfg.Alerts.Aurora.MinFrames,
			MaxCloud:       cfg.Alerts.Aurora.MaxCloud,
		})
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

	// Clear partial frame/DNG files left by a writeFileAtomic interrupted by a
	// previous crash, so they don't accumulate.
	if n := sweepStaleTmp(l.cfg.Output.Directory); n > 0 {
		l.logger.Info("removed stale partial-write files", "count", n)
	}

	// Drain in-flight end-of-night processing before returning; on shutdown
	// the cancelled context aborts it promptly.
	defer l.nightEndWg.Wait()

	// Determine initial mode.
	l.mode = l.currentMode()
	l.logger.Info("initial mode", "mode", l.mode)
	l.initMode()

	l.lastCaptured = time.Now()

	for {
		// Pet the systemd watchdog (no-op outside systemd). Sent every
		// iteration — including failure/backoff paths — so the watchdog
		// fires only when the loop is truly wedged, not when the camera is
		// merely erroring.
		sdnotify.Heartbeat()

		// Staleness tripwire: the watchdog catches a wedged process and
		// backoff handles a flaky camera, but a camera that errors forever
		// keeps the loop alive while producing nothing. Exit non-zero so
		// systemd restarts the whole process (fresh camera stack) rather
		// than silently losing the night. Keyed on successful captures, not
		// saves, so long-delay skip-frame stretches can't false-trip it.
		if limit := staleLimit(l.modeConfig().Delay); time.Since(l.lastCaptured) > limit {
			return fmt.Errorf("no successful capture in %s (limit %s) — exiting for a clean restart",
				time.Since(l.lastCaptured).Round(time.Second), limit)
		}

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

			// End-of-night processing runs in the background so day capture
			// starts immediately instead of stalling behind timelapse
			// encoding (encodes are niced, so they yield CPU to capture).
			// The goroutine owns the finished night's metrics and segment
			// WaitGroup; initMode gives the next session fresh ones.
			if l.mode == ModeNight && newMode == ModeDay {
				if l.nightSessionDir != "" {
					nightDir := filepath.Join(l.cfg.Output.Directory, l.nightSessionDir)
					nightMetrics := l.cloudMetrics
					segWg := l.segmentWg
					l.cloudMetrics = nil

					l.nightEndWg.Add(1)
					go func() {
						defer l.nightEndWg.Done()
						// Flush cloud metrics before handing off to OnNightEnd.
						if len(nightMetrics) > 0 {
							if err := cloud.WriteReport(nightDir, nightMetrics); err != nil {
								l.logger.Error("cloud report write failed", "error", err)
							} else {
								l.logger.Info("cloud coverage report written",
									"dir", nightDir,
									"frames", len(nightMetrics),
									"summary", cloud.SummaryLine(nightMetrics),
								)
							}
						}
						// Drain any in-flight segment encodes before finalizing.
						segWg.Wait()
						if l.OnNightEnd != nil {
							l.OnNightEnd(nightDir)
						}
					}()
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
			SaveRaw:  modeCfg.SaveRaw,
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
			l.failureStreak++
			backoff := failureBackoff(l.failureStreak)
			l.logger.Error("capture failed",
				"error", err,
				"consecutive_failures", l.failureStreak,
				"retry_in", backoff,
			)

			// A persistent failure streak usually means a wedged device
			// (USB stall, crashed pipeline) that retrying alone won't fix —
			// cycle the camera connection.
			if l.failureStreak%5 == 0 {
				l.logger.Warn("reinitializing camera after repeated capture failures")
				if cerr := l.cam.Close(); cerr != nil {
					l.logger.Error("camera close failed", "error", cerr)
				}
				if oerr := l.cam.Open(); oerr != nil {
					l.logger.Error("camera reopen failed", "error", oerr)
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}
		l.failureStreak = 0
		l.lastCaptured = time.Now()

		l.frameCount++

		// Experimental raw pipeline (opt-in via output.raw_calibration; the
		// flag guard keeps every raw code path inert when disabled). On
		// success the frame image and metered mean both come from the linear
		// raw data; any failure falls back to the standard processed image.
		processedImg := result.Image
		var meteredMean float64
		rawCalibrated := false
		if l.cfg.Output.RawCalibration && len(result.DNGData) > 0 {
			img, mean, err := l.calibrateRaw(result, modeCfg)
			if err != nil {
				if !l.rawWarned {
					l.logger.Warn("raw calibration failed; using standard processed image", "error", err)
					l.rawWarned = true
				}
			} else {
				processedImg, meteredMean = img, mean
				rawCalibrated = true
				l.rawWarned = false
			}
		}
		if !rawCalibrated {
			// Metered brightness for auto-exposure from the processed image.
			meteredMean = ZoneMean(result.Image, modeCfg.MeteringZone)
		}

		// Moon-compensated auto-exposure: while the moon is up, raise the
		// target proportionally to its illuminated fraction so the
		// controller stops fighting moonlight with maximum gain.
		if modeCfg.AutoExposure && modeCfg.MoonTargetBoost > 0 {
			target := modeCfg.TargetBrightness
			now := time.Now()
			if astro.MoonPosition(now, l.cfg.Location.Latitude, l.cfg.Location.Longitude).Altitude > 0 {
				target *= 1 + modeCfg.MoonTargetBoost*astro.MoonPhase(now)
			}
			l.exposureCtrl.TargetBrightness = target
		}

		// Skip frames after mode transition.
		if l.skipRemaining > 0 {
			l.skipRemaining--
			l.logger.Debug("skipping frame", "remaining", l.skipRemaining)
			if modeCfg.AutoExposure {
				l.exposureCtrl.Adjust(meteredMean)
			}
			continue
		}

		// Dark frame subtraction (8-bit path). Select the dark matching this
		// frame's actual exposure/gain (which drift under auto-exposure), not
		// the mode's configured base. Skipped when the raw pipeline already
		// subtracted a linear master dark.
		if !rawCalibrated && l.darkMgr != nil {
			dark := l.darkMgr.SelectDark(DarkFrameKey{
				Exposure: result.Meta.Exposure,
				Gain:     result.Meta.Gain,
				Binning:  modeCfg.Binning,
			})
			if dark != nil {
				processedImg = SubtractDark(processedImg, dark)
			}
		}

		// Flat field correction.
		if l.flatMgr != nil && l.flatMgr.Ready() {
			processedImg = l.flatMgr.Apply(processedImg)
		}

		// Histogram stretch — night only; daytime images have full dynamic range
		// and stretching washes out colour and blows out highlights.
		if l.cfg.Output.Stretch.Enabled && l.mode == ModeNight {
			sc := l.cfg.Output.Stretch
			processedImg = imgutil.Stretch(processedImg, sc.Mode, sc.BlackPoint, sc.WhitePoint, sc.AutoBlackPercentile, sc.AutoWhitePercentile)
		}

		// Sky metrics — night frames only: cloud coverage, green ratio,
		// star statistics, and aurora detection.
		if l.mode == ModeNight {
			m := cloud.Estimate(processedImg, result.Meta.Timestamp)

			// Star detection is heavier than the sampled stats; run it on
			// every starDetectEvery-th frame and carry the result forward.
			if l.frameCount%starDetectEvery == 0 {
				l.lastStars = stars.Detect(processedImg)
			}
			m.StarCount = l.lastStars.Count
			m.StarFWHM = l.lastStars.MeanFWHM

			l.cloudMetrics = append(l.cloudMetrics, m)

			if l.auroraDet != nil && l.auroraDet.Observe(m.GreenRatio, m.Coverage) {
				l.sendAuroraAlert(ctx, m)
			}
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

		if err := writeFileAtomic(outputPath, data); err != nil {
			l.logger.Error("save image", "error", err)
			continue
		}

		l.logger.Info("saved",
			"path", outputPath,
			"size_kb", len(data)/1024,
			"frame", l.frameCount,
		)

		// Save DNG alongside the main image when raw capture is enabled.
		if len(result.DNGData) > 0 {
			stem := outputPath[:len(outputPath)-len(filepath.Ext(outputPath))]
			dngPath := stem + ".dng"
			if err := writeFileAtomic(dngPath, result.DNGData); err != nil {
				l.logger.Error("save DNG", "error", err)
			} else {
				l.logger.Debug("saved DNG", "path", dngPath, "size_kb", len(result.DNGData)/1024)
			}
		}

		// Generate thumbnail from the already-decoded image so the web UI
		// serves pre-built thumbnails without re-decoding from disk.
		select {
		case l.thumbSem <- struct{}{}:
			go func(img image.Image, path string) {
				defer func() { <-l.thumbSem }()
				if err := imgutil.CacheThumb(img, path); err != nil {
					l.logger.Debug("thumbnail generation failed", "error", err)
				}
			}(processedImg, outputPath)
		default:
			l.logger.Debug("thumbnail workers busy, skipping", "path", outputPath)
		}

		// Notify upload handler.
		if l.OnImageSaved != nil {
			l.OnImageSaved(outputPath, result.Meta)
		}

		// Iterative timelapse: encode a segment every SegmentFrames night frames.
		if l.mode == ModeNight && l.cfg.Output.Timelapse.Enabled {
			if sf := l.cfg.Output.Timelapse.SegmentFrames; sf > 0 {
				l.nightFrameCount++
				if l.nightFrameCount%sf == 0 {
					segIdx := (l.nightFrameCount / sf) - 1
					dir := filepath.Join(l.cfg.Output.Directory, l.nightSessionDir)
					tlCfg := timelapse.FromConfig(l.cfg.Output.Timelapse)
					segWg := l.segmentWg
					segWg.Add(1)
					go func(idx int) {
						defer segWg.Done()
						if _, err := timelapse.GenerateSegment(ctx, dir, idx, sf, tlCfg, l.logger); err != nil {
							l.logger.Error("timelapse segment failed", "segment", idx, "error", err)
						}
					}(segIdx)
				}
			}
		}

		// Auto-exposure adjustment using the metered zone mean.
		if modeCfg.AutoExposure {
			l.exposureCtrl.Adjust(meteredMean)
		}

		// Write live metrics for the web server's /api/metrics endpoint.
		cloudCov, greenRatio := 0.0, 0.0
		if len(l.cloudMetrics) > 0 {
			last := l.cloudMetrics[len(l.cloudMetrics)-1]
			cloudCov, greenRatio = last.Coverage, last.GreenRatio
		}
		now := time.Now()
		snap := metrics.Snapshot{
			Mode:           l.mode.String(),
			ExposureNs:     l.exposureCtrl.Exposure.Nanoseconds(),
			Exposure:       imgutil.FormatExposure(l.exposureCtrl.Exposure),
			Gain:           l.exposureCtrl.Gain,
			FrameCount:     l.frameCount,
			MeanBrightness: meteredMean,
			CloudCoverage:  cloudCov,
			GreenRatio:     greenRatio,
			StarCount:      l.lastStars.Count,
			StarFWHM:       l.lastStars.MeanFWHM,
			MoonAltitude:   astro.MoonPosition(now, l.cfg.Location.Latitude, l.cfg.Location.Longitude).Altitude,
			MoonIllum:      astro.MoonPhase(now),
			SensorTempC:    result.Meta.Temperature,
			LastCapture:    result.Meta.Timestamp,
		}
		if err := metrics.Write(l.cfg.Output.MetricsDirectory(), snap); err != nil {
			l.logger.Debug("metrics write failed", "error", err)
		}

		// Mid-session disk guard: end-of-night cleanup alone lets a long
		// night fill the card mid-session, and a full filesystem takes the
		// whole system down with it. Reuses the space-based cleanup with the
		// active night session protected.
		if l.cfg.Output.MinFreeGB > 0 && l.frameCount%diskCheckInterval == 0 {
			if free, err := DiskFreeGB(l.cfg.Output.Directory); err == nil && free < l.cfg.Output.MinFreeGB {
				l.logger.Warn("disk space low mid-session, removing oldest captures",
					"free_gb", free, "min_free_gb", l.cfg.Output.MinFreeGB)
				if err := CleanForSpace(l.cfg.Output.Directory, l.cfg.Output.MinFreeGB, l.nightSessionDir, l.logger); err != nil {
					l.logger.Error("mid-session cleanup failed", "error", err)
				}
			}
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
	if l.mode == ModeNight {
		if l.nightSessionDir == "" {
			// Label the session by the date of the most recent dusk, not the
			// wall clock: a restart after midnight would otherwise start a
			// second directory for the same night, splitting the timelapse.
			sessionTime := time.Now()
			if dusk, ok := astro.MostRecentDusk(sessionTime,
				l.cfg.Location.Latitude, l.cfg.Location.Longitude, l.cfg.Location.Angle); ok {
				sessionTime = dusk
			}
			l.nightSessionDir = sessionTime.Format("2006-01-02")
			// Fresh group per session — the previous night's group may still
			// be drained by its end-of-night goroutine.
			l.segmentWg = &sync.WaitGroup{}
			// Fresh sky-metric state for the new night.
			if l.auroraDet != nil {
				l.auroraDet.Reset()
			}
			l.lastStars = stars.Result{}
		}
		l.nightFrameCount = 0
	}

	modeCfg := l.modeConfig()

	// Holy Grail: at dusk (day→night), seed the new controller with the current
	// exposure/gain so the algorithm ramps naturally into darkness rather than
	// jumping to the configured night defaults. At dawn (night→day), skip this
	// and use the configured day defaults instead — the skip-frames warmup cycle
	// then converges from a known starting point, exactly like first startup.
	initExposure := modeCfg.Exposure
	initGain := modeCfg.Gain
	if l.exposureCtrl != nil && l.mode == ModeNight {
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

	// Dark frames are now selected per-frame against actual settings in the
	// capture loop (see SelectDark), not loaded once per mode.
}

// calibrateRaw runs the linear-raw pipeline on a frame's DNG: decode,
// subtract the nearest raw master dark (if any), meter brightness from the
// linear data, then debayer with the mode's WB gains and display gamma.
// Returns the RGB image and the linear metered mean.
//
// Note: the linear mean reads darker than the gamma-display mean for the same
// scene, so target_brightness needs retuning when raw calibration is enabled.
// When AWB is on, libcamera's dynamic gains are not in the DNG; the configured
// wb_red/wb_blue are used instead.
func (l *Loop) calibrateRaw(result *camera.CaptureResult, modeCfg config.ModeConfig) (image.Image, float64, error) {
	rimg, err := raw.DecodeDNG(result.DNGData)
	if err != nil {
		return nil, 0, fmt.Errorf("decode DNG: %w", err)
	}

	if l.darkMgr != nil {
		dark := l.darkMgr.SelectRawDark(DarkFrameKey{
			Exposure: result.Meta.Exposure,
			Gain:     result.Meta.Gain,
			Binning:  modeCfg.Binning,
		})
		if dark != nil {
			if err := rimg.SubtractDark(dark); err != nil {
				l.logger.Warn("raw dark subtraction skipped", "error", err)
			}
		}
	}

	mean := rimg.MeanBrightness(modeCfg.MeteringZone)
	rgb := rimg.Debayer(raw.DebayerOptions{
		WBRed:  modeCfg.WBRed,
		WBBlue: modeCfg.WBBlue,
		Gamma:  2.2,
	})
	return rgb, mean, nil
}

// sendAuroraAlert delivers an aurora notification in the background.
func (l *Loop) sendAuroraAlert(ctx context.Context, m cloud.Metric) {
	msg := fmt.Sprintf("Possible aurora at %s — green ratio %.2f, cloud %.0f%%, %d stars visible.",
		m.Timestamp.Format("15:04"), m.GreenRatio, m.Coverage*100, m.StarCount)
	if base := l.cfg.Alerts.BaseURL; base != "" {
		msg += " Live view: " + strings.TrimRight(base, "/") + "/latest"
	}
	l.logger.Info("aurora alert triggered", "green_ratio", m.GreenRatio, "cloud", m.Coverage)
	go func() {
		if err := l.Notifier.Send(ctx, "Possible aurora", msg); err != nil {
			l.logger.Error("aurora alert delivery failed", "error", err)
		}
	}()
}

// staleLimit is how long the loop tolerates zero successful captures before
// exiting for a restart: 30 minutes, or 3× the configured inter-frame delay
// when that is longer (a 20-minute day delay must not trip it).
func staleLimit(delay time.Duration) time.Duration {
	const base = 30 * time.Minute
	if d := 3 * delay; d > base {
		return d
	}
	return base
}

// failureBackoff returns the retry delay after n consecutive capture
// failures: 1s, 2s, 4s, ... capped at 60s.
func failureBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := time.Second << uint(min(n-1, 6))
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// sweepStaleTmp removes leftover ".tmp" files (from an interrupted
// writeFileAtomic) under outputDir's date directories. Returns the count
// removed. Best-effort: read/remove errors are ignored.
func sweepStaleTmp(outputDir string) int {
	dates, err := os.ReadDir(outputDir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, d := range dates {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(outputDir, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".tmp") {
				continue
			}
			if os.Remove(filepath.Join(dir, f.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

// writeFileAtomic writes data to path via a temp file and rename, so the
// concurrent readers of the output directory (segment encoder, uploader, web
// server) never observe a partially written frame.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// PruneRawImages removes raw captured image files from night directories older
// than afterDays, while keeping synthesized outputs (timelapse, keogram, star
// trails, WB analysis report). The .thumbs cache is also removed since its
// source images will be gone. Today's directory is never touched.
func PruneRawImages(outputDir string, afterDays int, logger *slog.Logger) error {
	if afterDays <= 0 {
		return nil
	}

	// Compare directory names lexically against a date-string cutoff. Parsing
	// names with time.Parse yields UTC midnight, and comparing that against a
	// local-time cutoff made afterDays=1 prune the night that had just ended.
	cutoff := time.Now().AddDate(0, 0, -afterDays).Format("2006-01-02")

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := time.Parse("2006-01-02", entry.Name()); err != nil {
			continue
		}
		if entry.Name() >= cutoff {
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

		// Keep synthesized outputs and DNG raw files.
		if strings.HasPrefix(lower, "timelapse-") ||
			strings.HasPrefix(lower, "keogram-") ||
			strings.HasPrefix(lower, "startrails-") ||
			strings.HasPrefix(lower, "wb-analysis-") ||
			strings.HasPrefix(lower, "cloud-") ||
			strings.HasSuffix(lower, ".dng") {
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

// CleanOldData removes output directories older than DaysToKeep. The last
// daysToKeep date directories (counting today) are kept, so daysToKeep=1
// preserves the night that just ended.
func CleanOldData(outputDir string, daysToKeep int, logger *slog.Logger) error {
	if daysToKeep <= 0 {
		return nil
	}

	// Lexical date-string comparison — see PruneRawImages for why time.Parse
	// comparisons are wrong here.
	cutoff := time.Now().AddDate(0, 0, -daysToKeep).Format("2006-01-02")

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := time.Parse("2006-01-02", entry.Name()); err != nil {
			continue // not a date directory
		}
		if entry.Name() >= cutoff {
			continue
		}

		dirPath := filepath.Join(outputDir, entry.Name())
		logger.Info("removing old data", "dir", dirPath, "date", entry.Name())
		if err := os.RemoveAll(dirPath); err != nil {
			logger.Error("failed to remove old data", "dir", dirPath, "error", err)
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
