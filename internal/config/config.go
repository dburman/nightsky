package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

var Version = "dev"

// Config is the top-level configuration for nightsky.
type Config struct {
	// MemoryLimitMB sets a soft Go heap limit (runtime/debug.SetMemoryLimit)
	// so the garbage collector works harder as the limit approaches instead
	// of letting the kernel OOM killer decide. Recommended ~250 on 512 MB
	// boards (leave headroom for ffmpeg). 0 = no limit. Ignored when the
	// GOMEMLIMIT environment variable is set.
	MemoryLimitMB int `mapstructure:"memory_limit_mb" json:"memory_limit_mb"`

	Camera   CameraConfig   `mapstructure:"camera"    json:"camera"`
	Location LocationConfig `mapstructure:"location"  json:"location"`
	Day      ModeConfig     `mapstructure:"day"       json:"day"`
	Night    ModeConfig     `mapstructure:"night"     json:"night"`
	Output   OutputConfig   `mapstructure:"output"    json:"output"`
	Upload   UploadConfig   `mapstructure:"upload"    json:"upload"`
	Dark     DarkConfig     `mapstructure:"dark"      json:"dark"`
	Flat     FlatConfig     `mapstructure:"flat"      json:"flat"`
	Alerts   AlertsConfig   `mapstructure:"alerts"    json:"alerts"`
}

// AlertsConfig controls push notifications via a webhook (ntfy-compatible:
// message body + Title header).
type AlertsConfig struct {
	// WebhookURL receives notifications (e.g. https://ntfy.sh/<topic>).
	// Empty disables all alerts.
	WebhookURL string `mapstructure:"webhook_url" json:"webhook_url,omitempty"`
	// BaseURL, when set, is used to build links in messages (e.g.
	// http://astrocam:8080 → links to /latest).
	BaseURL string `mapstructure:"base_url" json:"base_url"`
	// NightSummary sends a dawn message with the night's statistics.
	NightSummary bool `mapstructure:"night_summary" json:"night_summary"`
	// Aurora detection settings.
	Aurora AuroraAlertConfig `mapstructure:"aurora" json:"aurora"`
}

// AuroraAlertConfig tunes the green-excess aurora detector.
type AuroraAlertConfig struct {
	Enabled bool `mapstructure:"enabled" json:"enabled"`
	// RatioThreshold: green ratio must exceed baseline × this (default 1.3).
	RatioThreshold float64 `mapstructure:"ratio_threshold" json:"ratio_threshold"`
	// MinFrames of sustained excess before alerting (default 3).
	MinFrames int `mapstructure:"min_frames" json:"min_frames"`
	// MaxCloud suppresses detection above this coverage (default 0.5).
	MaxCloud float64 `mapstructure:"max_cloud" json:"max_cloud"`
}

// CameraConfig identifies which camera backend and device to use.
type CameraConfig struct {
	// Type: "zwo" or "libcamera"
	Type string `mapstructure:"type"          json:"type"`
	// Index for ZWO cameras when multiple are connected (default 0).
	Index int `mapstructure:"index"         json:"index"`
	// Device path for libcamera (default 0).
	Device int `mapstructure:"device"        json:"device"`
	// USB bandwidth limit for ZWO (40-100, default 80).
	USBBandwidth int `mapstructure:"usb_bandwidth" json:"usb_bandwidth"`
	// Flip: 0=none, 1=horizontal, 2=vertical, 3=both
	Flip int `mapstructure:"flip"          json:"flip"`
}

// LocationConfig provides geographic position for sun calculations.
type LocationConfig struct {
	Latitude  float64 `mapstructure:"latitude"  json:"latitude"`
	Longitude float64 `mapstructure:"longitude" json:"longitude"`
	// Sun altitude angle in degrees to transition day/night (default -6 = civil twilight).
	Angle float64 `mapstructure:"angle" json:"angle"`
	// GPS enables automatic lat/lon lookup from a local gpsd daemon.
	// When enabled, a fix is fetched at startup and overrides latitude/longitude.
	GPS bool `mapstructure:"gps" json:"gps"`
	// GPSAddr is the gpsd address (default "localhost:2947").
	GPSAddr string `mapstructure:"gps_addr" json:"gps_addr"`
}

// ModeConfig holds capture settings for either day or night mode.
type ModeConfig struct {
	// Exposure time. Parsed as duration (e.g., "100ms", "10s", "30s").
	Exposure time.Duration `mapstructure:"exposure"          json:"exposure"`
	// Maximum exposure for auto-exposure mode.
	MaxExposure time.Duration `mapstructure:"max_exposure"      json:"max_exposure"`
	// Gain (0-600 for ZWO, 0-16 for libcamera).
	Gain float64 `mapstructure:"gain"              json:"gain"`
	// Maximum gain for auto-exposure mode.
	MaxGain float64 `mapstructure:"max_gain"          json:"max_gain"`
	// AutoExposure enables the mean-brightness auto-exposure algorithm.
	AutoExposure bool `mapstructure:"auto_exposure"     json:"auto_exposure"`
	// TargetBrightness is the target mean pixel brightness (0-255, default 90 night / 160 day).
	TargetBrightness float64 `mapstructure:"target_brightness" json:"target_brightness"`
	// Delay between captures.
	Delay time.Duration `mapstructure:"delay"             json:"delay"`
	// Binning (1, 2, or 4). Higher values increase sensitivity at lower resolution.
	Binning int `mapstructure:"binning"           json:"binning"`
	// White balance red component (ZWO: 0-99, libcamera: 0.0-10.0).
	WBRed float64 `mapstructure:"wb_red"            json:"wb_red"`
	// White balance blue component.
	WBBlue float64 `mapstructure:"wb_blue"           json:"wb_blue"`
	// AWB enables auto white balance (libcamera only).
	AWB bool `mapstructure:"awb"               json:"awb"`
	// ImageType: "jpg" or "png"
	ImageType string `mapstructure:"image_type"        json:"image_type"`
	// Quality: JPEG quality 1-100 (default 95).
	Quality int `mapstructure:"quality"           json:"quality"`
	// SkipFrames: number of frames to discard after mode transition.
	SkipFrames int `mapstructure:"skip_frames"       json:"skip_frames"`
	// Denoise: "off", "cdn_off", "cdn_fast", "cdn_hq" (libcamera only).
	Denoise string `mapstructure:"denoise"           json:"denoise"`
	// Cooler settings (ZWO cameras with cooling).
	CoolerEnabled bool    `mapstructure:"cooler_enabled"    json:"cooler_enabled"`
	CoolerTarget  float64 `mapstructure:"cooler_target"     json:"cooler_target"`
	// MeteringZone sets the region used to compute mean brightness for
	// auto-exposure. "full" = whole frame (default); "center" = inner 50%
	// radius circle (zenith for upward-pointing all-sky cameras); "top" = top
	// third of frame.
	MeteringZone string `mapstructure:"metering_zone"     json:"metering_zone"`
	// SaveRaw saves a DNG file alongside each captured image (libcamera only).
	// DNG preserves the full sensor bit depth and Bayer pattern for
	// post-processing in tools like Lightroom, darktable, or RawTherapee.
	SaveRaw bool `mapstructure:"save_raw"          json:"save_raw"`
	// MoonTargetBoost raises the auto-exposure target while the moon is up,
	// proportionally to its illuminated fraction: effective target =
	// target_brightness × (1 + boost × illumination). Stops auto-exposure
	// from fighting moonlight with maximum gain. 0 = disabled. Night mode
	// only; typical value 0.5.
	MoonTargetBoost float64 `mapstructure:"moon_target_boost" json:"moon_target_boost"`
}

// OutputConfig controls where and how images are saved.
type OutputConfig struct {
	// Directory is the base output directory. Images are saved to <dir>/YYYY-MM-DD/.
	Directory string `mapstructure:"directory"        json:"directory"`
	// MetricsDir overrides where the live .metrics.json is written/read.
	// It's rewritten after every frame, so pointing it at tmpfs (e.g.
	// /run/nightsky via systemd RuntimeDirectory=nightsky) avoids constant
	// small writes wearing the SD card. Empty = use Directory. The capture
	// and serve processes must agree on this value.
	MetricsDir string `mapstructure:"metrics_dir"      json:"metrics_dir"`
	// FilenamePrefix for saved images (default "allsky").
	FilenamePrefix string `mapstructure:"filename_prefix"  json:"filename_prefix"`
	// DaysToKeep: delete directories older than this many days at end of night. 0 = disabled.
	DaysToKeep int `mapstructure:"days_to_keep"        json:"days_to_keep"`
	// MinFreeGB: when set, delete the oldest date directories at end of night
	// until free disk space on the output volume exceeds this threshold (in GB).
	// Takes precedence over DaysToKeep. Today's directory is never removed.
	MinFreeGB float64 `mapstructure:"min_free_gb"          json:"min_free_gb"`
	// PruneRawAfterDays: after N days, delete raw captured images from a night
	// directory but keep the synthesized outputs (timelapse, keogram, star
	// trails, WB analysis). 0 = disabled. Runs independently of DaysToKeep /
	// MinFreeGB — use together to retain summaries longer than raw frames.
	PruneRawAfterDays int `mapstructure:"prune_raw_after_days" json:"prune_raw_after_days"`
	// RawCalibration is the single switch for the experimental linear-raw
	// pipeline. When true, DNG capture is implied for both modes (save_raw is
	// forced on at config load) and each frame is decoded from its DNG,
	// dark-subtracted against a raw master (darkraw_*.png), metered, and
	// debayered in linear space before the usual flat/stretch/overlay steps;
	// per-frame errors fall back to the standard 8-bit image. libcamera only.
	// When false (default), capture runs exactly as it always has — none of
	// the raw pipeline code executes.
	RawCalibration bool `mapstructure:"raw_calibration" json:"raw_calibration"`
	// Overlay enables timestamp/metadata text on images.
	Overlay bool `mapstructure:"overlay"          json:"overlay"`
	// OverlayFontSize in points (default 24).
	OverlayFontSize float64 `mapstructure:"overlay_font_size" json:"overlay_font_size"`
	// Stretch controls histogram stretching applied to saved images.
	Stretch StretchConfig `mapstructure:"stretch"          json:"stretch"`
	// Timelapse settings.
	Timelapse TimelapseConfig `mapstructure:"timelapse"        json:"timelapse"`
	// Keogram settings.
	Keogram KeogramConfig `mapstructure:"keogram"          json:"keogram"`
	// StarTrails settings.
	StarTrails StarTrailsConfig `mapstructure:"startrails"       json:"startrails"`
	// WebP conversion settings.
	WebP WebPConfig `mapstructure:"webp"             json:"webp"`
}

// MetricsDirectory returns where the live metrics file lives: MetricsDir if
// set, otherwise the output directory.
func (o OutputConfig) MetricsDirectory() string {
	if o.MetricsDir != "" {
		return o.MetricsDir
	}
	return o.Directory
}

// KeogramConfig controls end-of-night keogram generation.
type KeogramConfig struct {
	// Enabled generates a keogram at end of night (default true).
	Enabled bool `mapstructure:"enabled" json:"enabled"`
}

// StarTrailsConfig controls end-of-night star trails generation.
type StarTrailsConfig struct {
	// Enabled generates a star trails image at end of night (default true).
	Enabled bool `mapstructure:"enabled" json:"enabled"`
}

// StretchConfig controls histogram stretching applied to saved images.
type StretchConfig struct {
	// Enabled applies a histogram stretch to saved images. Raw scientific data
	// is not affected — this only changes the saved JPEG/PNG appearance.
	Enabled bool `mapstructure:"enabled"      json:"enabled"`
	// Mode: "auto" (percentile-based, default) or "manual".
	Mode string `mapstructure:"mode"         json:"mode"`
	// BlackPoint is the input level (0–255) mapped to black in manual mode.
	BlackPoint int `mapstructure:"black_point"  json:"black_point"`
	// WhitePoint is the input level (0–255) mapped to white in manual mode.
	WhitePoint int `mapstructure:"white_point"  json:"white_point"`
	// AutoBlackPercentile is the percentile used for the black point in auto
	// mode (0–100). Higher values push the black point closer to the sky
	// background, making the sky go darker and revealing faint stars.
	// Default 10 works well for dark sites; reduce toward 1 for light-polluted skies.
	AutoBlackPercentile float64 `mapstructure:"auto_black_percentile" json:"auto_black_percentile"`
	// AutoWhitePercentile is the percentile used for the white point in auto
	// mode (0–100). Higher values preserve more star brightness before clipping.
	// Default 99.9 avoids clipping all but the very brightest pixels.
	AutoWhitePercentile float64 `mapstructure:"auto_white_percentile" json:"auto_white_percentile"`
}

// WebPConfig controls end-of-night PNG→WebP conversion.
type WebPConfig struct {
	// Enabled converts captured PNG images to WebP at end of night.
	Enabled bool `mapstructure:"enabled"          json:"enabled"`
	// Quality sets the lossy WebP quality (0–100, default 85).
	Quality int `mapstructure:"quality"          json:"quality"`
	// DeleteOriginals removes the source PNG after successful conversion.
	DeleteOriginals bool `mapstructure:"delete_originals" json:"delete_originals"`
}

// TimelapseConfig controls video generation.
type TimelapseConfig struct {
	// Enabled enables timelapse generation at end of night.
	Enabled bool `mapstructure:"enabled"    json:"enabled"`
	// FPS for the timelapse video (default 25).
	FPS int `mapstructure:"fps"        json:"fps"`
	// Bitrate for the video (default "2000k"). Ignored when CRF > 0.
	Bitrate string `mapstructure:"bitrate"    json:"bitrate"`
	// Codec: "libx264" (default), "libx265", "libaom-av1", "libsvtav1".
	Codec string `mapstructure:"codec"      json:"codec"`
	// CRF sets the constant-rate-factor quality (0 = use Bitrate).
	// Typical: 18–23 for libx264, 24–28 for libx265. Lower = better quality.
	CRF int `mapstructure:"crf"        json:"crf"`
	// Deflicker smooths per-frame brightness variation in the output video.
	Deflicker bool `mapstructure:"deflicker"  json:"deflicker"`
	// Preset selects the encoder speed/efficiency trade-off (e.g. "slow" for
	// x264/x265, "0"–"13" for SVT-AV1). Slower = smaller at equal quality.
	Preset string `mapstructure:"preset" json:"preset"`
	// GOP sets the max keyframe interval (-g). Long GOPs shrink a
	// static-camera timelapse. 0 = encoder default.
	GOP int `mapstructure:"gop" json:"gop"`
	// Tune passes an encoder -tune value (codec-specific). Empty = none.
	Tune string `mapstructure:"tune" json:"tune"`
	// Threads caps encoder threads (-threads, plus lp=N for SVT-AV1).
	// Fewer threads = lower peak memory and a smaller CPU/power spike —
	// set to 2 on low-memory boards like the Pi Zero 2. 0 = encoder default.
	Threads int `mapstructure:"threads" json:"threads"`
	// ThermalLimitC defers encode start while the SoC is hotter than this
	// (°C, read from /sys/class/thermal), rechecking every 2 minutes for up
	// to 30 minutes. Prevents piling encode heat onto an already-hot chip
	// (frequency capping, SDIO WiFi instability on Pi Zero 2). Recommended
	// 70 on Pi-class hardware. 0 = disabled.
	ThermalLimitC int `mapstructure:"thermal_limit_c" json:"thermal_limit_c"`
	// SegmentFrames, when > 0, enables iterative mode: a video segment is
	// encoded after every N captured frames. At end of night the segments are
	// concatenated with stream copy (no re-encode). 0 = encode at end of night.
	SegmentFrames int `mapstructure:"segment_frames" json:"segment_frames"`
}

// UploadConfig controls how images/videos are uploaded.
type UploadConfig struct {
	// UploadImages uploads each captured image (can be bandwidth-heavy).
	UploadImages bool `mapstructure:"upload_images"    json:"upload_images"`
	// UploadTimelapse uploads the timelapse video at end of night.
	UploadTimelapse bool `mapstructure:"upload_timelapse" json:"upload_timelapse"`
	// S3 configuration.
	S3 S3Config `mapstructure:"s3"               json:"s3"`
	// HTTP upload configuration.
	HTTP HTTPConfig `mapstructure:"http"             json:"http"`
}

// S3Config for AWS S3 uploads.
type S3Config struct {
	Enabled  bool   `mapstructure:"enabled"  json:"enabled"`
	Bucket   string `mapstructure:"bucket"   json:"bucket"`
	Region   string `mapstructure:"region"   json:"region"`
	Prefix   string `mapstructure:"prefix"   json:"prefix"`
	Endpoint string `mapstructure:"endpoint" json:"endpoint"`
}

// HTTPConfig for HTTP POST uploads.
type HTTPConfig struct {
	Enabled bool   `mapstructure:"enabled"                json:"enabled"`
	URL     string `mapstructure:"url"                    json:"url"`
	// Authorization header value (e.g., "Bearer <token>").
	Authorization string `mapstructure:"authorization" json:"authorization,omitempty"`
}

// DarkConfig controls dark frame capture and subtraction.
type DarkConfig struct {
	// Enabled enables dark frame subtraction during capture.
	Enabled bool `mapstructure:"enabled"   json:"enabled"`
	// Directory where dark frames are stored.
	Directory string `mapstructure:"directory" json:"directory"`
	// Count: number of dark frames to average when capturing darks.
	Count int `mapstructure:"count"     json:"count"`
}

// FlatConfig controls flat-field correction for lens vignetting.
type FlatConfig struct {
	// Enabled applies flat-field correction during capture.
	Enabled bool `mapstructure:"enabled"   json:"enabled"`
	// Directory where the master flat frame is stored.
	Directory string `mapstructure:"directory" json:"directory"`
	// Count: number of flat frames to average when capturing flats.
	Count int `mapstructure:"count"     json:"count"`
}

// Load reads configuration from file and environment, applying defaults.
func Load(configPath string) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("nightsky")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.config/nightsky")
		v.AddConfigPath("/etc/nightsky")
	}

	v.SetEnvPrefix("NIGHTSKY")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	// raw_calibration is the single switch for the raw pipeline: it implies
	// DNG capture in both modes so no second flag must be coordinated. This
	// applies to every command (capture, dark, process) uniformly. When the
	// flag is off, nothing is changed and capture runs as it always has.
	if cfg.Output.RawCalibration {
		cfg.Night.SaveRaw = true
		cfg.Day.SaveRaw = true
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("memory_limit_mb", 0)
	v.SetDefault("camera.type", "libcamera")
	v.SetDefault("camera.index", 0)
	v.SetDefault("camera.device", 0)
	v.SetDefault("camera.usb_bandwidth", 80)
	v.SetDefault("camera.flip", 0)

	v.SetDefault("location.latitude", 0.0)
	v.SetDefault("location.longitude", 0.0)
	v.SetDefault("location.angle", -6.0)
	v.SetDefault("location.gps", false)
	v.SetDefault("location.gps_addr", "localhost:2947")

	// Day defaults
	v.SetDefault("day.exposure", "1ms")
	v.SetDefault("day.max_exposure", "100ms")
	v.SetDefault("day.gain", 1.0)
	v.SetDefault("day.max_gain", 10.0)
	v.SetDefault("day.auto_exposure", true)
	v.SetDefault("day.target_brightness", 160.0)
	v.SetDefault("day.delay", "5s")
	v.SetDefault("day.binning", 1)
	v.SetDefault("day.wb_red", 52)
	v.SetDefault("day.wb_blue", 90)
	v.SetDefault("day.awb", true)
	v.SetDefault("day.image_type", "jpg")
	v.SetDefault("day.quality", 95)
	v.SetDefault("day.skip_frames", 5)
	v.SetDefault("day.denoise", "cdn_fast")

	// Night defaults
	v.SetDefault("night.exposure", "10s")
	v.SetDefault("night.max_exposure", "60s")
	v.SetDefault("night.gain", 200.0)
	v.SetDefault("night.max_gain", 400.0)
	v.SetDefault("night.auto_exposure", true)
	v.SetDefault("night.target_brightness", 90.0)
	v.SetDefault("night.delay", "0s")
	v.SetDefault("night.binning", 1)
	v.SetDefault("night.wb_red", 52)
	v.SetDefault("night.wb_blue", 90)
	v.SetDefault("night.awb", false)
	v.SetDefault("night.image_type", "png")
	v.SetDefault("night.quality", 95)
	v.SetDefault("night.skip_frames", 1)
	v.SetDefault("night.denoise", "off")
	v.SetDefault("night.metering_zone", "center")
	v.SetDefault("day.metering_zone", "full")
	v.SetDefault("night.save_raw", false)
	v.SetDefault("day.save_raw", false)

	v.SetDefault("output.directory", "./output")
	v.SetDefault("output.metrics_dir", "")
	v.SetDefault("output.filename_prefix", "allsky")
	v.SetDefault("output.raw_calibration", false)
	v.SetDefault("output.days_to_keep", 0)
	v.SetDefault("output.min_free_gb", 0.0)
	v.SetDefault("output.prune_raw_after_days", 0)
	v.SetDefault("output.overlay", true)
	v.SetDefault("output.overlay_font_size", 24.0)
	v.SetDefault("output.stretch.enabled", false)
	v.SetDefault("output.stretch.mode", "auto")
	v.SetDefault("output.stretch.black_point", 0)
	v.SetDefault("output.stretch.white_point", 255)
	v.SetDefault("output.stretch.auto_black_percentile", 10.0)
	v.SetDefault("output.stretch.auto_white_percentile", 99.9)
	v.SetDefault("output.timelapse.enabled", true)
	v.SetDefault("output.timelapse.fps", 25)
	v.SetDefault("output.timelapse.bitrate", "2000k")
	v.SetDefault("output.timelapse.codec", "libx264")
	v.SetDefault("output.timelapse.crf", 0)
	v.SetDefault("output.timelapse.deflicker", false)
	v.SetDefault("output.timelapse.segment_frames", 0)
	v.SetDefault("output.timelapse.preset", "")
	v.SetDefault("output.timelapse.gop", 0)
	v.SetDefault("output.timelapse.tune", "")
	v.SetDefault("output.timelapse.threads", 0)
	v.SetDefault("output.timelapse.thermal_limit_c", 0)

	v.SetDefault("output.keogram.enabled", true)
	v.SetDefault("output.startrails.enabled", true)

	v.SetDefault("output.webp.enabled", false)
	v.SetDefault("output.webp.quality", 85)
	v.SetDefault("output.webp.delete_originals", false)

	v.SetDefault("upload.upload_images", false)
	v.SetDefault("upload.upload_timelapse", true)
	v.SetDefault("upload.s3.enabled", false)
	v.SetDefault("upload.s3.region", "us-east-1")
	v.SetDefault("upload.s3.prefix", "nightsky")
	v.SetDefault("upload.http.enabled", false)

	v.SetDefault("dark.enabled", false)
	v.SetDefault("dark.directory", "./darks")
	v.SetDefault("dark.count", 5)

	v.SetDefault("flat.enabled", false)
	v.SetDefault("flat.directory", "./flats")
	v.SetDefault("flat.count", 10)

	v.SetDefault("night.moon_target_boost", 0.0)
	v.SetDefault("day.moon_target_boost", 0.0)

	v.SetDefault("alerts.webhook_url", "")
	v.SetDefault("alerts.base_url", "")
	v.SetDefault("alerts.night_summary", false)
	v.SetDefault("alerts.aurora.enabled", false)
	v.SetDefault("alerts.aurora.ratio_threshold", 1.3)
	v.SetDefault("alerts.aurora.min_frames", 3)
	v.SetDefault("alerts.aurora.max_cloud", 0.5)
}

func validate(cfg *Config) error {
	switch cfg.Camera.Type {
	case "zwo", "libcamera":
	default:
		return fmt.Errorf("camera.type must be 'zwo' or 'libcamera', got %q", cfg.Camera.Type)
	}

	if cfg.Location.Latitude < -90 || cfg.Location.Latitude > 90 {
		return fmt.Errorf("location.latitude must be between -90 and 90")
	}
	if cfg.Location.Longitude < -180 || cfg.Location.Longitude > 180 {
		return fmt.Errorf("location.longitude must be between -180 and 180")
	}

	for _, mode := range []struct {
		name string
		cfg  ModeConfig
	}{{"day", cfg.Day}, {"night", cfg.Night}} {
		if mode.cfg.Exposure < 0 {
			return fmt.Errorf("%s.exposure must be non-negative", mode.name)
		}
		if mode.cfg.Gain < 0 {
			return fmt.Errorf("%s.gain must be non-negative", mode.name)
		}
		if mode.cfg.Binning != 0 && mode.cfg.Binning != 1 && mode.cfg.Binning != 2 && mode.cfg.Binning != 4 {
			return fmt.Errorf("%s.binning must be 1, 2, or 4", mode.name)
		}
		if mode.cfg.Quality < 1 || mode.cfg.Quality > 100 {
			return fmt.Errorf("%s.quality must be between 1 and 100", mode.name)
		}
	}

	return nil
}
