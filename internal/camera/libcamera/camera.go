// Package libcamera provides a Camera implementation that wraps the libcamera-still CLI tool.
// This is the same approach used by AllskyTeam/allsky for Raspberry Pi cameras.
package libcamera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dburman/nightsky/internal/camera"
)

// Camera implements camera.Camera using the rpicam-still or libcamera-still CLI tool.
// On Bookworm+, the binaries were renamed from libcamera-* to rpicam-*.
// We try rpicam-still first and fall back to libcamera-still.
type Camera struct {
	device    int
	logger    *slog.Logger
	info      camera.CameraInfo
	tmpDir    string
	stillBin  string // resolved path to rpicam-still or libcamera-still
}

// New creates a new libcamera camera instance.
func New(device int, logger *slog.Logger) *Camera {
	return &Camera{
		device: device,
		logger: logger,
	}
}

func (c *Camera) Open() error {
	// Resolve the capture binary. Bookworm+ renamed libcamera-* to rpicam-*.
	c.stillBin = resolveStillBinary(c.logger)
	if c.stillBin == "" {
		return fmt.Errorf("neither rpicam-still nor libcamera-still found in PATH")
	}

	// Create a temp directory for capture output.
	var err error
	c.tmpDir, err = os.MkdirTemp("", "nightsky-libcamera-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}

	// Probe camera info using --list-cameras.
	c.info = c.probeCamera()

	return nil
}

// resolveStillBinary finds rpicam-still (Bookworm+) or falls back to libcamera-still (Bullseye).
func resolveStillBinary(logger *slog.Logger) string {
	// Try rpicam-still first (Bookworm and later).
	if path, err := exec.LookPath("rpicam-still"); err == nil {
		logger.Info("found rpicam-still", "path", path)
		return path
	}

	// Fall back to libcamera-still (Bullseye and earlier).
	if path, err := exec.LookPath("libcamera-still"); err == nil {
		logger.Info("found libcamera-still (legacy)", "path", path)
		return path
	}

	return ""
}

func (c *Camera) Close() error {
	if c.tmpDir != "" {
		os.RemoveAll(c.tmpDir)
	}
	return nil
}

func (c *Camera) Info() camera.CameraInfo {
	return c.info
}

func (c *Camera) Capture(ctx context.Context, settings camera.CaptureSettings) (*camera.CaptureResult, error) {
	ext := "png"
	encoding := "png"
	if settings.Format == camera.FormatJPEG {
		ext = "jpg"
		encoding = "jpg"
	}

	outputPath := filepath.Join(c.tmpDir, fmt.Sprintf("capture.%s", ext))
	metadataPath := filepath.Join(c.tmpDir, "metadata.txt")

	args := c.buildArgs(settings, outputPath, metadataPath, encoding)

	c.logger.Debug("capture command", "bin", c.stillBin, "args", strings.Join(args, " "))

	// Use the caller's context directly. The capture loop already sets a
	// deadline of exposure+120s, which covers slow rpicam-still startup on
	// a loaded Pi (typically 3-10s), the shutter open time, and PNG encoding.
	cmd := exec.CommandContext(ctx, c.stillBin, args...)
	cmd.Env = append(os.Environ(), "LIBCAMERA_LOG_LEVELS=ERROR,FATAL")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	startTime := time.Now()

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s failed: %w\nstderr: %s", filepath.Base(c.stillBin), err, stderr.String())
	}

	captureTime := time.Since(startTime)

	// Read the captured image.
	imgData, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read captured image: %w", err)
	}

	// Decode the image.
	var img image.Image
	reader := bytes.NewReader(imgData)
	switch ext {
	case "png":
		img, err = png.Decode(reader)
	case "jpg":
		img, err = jpeg.Decode(reader)
	}
	if err != nil {
		return nil, fmt.Errorf("decode captured image: %w", err)
	}

	// Parse metadata for actual capture parameters.
	meta := c.parseMetadata(metadataPath)
	meanBrightness := computeMeanBrightness(img)

	bounds := img.Bounds()

	result := &camera.CaptureResult{
		Image:   img,
		RawData: imgData,
		Meta: camera.CaptureMeta{
			Timestamp:      startTime,
			Exposure:       captureTime,
			Gain:           meta.gain,
			Temperature:    meta.temperature,
			Width:          bounds.Dx(),
			Height:         bounds.Dy(),
			Format:         settings.Format,
			Binning:        settings.Binning,
			MeanBrightness: meanBrightness,
		},
	}

	// Use actual exposure from metadata if available.
	if meta.exposure > 0 {
		result.Meta.Exposure = meta.exposure
	}

	c.logger.Info("capture complete",
		"exposure", result.Meta.Exposure,
		"gain", result.Meta.Gain,
		"temp", fmt.Sprintf("%.1f°C", result.Meta.Temperature),
		"mean", fmt.Sprintf("%.1f", meanBrightness),
		"size", fmt.Sprintf("%dx%d", bounds.Dx(), bounds.Dy()),
	)

	return result, nil
}

func (c *Camera) SetCooler(_ camera.CoolerSettings) error {
	// RPi cameras don't have coolers.
	return nil
}

func (c *Camera) Temperature() (float64, error) {
	// Temperature is only available after capture via metadata.
	return 0, nil
}

func (c *Camera) buildArgs(s camera.CaptureSettings, outputPath, metadataPath, encoding string) []string {
	args := []string{
		"--camera", strconv.Itoa(c.device),
		"--immediate",
		"--nopreview",
		"--output", outputPath,
		"--metadata", metadataPath,
		"--metadata-format", "txt",
		"--encoding", encoding,
	}

	// Exposure in microseconds. 0 = auto.
	exposureUs := int64(s.Exposure / time.Microsecond)
	if exposureUs > 0 {
		args = append(args, "--shutter", strconv.FormatInt(exposureUs, 10))
	}

	// Analog gain.
	if s.Gain > 0 {
		args = append(args, "--analoggain", strconv.FormatFloat(s.Gain, 'f', 2, 64))
	}

	// White balance.
	if s.AWB {
		args = append(args, "--awb", "auto")
	} else if s.WBRed > 0 && s.WBBlue > 0 {
		args = append(args, "--awbgains",
			fmt.Sprintf("%.2f,%.2f", s.WBRed, s.WBBlue))
	}

	// Denoise mode.
	if s.Denoise != "" && s.Denoise != "off" {
		args = append(args, "--denoise", s.Denoise)
	}

	// Flip.
	switch s.Flip {
	case 1:
		args = append(args, "--hflip")
	case 2:
		args = append(args, "--vflip")
	case 3:
		args = append(args, "--hflip", "--vflip")
	}

	// JPEG quality.
	if encoding == "jpg" {
		args = append(args, "--quality", "95")
	}

	return args
}

type captureMetadata struct {
	exposure    time.Duration
	gain        float64
	temperature float64
}

func (c *Camera) parseMetadata(path string) captureMetadata {
	meta := captureMetadata{}

	data, err := os.ReadFile(path)
	if err != nil {
		c.logger.Debug("no metadata file", "error", err)
		return meta
	}

	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "ExposureTime":
			if us, err := strconv.ParseInt(val, 10, 64); err == nil {
				meta.exposure = time.Duration(us) * time.Microsecond
			}
		case "AnalogueGain":
			if g, err := strconv.ParseFloat(val, 64); err == nil {
				meta.gain = g
			}
		case "SensorTemperature":
			if t, err := strconv.ParseFloat(val, 64); err == nil {
				meta.temperature = t
			}
		}
	}

	return meta
}

func (c *Camera) probeCamera() camera.CameraInfo {
	info := camera.CameraInfo{
		Name:  "Raspberry Pi Camera",
		Model: "unknown",
	}

	cmd := exec.Command(c.stillBin, "--list-cameras")
	cmd.Env = append(os.Environ(), "LIBCAMERA_LOG_LEVELS=ERROR,FATAL")

	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Warn("failed to probe camera", "error", err)
		return info
	}

	// Parse output like:
	// 0 : imx477 [4056x3040 12-bit RGGB] (/base/soc/i2c0mux/...)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, strconv.Itoa(c.device)+" :") {
			continue
		}

		// Extract model name.
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		remainder := strings.TrimSpace(parts[1])

		// Model is first word.
		fields := strings.Fields(remainder)
		if len(fields) > 0 {
			info.Model = fields[0]
			info.Name = fmt.Sprintf("RPi %s", fields[0])
		}

		// Resolution is in brackets like [4056x3040 ...]
		if idx := strings.Index(remainder, "["); idx >= 0 {
			if end := strings.Index(remainder[idx:], "]"); end >= 0 {
				bracketContent := remainder[idx+1 : idx+end]
				resParts := strings.Fields(bracketContent)
				if len(resParts) > 0 {
					res := strings.SplitN(resParts[0], "x", 2)
					if len(res) == 2 {
						if w, err := strconv.Atoi(res[0]); err == nil {
							info.MaxWidth = w
						}
						if h, err := strconv.Atoi(res[1]); err == nil {
							info.MaxHeight = h
						}
					}
				}
				// Check for Bayer pattern.
				for _, part := range resParts {
					switch part {
					case "RGGB", "BGGR", "GRBG", "GBRG":
						info.BayerPattern = part
						info.IsColor = true
					}
				}
			}
		}

		c.logger.Info("detected camera",
			"model", info.Model,
			"resolution", fmt.Sprintf("%dx%d", info.MaxWidth, info.MaxHeight),
			"bayer", info.BayerPattern,
		)
		break
	}

	return info
}

func computeMeanBrightness(img image.Image) float64 {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w == 0 || h == 0 {
		return 0
	}

	var sum float64
	var count int
	step := 4

	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			lum := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
			sum += lum
			count++
		}
	}

	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
