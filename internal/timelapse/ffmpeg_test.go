package timelapse

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dburman/nightsky/internal/config"
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

// encodeArgs is the arg-building logic of encodeImages, extracted for testing
// without invoking ffmpeg. Keep in sync with encodeImages.
func buildEncodeArgsForTest(listPath string, cfg Config, allWebPInput bool) []string {
	args := []string{"-y", "-r", "25", "-f", "concat", "-safe", "0"}
	if allWebPInput {
		args = append(args, "-c:v", "webp")
	}
	args = append(args, "-i", listPath, "-vcodec", cfg.Codec)
	if cfg.CRF > 0 {
		args = append(args, "-crf", "x")
	} else {
		args = append(args, "-b:v", cfg.Bitrate)
	}
	if cfg.Preset != "" {
		args = append(args, "-preset", cfg.Preset)
	}
	if cfg.Tune != "" {
		args = append(args, "-tune", cfg.Tune)
	}
	if cfg.GOP > 0 {
		args = append(args, "-g", "x")
	}
	return args
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestEncodeArgs_OptionalFlags(t *testing.T) {
	// Unset preset/tune/gop must not appear.
	bare := buildEncodeArgsForTest("list.txt", Config{Codec: "libx264", Bitrate: "2000k"}, false)
	for _, flag := range []string{"-preset", "-tune", "-g"} {
		if hasFlag(bare, flag) {
			t.Errorf("%s present when unset", flag)
		}
	}

	// Set values must appear.
	full := buildEncodeArgsForTest("list.txt", Config{
		Codec: "libsvtav1", CRF: 32, Preset: "6", GOP: 250, Tune: "psnr",
	}, false)
	for _, flag := range []string{"-preset", "-tune", "-g", "-crf"} {
		if !hasFlag(full, flag) {
			t.Errorf("%s missing when set", flag)
		}
	}
}

func TestFromConfig_MapsAllFields(t *testing.T) {
	c := config.TimelapseConfig{
		FPS: 30, Bitrate: "3000k", Codec: "libsvtav1", CRF: 32,
		Deflicker: true, Preset: "6", GOP: 250, Tune: "psnr",
	}
	got := FromConfig(c)
	want := Config{
		FPS: 30, Bitrate: "3000k", Codec: "libsvtav1", CRF: 32,
		Deflicker: true, Preset: "6", GOP: 250, Tune: "psnr",
	}
	if got != want {
		t.Errorf("FromConfig = %+v, want %+v", got, want)
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
