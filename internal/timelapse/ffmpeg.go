// Package timelapse provides ffmpeg-based video assembly from captured images.
package timelapse

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Config holds parameters for timelapse video generation.
type Config struct {
	FPS     int
	Bitrate string
	Codec   string
	// CRF sets the constant-rate-factor quality level (0 = use Bitrate instead).
	// Typical values: 18–23 for libx264, 24–28 for libx265.
	// Lower = better quality, larger file. CRF takes precedence over Bitrate.
	CRF int
	// Deflicker smooths per-frame brightness variation caused by clouds,
	// auto-exposure steps, and atmospheric changes.
	Deflicker bool
}

// DefaultConfig returns sensible timelapse defaults.
func DefaultConfig() Config {
	return Config{
		FPS:       25,
		Bitrate:   "2000k",
		Codec:     "libx264",
		CRF:       0,
		Deflicker: false,
	}
}

// Generate creates a timelapse video from all images in the given directory.
// The output file is written to <dir>/timelapse-<date>.mp4.
func Generate(ctx context.Context, imageDir string, cfg Config, logger *slog.Logger) (string, error) {
	// Verify ffmpeg is available.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	// Collect image files, sorted by name (which includes timestamp).
	images, err := collectImages(imageDir)
	if err != nil {
		return "", fmt.Errorf("collect images: %w", err)
	}

	if len(images) == 0 {
		return "", fmt.Errorf("no images found in %s", imageDir)
	}

	logger.Info("generating timelapse",
		"dir", imageDir,
		"images", len(images),
		"fps", cfg.FPS,
		"codec", cfg.Codec,
	)

	// Create a temporary file list for ffmpeg's concat demuxer.
	// This avoids issues with glob patterns and mixed image formats.
	listPath := filepath.Join(imageDir, "timelapse_input.txt")
	if err := writeFileList(listPath, images); err != nil {
		return "", fmt.Errorf("write file list: %w", err)
	}
	defer os.Remove(listPath)

	date := filepath.Base(imageDir)
	outputPath := filepath.Join(imageDir, "timelapse-"+date+".mp4")
	tmpOutput := filepath.Join(imageDir, "timelapse-"+date+".tmp.mp4")

	// Build ffmpeg command.
	args := []string{
		"-y",
		"-r", fmt.Sprintf("%d", cfg.FPS),
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-vcodec", cfg.Codec,
	}

	// Quality: CRF takes precedence over fixed bitrate.
	if cfg.CRF > 0 {
		args = append(args, "-crf", fmt.Sprintf("%d", cfg.CRF))
	} else {
		args = append(args, "-b:v", cfg.Bitrate)
	}

	// Deflicker filter smooths per-frame brightness variation.
	if cfg.Deflicker {
		args = append(args, "-vf", "deflicker=size=5:mode=am")
	}

	args = append(args,
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		tmpOutput,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	startTime := time.Now()
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w\nstderr: %s", err, stderr.String())
	}

	if err := os.Rename(tmpOutput, outputPath); err != nil {
		return "", fmt.Errorf("rename output: %w", err)
	}

	stat, _ := os.Stat(outputPath)
	sizeMB := float64(0)
	if stat != nil {
		sizeMB = float64(stat.Size()) / (1024 * 1024)
	}

	logger.Info("timelapse complete",
		"output", outputPath,
		"size_mb", fmt.Sprintf("%.1f", sizeMB),
		"duration_s", time.Since(startTime).Seconds(),
		"frames", len(images),
		"video_duration_s", fmt.Sprintf("%.1f", float64(len(images))/float64(cfg.FPS)),
	)

	return outputPath, nil
}

// collectImages returns sorted image file paths from a directory.
func collectImages(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var images []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if strings.HasSuffix(name, ".jpg") ||
			strings.HasSuffix(name, ".jpeg") ||
			strings.HasSuffix(name, ".png") {
			images = append(images, filepath.Join(dir, entry.Name()))
		}
	}

	sort.Strings(images)
	return images, nil
}

// writeFileList writes an ffmpeg concat demuxer file list.
// Paths are written as basenames since the list file lives in the same directory
// as the images, and ffmpeg resolves paths relative to the list file location.
func writeFileList(path string, images []string) error {
	var buf strings.Builder
	for _, img := range images {
		fmt.Fprintf(&buf, "file '%s'\n", filepath.Base(img))
	}
	return os.WriteFile(path, []byte(buf.String()), 0644)
}
