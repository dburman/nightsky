package keogram

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	_ "golang.org/x/image/webp"
)

// outputHeight is the fixed pixel height of the generated keogram.
const outputHeight = 480

// DefaultMaxMeanBrightness is the mean pixel brightness (0–255) above which a
// frame is considered twilight or daylight and excluded from the keogram.
const DefaultMaxMeanBrightness = 50

// Generate builds a keogram from the dark frames in dateDir and saves it to
// <dateDir>/keogram-<date>.jpg. Returns the output path.
//
// A keogram is constructed by extracting the center column from each qualifying
// image (in time order) and stitching the columns left-to-right. Frames whose
// mean brightness exceeds maxMeanBrightness are skipped so that twilight and
// daylight do not wash out the result.
//
// Images are decoded in parallel using runtime.NumCPU() workers. Canvas
// writes are lock-free because each goroutine owns a distinct column.
func Generate(ctx context.Context, dateDir string, maxMeanBrightness float64, logger *slog.Logger) (string, error) {
	entries, err := os.ReadDir(dateDir)
	if err != nil {
		return "", fmt.Errorf("read dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		// Skip synthesized outputs — they have different dimensions or represent
		// aggregated data, not individual captures.
		if strings.HasPrefix(lower, "timelapse-") ||
			strings.HasPrefix(lower, "keogram-") ||
			strings.HasPrefix(lower, "startrails-") ||
			strings.HasPrefix(lower, "wb-analysis-") ||
			strings.HasPrefix(lower, "cloud-") {
			continue
		}
		if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
			strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp") {
			files = append(files, filepath.Join(dateDir, e.Name()))
		}
	}
	slices.Sort(files)

	if len(files) == 0 {
		return "", fmt.Errorf("no images found in %s", dateDir)
	}

	logger.Info("generating keogram", "images", len(files), "dir", dateDir)

	// Pre-allocate strip storage: each element is outputHeight*4 bytes (RGBA),
	// nil means the image was skipped (too bright or decode error).
	strips := make([][]byte, len(files))

	// Bounded worker pool — limit concurrent image decodes to avoid OOM on
	// memory-constrained hardware (e.g. Raspberry Pi Zero 2W with 512 MB).
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	var skipped atomic.Int64

	for x, path := range files {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(x int, path string) {
			defer wg.Done()
			defer func() { <-sem }()

			data, err := extractCenterStrip(path, maxMeanBrightness)
			if err != nil {
				logger.Warn("keogram: skipping image", "path", path, "error", err)
				skipped.Add(1)
				return
			}
			if data == nil {
				// Frame too bright — silently skip.
				skipped.Add(1)
				return
			}
			strips[x] = data // each goroutine writes to a distinct index: no race
		}(x, path)
	}
	wg.Wait()

	logger.Info("keogram brightness filter", "total", len(files), "skipped", skipped.Load())

	canvas := image.NewRGBA(image.Rect(0, 0, len(files), outputHeight))

	for x, strip := range strips {
		if strip == nil {
			continue
		}
		for dy := range outputHeight {
			off := canvas.PixOffset(x, dy)
			copy(canvas.Pix[off:off+4], strip[dy*4:dy*4+4])
		}
	}

	date := filepath.Base(dateDir)
	outPath := filepath.Join(dateDir, "keogram-"+date+".jpg")

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 90}); err != nil {
		return "", fmt.Errorf("encode keogram: %w", err)
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write keogram: %w", err)
	}

	logger.Info("keogram saved", "path", outPath)
	return outPath, nil
}

// extractCenterStrip decodes the image at path, checks its mean brightness
// against maxMeanBrightness, and returns outputHeight RGBA pixels (4 bytes
// each) scaled to fit. Returns nil, nil when the frame is too bright.
func extractCenterStrip(path string, maxMeanBrightness float64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	// Discard twilight and daylight frames.
	if meanBrightness(img) > maxMeanBrightness {
		return nil, nil
	}

	// Convert to RGBA once for direct buffer access.
	var rgba *image.RGBA
	if r, ok := img.(*image.RGBA); ok {
		rgba = r
	} else {
		b := img.Bounds()
		rgba = image.NewRGBA(b)
		draw.Draw(rgba, b, img, b.Min, draw.Src)
	}

	b := rgba.Bounds()
	cx := b.Min.X + b.Dx()/2
	srcH := b.Dy()

	out := make([]byte, outputHeight*4)
	for dy := range outputHeight {
		sy := b.Min.Y + dy*srcH/outputHeight
		si := rgba.PixOffset(cx, sy)
		copy(out[dy*4:dy*4+4], rgba.Pix[si:si+4])
	}
	return out, nil
}

// meanBrightness returns the luminance-weighted mean brightness (0–255) of img,
// sampling every 4th pixel for speed.
func meanBrightness(img image.Image) float64 {
	b := img.Bounds()
	var sum float64
	var count int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			lum := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			sum += lum
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
