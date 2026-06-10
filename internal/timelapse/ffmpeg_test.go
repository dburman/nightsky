package timelapse

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// A segment encode killed mid-write leaves a .tmp.mp4 behind; it must never
// be picked up as a real segment for concatenation.
func TestCollectSegments_ExcludesTmpFiles(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "timelapse-segment-0000.mp4")
	touch(t, dir, "timelapse-segment-0001.mp4")
	touch(t, dir, "timelapse-segment-0002.tmp.mp4") // killed mid-encode
	touch(t, dir, "timelapse-2026-06-09.mp4")       // final video, not a segment
	touch(t, dir, "allsky-20260609221530.jpg")

	segs, err := collectSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "timelapse-segment-0000.mp4"),
		filepath.Join(dir, "timelapse-segment-0001.mp4"),
	}
	if len(segs) != len(want) {
		t.Fatalf("got %v, want %v", segs, want)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Errorf("segs[%d] = %s, want %s", i, segs[i], want[i])
		}
	}
}

func TestConcatListEntry_EscapesQuotes(t *testing.T) {
	got := concatListEntry("it's-allsky-1.jpg")
	want := `file 'it'\''s-allsky-1.jpg'` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if plain := concatListEntry("allsky-1.jpg"); plain != "file 'allsky-1.jpg'\n" {
		t.Errorf("got %q for plain name", plain)
	}
}

func TestSegmentIndex(t *testing.T) {
	cases := []struct {
		path string
		idx  int
		ok   bool
	}{
		{"/x/timelapse-segment-0000.mp4", 0, true},
		{"/x/timelapse-segment-0042.mp4", 42, true},
		{"/x/timelapse-2026-06-09.mp4", 0, false},
		{"/x/timelapse-segment-abcd.mp4", 0, false},
	}
	for _, c := range cases {
		idx, ok := segmentIndex(c.path)
		if idx != c.idx || ok != c.ok {
			t.Errorf("segmentIndex(%q) = (%d, %v), want (%d, %v)", c.path, idx, ok, c.idx, c.ok)
		}
	}
}

func TestRemoveStaleTmp(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "timelapse-segment-0002.tmp.mp4")
	touch(t, dir, "timelapse-2026-06-09.tmp.mp4")
	touch(t, dir, "timelapse-segment-0001.mp4")
	touch(t, dir, "allsky-20260609221530.jpg")

	removeStaleTmp(dir, testLogger())

	for _, gone := range []string{"timelapse-segment-0002.tmp.mp4", "timelapse-2026-06-09.tmp.mp4"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	for _, kept := range []string{"timelapse-segment-0001.mp4", "allsky-20260609221530.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
}
