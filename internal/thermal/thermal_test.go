package thermal

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func setTempFixture(t *testing.T, milliDegrees string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "temp")
	if err := os.WriteFile(path, []byte(milliDegrees+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	old := TempPath
	TempPath = path
	t.Cleanup(func() { TempPath = old })
}

func TestReadTemp(t *testing.T) {
	setTempFixture(t, "48234")
	temp, ok := ReadTemp()
	if !ok {
		t.Fatal("expected read to succeed")
	}
	if temp < 48.2 || temp > 48.3 {
		t.Errorf("temp = %v, want ~48.234", temp)
	}
}

func TestReadTemp_MissingFile(t *testing.T) {
	old := TempPath
	TempPath = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { TempPath = old })
	if _, ok := ReadTemp(); ok {
		t.Error("expected ok=false for missing thermal zone")
	}
}

// The gate must return immediately when cool, when disabled, and when the
// thermal zone is unreadable — it may only wait when genuinely hot.
func TestWait_NoWaitCases(t *testing.T) {
	ctx := context.Background()

	start := time.Now()
	Wait(ctx, 0, testLogger()) // disabled
	if time.Since(start) > time.Second {
		t.Error("disabled gate waited")
	}

	setTempFixture(t, "45000") // cool: 45°C vs 70 limit
	start = time.Now()
	Wait(ctx, 70, testLogger())
	if time.Since(start) > time.Second {
		t.Error("cool gate waited")
	}
}

// A hot chip with a cancelled context must not block.
func TestWait_HotHonorsContext(t *testing.T) {
	setTempFixture(t, "82000") // 82°C, limit 70
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		Wait(ctx, 70, testLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not honor cancelled context")
	}
}
