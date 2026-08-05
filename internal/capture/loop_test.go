package capture

import (
	"encoding/json"
	"io"
	"log/slog"
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
	cloudyOnly := []cloud.Metric{{Timestamp: base, Coverage: 0.9, StarCount: 0}}
	quiet := nightSummaryMessage("2026-07-28", cloudyOnly, 0, "")
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

// Backfill path: metrics read from a CSV predating the file column carry no
// filename, so WriteHighlights must recover the frame from its timestamp.
func TestWriteHighlights_BackfillsByTimestamp(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	base := time.Date(2026, 8, 4, 22, 34, 17, 0, time.Local)
	var ms []cloud.Metric
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		name := "sky-" + ts.Format("20060102150405") + ".png"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("frame"), 0644); err != nil {
			t.Fatal(err)
		}
		ms = append(ms, cloud.Metric{
			Timestamp: ts,
			StarCount: 100 + i, // last frame scores highest
			Coverage:  0.4,
			// File deliberately empty — the legacy CSV had no such column.
		})
	}

	WriteHighlights(dir, ms, logger)

	data, err := os.ReadFile(filepath.Join(dir, "highlights-"+filepath.Base(dir)+".json"))
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
	var manifest struct {
		Frames []struct {
			File  string `json:"file"`
			Stars int    `json:"stars"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("bad manifest: %v", err)
	}
	if len(manifest.Frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(manifest.Frames))
	}
	// Highest star count ranks first, and the pick is a protected copy.
	top := manifest.Frames[0]
	if top.Stars != 102 {
		t.Errorf("top frame stars = %d, want 102", top.Stars)
	}
	want := "highlight-1-sky-" + base.Add(2*time.Minute).Format("20060102150405") + ".png"
	if top.File != want {
		t.Errorf("top frame file = %q, want %q", top.File, want)
	}
	if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
		t.Errorf("protected copy missing: %v", err)
	}
}

// Re-running must not nest highlight- prefixes or pick a prior run's copies.
func TestWriteHighlights_RerunIsStable(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ts := time.Date(2026, 8, 4, 22, 34, 17, 0, time.Local)
	name := "sky-" + ts.Format("20060102150405") + ".png"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("frame"), 0644); err != nil {
		t.Fatal(err)
	}
	ms := []cloud.Metric{{Timestamp: ts, StarCount: 50, Coverage: 0.2}}

	WriteHighlights(dir, ms, logger)
	WriteHighlights(dir, ms, logger)

	copies, _ := filepath.Glob(filepath.Join(dir, "highlight-*-*.png"))
	if len(copies) != 1 {
		t.Fatalf("got %d protected copies after re-run, want 1: %v", len(copies), copies)
	}
	if got := filepath.Base(copies[0]); got != "highlight-1-"+name {
		t.Errorf("copy = %q, want %q", got, "highlight-1-"+name)
	}
}
