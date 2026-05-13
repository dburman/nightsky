//go:build zwo

package zwo

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"time"

	"github.com/dburman/nightsky/internal/camera"
)

// Camera implements the camera.Camera interface for ZWO ASI cameras.
type Camera struct {
	index      int
	id         int
	props      CameraProperties
	controls   []ControlCaps
	opened     bool
	logger     *slog.Logger
}

// New creates a new ZWO camera for the given device index.
func New(index int, logger *slog.Logger) *Camera {
	return &Camera{
		index:  index,
		logger: logger,
	}
}

func (c *Camera) Open() error {
	n := GetNumConnected()
	if n == 0 {
		return fmt.Errorf("no ZWO cameras connected")
	}
	if c.index >= n {
		return fmt.Errorf("camera index %d out of range (found %d cameras)", c.index, n)
	}

	props, err := GetCameraProperties(c.index)
	if err != nil {
		return fmt.Errorf("get camera properties: %w", err)
	}
	c.props = props
	c.id = props.CameraID

	c.logger.Info("opening ZWO camera",
		"name", props.Name,
		"id", c.id,
		"resolution", fmt.Sprintf("%dx%d", props.MaxWidth, props.MaxHeight),
		"color", props.IsColor,
		"cooler", props.HasCooler,
		"usb3", props.IsUSB3,
	)

	if err := OpenCamera(c.id); err != nil {
		return fmt.Errorf("open camera: %w", err)
	}

	if err := InitCamera(c.id); err != nil {
		CloseCamera(c.id)
		return fmt.Errorf("init camera: %w", err)
	}

	// Read control capabilities.
	numControls, err := GetNumControls(c.id)
	if err != nil {
		CloseCamera(c.id)
		return fmt.Errorf("get num controls: %w", err)
	}

	c.controls = make([]ControlCaps, 0, numControls)
	for i := 0; i < numControls; i++ {
		caps, err := GetControlCaps(c.id, i)
		if err != nil {
			c.logger.Warn("failed to read control", "index", i, "error", err)
			continue
		}
		c.controls = append(c.controls, caps)
		c.logger.Debug("camera control",
			"name", caps.Name,
			"min", caps.MinValue,
			"max", caps.MaxValue,
			"default", caps.DefaultValue,
		)
	}

	c.opened = true
	return nil
}

func (c *Camera) Close() error {
	if !c.opened {
		return nil
	}
	c.opened = false
	return CloseCamera(c.id)
}

func (c *Camera) Info() camera.CameraInfo {
	info := camera.CameraInfo{
		Name:      c.props.Name,
		Model:     c.props.Name,
		MaxWidth:  c.props.MaxWidth,
		MaxHeight: c.props.MaxHeight,
		HasCooler: c.props.HasCooler,
		IsColor:   c.props.IsColor,
	}

	if c.props.IsColor {
		info.BayerPattern = BayerPatternString(c.props.BayerPattern)
	}

	for _, ctrl := range c.controls {
		info.Controls = append(info.Controls, camera.ControlInfo{
			Name:            ctrl.Name,
			Min:             float64(ctrl.MinValue),
			Max:             float64(ctrl.MaxValue),
			Default:         float64(ctrl.DefaultValue),
			IsAutoSupported: ctrl.IsAutoSupported,
		})
	}

	return info
}

func (c *Camera) Capture(ctx context.Context, settings camera.CaptureSettings) (*camera.CaptureResult, error) {
	if !c.opened {
		return nil, fmt.Errorf("camera not opened")
	}

	// Apply settings.
	if err := c.applySettings(settings); err != nil {
		return nil, fmt.Errorf("apply settings: %w", err)
	}

	// Determine image format and buffer size.
	imgType := ImgRGB24
	bytesPerPixel := 3
	switch settings.Format {
	case camera.FormatRAW8:
		imgType = ImgRAW8
		bytesPerPixel = 1
	case camera.FormatRAW16:
		imgType = ImgRAW16
		bytesPerPixel = 2
	case camera.FormatRGB24:
		imgType = ImgRGB24
		bytesPerPixel = 3
	}

	bin := settings.Binning
	if bin < 1 {
		bin = 1
	}
	width := c.props.MaxWidth / bin
	height := c.props.MaxHeight / bin

	if err := SetROIFormat(c.id, width, height, bin, imgType); err != nil {
		return nil, fmt.Errorf("set ROI format: %w", err)
	}

	bufSize := width * height * bytesPerPixel
	buf := make([]byte, bufSize)

	// Start exposure.
	startTime := time.Now()
	if err := StartExposure(c.id, false); err != nil {
		return nil, fmt.Errorf("start exposure: %w", err)
	}

	// Poll for completion.
	timeout := settings.Exposure + 10*time.Second // extra 10s grace
	deadline := time.Now().Add(timeout)

	for {
		select {
		case <-ctx.Done():
			StopExposure(c.id)
			return nil, ctx.Err()
		default:
		}

		status, err := GetExposureStatus(c.id)
		if err != nil {
			StopExposure(c.id)
			return nil, fmt.Errorf("get exposure status: %w", err)
		}

		switch status {
		case ExpSuccess:
			goto readData
		case ExpFailed:
			return nil, fmt.Errorf("exposure failed")
		case ExpWorking:
			if time.Now().After(deadline) {
				StopExposure(c.id)
				return nil, fmt.Errorf("exposure timed out after %v", timeout)
			}
			// Poll interval scales with exposure time.
			pollInterval := settings.Exposure / 20
			if pollInterval < 10*time.Millisecond {
				pollInterval = 10 * time.Millisecond
			}
			if pollInterval > 500*time.Millisecond {
				pollInterval = 500 * time.Millisecond
			}
			time.Sleep(pollInterval)
		}
	}

readData:
	if err := GetDataAfterExp(c.id, buf); err != nil {
		return nil, fmt.Errorf("get data after exposure: %w", err)
	}

	exposureDuration := time.Since(startTime)

	// Read actual temperature (returned as 10x Celsius).
	temp := 0.0
	if tempVal, _, err := GetControlValue(c.id, CtrlTemperature); err == nil {
		temp = float64(tempVal) / 10.0
	}

	// Convert raw buffer to Go image.
	img := c.bufferToImage(buf, width, height, imgType)

	// Compute mean brightness.
	meanBrightness := computeMeanBrightness(img)

	result := &camera.CaptureResult{
		Image: img,
		Meta: camera.CaptureMeta{
			Timestamp:      startTime,
			Exposure:       exposureDuration,
			Gain:           settings.Gain,
			Temperature:    temp,
			Width:          width,
			Height:         height,
			Binning:        bin,
			MeanBrightness: meanBrightness,
		},
	}

	c.logger.Info("capture complete",
		"exposure", exposureDuration,
		"gain", settings.Gain,
		"temp", fmt.Sprintf("%.1f°C", temp),
		"mean", fmt.Sprintf("%.1f", meanBrightness),
		"size", fmt.Sprintf("%dx%d", width, height),
	)

	return result, nil
}

func (c *Camera) SetCooler(settings camera.CoolerSettings) error {
	if !c.props.HasCooler {
		return nil
	}

	var coolerOn int64
	if settings.Enabled {
		coolerOn = 1
	}
	if err := SetControlValue(c.id, CtrlCoolerOn, coolerOn, false); err != nil {
		return fmt.Errorf("set cooler: %w", err)
	}

	if settings.Enabled {
		if err := SetControlValue(c.id, CtrlTargetTemp, int64(settings.TargetTemp), false); err != nil {
			return fmt.Errorf("set target temp: %w", err)
		}
	}

	return nil
}

func (c *Camera) Temperature() (float64, error) {
	val, _, err := GetControlValue(c.id, CtrlTemperature)
	if err != nil {
		return 0, err
	}
	return float64(val) / 10.0, nil
}

func (c *Camera) applySettings(s camera.CaptureSettings) error {
	// Exposure in microseconds.
	exposureUs := int64(s.Exposure / time.Microsecond)
	if err := SetControlValue(c.id, CtrlExposure, exposureUs, false); err != nil {
		return fmt.Errorf("set exposure: %w", err)
	}

	if err := SetControlValue(c.id, CtrlGain, int64(s.Gain), false); err != nil {
		return fmt.Errorf("set gain: %w", err)
	}

	if err := SetControlValue(c.id, CtrlBandwidth, 80, false); err != nil {
		c.logger.Warn("failed to set USB bandwidth", "error", err)
	}

	if s.WBRed > 0 {
		if err := SetControlValue(c.id, CtrlWBR, int64(s.WBRed), false); err != nil {
			c.logger.Warn("failed to set WB red", "error", err)
		}
	}
	if s.WBBlue > 0 {
		if err := SetControlValue(c.id, CtrlWBB, int64(s.WBBlue), false); err != nil {
			c.logger.Warn("failed to set WB blue", "error", err)
		}
	}

	if err := SetControlValue(c.id, CtrlFlip, int64(s.Flip), false); err != nil {
		c.logger.Warn("failed to set flip", "error", err)
	}

	return nil
}

func (c *Camera) bufferToImage(buf []byte, width, height, imgType int) image.Image {
	switch imgType {
	case ImgRGB24:
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				offset := (y*width + x) * 3
				// ZWO SDK returns BGR, not RGB.
				img.SetRGBA(x, y, color.RGBA{
					R: buf[offset+2],
					G: buf[offset+1],
					B: buf[offset],
					A: 255,
				})
			}
		}
		return img

	case ImgRAW8, ImgY8:
		img := image.NewGray(image.Rect(0, 0, width, height))
		copy(img.Pix, buf[:width*height])
		return img

	case ImgRAW16:
		img := image.NewGray16(image.Rect(0, 0, width, height))
		copy(img.Pix, buf[:width*height*2])
		return img

	default:
		// Fallback to grayscale.
		img := image.NewGray(image.Rect(0, 0, width, height))
		copy(img.Pix, buf[:width*height])
		return img
	}
}

func computeMeanBrightness(img image.Image) float64 {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w == 0 || h == 0 {
		return 0
	}

	// Sample a grid for performance (every 4th pixel).
	var sum float64
	var count int
	step := 4

	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			// Convert from 16-bit to 8-bit and use luminance formula.
			lum := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
			sum += lum
			count++
		}
	}

	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
