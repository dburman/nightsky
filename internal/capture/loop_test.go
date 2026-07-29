package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dburman/nightsky/internal/cloud"
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

func TestStaleLimit(t *testing.T) {
	cases := []struct {
		delay time.Duration
		want  time.Duration
	}{
		{0, 30 * time.Minute},                 // night: continuous capture
		{5 * time.Second, 30 * time.Minute},   // short delay: base applies
		{10 * time.Minute, 30 * time.Minute},  // 3x = 30m, base still applies
		{20 * time.Minute, 60 * time.Minute},  // long day delay stretches limit
	}
	for _, c := range cases {
		if got := staleLimit(c.delay); got != c.want {
			t.Errorf("staleLimit(%v) = %v, want %v", c.delay, got, c.want)
		}
	}
}

func TestNightSummaryMessage(t *testing.T) {
	base := time.Date(2026, 7, 28, 22, 0, 0, 0, time.UTC)
	ms := []cloud.Metric{
		{Timestamp: base, Coverage: 0.1, StarCount: 120},
		{Timestamp: base.Add(4 * time.Hour), Coverage: 0.2, StarCount: 340},
		{Timestamp: base.Add(8 * time.Hour), Coverage: 0.9, StarCount: 0},
	}
	msg := nightSummaryMessage("2026-07-28", ms, 2, "http://astrocam:8080/")

	for _, want := range []string{
		"Night 2026-07-28: 3 frames",
		"over 8h0m0s",
		"Peak stars: 340",
		"Aurora alerts: 2",
		"http://astrocam:8080",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary missing %q in: %s", want, msg)
		}
	}
	if strings.Contains(msg, "8080/") {
		t.Errorf("trailing slash not trimmed: %s", msg)
	}

	// No stars / no aurora / no URL → those clauses are absent.
	quiet := nightSummaryMessage("2026-07-28", ms[:1], 0, "")
	for _, absent := range []string{"Peak stars", "Aurora", "http"} {
		if strings.Contains(quiet, absent) {
			t.Errorf("quiet summary should not contain %q: %s", absent, quiet)
		}
	}
}

func TestSweepStaleTmp_MissingDir(t *testing.T) {
	if n := sweepStaleTmp(filepath.Join(t.TempDir(), "nope")); n != 0 {
		t.Errorf("missing dir should sweep nothing, got %d", n)
	}
}
