// Package thermal gates heavy processing on SoC temperature. Sustained
// CPU-intensive work on an already-hot chip (an all-sky enclosure in summer
// sun) drives the firmware into frequency capping and, on Pi Zero 2 boards,
// destabilizes the SDIO WiFi — deferring the work until the chip cools is
// cheaper than recovering from the crash it can cause.
package thermal

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// TempPath is the standard Linux thermal zone for the SoC; variable so tests
// can point it at a fixture. Absent on non-Linux hosts, which disables the
// gate.
var TempPath = "/sys/class/thermal/thermal_zone0/temp"

const (
	// recheck is how often the gate re-reads the temperature while waiting.
	recheck = 2 * time.Minute
	// maxWait bounds how long work can be deferred; past this it proceeds
	// hot rather than never running.
	maxWait = 30 * time.Minute
)

// ReadTemp returns the SoC temperature in °C, or ok=false when the thermal
// zone can't be read (non-Linux, missing sysfs).
func ReadTemp() (float64, bool) {
	data, err := os.ReadFile(TempPath)
	if err != nil {
		return 0, false
	}
	milli, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return float64(milli) / 1000.0, true
}

// Wait defers heavy work while the SoC is above limitC, rechecking every two
// minutes until it cools, the context is cancelled, or thirty minutes elapse
// — it delays heat, never blocks work forever. limitC <= 0 disables the gate.
func Wait(ctx context.Context, limitC int, logger *slog.Logger) {
	if limitC <= 0 {
		return
	}

	deadline := time.Now().Add(maxWait)
	for {
		temp, ok := ReadTemp()
		if !ok || temp <= float64(limitC) {
			return
		}
		if time.Now().After(deadline) {
			logger.Warn("SoC still hot after max deferral, proceeding anyway",
				"temp_c", temp, "limit_c", limitC)
			return
		}
		logger.Info("deferring work until SoC cools",
			"temp_c", temp, "limit_c", limitC, "recheck", recheck)
		select {
		case <-ctx.Done():
			return
		case <-time.After(recheck):
		}
	}
}
