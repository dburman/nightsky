//go:build zwo

package main

import (
	"log/slog"

	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/camera/zwo"
	"github.com/dburman/nightsky/internal/config"
)

func newZWOCamera(cfg *config.Config, logger *slog.Logger) (camera.Camera, error) {
	return zwo.New(cfg.Camera.Index, logger), nil
}
