package upload

import (
	"context"
	"log/slog"
	"time"
)

// retryDelays are the waits between upload attempts. Two retries with short
// backoff covers the common transient failures (a WiFi blip, a dropped TLS
// handshake) without holding a worker hostage to a genuinely dead uplink.
var retryDelays = []time.Duration{5 * time.Second, 30 * time.Second}

// WithRetry runs fn, retrying after each of retryDelays on failure. It
// returns the last error when all attempts fail, and nil as soon as one
// succeeds or when the context is cancelled mid-wait (cancellation is a
// shutdown, not an upload failure worth logging twice).
func WithRetry(ctx context.Context, logger *slog.Logger, label string, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = fn()
		if err == nil {
			if attempt > 0 {
				logger.Info("upload succeeded after retry", "what", label, "attempt", attempt+1)
			}
			return nil
		}
		if attempt >= len(retryDelays) {
			return err
		}
		logger.Warn("upload failed, retrying",
			"what", label, "attempt", attempt+1, "retry_in", retryDelays[attempt], "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryDelays[attempt]):
		}
	}
}
