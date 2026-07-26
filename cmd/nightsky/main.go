package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/camera/libcamera"
	"github.com/dburman/nightsky/internal/capture"
	"github.com/dburman/nightsky/internal/config"
	"github.com/dburman/nightsky/internal/convert"
	"github.com/dburman/nightsky/internal/flat"
	"github.com/dburman/nightsky/internal/gps"
	"github.com/dburman/nightsky/internal/keogram"
	"github.com/dburman/nightsky/internal/startrails"
	"github.com/dburman/nightsky/internal/timelapse"
	"github.com/dburman/nightsky/internal/upload"
	"github.com/dburman/nightsky/internal/whitebalance"
	"github.com/spf13/cobra"
)

var (
	cfgFile  string
	logLevel string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "nightsky",
		Short: "All-sky camera capture service",
		Long:  "A stripped-down, CLI-configured all-sky camera service for long-exposure night photography and timelapse generation.",
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file (default: ./nightsky.yaml)")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "log level (debug, info, warn, error)")

	rootCmd.AddCommand(
		captureCmd(),
		timelapseCmd(),
		darkCmd(),
		flatCmd(),
		infoCmd(),
		cleanCmd(),
		versionCmd(),
		serveCmd(),
		analyzeCmd(),
		webpCmd(),
		processCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func setupLogger() *slog.Logger {
	var level slog.Level
	switch logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// applyMemoryLimit installs the configured soft Go heap limit so the GC
// collects aggressively near the ceiling rather than letting the kernel OOM
// killer pick a victim. A GOMEMLIMIT environment variable takes precedence
// (the runtime already honours it at startup).
func applyMemoryLimit(cfg *config.Config, logger *slog.Logger) {
	if cfg.MemoryLimitMB <= 0 {
		return
	}
	if env := os.Getenv("GOMEMLIMIT"); env != "" {
		logger.Info("GOMEMLIMIT set in environment; ignoring memory_limit_mb", "gomemlimit", env)
		return
	}
	debug.SetMemoryLimit(int64(cfg.MemoryLimitMB) << 20)
	logger.Info("Go memory limit set", "limit_mb", cfg.MemoryLimitMB)
}

func loadConfig() (*config.Config, error) {
	return config.Load(cfgFile)
}

func openCamera(cfg *config.Config, logger *slog.Logger) (camera.Camera, error) {
	switch cfg.Camera.Type {
	case "libcamera":
		return libcamera.New(cfg.Camera.Device, logger), nil
	case "zwo":
		return newZWOCamera(cfg, logger)
	default:
		return nil, fmt.Errorf("unknown camera type: %s", cfg.Camera.Type)
	}
}

// captureCmd is the main command that runs the continuous capture loop.
func captureCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Start the capture loop",
		Long:  "Run the all-sky camera capture loop, automatically switching between day and night modes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}

			// Deflicker smooths brightness within a single encode, but segment
			// mode concatenates independently-encoded segments with stream
			// copy, so brightness steps at segment boundaries are never
			// smoothed.
			if cfg.Output.Timelapse.Deflicker && cfg.Output.Timelapse.SegmentFrames > 0 {
				logger.Warn("timelapse.deflicker has no effect across segment boundaries with segment_frames > 0; " +
					"set segment_frames: 0 for full-night deflicker")
			}

			applyMemoryLimit(cfg, logger)

			// raw_calibration is the single switch for the raw pipeline; config
			// loading already turned on DNG capture for both modes when set.
			if cfg.Output.RawCalibration {
				if cfg.Camera.Type != "libcamera" {
					logger.Warn("output.raw_calibration requires DNG capture, which only the libcamera backend provides; " +
						"the raw pipeline will never run with camera.type=" + cfg.Camera.Type)
				} else {
					logger.Info("raw calibration pipeline enabled (DNG capture on in both modes)")
				}
			}

			// GPS auto-location: fetch a fix from gpsd and override config lat/lon.
			if cfg.Location.GPS {
				gpsCtx, gpsCancel := context.WithTimeout(context.Background(), 30*time.Second)
				fix, err := gps.FetchFix(gpsCtx, cfg.Location.GPSAddr)
				gpsCancel()
				if err != nil {
					logger.Warn("GPS fix failed, using config coordinates", "error", err)
				} else {
					logger.Info("GPS fix acquired",
						"lat", fix.Latitude,
						"lon", fix.Longitude,
					)
					cfg.Location.Latitude = fix.Latitude
					cfg.Location.Longitude = fix.Longitude
				}
			}

			cam, err := openCamera(cfg, logger)
			if err != nil {
				return fmt.Errorf("open camera: %w", err)
			}

			if err := cam.Open(); err != nil {
				return fmt.Errorf("init camera: %w", err)
			}
			defer cam.Close()

			info := cam.Info()
			logger.Info("camera ready",
				"name", info.Name,
				"resolution", fmt.Sprintf("%dx%d", info.MaxWidth, info.MaxHeight),
			)

			// Set up upload handlers.
			var s3Uploader *upload.S3Uploader
			var httpUploader *upload.HTTPUploader

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if cfg.Upload.S3.Enabled {
				s3Uploader, err = upload.NewS3Uploader(ctx, upload.S3Config{
					Bucket:   cfg.Upload.S3.Bucket,
					Region:   cfg.Upload.S3.Region,
					Prefix:   cfg.Upload.S3.Prefix,
					Endpoint: cfg.Upload.S3.Endpoint,
				}, logger)
				if err != nil {
					return fmt.Errorf("init S3 uploader: %w", err)
				}
				logger.Info("S3 upload enabled", "bucket", cfg.Upload.S3.Bucket)
			}

			if cfg.Upload.HTTP.Enabled {
				httpUploader = upload.NewHTTPUploader(upload.HTTPConfig{
					URL:           cfg.Upload.HTTP.URL,
					Authorization: cfg.Upload.HTTP.Authorization,
				}, logger)
				logger.Info("HTTP upload enabled", "url", cfg.Upload.HTTP.URL)
			}

			// Create and configure the capture loop.
			loop := capture.NewLoop(cam, cfg, logger)

			// Wire up image upload callback. Uploads run on a small worker
			// pool fed by a bounded queue, so a slow uplink can't pile up a
			// goroutine per frame; when the queue is full the frame's upload
			// is dropped with a warning (the uplink is behind regardless).
			if cfg.Upload.UploadImages && (s3Uploader != nil || httpUploader != nil) {
				type uploadJob struct {
					path    string
					dateDir string
				}
				jobs := make(chan uploadJob, 64)
				for i := 0; i < 2; i++ {
					go func() {
						for j := range jobs {
							if s3Uploader != nil {
								if err := s3Uploader.Upload(ctx, j.path, j.dateDir); err != nil {
									logger.Error("S3 image upload failed", "error", err)
								}
							}
							if httpUploader != nil {
								if err := httpUploader.Upload(ctx, j.path); err != nil {
									logger.Error("HTTP image upload failed", "error", err)
								}
							}
						}
					}()
				}
				loop.OnImageSaved = func(path string, meta camera.CaptureMeta) {
					j := uploadJob{path: path, dateDir: meta.Timestamp.Format("2006-01-02")}
					select {
					case jobs <- j:
					default:
						logger.Warn("upload queue full, skipping frame upload", "path", path)
					}
				}
			}

			// Wire up end-of-night callback for timelapse + upload.
			loop.OnNightEnd = func(dateDir string) {
				runNightEndProcessing(ctx, dateDir, cfg, s3Uploader, httpUploader, logger)

				// Prune raw images from older nights, keeping synthesized outputs.
				if cfg.Output.PruneRawAfterDays > 0 {
					if err := capture.PruneRawImages(cfg.Output.Directory, cfg.Output.PruneRawAfterDays, logger); err != nil {
						logger.Error("raw image pruning failed", "error", err)
					}
				}

				// Clean old data. Space-based cleanup takes precedence over
				// time-based: only delete when the disk is actually getting full.
				switch {
				case cfg.Output.MinFreeGB > 0:
					if err := capture.CleanForSpace(cfg.Output.Directory, cfg.Output.MinFreeGB, filepath.Base(dateDir), logger); err != nil {
						logger.Error("space-based cleanup failed", "error", err)
					}
				case cfg.Output.DaysToKeep > 0:
					if err := capture.CleanOldData(cfg.Output.Directory, cfg.Output.DaysToKeep, logger); err != nil {
						logger.Error("cleanup failed", "error", err)
					}
				}
			}

			// Handle signals for graceful shutdown.
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigChan
				logger.Info("received signal, shutting down", "signal", sig)
				cancel()
			}()

			return loop.Run(ctx)
		},
	}

	return cmd
}

// timelapseCmd generates a timelapse from an existing image directory.
func timelapseCmd() *cobra.Command {
	var (
		dir     string
		fps     int
		bitrate string
		codec   string
		crf     int
		preset  string
		gop     int
		tune    string
		threads int
	)

	cmd := &cobra.Command{
		Use:   "timelapse",
		Short: "Generate a timelapse video from captured images",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()

			if dir == "" {
				// Default to most recent date directory.
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				dirs, err := capture.ListDateDirs(cfg.Output.Directory)
				if err != nil || len(dirs) == 0 {
					return fmt.Errorf("no image directories found")
				}
				dir = dirs[0]
				logger.Info("using most recent directory", "dir", dir)
			}

			tlCfg := timelapse.Config{
				FPS:     fps,
				Bitrate: bitrate,
				Codec:   codec,
				CRF:     crf,
				Preset:  preset,
				GOP:     gop,
				Tune:    tune,
				Threads: threads,
			}

			path, err := timelapse.Generate(context.Background(), dir, tlCfg, logger)
			if err != nil {
				return err
			}

			fmt.Printf("Timelapse saved to: %s\n", path)
			return nil
		},
	}

	cmd.Flags().StringVarP(&dir, "dir", "d", "", "image directory (default: most recent)")
	cmd.Flags().IntVar(&fps, "fps", 25, "frames per second")
	cmd.Flags().StringVar(&bitrate, "bitrate", "2000k", "video bitrate (used when crf is 0)")
	cmd.Flags().StringVar(&codec, "codec", "libx264", "video codec (libx264, libx265, libsvtav1, libaom-av1)")
	cmd.Flags().IntVar(&crf, "crf", 0, "constant-rate-factor quality (0 = use bitrate)")
	cmd.Flags().StringVar(&preset, "preset", "", "encoder preset (e.g. slow for x264/x265, 6 for SVT-AV1)")
	cmd.Flags().IntVar(&gop, "gop", 0, "max keyframe interval in frames (0 = encoder default)")
	cmd.Flags().StringVar(&tune, "tune", "", "encoder tune value (codec-specific)")
	cmd.Flags().IntVar(&threads, "threads", 0, "cap encoder threads; lowers peak memory (0 = encoder default)")

	return cmd
}

// darkCmd captures dark frames for calibration.
func darkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dark",
		Short: "Capture dark frames (cover lens first!)",
		Long:  "Capture and average multiple dark frames for dark subtraction. Cover the camera lens before running.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			cam, err := openCamera(cfg, logger)
			if err != nil {
				return err
			}
			if err := cam.Open(); err != nil {
				return err
			}
			defer cam.Close()

			darkMgr := capture.NewDarkFrameManager(cfg.Dark.Directory, cfg.Dark.Count, cam, logger)

			// Capture a dark library covering the auto-exposure grid for each
			// mode, so the per-frame SelectDark always finds a close match as
			// exposure and gain drift through the night.
			for _, mode := range []struct {
				name string
				cfg  config.ModeConfig
			}{
				{"night", cfg.Night},
				{"day", cfg.Day},
			} {
				grid := capture.DarkGrid(mode.cfg)
				fmt.Printf("Capturing %d dark library point(s) for %s mode...\n", len(grid), mode.name)

				for i, key := range grid {
					fmt.Printf("  [%d/%d] %s mode: exposure=%v, gain=%.0f, bin=%d\n",
						i+1, len(grid), mode.name, key.Exposure, key.Gain, key.Binning)

					settings := camera.CaptureSettings{
						Exposure: key.Exposure,
						Gain:     key.Gain,
						Binning:  key.Binning,
						Format:   camera.FormatRGB24,
						SaveRaw:  mode.cfg.SaveRaw,
					}

					if err := darkMgr.CaptureDarks(context.Background(), settings); err != nil {
						logger.Error("dark frame capture failed",
							"mode", mode.name, "exposure", key.Exposure, "gain", key.Gain, "error", err)
						continue
					}
				}
			}

			fmt.Println("Dark frame capture complete.")
			return nil
		},
	}

	return cmd
}

// flatCmd captures flat frames for vignetting correction.
func flatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "flat",
		Short: "Capture flat frames for vignetting correction",
		Long:  "Capture and average multiple flat frames to build a master flat for lens vignetting correction. Point the camera at a uniformly lit surface (white screen, overcast sky) before running.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			cam, err := openCamera(cfg, logger)
			if err != nil {
				return err
			}
			if err := cam.Open(); err != nil {
				return err
			}
			defer cam.Close()

			flatMgr := flat.NewManager(cfg.Flat.Directory, cfg.Flat.Count, cam, logger)

			// Use night settings (representative exposure for vignette pattern).
			settings := camera.CaptureSettings{
				Exposure: cfg.Night.Exposure,
				Gain:     cfg.Night.Gain,
				Binning:  cfg.Night.Binning,
				Format:   camera.FormatRGB24,
				SaveRaw:  cfg.Night.SaveRaw,
			}

			fmt.Printf("Capturing %d flat frames (exposure=%v, gain=%.1f)...\n",
				cfg.Flat.Count, settings.Exposure, settings.Gain)

			if err := flatMgr.CaptureFlat(context.Background(), settings); err != nil {
				return fmt.Errorf("flat capture failed: %w", err)
			}

			fmt.Println("Flat frame capture complete.")
			return nil
		},
	}
}

// infoCmd displays camera information.
func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Display camera information",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			cam, err := openCamera(cfg, logger)
			if err != nil {
				return err
			}
			if err := cam.Open(); err != nil {
				return err
			}
			defer cam.Close()

			info := cam.Info()
			fmt.Printf("Camera: %s\n", info.Name)
			fmt.Printf("Model:  %s\n", info.Model)
			fmt.Printf("Resolution: %d x %d\n", info.MaxWidth, info.MaxHeight)
			fmt.Printf("Color:  %v\n", info.IsColor)
			fmt.Printf("Cooler: %v\n", info.HasCooler)
			if info.BayerPattern != "" {
				fmt.Printf("Bayer:  %s\n", info.BayerPattern)
			}

			if len(info.Controls) > 0 {
				fmt.Println("\nControls:")
				for _, ctrl := range info.Controls {
					auto := ""
					if ctrl.IsAutoSupported {
						auto = " [auto]"
					}
					fmt.Printf("  %-25s min=%-8.0f max=%-8.0f default=%.0f%s\n",
						ctrl.Name, ctrl.Min, ctrl.Max, ctrl.Default, auto)
				}
			}

			temp, err := cam.Temperature()
			if err == nil && temp != 0 {
				fmt.Printf("\nSensor Temperature: %.1f°C\n", temp)
			}

			return nil
		},
	}
}

// cleanCmd removes old capture directories.
func cleanCmd() *cobra.Command {
	var days int

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove old capture directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if days <= 0 {
				days = cfg.Output.DaysToKeep
			}

			return capture.CleanOldData(cfg.Output.Directory, days, logger)
		},
	}

	cmd.Flags().IntVar(&days, "days", 0, "keep directories newer than this many days (default: from config)")
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("nightsky %s\n", config.Version)
		},
	}
}

// analyzeCmd analyses images in a directory and writes WB suggestions.
func analyzeCmd() *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Analyse images and suggest white balance settings",
		Long:  "Samples captured images to measure mean R/G/B channel values and suggests WB red/blue adjustments. Results are written to wb-analysis-<date>.txt in the image directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if dir == "" {
				dirs, err := capture.ListDateDirs(cfg.Output.Directory)
				if err != nil || len(dirs) == 0 {
					return fmt.Errorf("no image directories found")
				}
				dir = dirs[0]
				logger.Info("using most recent directory", "dir", dir)
			}

			results := map[string]*whitebalance.Result{}

			nightResult, err := whitebalance.Analyze(dir, whitebalance.ModeSettings{
				WBRed:      cfg.Night.WBRed,
				WBBlue:     cfg.Night.WBBlue,
				AWB:        cfg.Night.AWB,
				Label:      "Night",
				CameraType: cfg.Camera.Type,
			})
			if err != nil {
				logger.Warn("night WB analysis failed", "error", err)
			} else {
				results["Night"] = nightResult
			}

			dayResult, err := whitebalance.Analyze(dir, whitebalance.ModeSettings{
				WBRed:      cfg.Day.WBRed,
				WBBlue:     cfg.Day.WBBlue,
				AWB:        cfg.Day.AWB,
				Label:      "Day",
				CameraType: cfg.Camera.Type,
			})
			if err != nil {
				logger.Warn("day WB analysis failed", "error", err)
			} else {
				results["Day"] = dayResult
			}

			if len(results) == 0 {
				return fmt.Errorf("no images could be analysed")
			}

			outPath, _ := whitebalance.ReportPath(dir)
			if err := whitebalance.WriteReport(dir, results); err != nil {
				return fmt.Errorf("write report: %w", err)
			}
			fmt.Printf("WB analysis written to: %s\n", outPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&dir, "dir", "d", "", "image directory to analyse (default: most recent)")
	return cmd
}

// runNightEndProcessing runs the full end-of-night synthesis suite for dateDir:
// timelapse, keogram, star trails, WB analysis, and WebP conversion.
// Disk cleanup (prune/clean) is intentionally excluded so callers can apply
// their own policy (the capture loop's OnNightEnd adds cleanup on top of this).
func runNightEndProcessing(
	ctx context.Context,
	dateDir string,
	cfg *config.Config,
	s3Uploader *upload.S3Uploader,
	httpUploader *upload.HTTPUploader,
	logger *slog.Logger,
) {
	logger.Info("end of night processing", "dir", dateDir)

	// The heavy synthesis steps run strictly sequentially. They were once
	// parallel ("different CPU resources"), but on low-memory boards the
	// binding constraint is RAM, not CPU: an SVT-AV1 ffmpeg encode plus the
	// keogram's NumCPU decode workers together exhaust a 512 MB Pi Zero 2 and
	// summon the OOM killer. Sequential costs a few minutes of wall clock on
	// large machines and makes dawn survivable on small ones.
	if cfg.Output.Timelapse.Enabled {
		tlCfg := timelapse.FromConfig(cfg.Output.Timelapse)
		date := filepath.Base(dateDir)
		var videoPath string
		var err error
		if cfg.Output.Timelapse.SegmentFrames > 0 {
			videoPath, err = timelapse.FinalizeSegments(ctx, dateDir, date, cfg.Output.Timelapse.SegmentFrames, tlCfg, logger)
		} else {
			videoPath, err = timelapse.Generate(ctx, dateDir, tlCfg, logger)
		}
		if err != nil {
			logger.Error("timelapse generation failed", "error", err)
		} else if cfg.Upload.UploadTimelapse {
			// Uploads are network-bound and may proceed in the background.
			if s3Uploader != nil {
				go func() {
					if err := s3Uploader.Upload(ctx, videoPath, "timelapse"); err != nil {
						logger.Error("S3 timelapse upload failed", "error", err)
					}
				}()
			}
			if httpUploader != nil {
				go func() {
					if err := httpUploader.Upload(ctx, videoPath); err != nil {
						logger.Error("HTTP timelapse upload failed", "error", err)
					}
				}()
			}
		}
	}

	if cfg.Output.Keogram.Enabled {
		if _, err := keogram.Generate(ctx, dateDir, keogram.DefaultMaxMeanBrightness, logger); err != nil {
			logger.Error("keogram generation failed", "error", err)
		}
	}

	if cfg.Output.StarTrails.Enabled {
		if _, err := startrails.Generate(ctx, dateDir, startrails.DefaultMaxMeanBrightness, logger); err != nil {
			logger.Error("star trails generation failed", "error", err)
		}
	}

	// White balance analysis.
	wbResults := map[string]*whitebalance.Result{}
	nightResult, err := whitebalance.Analyze(dateDir, whitebalance.ModeSettings{
		WBRed:      cfg.Night.WBRed,
		WBBlue:     cfg.Night.WBBlue,
		AWB:        cfg.Night.AWB,
		Label:      "Night",
		CameraType: cfg.Camera.Type,
	})
	if err != nil {
		logger.Warn("WB analysis failed", "error", err)
	} else {
		wbResults["Night"] = nightResult
	}
	if len(wbResults) > 0 {
		if err := whitebalance.WriteReport(dateDir, wbResults); err != nil {
			logger.Error("WB report write failed", "error", err)
		} else {
			logger.Info("WB analysis written", "dir", dateDir)
		}
	}

	// WebP conversion — runs after all generation steps so the timelapse
	// input list is never broken by missing PNGs.
	if cfg.Output.WebP.Enabled {
		if err := convert.ConvertPNGsToWebP(ctx, dateDir,
			cfg.Output.WebP.Quality,
			cfg.Output.WebP.DeleteOriginals,
			logger,
		); err != nil {
			logger.Error("webp conversion failed", "error", err)
		}
	}
}

// processCmd runs the full end-of-night processing suite on an existing
// capture directory without needing the capture loop to have completed.
// Useful when capture was interrupted before dawn or for reprocessing a night.
func processCmd() *cobra.Command {
	var dir string

	cmd := &cobra.Command{
		Use:   "process",
		Short: "Run end-of-night processing on a capture directory",
		Long:  "Generates timelapse, keogram, star trails, white balance analysis, and WebP conversion for an existing capture directory. Respects the enabled/disabled flags in config for each output type.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}

			if dir == "" {
				dirs, err := capture.ListDateDirs(cfg.Output.Directory)
				if err != nil || len(dirs) == 0 {
					return fmt.Errorf("no capture directories found in %s", cfg.Output.Directory)
				}
				dir = dirs[0]
				logger.Info("using most recent directory", "dir", dir)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigChan
				logger.Info("received signal, stopping", "signal", sig)
				cancel()
			}()

			applyMemoryLimit(cfg, logger)
			runNightEndProcessing(ctx, dir, cfg, nil, nil, logger)
			return nil
		},
	}

	cmd.Flags().StringVarP(&dir, "dir", "d", "", "capture directory to process (default: most recent)")
	return cmd
}

// webpCmd converts PNG images in a capture directory to WebP.
func webpCmd() *cobra.Command {
	var (
		dir             string
		quality         int
		deleteOriginals bool
	)

	cmd := &cobra.Command{
		Use:   "webp",
		Short: "Convert captured PNGs to WebP",
		Long:  "Converts all captured PNG images in a directory to WebP format using cwebp. Requires the 'webp' package (apt-get install webp). Skips keogram and star-trails files.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if dir == "" {
				dirs, err := capture.ListDateDirs(cfg.Output.Directory)
				if err != nil || len(dirs) == 0 {
					return fmt.Errorf("no capture directories found in %s", cfg.Output.Directory)
				}
				dir = dirs[0]
				logger.Info("using most recent directory", "dir", dir)
			}

			if quality <= 0 {
				quality = cfg.Output.WebP.Quality
			}
			if !cmd.Flags().Changed("delete-originals") {
				deleteOriginals = cfg.Output.WebP.DeleteOriginals
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			return convert.ConvertPNGsToWebP(ctx, dir, quality, deleteOriginals, logger)
		},
	}

	cmd.Flags().StringVarP(&dir, "dir", "d", "", "directory to convert (default: most recent)")
	cmd.Flags().IntVarP(&quality, "quality", "q", 0, "WebP quality 0-100 (default: from config)")
	cmd.Flags().BoolVar(&deleteOriginals, "delete-originals", false, "remove source PNGs after successful conversion (default: from config)")
	return cmd
}
