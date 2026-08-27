package upload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// The multipart body is streamed rather than buffered; this checks the wire
// format still round-trips, including a payload larger than the pipe's
// internal copy buffer.
func TestHTTPUploader_StreamsMultipart(t *testing.T) {
	payload := bytes.Repeat([]byte("nightsky"), 128*1024) // 1 MiB

	dir := t.TempDir()
	path := filepath.Join(dir, "timelapse-2026-06-10.mp4")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	type received struct {
		auth     string
		filename string
		ctype    string
		body     []byte
	}
	var got received

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got.auth = r.Header.Get("Authorization")
		got.filename = r.FormValue("filename")
		got.ctype = r.FormValue("content_type")

		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		if hdr.Filename != "timelapse-2026-06-10.mp4" {
			t.Errorf("part filename = %q", hdr.Filename)
		}
		got.body, _ = io.ReadAll(f)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := NewHTTPUploader(HTTPConfig{URL: srv.URL, Authorization: "Bearer tok"}, retryLogger())
	if err := u.Upload(context.Background(), path); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if got.auth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", got.auth, "Bearer tok")
	}
	if got.filename != "timelapse-2026-06-10.mp4" {
		t.Errorf("filename field = %q", got.filename)
	}
	if got.ctype == "" {
		t.Error("content_type field is empty")
	}
	if !bytes.Equal(got.body, payload) {
		t.Errorf("body round-trip mismatch: got %d bytes, want %d", len(got.body), len(payload))
	}
}

// A missing file must fail before any request is made, and must not leave the
// writer goroutine blocked on the pipe.
func TestHTTPUploader_MissingFile(t *testing.T) {
	u := NewHTTPUploader(HTTPConfig{URL: "http://127.0.0.1:1"}, retryLogger())
	if err := u.Upload(context.Background(), filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
