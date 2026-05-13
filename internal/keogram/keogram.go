package keogram

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	sort.Strings(files)

	if len(files) == 0 {
		return "", fmt.Errorf("no images found in %s", dateDir)
	}

	logger.Info("generating keogram", "images", len(files), "dir", dateDir)

	canvas := image.NewRGBA(image.Rect(0, 0, len(files), outputHeight))

	for x, path := range files {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		strip, err := centerStrip(path)
		if err != nil {
			logger.Warn("keogram: skipping image", "path", path, "error", err)
			continue
		}

		srcH := len(strip)
		for dy := 0; dy < outputHeight; dy++ {
			sy := dy * srcH / outputHeight
			if sy >= srcH {
				sy = srcH - 1
			}
			canvas.Set(x, dy, strip[sy])
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

// centerStrip returns the center column of pixels from the image at path,
// ordered top to bottom.
func centerStrip(path string) ([]color.Color, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	b := img.Bounds()
	cx := b.Min.X + b.Dx()/2
	h := b.Dy()

	strip := make([]color.Color, h)
	for y := 0; y < h; y++ {
		strip[y] = img.At(cx, b.Min.Y+y)
	}
	return strip, nil
}
