// Package metrics writes and reads a live capture state file so the web server
// can expose current capture stats without sharing memory with the capture loop.
// Both processes (nightsky capture + nightsky serve) access the same file via
// the shared output volume, making this work identically in Docker Compose and
// systemd split-service deployments.
package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const filename = ".metrics.json"

// Snapshot is the live capture state written after every frame.
type Snapshot struct {
	Mode           string    `json:"mode"`
	ExposureNs     int64     `json:"exposure_ns"`
	Exposure       string    `json:"exposure"`
	Gain           float64   `json:"gain"`
	FrameCount     int64     `json:"frame_count"`
	MeanBrightness float64   `json:"mean_brightness"`
	CloudCoverage  float64   `json:"cloud_coverage"`
	SensorTempC    float64   `json:"sensor_temp_c"`
	LastCapture    time.Time `json:"last_capture"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Write atomically writes snap to <outputDir>/.metrics.json.
func Write(outputDir string, snap Snapshot) error {
	snap.UpdatedAt = time.Now()

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal metrics: %w", err)
	}

	path := filepath.Join(outputDir, filename)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write metrics tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// Read returns the most recent snapshot from <outputDir>/.metrics.json.
// Returns an error if the file doesn't exist (capture not running).
func Read(outputDir string) (Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(outputDir, filename))
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("parse metrics: %w", err)
	}
	return snap, nil
}
