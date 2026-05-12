// Package upload provides upload backends for S3 and HTTP.
package upload

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Uploader uploads files to an S3 bucket.
type S3Uploader struct {
	client   *s3.Client
	bucket   string
	prefix   string
	logger   *slog.Logger
}

// S3Config holds S3 upload configuration.
type S3Config struct {
	Bucket   string
	Region   string
	Prefix   string
	Endpoint string // optional custom endpoint (for S3-compatible services)
}

// NewS3Uploader creates a new S3 upload client.
func NewS3Uploader(ctx context.Context, cfg S3Config, logger *slog.Logger) (*S3Uploader, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	var clientOpts []func(*s3.Options)
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)

	return &S3Uploader{
		client: client,
		bucket: cfg.Bucket,
		prefix: cfg.Prefix,
		logger: logger,
	}, nil
}

// Upload uploads a file to S3.
// The S3 key is constructed as: <prefix>/<filename> or <prefix>/<subdir>/<filename>.
func (u *S3Uploader) Upload(ctx context.Context, localPath string, subdir string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	filename := filepath.Base(localPath)
	key := filepath.Join(u.prefix, subdir, filename)
	// Normalize to forward slashes for S3.
	key = strings.ReplaceAll(key, string(filepath.Separator), "/")

	contentType := detectContentType(filename)

	_, err = u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put object s3://%s/%s: %w", u.bucket, key, err)
	}

	u.logger.Info("uploaded to S3",
		"bucket", u.bucket,
		"key", key,
	)

	return nil
}

// UploadDirectory uploads all files in a directory to S3.
func (u *S3Uploader) UploadDirectory(ctx context.Context, dir string, subdir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		localPath := filepath.Join(dir, entry.Name())
		if err := u.Upload(ctx, localPath, subdir); err != nil {
			u.logger.Error("upload failed", "file", entry.Name(), "error", err)
			// Continue with other files.
		}
	}

	return nil
}

func detectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".mp4":
		return "video/mp4"
	case ".avi":
		return "video/x-msvideo"
	default:
		return "application/octet-stream"
	}
}
