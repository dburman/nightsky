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

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestBuildEncodeArgs_OptionalFlags(t *testing.T) {
	pngs := []string{"a.png", "b.png"}

	// Unset preset/tune/gop/threads must not appear.
	bare := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{FPS: 25, Codec: "libx264", Bitrate: "2000k"})
	for _, flag := range []string{"-preset", "-tune", "-g", "-threads", "-svtav1-params"} {
		if hasFlag(bare, flag) {
			t.Errorf("%s present when unset", flag)
		}
	}

	// Set values must appear; SVT-AV1 additionally gets lp= via -svtav1-params.
	full := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{
		FPS: 25, Codec: "libsvtav1", CRF: 32, Preset: "6", GOP: 250, Tune: "psnr", Threads: 2,
	})
	for _, flag := range []string{"-preset", "-tune", "-g", "-crf", "-threads", "-svtav1-params"} {
		if !hasFlag(full, flag) {
			t.Errorf("%s missing when set", flag)
		}
	}

	// Non-AV1 codecs get -threads but not -svtav1-params.
	x264 := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{FPS: 25, Codec: "libx264", Bitrate: "2000k", Threads: 2})
	if !hasFlag(x264, "-threads") || hasFlag(x264, "-svtav1-params") {
		t.Error("x264 threads handling wrong")
	}

	// All-WebP input forces the WebP decoder before -i.
	webp := buildEncodeArgs("list.txt", "out.mp4", []string{"a.webp"}, Config{FPS: 25, Codec: "libx264", Bitrate: "2000k"})
	if !hasFlag(webp, "-c:v") {
		t.Error("all-WebP input did not force the webp decoder")
	}
}

// The encode must convert AND tag BT.709 limited range in one -vf chain —
// mismatched conversion coefficients vs container tags shift saturated hues.
func TestBuildEncodeArgs_ColorHandling(t *testing.T) {
	pngs := []string{"a.png"}

	const colorChain = "scale=out_color_matrix=bt709:out_range=tv," +
		"setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709:range=tv"

	plain := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{FPS: 25, Codec: "libx264", Bitrate: "2000k"})
	if got := flagValue(plain, "-vf"); got != colorChain {
		t.Errorf("-vf = %q, want bt709 convert+tag chain", got)
	}

	// Deflicker joins the same single -vf chain (ffmpeg honours only one -vf).
	withDeflicker := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{FPS: 25, Codec: "libx264", Bitrate: "2000k", Deflicker: true})
	vfCount := 0
	for _, a := range withDeflicker {
		if a == "-vf" {
			vfCount++
		}
	}
	if vfCount != 1 {
		t.Fatalf("-vf appears %d times, want exactly 1", vfCount)
	}
	if got := flagValue(withDeflicker, "-vf"); got != "deflicker=size=5:mode=am,"+colorChain {
		t.Errorf("combined -vf = %q", got)
	}
}

func TestBuildEncodeArgs_PixFmt(t *testing.T) {
	pngs := []string{"a.png"}
	def := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{FPS: 25, Codec: "libx264", Bitrate: "2000k"})
	if got := flagValue(def, "-pix_fmt"); got != "yuv420p" {
		t.Errorf("default pix_fmt = %q, want yuv420p", got)
	}
	tenBit := buildEncodeArgs("list.txt", "out.mp4", pngs, Config{FPS: 25, Codec: "libsvtav1", CRF: 32, PixFmt: "yuv420p10le"})
	if got := flagValue(tenBit, "-pix_fmt"); got != "yuv420p10le" {
		t.Errorf("pix_fmt override = %q, want yuv420p10le", got)
	}
}

func TestFromConfig_MapsAllFields(t *testing.T) {
	c := config.TimelapseConfig{
		FPS: 30, Bitrate: "3000k", Codec: "libsvtav1", CRF: 32,
		Deflicker: true, Preset: "6", GOP: 250, Tune: "psnr", Threads: 2,
	}
	got := FromConfig(c)
	want := Config{
		FPS: 30, Bitrate: "3000k", Codec: "libsvtav1", CRF: 32,
		Deflicker: true, Preset: "6", GOP: 250, Tune: "psnr", Threads: 2,
	}
	if got != want {
		t.Errorf("FromConfig = %+v, want %+v", got, want)
	}
}

// A PNG/WebP mix (partially converted night) must reduce to the dominant
// format in order, not feed a mixed list to the concat demuxer.
func TestDominantFormat(t *testing.T) {
	mixed := []string{
		"a-01.webp", "a-02.webp", // early frames converted by an earlier run
		"a-03.png", "a-04.png", "a-05.png", "a-06.png",
	}
	kept, dropped := dominantFormat(mixed)
	if dropped != 2 || len(kept) != 4 {
		t.Fatalf("kept %d dropped %d, want 4/2", len(kept), dropped)
	}
	for i, want := range []string{"a-03.png", "a-04.png", "a-05.png", "a-06.png"} {
		if kept[i] != want {
			t.Errorf("kept[%d] = %s, want %s", i, kept[i], want)
		}
	}

	// jpg and jpeg are the same codec family — not a mix.
	jpgs := []string{"a.jpg", "b.jpeg", "c.jpg"}
	if kept, dropped := dominantFormat(jpgs); dropped != 0 || len(kept) != 3 {
		t.Errorf("jpg/jpeg treated as mixed: kept %d dropped %d", len(kept), dropped)
	}

	// Homogeneous list passes through untouched.
	if _, dropped := dominantFormat([]string{"a.png", "b.png"}); dropped != 0 {
		t.Errorf("homogeneous list dropped %d", dropped)
	}
	if kept, dropped := dominantFormat(nil); kept != nil || dropped != 0 {
		t.Error("nil list should be a no-op")
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
