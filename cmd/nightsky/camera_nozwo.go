//go:build !zwo

package main

import (
	"fmt"
	"log/slog"

	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/config"
)

func newZWOCamera(_ *config.Config, _ *slog.Logger) (camera.Camera, error) {
	return nil, fmt.Errorf("ZWO camera support not compiled in (build with -tags zwo)")
}

// Ensure camera.Camera interface is referenced to avoid import cycle errors.
var _ camera.Camera
