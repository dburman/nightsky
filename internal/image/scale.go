package image

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ThumbWidth is the pixel width used for cached thumbnails throughout the app.
const ThumbWidth = 160

// ToRGBA converts any image.Image to *image.RGBA. If src is already
// *image.RGBA it is returned as-is without copying.
func ToRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// ScaleTo scales src to targetW pixels wide (preserving aspect ratio) using
// nearest-neighbour sampling with direct pixel-buffer access. Rows are
// distributed across runtime.NumCPU() goroutines for parallelism.
func ScaleTo(src image.Image, targetW int) *image.RGBA {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= targetW {
		return ToRGBA(src)
	}
	targetH := srcH * targetW / srcW
	if targetH < 1 {
		targetH = 1
	}

	srcRGBA := ToRGBA(src)
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))

	nWorkers := runtime.NumCPU()
	rowsPerWorker := (targetH + nWorkers - 1) / nWorkers

	var wg sync.WaitGroup
	for i := range nWorkers {
		y0 := i * rowsPerWorker
		y1 := y0 + rowsPerWorker
		if y1 > targetH {
			y1 = targetH
		}
		if y0 >= y1 {
			break
		}
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for dy := y0; dy < y1; dy++ {
				sy := b.Min.Y + dy*srcH/targetH
				for dx := range targetW {
					sx := b.Min.X + dx*srcW/targetW
					si := srcRGBA.PixOffset(sx, sy)
					di := dst.PixOffset(dx, dy)
					copy(dst.Pix[di:di+4], srcRGBA.Pix[si:si+4])
				}
			}
		}(y0, y1)
	}
	wg.Wait()
	return dst
}

// CacheThumb generates a JPEG thumbnail from the already-decoded img and
// saves it to the standard cache location alongside srcPath. Avoids
// re-reading the source file from disk. No-op if the cache file exists.
func CacheThumb(img image.Image, srcPath string) error {
	dir := filepath.Dir(srcPath)
	base := filepath.Base(srcPath)
	baseNoExt := strings.TrimSuffix(base, filepath.Ext(base))

	cacheDir := filepath.Join(dir, ".thumbs")
	cachePath := filepath.Join(cacheDir, baseNoExt+"_"+strconv.Itoa(ThumbWidth)+".jpg")

	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}

	scaled := ScaleTo(img, ThumbWidth)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 75}); err != nil {
		return err
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, buf.Bytes(), 0644)
}
