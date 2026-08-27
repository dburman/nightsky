package upload

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// HTTPUploader uploads files via HTTP POST (multipart form data).
type HTTPUploader struct {
	url           string
	authorization string
	client        *http.Client
	logger        *slog.Logger
}

// HTTPConfig holds HTTP upload configuration.
type HTTPConfig struct {
	URL           string
	Authorization string
}

// NewHTTPUploader creates a new HTTP upload client.
func NewHTTPUploader(cfg HTTPConfig, logger *slog.Logger) *HTTPUploader {
	return &HTTPUploader{
		url:           cfg.URL,
		authorization: cfg.Authorization,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		logger: logger,
	}
}

// Upload uploads a file via HTTP POST multipart form data.
//
// The multipart body is streamed through an io.Pipe rather than assembled in
// a bytes.Buffer: a night's timelapse can be hundreds of megabytes, and
// buffering it would be the single largest allocation in the process — on a
// 512 MB board that competes directly with capture and the ffmpeg encode.
// Streaming keeps the footprint at one copy buffer regardless of file size.
func (u *HTTPUploader) Upload(ctx context.Context, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	name := filepath.Base(localPath)
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	// Read the boundary before the writer goroutine starts touching it.
	contentType := writer.FormDataContentType()

	go func() {
		// CloseWithError(nil) is equivalent to Close, so a single deferred
		// call reports whichever error stopped the write to the reader — the
		// request then fails with that error instead of sending a truncated
		// body.
		var werr error
		defer func() { pw.CloseWithError(werr) }()

		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			werr = fmt.Errorf("create form file: %w", err)
			return
		}
		if _, err := io.Copy(part, f); err != nil {
			werr = fmt.Errorf("copy file data: %w", err)
			return
		}
		if err := writer.WriteField("filename", name); err != nil {
			werr = fmt.Errorf("write filename field: %w", err)
			return
		}
		if err := writer.WriteField("content_type", detectContentType(name)); err != nil {
			werr = fmt.Errorf("write content_type field: %w", err)
			return
		}
		if err := writer.Close(); err != nil {
			werr = fmt.Errorf("close multipart writer: %w", err)
		}
	}()
	// Abandoning the request (error, context cancellation) must unblock the
	// writer goroutine rather than leak it.
	defer pr.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, pr)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	if u.authorization != "" {
		req.Header.Set("Authorization", u.authorization)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	u.logger.Info("uploaded via HTTP",
		"url", u.url,
		"file", name,
		"size_kb", stat.Size()/1024,
		"status", resp.StatusCode,
	)

	return nil
}
