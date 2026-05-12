// Package camera defines the Camera interface and shared types for all camera backends.
package camera

import (
	"context"
	"image"
	"time"
)

// ImageFormat represents the pixel format of a captured frame.
type ImageFormat int

const (
	FormatRAW8 ImageFormat = iota
	FormatRAW16
	FormatRGB24
	FormatJPEG
	FormatPNG
)

func (f ImageFormat) String() string {
	switch f {
	case FormatRAW8:
		return "RAW8"
	case FormatRAW16:
		return "RAW16"
	case FormatRGB24:
		return "RGB24"
	case FormatJPEG:
		return "JPEG"
	case FormatPNG:
		return "PNG"
	default:
		return "unknown"
	}
}

// CaptureResult holds a captured frame and its metadata.
type CaptureResult struct {
	// Image is the decoded image. May be nil if RawData is provided instead.
	Image image.Image
	// RawData holds the raw bytes when the camera outputs encoded formats (JPEG/PNG).
	RawData []byte
	// Metadata about the capture.
	Meta CaptureMeta
}

// CaptureMeta contains metadata about a single capture.
type CaptureMeta struct {
	Timestamp   time.Time
	Exposure    time.Duration
	Gain        float64
	Temperature float64 // sensor temperature in Celsius
	Width       int
	Height      int
	Format      ImageFormat
	Binning     int
	// MeanBrightness is the average pixel value (0-255) of the captured frame.
	// Computed during capture if available, otherwise -1.
	MeanBrightness float64
}

// CameraInfo describes a connected camera.
type CameraInfo struct {
	Name       string
	Model      string
	MaxWidth   int
	MaxHeight  int
	HasCooler  bool
	IsColor    bool
	BayerPattern string // "RGGB", "BGGR", "GRBG", "GBRG", or ""
	// Capabilities lists controllable parameters and their ranges.
	Controls []ControlInfo
}

// ControlInfo describes a single camera control.
type ControlInfo struct {
	Name         string
	Min          float64
	Max          float64
	Default      float64
	IsAutoSupported bool
}

// CaptureSettings holds the parameters for a single exposure.
type CaptureSettings struct {
	Exposure    time.Duration
	Gain        float64
	Binning     int
	WBRed       float64
	WBBlue      float64
	AWB         bool
	Flip        int // 0=none, 1=horiz, 2=vert, 3=both
	Format      ImageFormat
	// For libcamera: denoise mode.
	Denoise string
}

// CoolerSettings controls the camera's TEC cooler.
type CoolerSettings struct {
	Enabled    bool
	TargetTemp float64
}

// Camera is the interface that all camera backends must implement.
type Camera interface {
	// Open initializes the camera connection.
	Open() error

	// Close releases the camera.
	Close() error

	// Info returns information about the connected camera.
	Info() CameraInfo

	// Capture takes a single frame with the given settings.
	// The context can be used for timeout/cancellation.
	Capture(ctx context.Context, settings CaptureSettings) (*CaptureResult, error)

	// SetCooler adjusts the TEC cooler settings (no-op for cameras without cooling).
	SetCooler(settings CoolerSettings) error

	// Temperature returns the current sensor temperature in Celsius.
	Temperature() (float64, error)
}
