package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a minimal valid config containing body and loads it.
func loadWith(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nightsky.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// The end-of-night output toggles default to on, so omitting them from the
// yaml must not silently disable the feature.
func TestDefaults_NightOutputsEnabled(t *testing.T) {
	cfg := loadWith(t, "camera:\n  type: libcamera\n")

	for _, tc := range []struct {
		name string
		got  bool
	}{
		{"keogram", cfg.Output.Keogram.Enabled},
		{"startrails", cfg.Output.StarTrails.Enabled},
		{"highlights", cfg.Output.Highlights.Enabled},
	} {
		if !tc.got {
			t.Errorf("%s.enabled = false by default, want true", tc.name)
		}
	}
}

func TestHighlights_ExplicitlyDisabled(t *testing.T) {
	cfg := loadWith(t, "camera:\n  type: libcamera\noutput:\n  highlights:\n    enabled: false\n")
	if cfg.Output.Highlights.Enabled {
		t.Error("highlights.enabled = true, want false when set false in yaml")
	}
	// A sibling toggle in the same block keeps its default.
	if !cfg.Output.Keogram.Enabled {
		t.Error("keogram.enabled = false, want true (untouched default)")
	}
}

// README documents NIGHTSKY_* environment overrides; viper only honours them
// when the dot-to-underscore key replacer is installed and the key is
// registered, so both are covered here.
func TestLoad_EnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightsky.yaml")
	if err := os.WriteFile(path, []byte("camera:\n  type: libcamera\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("NIGHTSKY_CAMERA_TYPE", "zwo")
	t.Setenv("NIGHTSKY_OUTPUT_DIRECTORY", "/srv/frames")
	t.Setenv("NIGHTSKY_UPLOAD_HTTP_AUTHORIZATION", "Bearer secret")
	t.Setenv("NIGHTSKY_UPLOAD_S3_BUCKET", "sky-bucket")
	t.Setenv("NIGHTSKY_ALERTS_AURORA_MIN_FRAMES", "7")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Camera.Type != "zwo" {
		t.Errorf("camera.type = %q, want zwo (env must beat the config file)", cfg.Camera.Type)
	}
	if cfg.Output.Directory != "/srv/frames" {
		t.Errorf("output.directory = %q, want /srv/frames", cfg.Output.Directory)
	}
	if cfg.Upload.HTTP.Authorization != "Bearer secret" {
		t.Errorf("upload.http.authorization = %q, want %q", cfg.Upload.HTTP.Authorization, "Bearer secret")
	}
	if cfg.Upload.S3.Bucket != "sky-bucket" {
		t.Errorf("upload.s3.bucket = %q, want sky-bucket", cfg.Upload.S3.Bucket)
	}
	if cfg.Alerts.Aurora.MinFrames != 7 {
		t.Errorf("alerts.aurora.min_frames = %d, want 7", cfg.Alerts.Aurora.MinFrames)
	}
}

// The web listener is configurable from YAML and the environment, not just the
// --addr flag, so a systemd or Docker deployment can expose the UI without
// editing a unit file.
func TestLoad_WebAddr(t *testing.T) {
	t.Run("defaults to loopback", func(t *testing.T) {
		cfg := loadWith(t, "camera:\n  type: libcamera\n")
		if cfg.Web.Addr != "127.0.0.1:8080" {
			t.Errorf("web.addr = %q, want 127.0.0.1:8080", cfg.Web.Addr)
		}
	})

	t.Run("config file overrides the default", func(t *testing.T) {
		cfg := loadWith(t, "web:\n  addr: \"0.0.0.0:9000\"\n")
		if cfg.Web.Addr != "0.0.0.0:9000" {
			t.Errorf("web.addr = %q, want 0.0.0.0:9000", cfg.Web.Addr)
		}
	})

	t.Run("environment overrides the config file", func(t *testing.T) {
		t.Setenv("NIGHTSKY_WEB_ADDR", "192.168.1.50:8080")
		cfg := loadWith(t, "web:\n  addr: \"0.0.0.0:9000\"\n")
		if cfg.Web.Addr != "192.168.1.50:8080" {
			t.Errorf("web.addr = %q, want 192.168.1.50:8080", cfg.Web.Addr)
		}
	})
}
