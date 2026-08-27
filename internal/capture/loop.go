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
	cam    camera.Camera
	cfg    *config.Config
	logger *slog.Logger

	// State.
	mode Mode
	// night holds the in-progress night's directory, metrics, timelapse
	// segments and alert count. initMode creates it whenever mode is
	// ModeNight and clears it at dawn, so every night-mode code path may
	// assume it is non-nil; during the day it is nil.
	night         *nightSession
	exposureCtrl  *ExposureController
	darkMgr       *DarkFrameManager
	flatMgr       *flat.Manager
	frameCount    int64
	skipRemaining int
	failureStreak int       // consecutive capture failures, for backoff and reinit
	rawWarned     bool      // de-dups raw-calibration fallback warnings
	lastCaptured  time.Time // when the camera last delivered a frame, for the staleness tripwire

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

	// now supplies the wall clock for every mode/session/staleness decision;
	// defaults to time.Now and is replaced by tests to simulate a night.
	now func() time.Time
}

// NewLoop creates a new capture loop.
func NewLoop(cam camera.Camera, cfg *config.Config, logger *slog.Logger) *Loop {
	l := &Loop{
		cam:      cam,
		cfg:      cfg,
		logger:   logger,
		thumbSem: make(chan struct{}, 2),
		now:      time.Now,
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

	l.lastCaptured = l.now()

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
		if limit := staleLimit(l.modeConfig().Delay); l.now().Sub(l.lastCaptured) > limit {
			return fmt.Errorf("no successful capture in %s (limit %s) — exiting for a clean restart",
				l.now().Sub(l.lastCaptured).Round(time.Second), limit)
		}

		select {
		case <-ctx.Done():
			l.logger.Info("capture loop stopping")
			return ctx.Err()
		default:
		}

		l.applyModeTransition(ctx)

		// Get mode settings.
		modeCfg := l.modeConfig()

		result, err := l.captureFrame(ctx, modeCfg)
		if err != nil {
			if werr := l.backOffAfterFailure(ctx, err); werr != nil {
				return werr
			}
			continue
		}
		l.failureStreak = 0
		l.lastCaptured = l.now()

		l.frameCount++

		if l.processFrame(ctx, result, modeCfg) == frameAborted {
			continue
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

// frameOutcome tells Run whether a processed frame finished normally.
type frameOutcome int

const (
	// frameComplete: the frame was fully processed and saved; the caller
	// honours the configured inter-frame delay.
	frameComplete frameOutcome = iota
	// frameAborted: the frame was skipped (post-transition warmup) or failed
	// part-way through. The caller retries immediately without the delay,
	// matching the original loop's `continue`.
	frameAborted
)

// processFrame runs a captured frame through the pipeline: calibration,
// metering, sky metrics, overlay, save, and the per-frame side effects
// (thumbnail, upload hook, timelapse segmentation, metrics snapshot, disk
// guard).
func (l *Loop) processFrame(ctx context.Context, result *camera.CaptureResult, modeCfg config.ModeConfig) frameOutcome {
	processedImg, meteredMean, rawCalibrated := l.meterFrame(result, modeCfg)
	l.applyMoonTargetBoost(modeCfg)

	// Skip frames after mode transition.
	if l.skipRemaining > 0 {
		l.skipRemaining--
		l.logger.Debug("skipping frame", "remaining", l.skipRemaining)
		if modeCfg.AutoExposure {
			l.exposureCtrl.Adjust(meteredMean)
		}
		return frameAborted
	}

	processedImg = l.applyCalibration(processedImg, result, modeCfg, rawCalibrated)

	l.recordSkyMetrics(ctx, processedImg, result, modeCfg)

	// Apply overlay.
	overlayCfg := imgutil.DefaultOverlayConfig()
	overlayCfg.Enabled = l.cfg.Output.Overlay
	overlayCfg.FontSize = l.cfg.Output.OverlayFontSize
	processedImg = imgutil.ApplyOverlay(processedImg, result.Meta, overlayCfg)

	outputPath, ok := l.saveFrame(processedImg, result, modeCfg)
	if !ok {
		return frameAborted
	}

	l.cacheThumbnail(processedImg, outputPath)

	// Notify upload handler.
	if l.OnImageSaved != nil {
		l.OnImageSaved(outputPath, result.Meta)
	}

	l.maybeEncodeSegment(ctx)

	// Auto-exposure adjustment using the metered zone mean.
	if modeCfg.AutoExposure {
		l.exposureCtrl.Adjust(meteredMean)
	}

	l.writeMetricsSnapshot(result, meteredMean)
	l.guardDiskSpace()

	return frameComplete
}

// meterFrame produces the image the rest of the pipeline works on and the mean
// brightness auto-exposure steers by.
//
// The experimental raw pipeline is opt-in via output.raw_calibration; the flag
// guard keeps every raw code path inert when disabled. On success both the
// image and the metered mean come from the linear raw data; any failure falls
// back to the standard processed image. The returned bool reports whether raw
// calibration ran, because it also subtracts the master dark — the 8-bit dark
// subtraction downstream must not then run a second time.
func (l *Loop) meterFrame(result *camera.CaptureResult, modeCfg config.ModeConfig) (image.Image, float64, bool) {
	if l.cfg.Output.RawCalibration && len(result.DNGData) > 0 {
		img, mean, err := l.calibrateRaw(result, modeCfg)
		if err == nil {
			l.rawWarned = false
			return img, mean, true
		}
		if !l.rawWarned {
			l.logger.Warn("raw calibration failed; using standard processed image", "error", err)
			l.rawWarned = true
		}
	}
	// Metered brightness for auto-exposure from the processed image.
	return result.Image, ZoneMean(result.Image, modeCfg.MeteringZone), false
}

// applyMoonTargetBoost raises the auto-exposure target while the moon is up,
// proportionally to its illuminated fraction, so the controller stops fighting
// moonlight with maximum gain.
func (l *Loop) applyMoonTargetBoost(modeCfg config.ModeConfig) {
	if !modeCfg.AutoExposure || modeCfg.MoonTargetBoost <= 0 {
		return
	}
	target := modeCfg.TargetBrightness
	now := l.now()
	if astro.MoonPosition(now, l.cfg.Location.Latitude, l.cfg.Location.Longitude).Altitude > 0 {
		target *= 1 + modeCfg.MoonTargetBoost*astro.MoonPhase(now)
	}
	l.exposureCtrl.TargetBrightness = target
}

// applyCalibration runs the 8-bit correction chain: dark subtraction, flat
// division, then the night-only histogram stretch.
func (l *Loop) applyCalibration(img image.Image, result *camera.CaptureResult, modeCfg config.ModeConfig, rawCalibrated bool) image.Image {
	// Select the dark matching this frame's actual exposure/gain (which drift
	// under auto-exposure), not the mode's configured base. Skipped when the
	// raw pipeline already subtracted a linear master dark.
	if !rawCalibrated && l.darkMgr != nil {
		dark := l.darkMgr.SelectDark(DarkFrameKey{
			Exposure: result.Meta.Exposure,
			Gain:     result.Meta.Gain,
			Binning:  modeCfg.Binning,
		})
		if dark != nil {
			img = SubtractDark(img, dark)
		}
	}

	// Flat field correction.
	if l.flatMgr != nil && l.flatMgr.Ready() {
		img = l.flatMgr.Apply(img)
	}

	// Histogram stretch — night only; daytime images have full dynamic range
	// and stretching washes out colour and blows out highlights.
	if l.cfg.Output.Stretch.Enabled && l.mode == ModeNight {
		sc := l.cfg.Output.Stretch
		img = imgutil.Stretch(img, sc.Mode, sc.BlackPoint, sc.WhitePoint, sc.AutoBlackPercentile, sc.AutoWhitePercentile)
	}

	return img
}

// cacheThumbnail writes a thumbnail from the already-decoded image so the web
// UI serves pre-built thumbnails without re-decoding from disk. Bounded by
// thumbSem; when the workers are busy the thumbnail is skipped and the web
// server generates it on demand instead.
func (l *Loop) cacheThumbnail(img image.Image, outputPath string) {
	select {
	case l.thumbSem <- struct{}{}:
		go func() {
			defer func() { <-l.thumbSem }()
			if err := imgutil.CacheThumb(img, outputPath); err != nil {
				l.logger.Debug("thumbnail generation failed", "error", err)
			}
		}()
	default:
		l.logger.Debug("thumbnail workers busy, skipping", "path", outputPath)
	}
}

// maybeEncodeSegment advances the iterative-timelapse counter and kicks off a
// segment encode every SegmentFrames night frames, so the end-of-night job
// only has to concatenate.
func (l *Loop) maybeEncodeSegment(ctx context.Context) {
	if l.mode != ModeNight || !l.cfg.Output.Timelapse.Enabled {
		return
	}
	sf := l.cfg.Output.Timelapse.SegmentFrames
	if sf <= 0 {
		return
	}

	l.night.frameCount++
	if l.night.frameCount%sf != 0 {
		return
	}

	segIdx := (l.night.frameCount / sf) - 1
	dir := l.night.path(l.cfg.Output.Directory)
	tlCfg := timelapse.FromConfig(l.cfg.Output.Timelapse)
	segWg := &l.night.segmentWg
	segWg.Add(1)
	go func() {
		defer segWg.Done()
		if _, err := timelapse.GenerateSegment(ctx, dir, segIdx, sf, tlCfg, l.logger); err != nil {
			l.logger.Error("timelapse segment failed", "segment", segIdx, "error", err)
		}
	}()
}

// recordSkyMetrics computes the per-frame sky statistics — cloud coverage,
// green ratio, star count and FWHM — and feeds the aurora detector. Night
// frames only; daytime frames carry no sky signal worth measuring.
func (l *Loop) recordSkyMetrics(ctx context.Context, img image.Image, result *camera.CaptureResult, modeCfg config.ModeConfig) {
	if l.mode != ModeNight {
		return
	}

	m := cloud.Estimate(img, result.Meta.Timestamp)

	// Star detection is heavier than the sampled stats; run it on every
	// starDetectEvery-th frame and carry the result forward.
	if l.frameCount%starDetectEvery == 0 {
		l.lastStars = stars.Detect(img)
	}
	m.StarCount = l.lastStars.Count
	m.StarFWHM = l.lastStars.MeanFWHM
	m.File = frameFilename(l.cfg.Output.FilenamePrefix, result.Meta.Timestamp, modeCfg.ImageType)

	l.night.metrics = append(l.night.metrics, m)

	if l.auroraDet != nil && l.auroraDet.Observe(m.GreenRatio, m.Coverage) {
		l.sendAuroraAlert(ctx, m)
	}
}

// saveFrame encodes and atomically writes the frame, plus its DNG sidecar when
// raw capture is on. It returns the written path, and false if the frame could
// not be saved.
func (l *Loop) saveFrame(img image.Image, result *camera.CaptureResult, modeCfg config.ModeConfig) (string, bool) {
	// Night images all go into the folder named after the night's start date
	// so midnight crossings don't split the dataset.
	dateLabel := l.now().Format("2006-01-02")
	if l.mode == ModeNight {
		dateLabel = l.night.dir
	}
	outputDir := filepath.Join(l.cfg.Output.Directory, dateLabel)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		l.logger.Error("create output dir", "error", err)
		return "", false
	}

	filename := frameFilename(l.cfg.Output.FilenamePrefix, result.Meta.Timestamp, modeCfg.ImageType)
	outputPath := filepath.Join(outputDir, filename)

	data, err := imgutil.EncodeImage(img, modeCfg.ImageType, modeCfg.Quality)
	if err != nil {
		l.logger.Error("encode image", "error", err)
		return "", false
	}

	if err := writeFileAtomic(outputPath, data); err != nil {
		l.logger.Error("save image", "error", err)
		return "", false
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

	return outputPath, true
}

// writeMetricsSnapshot publishes the live state the web UI's /api/metrics
// endpoint reads. Failures are non-fatal — the capture is already on disk.
func (l *Loop) writeMetricsSnapshot(result *camera.CaptureResult, meteredMean float64) {
	cloudCov, greenRatio := 0.0, 0.0
	if last, ok := l.night.lastMetric(); ok {
		cloudCov, greenRatio = last.Coverage, last.GreenRatio
	}
	now := l.now()
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
}

// guardDiskSpace reclaims space mid-session. End-of-night cleanup alone lets a
// long night fill the card while it is still running, and a full filesystem
// takes the whole system down with it. Reuses the space-based cleanup with the
// active night session protected.
func (l *Loop) guardDiskSpace() {
	if l.cfg.Output.MinFreeGB <= 0 || l.frameCount%diskCheckInterval != 0 {
		return
	}
	free, err := DiskFreeGB(l.cfg.Output.Directory)
	if err != nil || free >= l.cfg.Output.MinFreeGB {
		return
	}
	l.logger.Warn("disk space low mid-session, removing oldest captures",
		"free_gb", free, "min_free_gb", l.cfg.Output.MinFreeGB)
	if err := CleanForSpace(l.cfg.Output.Directory, l.cfg.Output.MinFreeGB, l.activeNightDir(), l.logger); err != nil {
		l.logger.Error("mid-session cleanup failed", "error", err)
	}
}

// applyModeTransition switches day/night mode when the sun has crossed the
// configured angle, handing a finished night off to background processing.
func (l *Loop) applyModeTransition(ctx context.Context) {
	newMode := l.currentMode()
	if newMode == l.mode {
		return
	}
	l.logger.Info("mode transition", "from", l.mode, "to", newMode)

	// End-of-night processing runs in the background so day capture starts
	// immediately instead of stalling behind timelapse encoding (encodes are
	// niced, so they yield CPU to capture). The goroutine owns the finished
	// session; clearing l.night means the next dusk starts a fresh one, so
	// nothing the end-of-night pipeline reads can still be mutated here.
	if l.mode == ModeNight && newMode == ModeDay {
		if finished := l.night; finished != nil {
			l.nightEndWg.Add(1)
			go func() {
				defer l.nightEndWg.Done()
				l.finishNight(ctx, finished)
			}()
		}
		l.night = nil
	}

	l.mode = newMode
	l.initMode()
}

// captureFrame takes one exposure with the current mode's settings.
func (l *Loop) captureFrame(ctx context.Context, modeCfg config.ModeConfig) (*camera.CaptureResult, error) {
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

	// Grace period covers rpicam-still startup (3-10s on a Pi), the shutter
	// open time, and image encoding before the context fires.
	captureCtx, cancel := context.WithTimeout(ctx, settings.Exposure+120*time.Second)
	defer cancel()
	return l.cam.Capture(captureCtx, settings)
}

// backOffAfterFailure records a capture failure, cycles the camera when the
// streak suggests a wedged device, and waits out the backoff. It returns a
// non-nil error only when the context ended during the wait, which is the
// loop's signal to stop.
func (l *Loop) backOffAfterFailure(ctx context.Context, err error) error {
	l.failureStreak++
	backoff := failureBackoff(l.failureStreak)
	l.logger.Error("capture failed",
		"error", err,
		"consecutive_failures", l.failureStreak,
		"retry_in", backoff,
	)

	// A persistent failure streak usually means a wedged device (USB stall,
	// crashed pipeline) that retrying alone won't fix — cycle the camera
	// connection.
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
	return nil
}

// currentMode determines whether it's day or night based on sun position.
func (l *Loop) currentMode() Mode {
	if astro.IsNight(l.now(), l.cfg.Location.Latitude, l.cfg.Location.Longitude, l.cfg.Location.Angle) {
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
		if l.night == nil {
			// Label the session by the date of the most recent dusk, not the
			// wall clock: a restart after midnight would otherwise start a
			// second directory for the same night, splitting the timelapse.
			sessionTime := l.now()
			if dusk, ok := astro.MostRecentDusk(sessionTime,
				l.cfg.Location.Latitude, l.cfg.Location.Longitude, l.cfg.Location.Angle); ok {
				sessionTime = dusk
			}
			// A fresh session also means a fresh segment WaitGroup: the
			// previous night's may still be draining in its end-of-night
			// goroutine.
			l.night = newNightSession(sessionTime.Format("2006-01-02"))
			// Fresh sky-metric state for the new night.
			if l.auroraDet != nil {
				l.auroraDet.Reset()
			}
			l.lastStars = stars.Result{}
		}
		l.night.frameCount = 0
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

// frameFilename builds a saved frame's filename; used by both the save path
// and the sky-metric record so the highlights manifest can reference frames.
func frameFilename(prefix string, ts time.Time, imageType string) string {
	return fmt.Sprintf("%s-%s.%s", prefix, ts.Format("20060102150405"), imageType)
}

// sendAuroraAlert delivers an aurora notification in the background.
func (l *Loop) sendAuroraAlert(ctx context.Context, m cloud.Metric) {
	msg := fmt.Sprintf("Possible aurora at %s — green ratio %.2f, cloud %.0f%%, %d stars visible.",
		m.Timestamp.Format("15:04"), m.GreenRatio, m.Coverage*100, m.StarCount)
	if base := l.cfg.Alerts.BaseURL; base != "" {
		msg += " Live view: " + strings.TrimRight(base, "/") + "/latest"
	}
	l.night.auroraEvents++
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
			strings.HasPrefix(lower, "highlight") || // highlight-N-* copies + highlights-*.json
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
