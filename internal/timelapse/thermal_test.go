package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setTempFixture(t *testing.T, milliDegrees string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "temp")
	if err := os.WriteFile(path, []byte(milliDegrees+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	old := socTempPath
	socTempPath = path
	t.Cleanup(func() { socTempPath = old })
}

func TestReadSoCTemp(t *testing.T) {
	setTempFixture(t, "48234")
	temp, ok := readSoCTemp()
	if !ok {
		t.Fatal("expected read to succeed")
	}
	if temp < 48.2 || temp > 48.3 {
		t.Errorf("temp = %v, want ~48.234", temp)
	}
}

func TestReadSoCTemp_MissingFile(t *testing.T) {
	old := socTempPath
	socTempPath = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { socTempPath = old })
	if _, ok := readSoCTemp(); ok {
		t.Error("expected ok=false for missing thermal zone")
	}
}

// The gate must return immediately when cool, when disabled, and when the
// thermal zone is unreadable — it may only wait when genuinely hot.
func TestWaitForCoolSoC_NoWaitCases(t *testing.T) {
	ctx := context.Background()

	// Disabled.
	start := time.Now()
	waitForCoolSoC(ctx, 0, testLogger())
	if time.Since(start) > time.Second {
		t.Error("disabled gate waited")
	}

	// Cool chip (45°C vs 70 limit).
	setTempFixture(t, "45000")
	start = time.Now()
	waitForCoolSoC(ctx, 70, testLogger())
	if time.Since(start) > time.Second {
		t.Error("cool gate waited")
	}
}

// A hot chip with a cancelled context must not block.
func TestWaitForCoolSoC_HotHonorsContext(t *testing.T) {
	setTempFixture(t, "82000") // 82°C, limit 70
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		waitForCoolSoC(ctx, 70, testLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not honor cancelled context")
	}
}
