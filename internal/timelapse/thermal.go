package timelapse

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// socTempPath is the standard Linux thermal zone for the SoC; variable so
// tests can point it at a fixture. Absent on non-Linux hosts, which disables
// the gate.
var socTempPath = "/sys/class/thermal/thermal_zone0/temp"

const (
	// thermalRecheck is how often the gate re-reads the temperature while
	// waiting for the SoC to cool.
	thermalRecheck = 2 * time.Minute
	// thermalMaxWait bounds how long an encode can be deferred; past this
	// the encode proceeds hot rather than never running.
	thermalMaxWait = 30 * time.Minute
)

// readSoCTemp returns the SoC temperature in °C, or ok=false when the
// thermal zone can't be read (non-Linux, missing sysfs).
func readSoCTemp() (float64, bool) {
	data, err := os.ReadFile(socTempPath)
	if err != nil {
		return 0, false
	}
	milli, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return float64(milli) / 1000.0, true
}

// waitForCoolSoC defers an encode while the SoC is above limitC. Sustained
// encoding on an already-hot chip (all-sky enclosure in summer sun) drives
// the firmware into frequency capping and, on Pi Zero 2 boards, destabilizes
// the SDIO WiFi. The gate waits in thermalRecheck steps until the SoC cools,
// the context is cancelled, or thermalMaxWait elapses — it delays heat, never
// blocks an encode forever. limitC <= 0 disables the gate entirely.
func waitForCoolSoC(ctx context.Context, limitC int, logger *slog.Logger) {
	if limitC <= 0 {
		return
	}

	deadline := time.Now().Add(thermalMaxWait)
	for {
		temp, ok := readSoCTemp()
		if !ok || temp <= float64(limitC) {
			return
		}
		if time.Now().After(deadline) {
			logger.Warn("SoC still hot after max deferral, encoding anyway",
				"temp_c", temp, "limit_c", limitC)
			return
		}
		logger.Info("deferring encode until SoC cools",
			"temp_c", temp, "limit_c", limitC, "recheck", thermalRecheck)
		select {
		case <-ctx.Done():
			return
		case <-time.After(thermalRecheck):
		}
	}
}
