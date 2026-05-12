package upload

import (
	"bytes"
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

	// Build multipart form.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}

	if _, err := io.Copy(part, f); err != nil {
		return fmt.Errorf("copy file data: %w", err)
	}

	// Add metadata fields.
	writer.WriteField("filename", filepath.Base(localPath))
	writer.WriteField("content_type", detectContentType(filepath.Base(localPath)))

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, &body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
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
		"file", filepath.Base(localPath),
		"size_kb", stat.Size()/1024,
		"status", resp.StatusCode,
	)

	return nil
}
