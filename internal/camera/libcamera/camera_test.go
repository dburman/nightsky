package libcamera

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testCamera() *Camera {
	return &Camera{logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
}

func TestTemperature_ZeroBeforeCapture(t *testing.T) {
	c := testCamera()
	temp, err := c.Temperature()
	if err != nil {
		t.Fatal(err)
	}
	if temp != 0 {
		t.Errorf("temperature before first capture = %v, want 0", temp)
	}
}

func TestTemperature_ReturnsCachedValue(t *testing.T) {
	c := testCamera()
	c.mu.Lock()
	c.lastTemp = 18.5
	c.mu.Unlock()

	temp, err := c.Temperature()
	if err != nil {
		t.Fatal(err)
	}
	if temp != 18.5 {
		t.Errorf("cached temperature = %v, want 18.5", temp)
	}
}

// parseMetadata must recover the sensor temperature that Capture caches.
func TestParseMetadata_Temperature(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.txt")
	content := "ExposureTime=8000000\nAnalogueGain=2.0\nSensorTemperature=21.7\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	c := testCamera()
	meta := c.parseMetadata(path)
	if meta.temperature != 21.7 {
		t.Errorf("parsed temperature = %v, want 21.7", meta.temperature)
	}
	if meta.gain != 2.0 {
		t.Errorf("parsed gain = %v, want 2.0", meta.gain)
	}
}
