// Package flat provides flat-field correction to compensate for lens vignetting.
// A flat frame is captured against a uniformly lit surface; dividing each light
// frame by the normalized flat brightens the darker corners to match the centre.
package flat

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"math"
	"os"
	"path/filepath"

	"github.com/dburman/nightsky/internal/camera"
	imgutil "github.com/dburman/nightsky/internal/image"
)

const flatFilename = "master_flat.png"

// Manager handles capture, storage, and application of flat-field frames.
type Manager struct {
	Dir    string
	Count  int
	cam    camera.Camera
	logger *slog.Logger

	// norm holds the per-pixel normalization divisor (flat[x,y] / mean).
	// Loaded once at startup and reused for every frame.
	norm []float32
	w, h int
}

// NewManager creates a flat frame manager. Call Load after creation to prime
// the correction data if a master flat already exists.
func NewManager(dir string, count int, cam camera.Camera, logger *slog.Logger) *Manager {
	return &Manager{Dir: dir, Count: count, cam: cam, logger: logger}
}

// Load reads the master flat from disk and precomputes the normalization map.
// Returns nil if no master flat exists yet.
func (m *Manager) Load() error {
	path := filepath.Join(m.Dir, flatFilename)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no flat yet — correction disabled until one is captured
		}
		return fmt.Errorf("open flat frame: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return fmt.Errorf("decode flat frame: %w", err)
	}

	b := img.Bounds()
	m.w = b.Dx()
	m.h = b.Dy()
	total := m.w * m.h

	// Accumulate per-pixel luminance and compute the global mean.
	lum := make([]float32, total)
	var sum float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			v := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			idx := (y-b.Min.Y)*m.w + (x - b.Min.X)
			lum[idx] = float32(v)
			sum += v
		}
	}

	mean := sum / float64(total)
	if mean < 1 {
		return fmt.Errorf("flat frame appears to be blank (mean=%.2f)", mean)
	}

	// Precompute divisors: norm[i] = lum[i] / mean.
	// Dividing a pixel by its norm value rescales it to match the centre brightness.
	m.norm = make([]float32, total)
	for i, v := range lum {
		if v < 1 {
			m.norm[i] = 1 // avoid divide-by-zero in very dark corners
		} else {
			m.norm[i] = v / float32(mean)
		}
	}

	m.logger.Info("flat frame loaded", "path", path, "size", fmt.Sprintf("%dx%d", m.w, m.h), "mean", fmt.Sprintf("%.1f", mean))
	return nil
}

// Ready reports whether a flat frame has been loaded and correction is active.
func (m *Manager) Ready() bool {
	return len(m.norm) > 0
}

// Apply divides each pixel of img by its flat-field normalization factor.
// Returns img unchanged if no flat has been loaded or if dimensions mismatch.
func (m *Manager) Apply(img image.Image) image.Image {
	if !m.Ready() {
		return img
	}

	b := img.Bounds()
	if b.Dx() != m.w || b.Dy() != m.h {
		m.logger.Warn("flat frame size mismatch — skipping correction",
			"image", fmt.Sprintf("%dx%d", b.Dx(), b.Dy()),
			"flat", fmt.Sprintf("%dx%d", m.w, m.h),
		)
		return img
	}

	// Convert to RGBA for direct buffer access.
	rgba := toRGBA(img)
	out := image.NewRGBA(b)
	draw.Draw(out, b, rgba, b.Min, draw.Src)

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			idx := (y-b.Min.Y)*m.w + (x - b.Min.X)
			d := m.norm[idx]
			si := rgba.PixOffset(x, y)
			oi := out.PixOffset(x, y)
			out.Pix[oi+0] = clampF(float32(rgba.Pix[si+0]) / d)
			out.Pix[oi+1] = clampF(float32(rgba.Pix[si+1]) / d)
			out.Pix[oi+2] = clampF(float32(rgba.Pix[si+2]) / d)
			out.Pix[oi+3] = rgba.Pix[si+3]
		}
	}
	return out
}

// CaptureFlat captures Count frames and saves the average as the master flat.
// Point the camera at a uniformly lit surface (e.g. a white screen or overcast sky)
// before running this.
func (m *Manager) CaptureFlat(ctx context.Context, settings camera.CaptureSettings) error {
	if err := os.MkdirAll(m.Dir, 0755); err != nil {
		return fmt.Errorf("create flat dir: %w", err)
	}

	m.logger.Info("capturing flat frames", "count", m.Count)

	var frames []image.Image
	for i := range m.Count {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		m.logger.Info("flat frame", "frame", i+1, "of", m.Count)
		res, err := m.cam.Capture(ctx, settings)
		if err != nil {
			return fmt.Errorf("capture flat frame %d: %w", i+1, err)
		}
		frames = append(frames, res.Image)
	}

	averaged := averageFrames(frames)

	path := filepath.Join(m.Dir, flatFilename)
	data, err := imgutil.EncodeImage(averaged, "png", 95)
	if err != nil {
		return fmt.Errorf("encode flat frame: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("save flat frame: %w", err)
	}
	m.logger.Info("master flat saved", "path", path)
	return m.Load()
}

func clampF(v float32) uint8 {
	if v >= 255 {
		return 255
	}
	if v <= 0 || math.IsNaN(float64(v)) {
		return 0
	}
	return uint8(v)
}

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

func averageFrames(frames []image.Image) image.Image {
	if len(frames) == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	b := frames[0].Bounds()
	w, h := b.Dx(), b.Dy()
	accumR := make([]uint32, w*h)
	accumG := make([]uint32, w*h)
	accumB := make([]uint32, w*h)
	for _, fr := range frames {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, _ := fr.At(b.Min.X+x, b.Min.Y+y).RGBA()
				idx := y*w + x
				accumR[idx] += r >> 8
				accumG[idx] += g >> 8
				accumB[idx] += bl >> 8
			}
		}
	}
	n := uint32(len(frames))
	out := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			out.SetRGBA(b.Min.X+x, b.Min.Y+y, color.RGBA{
				R: uint8(accumR[idx] / n),
				G: uint8(accumG[idx] / n),
				B: uint8(accumB[idx] / n),
				A: 255,
			})
		}
	}
	return out
}
