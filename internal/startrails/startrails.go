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

	_ "golang.org/x/image/webp"
)

// DefaultMaxMeanBrightness is the mean pixel brightness (0–255) above which a
// frame is considered twilight or daylight and excluded from the star-trails
// stack. Max-blending a bright frame washes out all star detail, so we discard
// any frame whose scene-wide mean exceeds this value.
const DefaultMaxMeanBrightness = 50

// Generate builds a star-trails image from the dark frames in dateDir by
// max-blending each qualifying frame: for every pixel the brightest value seen
// across all frames is kept. Frames whose mean brightness exceeds
// maxMeanBrightness are skipped so that twilight and daylight do not wash out
// the result. Saves to <dateDir>/startrails-<date>.jpg and returns the path.
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
			strings.HasPrefix(lower, "cloud-") ||
			strings.HasPrefix(lower, "highlight") {
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

	logger.Info("generating star trails", "images", len(files), "dir", dateDir)

	var result *image.RGBA
	var used, skipped int

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
			skipped++
			continue
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			logger.Warn("star trails: skipping image", "path", path, "error", err)
			skipped++
			continue
		}

		// Discard twilight and daylight frames — they wash out star detail.
		if mean := meanBrightness(img); mean > maxMeanBrightness {
			skipped++
			continue
		}

		if result == nil {
			result = toRGBA(img)
			used++
			continue
		}

		// Skip frames whose dimensions don't match the first frame — they are
		// likely synthesized outputs or captures from a different mode/binning.
		if img.Bounds().Dx() != result.Bounds().Dx() || img.Bounds().Dy() != result.Bounds().Dy() {
			logger.Warn("star trails: skipping mismatched image size", "path", path,
				"got", fmt.Sprintf("%dx%d", img.Bounds().Dx(), img.Bounds().Dy()),
				"want", fmt.Sprintf("%dx%d", result.Bounds().Dx(), result.Bounds().Dy()))
			skipped++
			continue
		}

		maxBlend(result, img)
		used++
	}

	logger.Info("star trails brightness filter", "used", used, "skipped", skipped)

	if result == nil {
		return "", fmt.Errorf("no dark frames found in %s (all %d images exceeded brightness threshold %.0f)", dateDir, len(files), maxMeanBrightness)
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
