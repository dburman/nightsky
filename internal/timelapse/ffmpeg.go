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
	"strconv"
	"strings"
	"time"
)

// Config holds parameters for timelapse video generation.
type Config struct {
	FPS     int
	Bitrate string
	Codec   string
	// CRF sets the constant-rate-factor quality level (0 = use Bitrate instead).
	// Typical values: 18–23 for libx264, 24–28 for libx265, 30–45 for AV1.
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
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

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

	date := filepath.Base(imageDir)
	outputPath := filepath.Join(imageDir, "timelapse-"+date+".mp4")
	tmpOutput := filepath.Join(imageDir, "timelapse-"+date+".tmp.mp4")

	startTime := time.Now()
	if err := encodeImages(ctx, imageDir, images, tmpOutput, cfg); err != nil {
		return "", err
	}

	if err := os.Rename(tmpOutput, outputPath); err != nil {
		return "", fmt.Errorf("rename output: %w", err)
	}

	logCompletion(outputPath, len(images), cfg.FPS, time.Since(startTime), logger)
	return outputPath, nil
}

// GenerateSegment encodes a single segment for iterative timelapse. It reads
// all images from dir, takes the window at segIdx*segmentFrames, and writes
// timelapse-segment-NNNN.mp4 to dir. Safe to call concurrently for different
// segment indices.
func GenerateSegment(ctx context.Context, dir string, segIdx int, segmentFrames int, cfg Config, logger *slog.Logger) (string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	images, err := collectImages(dir)
	if err != nil {
		return "", fmt.Errorf("collect images: %w", err)
	}

	start := segIdx * segmentFrames
	if start >= len(images) {
		return "", fmt.Errorf("segment %d out of range (%d images available)", segIdx, len(images))
	}
	end := start + segmentFrames
	if end > len(images) {
		end = len(images)
	}
	images = images[start:end]

	outputPath := filepath.Join(dir, fmt.Sprintf("timelapse-segment-%04d.mp4", segIdx))
	tmpOutput := filepath.Join(dir, fmt.Sprintf("timelapse-segment-%04d.tmp.mp4", segIdx))

	logger.Info("generating timelapse segment", "dir", dir, "segment", segIdx, "frames", len(images))

	if err := encodeImages(ctx, dir, images, tmpOutput, cfg); err != nil {
		return "", err
	}

	if err := os.Rename(tmpOutput, outputPath); err != nil {
		return "", fmt.Errorf("rename segment: %w", err)
	}

	return outputPath, nil
}

// FinalizeSegments completes an iterative timelapse: encodes any remaining
// frames not yet in a segment, then concatenates all segments with stream copy
// (no re-encode). Segment files are removed on success.
func FinalizeSegments(ctx context.Context, dir string, date string, segmentFrames int, cfg Config, logger *slog.Logger) (string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	images, err := collectImages(dir)
	if err != nil {
		return "", fmt.Errorf("collect images: %w", err)
	}
	if len(images) == 0 {
		return "", fmt.Errorf("no images found in %s", dir)
	}

	removeStaleTmp(dir, logger)

	segments, err := collectSegments(dir)
	if err != nil {
		return "", fmt.Errorf("collect segments: %w", err)
	}

	// Encode every frame window not covered by an existing segment. Coverage
	// is derived from the segment indices in the filenames, not the segment
	// count — a segment that failed mid-night leaves a gap, and counting
	// would silently drop its frames and misalign the remainder.
	present := make(map[int]bool, len(segments))
	for _, seg := range segments {
		if idx, ok := segmentIndex(seg); ok {
			present[idx] = true
		}
	}

	total := (len(images) + segmentFrames - 1) / segmentFrames
	for idx := 0; idx < total; idx++ {
		if present[idx] {
			continue
		}
		start := idx * segmentFrames
		end := min(start+segmentFrames, len(images))
		segPath := filepath.Join(dir, fmt.Sprintf("timelapse-segment-%04d.mp4", idx))
		tmpPath := filepath.Join(dir, fmt.Sprintf("timelapse-segment-%04d.tmp.mp4", idx))
		logger.Info("encoding missing segment", "segment", idx, "frames", end-start)
		if err := encodeImages(ctx, dir, images[start:end], tmpPath, cfg); err != nil {
			return "", fmt.Errorf("encode segment %d: %w", idx, err)
		}
		if err := os.Rename(tmpPath, segPath); err != nil {
			return "", fmt.Errorf("rename segment %d: %w", idx, err)
		}
		segments = append(segments, segPath)
	}
	sort.Strings(segments)

	outputPath := filepath.Join(dir, "timelapse-"+date+".mp4")

	if len(segments) == 1 {
		if err := os.Rename(segments[0], outputPath); err != nil {
			return "", fmt.Errorf("rename single segment: %w", err)
		}
		logger.Info("timelapse complete (single segment)", "output", outputPath)
		return outputPath, nil
	}

	// Write segment list for concat demuxer.
	listPath := filepath.Join(dir, "timelapse_segments.txt")
	var buf strings.Builder
	for _, seg := range segments {
		buf.WriteString(concatListEntry(filepath.Base(seg)))
	}
	if err := os.WriteFile(listPath, []byte(buf.String()), 0644); err != nil {
		return "", fmt.Errorf("write segment list: %w", err)
	}
	defer os.Remove(listPath)

	tmpOutput := filepath.Join(dir, "timelapse-"+date+".tmp.mp4")
	args := []string{
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-c", "copy",
		tmpOutput,
	}

	logger.Info("concatenating timelapse segments", "segments", len(segments))
	cmd := ffmpegCommand(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmpOutput)
		return "", fmt.Errorf("ffmpeg concat failed: %w\nstderr: %s", err, stderr.String())
	}

	if err := os.Rename(tmpOutput, outputPath); err != nil {
		return "", fmt.Errorf("rename output: %w", err)
	}

	for _, seg := range segments {
		os.Remove(seg)
	}

	stat, _ := os.Stat(outputPath)
	sizeMB := float64(0)
	if stat != nil {
		sizeMB = float64(stat.Size()) / (1024 * 1024)
	}
	logger.Info("timelapse complete",
		"output", outputPath,
		"segments", len(segments),
		"size_mb", fmt.Sprintf("%.1f", sizeMB),
	)

	return outputPath, nil
}

// ffmpegCommand builds an ffmpeg invocation at lowest CPU priority via nice.
// Segment encodes run while the camera is capturing; on a Pi an unniced
// encode competes with frame readout and processing. Falls back to a direct
// invocation when nice isn't available.
func ffmpegCommand(ctx context.Context, args ...string) *exec.Cmd {
	if nicePath, err := exec.LookPath("nice"); err == nil {
		return exec.CommandContext(ctx, nicePath, append([]string{"-n", "19", "ffmpeg"}, args...)...)
	}
	return exec.CommandContext(ctx, "ffmpeg", args...)
}

// encodeImages runs ffmpeg to encode images into a video file at outputPath.
func encodeImages(ctx context.Context, dir string, images []string, outputPath string, cfg Config) error {
	listFile, err := os.CreateTemp(dir, "timelapse-*.txt")
	if err != nil {
		return fmt.Errorf("create temp list: %w", err)
	}
	listPath := listFile.Name()
	listFile.Close()
	defer os.Remove(listPath)

	if err := writeFileList(listPath, images); err != nil {
		return fmt.Errorf("write file list: %w", err)
	}

	// -r (not -framerate) is required here: -framerate is an image2-demuxer
	// option and the concat demuxer rejects it ("Option framerate not found").
	// As an input option before -i, -r forces the input frame rate.
	args := []string{
		"-y",
		"-r", fmt.Sprintf("%d", cfg.FPS),
		"-f", "concat",
		"-safe", "0",
	}

	// ffmpeg's format probing misidentifies WebP RIFF containers as MJPEG.
	// Force the WebP decoder when all input frames are WebP; the flag must
	// appear before -i to apply to the input stream.
	if allWebP(images) {
		args = append(args, "-c:v", "webp")
	}

	args = append(args,
		"-i", listPath,
		"-vcodec", cfg.Codec,
	)

	// Quality: CRF takes precedence over fixed bitrate.
	// AV1 encoders (libaom-av1, libsvtav1) require -b:v 0 alongside -crf to
	// enable true constant-quality mode rather than constrained-quality mode.
	if cfg.CRF > 0 {
		args = append(args, "-crf", fmt.Sprintf("%d", cfg.CRF))
		if strings.Contains(cfg.Codec, "av1") {
			args = append(args, "-b:v", "0")
		}
	} else {
		args = append(args, "-b:v", cfg.Bitrate)
	}

	if cfg.Deflicker {
		args = append(args, "-vf", "deflicker=size=5:mode=am")
	}

	args = append(args,
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		outputPath,
	)

	cmd := ffmpegCommand(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("ffmpeg failed: %w\nstderr: %s", err, stderr.String())
	}
	return nil
}

func logCompletion(outputPath string, frames int, fps int, elapsed time.Duration, logger *slog.Logger) {
	stat, _ := os.Stat(outputPath)
	sizeMB := float64(0)
	if stat != nil {
		sizeMB = float64(stat.Size()) / (1024 * 1024)
	}
	logger.Info("timelapse complete",
		"output", outputPath,
		"size_mb", fmt.Sprintf("%.1f", sizeMB),
		"duration_s", elapsed.Seconds(),
		"frames", frames,
		"video_duration_s", fmt.Sprintf("%.1f", float64(frames)/float64(fps)),
	)
}

// collectImages returns sorted captured-image file paths from a directory.
// Synthesized outputs (keogram, startrails, timelapse, WB analysis, cloud
// report) are excluded — they have different dimensions or frame rates and
// must not appear in the timelapse frame sequence.
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
		if strings.HasPrefix(name, "timelapse-") ||
			strings.HasPrefix(name, "keogram-") ||
			strings.HasPrefix(name, "startrails-") ||
			strings.HasPrefix(name, "wb-analysis-") ||
			strings.HasPrefix(name, "cloud-") {
			continue
		}
		if strings.HasSuffix(name, ".jpg") ||
			strings.HasSuffix(name, ".jpeg") ||
			strings.HasSuffix(name, ".png") ||
			strings.HasSuffix(name, ".webp") {
			images = append(images, filepath.Join(dir, entry.Name()))
		}
	}

	sort.Strings(images)
	return images, nil
}

// segmentIndex parses the numeric index from a timelapse-segment-NNNN.mp4
// path. Returns false for names that don't match the segment pattern.
func segmentIndex(path string) (int, bool) {
	name := strings.ToLower(filepath.Base(path))
	name = strings.TrimPrefix(name, "timelapse-segment-")
	name = strings.TrimSuffix(name, ".mp4")
	idx, err := strconv.Atoi(name)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

// collectSegments returns sorted timelapse-segment-*.mp4 paths from dir.
// In-progress or stale .tmp.mp4 files (left behind when an encode is killed
// mid-write) are excluded — concatenating a truncated segment would corrupt
// the final video.
func collectSegments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasPrefix(name, "timelapse-segment-") || strings.Contains(name, ".tmp.") {
			continue
		}
		segs = append(segs, filepath.Join(dir, e.Name()))
	}
	sort.Strings(segs)
	return segs, nil
}

// removeStaleTmp deletes leftover timelapse-*.tmp.mp4 files from dir. Called
// before finalizing so segments killed mid-encode on a previous run don't
// linger alongside the real outputs.
func removeStaleTmp(dir string, logger *slog.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if e.IsDir() || !strings.HasPrefix(name, "timelapse-") || !strings.HasSuffix(name, ".tmp.mp4") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err == nil {
			logger.Info("removed stale timelapse temp file", "path", path)
		}
	}
}

// allWebP returns true when every image in the list is a WebP file. Used to
// decide whether to force the WebP input decoder.
func allWebP(images []string) bool {
	for _, img := range images {
		if !strings.HasSuffix(strings.ToLower(img), ".webp") {
			return false
		}
	}
	return len(images) > 0
}

// concatListEntry formats one ffmpeg concat-demuxer list line, escaping
// single quotes in the filename ('  →  '\'') so a quote in the configured
// filename prefix can't break the list syntax.
func concatListEntry(name string) string {
	return "file '" + strings.ReplaceAll(name, "'", `'\''`) + "'\n"
}

// writeFileList writes an ffmpeg concat demuxer file list.
// Paths are written as basenames since the list file lives in the same directory
// as the images, and ffmpeg resolves paths relative to the list file location.
func writeFileList(path string, images []string) error {
	var buf strings.Builder
	for _, img := range images {
		buf.WriteString(concatListEntry(filepath.Base(img)))
	}
	return os.WriteFile(path, []byte(buf.String()), 0644)
}
