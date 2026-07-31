// Package convert provides end-of-night image format conversion utilities.
package convert

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConvertPNGsToWebP converts all PNG capture images in dir to WebP using cwebp.
// Synthetic outputs (keogram, startrails) are skipped.
// If deleteOriginals is true, source PNGs are removed after successful conversion.
func ConvertPNGsToWebP(ctx context.Context, dir string, quality int, deleteOriginals bool, logger *slog.Logger) error {
	if _, err := exec.LookPath("cwebp"); err != nil {
		return fmt.Errorf("cwebp not found in PATH — install the 'webp' package (apt-get install webp)")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}

	var converted, failed int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.HasSuffix(lower, ".png") {
			continue
		}
		// Skip synthetic outputs — they are already JPEG.
		if strings.HasPrefix(lower, "keogram-") || strings.HasPrefix(lower, "startrails-") || strings.HasPrefix(lower, "highlight") {
			continue
		}

		src := filepath.Join(dir, e.Name())
		dst := src[:len(src)-len(filepath.Ext(src))] + ".webp"

		cmd := exec.CommandContext(ctx, "cwebp", "-q", fmt.Sprintf("%d", quality), src, "-o", dst)
		if out, err := cmd.CombinedOutput(); err != nil {
			logger.Warn("webp conversion failed", "file", e.Name(), "error", strings.TrimSpace(string(out)))
			failed++
			continue
		}

		if deleteOriginals {
			if err := os.Remove(src); err != nil {
				logger.Warn("failed to remove original PNG", "file", src, "error", err)
			}
		}
		converted++
	}

	logger.Info("webp conversion complete", "converted", converted, "failed", failed, "dir", dir)
	return nil
}
