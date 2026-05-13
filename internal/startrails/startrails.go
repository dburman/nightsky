package startrails

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

// Generate builds a star-trails image from all images in dateDir by
// max-blending each frame: for every pixel the brightest value seen across
// all frames is kept. Saves the result to <dateDir>/startrails-<date>.jpg
// and returns the output path.
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

	logger.Info("generating star trails", "images", len(files), "dir", dateDir)

	var result *image.RGBA

	for i, path := range files {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if i > 0 && i%50 == 0 {
			logger.Info("star trails progress", "processed", i, "total", len(files))
		}

		f, err := os.Open(path)
		if err != nil {
			logger.Warn("star trails: skipping image", "path", path, "error", err)
			continue
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			logger.Warn("star trails: skipping image", "path", path, "error", err)
			continue
		}

		if result == nil {
			result = toRGBA(img)
			continue
		}

		maxBlend(result, img)
	}

	if result == nil {
		return "", fmt.Errorf("no images could be decoded in %s", dateDir)
	}

	date := filepath.Base(dateDir)
	outPath := filepath.Join(dateDir, "startrails-"+date+".jpg")

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, result, &jpeg.Options{Quality: 92}); err != nil {
		return "", fmt.Errorf("encode star trails: %w", err)
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write star trails: %w", err)
	}

	logger.Info("star trails saved", "path", outPath)
	return outPath, nil
}

// toRGBA converts any image.Image to *image.RGBA using draw.Draw for a
// single-pass colour-space conversion rather than per-pixel At() calls.
func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// maxBlend updates dst in-place, keeping the per-channel maximum between dst
// and src at each pixel. Rows are distributed across runtime.NumCPU()
// goroutines. src is converted to RGBA once before the parallel phase.
func maxBlend(dst *image.RGBA, src image.Image) {
	srcRGBA := toRGBA(src)
	b := dst.Bounds()
	h := b.Dy()
	w := b.Dx()

	nWorkers := runtime.NumCPU()
	rowsPerWorker := (h + nWorkers - 1) / nWorkers

	var wg sync.WaitGroup
	for i := range nWorkers {
		y0 := b.Min.Y + i*rowsPerWorker
		y1 := y0 + rowsPerWorker
		if y1 > b.Max.Y {
			y1 = b.Max.Y
		}
		if y0 >= y1 {
			break
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				di := dst.PixOffset(b.Min.X, y)
				si := srcRGBA.PixOffset(b.Min.X, y)
				dp := dst.Pix[di : di+w*4 : di+w*4]
				sp := srcRGBA.Pix[si : si+w*4 : si+w*4]
				for j, sv := range sp {
					if sv > dp[j] {
						dp[j] = sv
					}
				}
			}
		}(y0, y1)
	}
	wg.Wait()
}
