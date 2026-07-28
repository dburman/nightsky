package upload

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

func fastRetries(t *testing.T) {
	t.Helper()
	old := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = old })
}

func retryLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	fastRetries(t)
	calls := 0
	err := WithRetry(context.Background(), retryLogger(), "test", func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_ReturnsLastErrorWhenExhausted(t *testing.T) {
	fastRetries(t)
	calls := 0
	err := WithRetry(context.Background(), retryLogger(), "test", func() error {
		calls++
		return errors.New("permanent")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != 3 { // initial + 2 retries
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_StopsOnCancelledContext(t *testing.T) {
	// Real (long) delays: cancellation must short-circuit the wait.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	done := make(chan struct{})
	go func() {
		WithRetry(ctx, retryLogger(), "test", func() error {
			calls++
			return errors.New("fail")
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry loop did not honor cancelled context")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retries after cancel)", calls)
	}
}
