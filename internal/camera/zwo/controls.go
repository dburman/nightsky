//go:build zwo

package zwo

import "fmt"

// ListCameras enumerates all connected ZWO ASI cameras.
func ListCameras() ([]CameraProperties, error) {
	n := GetNumConnected()
	cameras := make([]CameraProperties, 0, n)

	for i := 0; i < n; i++ {
		props, err := GetCameraProperties(i)
		if err != nil {
			return nil, fmt.Errorf("camera %d: %w", i, err)
		}
		cameras = append(cameras, props)
	}

	return cameras, nil
}

// ListControls returns all controls for an opened camera.
func ListControls(cameraID int) ([]ControlCaps, error) {
	n, err := GetNumControls(cameraID)
	if err != nil {
		return nil, err
	}

	controls := make([]ControlCaps, 0, n)
	for i := 0; i < n; i++ {
		caps, err := GetControlCaps(cameraID, i)
		if err != nil {
			return nil, fmt.Errorf("control %d: %w", i, err)
		}
		controls = append(controls, caps)
	}

	return controls, nil
}

// SetUSBBandwidth sets the USB bandwidth limit (40-100).
// Lower values are needed when multiple cameras share a USB bus.
func SetUSBBandwidth(cameraID, bandwidth int) error {
	if bandwidth < 40 || bandwidth > 100 {
		return fmt.Errorf("USB bandwidth must be between 40 and 100, got %d", bandwidth)
	}
	return SetControlValue(cameraID, CtrlBandwidth, int64(bandwidth), false)
}

// SetHighSpeedMode enables or disables high speed mode.
// High speed mode reduces image quality but increases frame rate.
func SetHighSpeedMode(cameraID int, enabled bool) error {
	var val int64
	if enabled {
		val = 1
	}
	return SetControlValue(cameraID, CtrlHighSpeedMode, val, false)
}

// GetTemperature reads the sensor temperature.
// Returns temperature in Celsius (the SDK returns 10x the actual temperature).
func GetTemperature(cameraID int) (float64, error) {
	val, _, err := GetControlValue(cameraID, CtrlTemperature)
	if err != nil {
		return 0, err
	}
	return float64(val) / 10.0, nil
}

// GetCoolerPower returns the current cooler power percentage (0-100).
func GetCoolerPower(cameraID int) (int, error) {
	val, _, err := GetControlValue(cameraID, CtrlCoolerPowerPercent)
	if err != nil {
		return 0, err
	}
	return int(val), nil
}
