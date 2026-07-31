package capture

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func dateLabel(daysAgo int) string {
	return time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")
}

func mkDateDir(t *testing.T, root string, label string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, label)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func dirExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatal(err)
	return false
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// days_to_keep=1 must keep today and the just-finished night (dated
// yesterday), and remove everything older. This pins the fix for the
// UTC-parse vs local-cutoff comparison that deleted the night that had
// just ended.
func TestCleanOldData_KeepsJustFinishedNight(t *testing.T) {
	root := t.TempDir()
	today := mkDateDir(t, root, dateLabel(0), "a.jpg")
	yesterday := mkDateDir(t, root, dateLabel(1), "a.jpg")
	old2 := mkDateDir(t, root, dateLabel(2), "a.jpg")
	old3 := mkDateDir(t, root, dateLabel(3), "a.jpg")
	other := mkDateDir(t, root, "not-a-date", "a.jpg")

	if err := CleanOldData(root, 1, testLogger()); err != nil {
		t.Fatal(err)
	}

	if !dirExists(t, today) {
		t.Error("today's directory was removed")
	}
	if !dirExists(t, yesterday) {
		t.Error("just-finished night (yesterday) was removed with days_to_keep=1")
	}
	if dirExists(t, old2) || dirExists(t, old3) {
		t.Error("directories older than days_to_keep were not removed")
	}
	if !dirExists(t, other) {
		t.Error("non-date directory was removed")
	}
}

func TestCleanOldData_Disabled(t *testing.T) {
	root := t.TempDir()
	old := mkDateDir(t, root, dateLabel(30), "a.jpg")

	if err := CleanOldData(root, 0, testLogger()); err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, old) {
		t.Error("cleanup ran with days_to_keep=0")
	}
}

// prune_raw_after_days=1 must not touch the night that just ended
// (dated yesterday); a 2-day-old dir loses raw frames and the .thumbs
// cache but keeps synthesized outputs and DNGs.
func TestPruneRawImages_Boundary(t *testing.T) {
	root := t.TempDir()
	yesterday := mkDateDir(t, root, dateLabel(1), "allsky-1.jpg")
	old := mkDateDir(t, root, dateLabel(2),
		"allsky-1.jpg", "allsky-2.png", "allsky-3.dng",
		"timelapse-x.mp4", "keogram-x.jpg", "startrails-x.jpg",
		"highlight-1-allsky-1.jpg", "highlights-2026-06-08.json",
	)
	if err := os.MkdirAll(filepath.Join(old, ".thumbs"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := PruneRawImages(root, 1, testLogger()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(yesterday, "allsky-1.jpg")); err != nil {
		t.Error("just-finished night was pruned with prune_raw_after_days=1")
	}

	for _, gone := range []string{"allsky-1.jpg", "allsky-2.png", ".thumbs"} {
		if _, err := os.Stat(filepath.Join(old, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", gone)
		}
	}
	for _, kept := range []string{"allsky-3.dng", "timelapse-x.mp4", "keogram-x.jpg", "startrails-x.jpg",
		"highlight-1-allsky-1.jpg", "highlights-2026-06-08.json"} {
		if _, err := os.Stat(filepath.Join(old, kept)); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
}

// With an unreachable free-space target, CleanForSpace must remove old
// directories oldest-first but never today's directory or the protected
// (just-finished) night, and must stop without error once only protected
// directories remain.
func TestCleanForSpace_ProtectsActiveNight(t *testing.T) {
	root := t.TempDir()
	today := mkDateDir(t, root, dateLabel(0), "a.jpg")
	night := mkDateDir(t, root, dateLabel(1), "a.jpg")
	old2 := mkDateDir(t, root, dateLabel(2), "a.jpg")
	old3 := mkDateDir(t, root, dateLabel(3), "a.jpg")

	// Free space can never exceed this, so cleanup runs until only
	// protected dirs remain.
	const impossible = 1 << 30

	if err := CleanForSpace(root, impossible, dateLabel(1), testLogger()); err != nil {
		t.Fatal(err)
	}

	if !dirExists(t, today) {
		t.Error("today's directory was removed")
	}
	if !dirExists(t, night) {
		t.Error("protected night directory was removed")
	}
	if dirExists(t, old2) || dirExists(t, old3) {
		t.Error("old directories were not removed under disk pressure")
	}
}

func TestCleanForSpace_NoopWhenSpaceSufficient(t *testing.T) {
	root := t.TempDir()
	old := mkDateDir(t, root, dateLabel(30), "a.jpg")

	// Threshold of 0 GB is always satisfied.
	if err := CleanForSpace(root, 0, "", testLogger()); err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, old) {
		t.Error("directory removed despite sufficient free space")
	}
}

func TestFailureBackoff(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{7, 60 * time.Second},
		{100, 60 * time.Second},
		{0, time.Second}, // defensive
	}
	for _, c := range cases {
		if got := failureBackoff(c.n); got != c.want {
			t.Errorf("failureBackoff(%d) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestCleanForSpace_ErrorOnMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := CleanForSpace(missing, 1<<30, "", testLogger()); err == nil {
		t.Error("expected error for missing output directory")
	}
}
