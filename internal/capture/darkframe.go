package capture

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dburman/nightsky/internal/camera"
	imgutil "github.com/dburman/nightsky/internal/image"
)

// DarkFrameManager handles capture, storage, and subtraction of dark frames.
// Dark frames are captured with the lens cap on and subtracted from light frames
// to remove fixed-pattern noise and hot pixels.
type DarkFrameManager struct {
	// Directory where dark frames are stored.
	Dir string
	// Count is the number of dark frames to average when capturing.
	Count int

	cam    camera.Camera
	logger *slog.Logger
}

// NewDarkFrameManager creates a new dark frame manager.
func NewDarkFrameManager(dir string, count int, cam camera.Camera, logger *slog.Logger) *DarkFrameManager {
	return &DarkFrameManager{
		Dir:    dir,
		Count:  count,
		cam:    cam,
		logger: logger,
	}
}

// DarkFrameKey uniquely identifies a dark frame by its capture parameters.
type DarkFrameKey struct {
	Exposure time.Duration
	Gain     float64
	Binning  int
}

func (k DarkFrameKey) filename() string {
	return fmt.Sprintf("dark_%dms_gain%.0f_bin%d.png",
		k.Exposure.Milliseconds(), k.Gain, k.Binning)
}

// CaptureDarks captures and averages multiple dark frames, saving the result.
// The camera lens must be covered before calling this.
func (dm *DarkFrameManager) CaptureDarks(ctx context.Context, settings camera.CaptureSettings) error {
	if err := os.MkdirAll(dm.Dir, 0755); err != nil {
		return fmt.Errorf("create dark dir: %w", err)
	}

	key := DarkFrameKey{
		Exposure: settings.Exposure,
		Gain:     settings.Gain,
		Binning:  settings.Binning,
	}

	dm.logger.Info("capturing dark frames",
		"count", dm.Count,
		"exposure", settings.Exposure,
		"gain", settings.Gain,
	)

	var frames []image.Image

	for i := 0; i < dm.Count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dm.logger.Info("dark frame", "frame", i+1, "of", dm.Count)

		result, err := dm.cam.Capture(ctx, settings)
		if err != nil {
			return fmt.Errorf("capture dark frame %d: %w", i+1, err)
		}
		frames = append(frames, result.Image)
	}

	// Average all dark frames.
	averaged := averageFrames(frames)

	// Save the master dark frame.
	path := filepath.Join(dm.Dir, key.filename())
	data, err := imgutil.EncodeImage(averaged, "png", 95)
	if err != nil {
		return fmt.Errorf("encode dark frame: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("save dark frame: %w", err)
	}

	dm.logger.Info("dark frame saved", "path", path)
	return nil
}

// LoadDark loads a previously captured dark frame matching the given parameters.
// Returns nil if no matching dark frame exists.
func (dm *DarkFrameManager) LoadDark(key DarkFrameKey) image.Image {
	path := filepath.Join(dm.Dir, key.filename())

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		dm.logger.Warn("failed to decode dark frame", "path", path, "error", err)
		return nil
	}

	dm.logger.Debug("loaded dark frame", "path", path)
	return img
}

// SubtractDark subtracts a dark frame from a light frame pixel by pixel.
// Values are clamped to 0 (no negative values).
func SubtractDark(light, dark image.Image) image.Image {
	bounds := light.Bounds()
	darkBounds := dark.Bounds()

	// Ensure same dimensions.
	if bounds.Dx() != darkBounds.Dx() || bounds.Dy() != darkBounds.Dy() {
		return light // dimensions don't match, skip subtraction
	}

	result := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			lr, lg, lb, la := light.At(x, y).RGBA()
			dr, dg, db, _ := dark.At(x, y).RGBA()

			// Subtract with clamping (values are 16-bit, shift to 8-bit).
			r := clampSub(lr>>8, dr>>8)
			g := clampSub(lg>>8, dg>>8)
			b := clampSub(lb>>8, db>>8)

			result.SetRGBA(x, y, color.RGBA{
				R: uint8(r),
				G: uint8(g),
				B: uint8(b),
				A: uint8(la >> 8),
			})
		}
	}

	return result
}

func clampSub(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return 0
}

// averageFrames computes the pixel-wise average of multiple frames.
func averageFrames(frames []image.Image) image.Image {
	if len(frames) == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if len(frames) == 1 {
		return frames[0]
	}

	bounds := frames[0].Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// Accumulate in 32-bit to avoid overflow.
	accumR := make([]uint32, w*h)
	accumG := make([]uint32, w*h)
	accumB := make([]uint32, w*h)

	for _, frame := range frames {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, b, _ := frame.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
				idx := y*w + x
				accumR[idx] += r >> 8
				accumG[idx] += g >> 8
				accumB[idx] += b >> 8
			}
		}
	}

	n := uint32(len(frames))
	result := image.NewRGBA(bounds)
	draw.Draw(result, bounds, image.Transparent, image.Point{}, draw.Src)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			result.SetRGBA(bounds.Min.X+x, bounds.Min.Y+y, color.RGBA{
				R: uint8(accumR[idx] / n),
				G: uint8(accumG[idx] / n),
				B: uint8(accumB[idx] / n),
				A: 255,
			})
		}
	}

	return result
}
