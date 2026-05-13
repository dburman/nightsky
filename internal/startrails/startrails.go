package startrails

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
	"slices"
	"strings"
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

// toRGBA converts any image.Image to *image.RGBA.
func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}

// maxBlend updates dst in-place, keeping the per-channel maximum between
// dst and src at each pixel.
func maxBlend(dst *image.RGBA, src image.Image) {
	b := dst.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dr, dg, db, da := dst.At(x, y).RGBA()
			sr, sg, sb, sa := src.At(x, y).RGBA()
			dst.Set(x, y, color.RGBA{
				R: uint8(max(dr, sr) >> 8),
				G: uint8(max(dg, sg) >> 8),
				B: uint8(max(db, sb) >> 8),
				A: uint8(max(da, sa) >> 8),
			})
		}
	}
}

