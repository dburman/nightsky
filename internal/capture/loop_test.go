package capture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweepStaleTmp(t *testing.T) {
	root := t.TempDir()
	dateDir := filepath.Join(root, "2026-06-10")
	if err := os.MkdirAll(dateDir, 0755); err != nil {
		t.Fatal(err)
	}

	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dateDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("allsky-20260610221530.png.tmp") // interrupted frame write
	write("allsky-20260610221540.dng.tmp") // interrupted DNG write
	write("allsky-20260610221530.png")     // real frame
	write("timelapse-2026-06-10.mp4")      // real output

	n := sweepStaleTmp(root)
	if n != 2 {
		t.Errorf("removed %d, want 2", n)
	}
	for _, gone := range []string{"allsky-20260610221530.png.tmp", "allsky-20260610221540.dng.tmp"} {
		if _, err := os.Stat(filepath.Join(dateDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	for _, kept := range []string{"allsky-20260610221530.png", "timelapse-2026-06-10.mp4"} {
		if _, err := os.Stat(filepath.Join(dateDir, kept)); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
}

func TestSweepStaleTmp_MissingDir(t *testing.T) {
	if n := sweepStaleTmp(filepath.Join(t.TempDir(), "nope")); n != 0 {
		t.Errorf("missing dir should sweep nothing, got %d", n)
	}
}
