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
