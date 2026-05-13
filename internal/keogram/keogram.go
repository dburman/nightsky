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
)

// outputHeight is the fixed pixel height of the generated keogram.
const outputHeight = 480

// Generate builds a keogram from all images in dateDir and saves it to
// <dateDir>/keogram-<date>.jpg. Returns the output path.
//
// A keogram is constructed by extracting the center column from each image
// (in time order) and stitching the columns left-to-right. The result is a
// single image where the X axis is time and the Y axis is the sky from
// horizon (top/bottom) to zenith (centre).
//
// Images are decoded in parallel using runtime.NumCPU() workers. Canvas
// writes are lock-free because each goroutine owns a distinct column.
func Generate(ctx context.Context, dateDir string, logger *slog.Logger) (string, error) {
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
		if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") {
			files = append(files, filepath.Join(dateDir, e.Name()))
		}
	}
	slices.Sort(files)

	if len(files) == 0 {
		return "", fmt.Errorf("no images found in %s", dateDir)
	}

	logger.Info("generating keogram", "images", len(files), "dir", dateDir)

	// Pre-allocate strip storage: each element is outputHeight*4 bytes (RGBA),
	// nil means the image could not be decoded.
	strips := make([][]byte, len(files))

	// Bounded worker pool — limit concurrent image decodes to avoid OOM on
	// memory-constrained hardware (e.g. Raspberry Pi Zero 2W with 512 MB).
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup

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

			data, err := extractCenterStrip(path)
			if err != nil {
				logger.Warn("keogram: skipping image", "path", path, "error", err)
				return
			}
			strips[x] = data // each goroutine writes to a distinct index: no race
		}(x, path)
	}
	wg.Wait()

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

// extractCenterStrip decodes the image at path, extracts its center column,
// and returns outputHeight RGBA pixels (4 bytes each) scaled to fit.
func extractCenterStrip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
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
