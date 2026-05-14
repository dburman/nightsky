package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/camera/libcamera"
	"github.com/dburman/nightsky/internal/capture"
	"github.com/dburman/nightsky/internal/config"
	"github.com/dburman/nightsky/internal/convert"
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
		infoCmd(),
		cleanCmd(),
		versionCmd(),
		serveCmd(),
		analyzeCmd(),
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

			// Wire up image upload callback.
			if cfg.Upload.UploadImages && (s3Uploader != nil || httpUploader != nil) {
				loop.OnImageSaved = func(path string, meta camera.CaptureMeta) {
					dateDir := meta.Timestamp.Format("2006-01-02")
					if s3Uploader != nil {
						go func() {
							if err := s3Uploader.Upload(ctx, path, dateDir); err != nil {
								logger.Error("S3 image upload failed", "error", err)
							}
						}()
					}
					if httpUploader != nil {
						go func() {
							if err := httpUploader.Upload(ctx, path); err != nil {
								logger.Error("HTTP image upload failed", "error", err)
							}
						}()
					}
				}
			}

			// Wire up end-of-night callback for timelapse + upload.
			loop.OnNightEnd = func(dateDir string) {
				logger.Info("end of night processing", "dir", dateDir)

				// Timelapse (ffmpeg) and keogram run in parallel — they are
				// independent and use different CPU resources.
				var wg sync.WaitGroup

				if cfg.Output.Timelapse.Enabled {
					wg.Add(1)
					go func() {
						defer wg.Done()
						tlCfg := timelapse.Config{
							FPS:       cfg.Output.Timelapse.FPS,
							Bitrate:   cfg.Output.Timelapse.Bitrate,
							Codec:     cfg.Output.Timelapse.Codec,
							CRF:       cfg.Output.Timelapse.CRF,
							Deflicker: cfg.Output.Timelapse.Deflicker,
						}
						videoPath, err := timelapse.Generate(ctx, dateDir, tlCfg, logger)
						if err != nil {
							logger.Error("timelapse generation failed", "error", err)
							return
						}
						if cfg.Upload.UploadTimelapse {
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
					}()
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := keogram.Generate(ctx, dateDir, keogram.DefaultMaxMeanBrightness, logger); err != nil {
						logger.Error("keogram generation failed", "error", err)
					}
				}()

				wg.Wait()

				// Star trails runs after timelapse+keogram complete — it is the
				// most memory-intensive task and benefits from others having
				// released their allocations first.
				if _, err := startrails.Generate(ctx, dateDir, startrails.DefaultMaxMeanBrightness, logger); err != nil {
					logger.Error("star trails generation failed", "error", err)
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

				// WebP conversion — runs after all generation steps so the
				// timelapse input list is never broken by missing PNGs.
				if cfg.Output.WebP.Enabled {
					if err := convert.ConvertPNGsToWebP(ctx, dateDir,
						cfg.Output.WebP.Quality,
						cfg.Output.WebP.DeleteOriginals,
						logger,
					); err != nil {
						logger.Error("webp conversion failed", "error", err)
					}
				}

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
					if err := capture.CleanForSpace(cfg.Output.Directory, cfg.Output.MinFreeGB, logger); err != nil {
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
	cmd.Flags().StringVar(&bitrate, "bitrate", "2000k", "video bitrate")
	cmd.Flags().StringVar(&codec, "codec", "libx264", "video codec")

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

			// Capture darks for both day and night modes.
			for _, mode := range []struct {
				name string
				cfg  config.ModeConfig
			}{
				{"night", cfg.Night},
				{"day", cfg.Day},
			} {
				fmt.Printf("Capturing dark frames for %s mode (exposure=%v, gain=%.0f)...\n",
					mode.name, mode.cfg.Exposure, mode.cfg.Gain)

				settings := camera.CaptureSettings{
					Exposure: mode.cfg.Exposure,
					Gain:     mode.cfg.Gain,
					Binning:  mode.cfg.Binning,
					Format:   camera.FormatRGB24,
				}

				if err := darkMgr.CaptureDarks(context.Background(), settings); err != nil {
					logger.Error("dark frame capture failed", "mode", mode.name, "error", err)
					continue
				}
			}

			fmt.Println("Dark frame capture complete.")
			return nil
		},
	}

	return cmd
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
