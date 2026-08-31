package web

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dburman/nightsky/internal/config"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Output.Directory = dir
	// Defaulted to true by the config loader, which a struct literal bypasses.
	cfg.Output.Highlights.Enabled = true
	srv := New(cfg, ":0", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return srv, dir
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 4), G: 128, B: uint8(y * 5), A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestOutput_ServesThumbAndCaches(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/output/2026-06-10/allsky-1.png?w=32")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %s, want image/jpeg", ct)
	}

	// The thumbnail must land in the .thumbs cache for reuse.
	cache := filepath.Join(dir, "2026-06-10", ".thumbs", "allsky-1_32.jpg")
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("thumb not cached at %s: %v", cache, err)
	}
}

func TestOutput_ServesFullFile(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/output/2026-06-10/allsky-1.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestFocus_ReturnsStarStats(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	for i := 0; i < 2; i++ { // second hit exercises the mtime cache
		resp, err := http.Get(ts.URL + "/api/focus")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Stars int     `json:"stars"`
			FWHM  float64 `json:"fwhm"`
			File  string  `json:"file"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if body.File != "allsky-1.png" {
			t.Errorf("file = %q, want allsky-1.png", body.File)
		}
		// The gradient test image has no stars; the endpoint must still
		// answer with zeros rather than erroring.
		if body.Stars != 0 {
			t.Errorf("stars = %d on a starless frame, want 0", body.Stars)
		}
	}
}

func TestDateImages_IncludesHighlights(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))
	manifest := `{"date":"2026-06-10","frames":[{"file":"allsky-1.png","time":"01:23:45","stars":210,"cloud":0.1}]}`
	if err := os.WriteFile(filepath.Join(dir, "2026-06-10", "highlights-2026-06-10.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	// A protected highlight copy must appear via the manifest, not as a
	// duplicate in the main image grid.
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "highlight-1-allsky-1.png"))

	resp, err := http.Get(ts.URL + "/api/captures/2026-06-10/images")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Images []struct {
			Name string `json:"name"`
		} `json:"images"`
		Highlights []struct {
			Name  string `json:"name"`
			URL   string `json:"url"`
			Stars int    `json:"stars"`
		} `json:"highlights"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Highlights) != 1 {
		t.Fatalf("highlights = %d, want 1", len(body.Highlights))
	}
	h := body.Highlights[0]
	if h.Name != "allsky-1.png" || h.Stars != 210 || h.URL != "/output/2026-06-10/allsky-1.png" {
		t.Errorf("unexpected highlight: %+v", h)
	}
	for _, img := range body.Images {
		if strings.HasPrefix(img.Name, "highlight") {
			t.Errorf("highlight copy leaked into the image grid: %s", img.Name)
		}
	}
}

func TestFocus_NoImages(t *testing.T) {
	srv, _ := testServer(t)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/focus")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// Path traversal via the output route must never reach files outside the
// output directory.
func TestOutput_RejectsTraversal(t *testing.T) {
	srv, dir := testServer(t)

	// A secret outside the output root.
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/output/x", nil)
	req.SetPathValue("path", "../secret.txt")
	rec := httptest.NewRecorder()
	srv.handleOutput(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("traversal returned 200 with body %q", rec.Body.String())
	}
}

// The gallery needs to tell "no good frames tonight" apart from "the feature
// is switched off" when the Highlights tab is empty.
func TestDateImages_ReportsHighlightsDisabled(t *testing.T) {
	srv, dir := testServer(t)
	srv.cfg.Output.Highlights.Enabled = false
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))

	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/captures/2026-06-10/images")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		HighlightsEnabled bool `json:"highlights_enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.HighlightsEnabled {
		t.Error("highlights_enabled = true, want false")
	}
}

// A symlink inside the output tree pointing outside it must not be served.
// http.ServeFile followed such links; resolving through os.Root does not.
func TestOutput_RejectsSymlinkEscape(t *testing.T) {
	srv, dir := testServer(t)

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/output/link.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("symlink escape served %d with body %q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "SECRET") {
		t.Fatalf("response leaked the target file: %q", body)
	}
}

// A relative symlink that stays inside the output tree is still legitimate
// content and must keep working. Note the target has to be relative: os.Root
// rejects absolute symlink targets outright, even ones that would land inside
// the root. Anything in the pipeline that links frames (a future highlights
// implementation, say) must therefore create relative links.
func TestOutput_AllowsSymlinkInsideRoot(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))
	if err := os.Symlink("allsky-1.png", filepath.Join(dir, "2026-06-10", "latest.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/output/2026-06-10/latest.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("in-root symlink returned %d, want 200", resp.StatusCode)
	}
}

// The {date} path segment reaches the filesystem; ".." there must not list a
// directory outside the output root.
func TestDateImages_RejectsTraversal(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(filepath.Dir(dir), "outside.png"))

	req := httptest.NewRequest("GET", "/api/captures/x/images", nil)
	req.SetPathValue("date", "..")
	rec := httptest.NewRecorder()
	srv.handleDateImages(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("traversal via date segment returned 200: %s", rec.Body.String())
	}
}

// Every response carries the baseline security headers.
func TestSecurityHeaders(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))

	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	for _, path := range []string{"/", "/api/captures", "/output/2026-06-10/allsky-1.png"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP = %q, want frame-ancestors 'none'", path, got)
		}
	}
}

// writeSizedPNG writes a PNG whose width identifies it, so a test can tell
// which file a handler chose to serve.
func writeSizedPNG(t *testing.T, path string, width int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, 8))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"127.0.0.53:8080", true},
		{":8080", false}, // wildcard: every interface
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"192.168.1.10:8080", false},
		{"astrocam:8080", false}, // a name that is not localhost
		{"127.0.0.1", true},      // bare host, no port
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.addr); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// /latest is a documented endpoint for external dashboards: it must return the
// newest frame, honour ?w=, and skip synthesized outputs like the timelapse.
func TestLatest_ServesNewestFrame(t *testing.T) {
	srv, dir := testServer(t)
	// Each frame gets a distinct width so the response identifies which file
	// was served — otherwise this test would pass even if /latest returned
	// the keogram.
	writeSizedPNG(t, filepath.Join(dir, "2026-06-10", "allsky-20260610220000.png"), 10)
	writeSizedPNG(t, filepath.Join(dir, "2026-06-11", "allsky-20260611220000.png"), 20)
	writeSizedPNG(t, filepath.Join(dir, "2026-06-11", "allsky-20260611230000.png"), 30) // newest
	// Synthesized outputs must not be picked, even though they sort last.
	writeSizedPNG(t, filepath.Join(dir, "2026-06-11", "keogram-2026-06-11.png"), 40)
	writeSizedPNG(t, filepath.Join(dir, "2026-06-11", "startrails-2026-06-11.png"), 50)

	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /latest = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		t.Fatalf("decode /latest response: %v", err)
	}
	if got := img.Bounds().Dx(); got != 30 {
		t.Errorf("/latest served the frame of width %d, want 30 (the newest capture)", got)
	}

	// ?w= goes through the thumbnail path and comes back as JPEG.
	resp2, err := http.Get(ts.URL + "/latest?w=64")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /latest?w=64 = %d, want 200", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("thumb Content-Type = %q, want image/jpeg", ct)
	}
}

func TestLatest_NoImages(t *testing.T) {
	srv, _ := testServer(t)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /latest with no captures = %d, want 404", resp.StatusCode)
	}
}

// Cancelling the context must shut the server down and return, rather than
// blocking forever or dropping the listener without cleanup.
func TestListenAndServe_ShutsDownOnContextCancel(t *testing.T) {
	srv, dir := testServer(t)
	writeTestPNG(t, filepath.Join(dir, "2026-06-10", "allsky-1.png"))
	srv.addr = "127.0.0.1:0"

	// Bind a real port so we can confirm it served before shutting down.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.addr = ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()

	// Wait for the listener to come up.
	var resp *http.Response
	for i := 0; i < 100; i++ {
		resp, err = http.Get("http://" + srv.addr + "/api/captures")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("server never came up: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ListenAndServe returned %v, want nil after clean shutdown", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ListenAndServe did not return after context cancellation")
	}
}

func TestDisplayURL(t *testing.T) {
	cases := []struct{ addr, want string }{
		{":8080", "http://localhost:8080"},
		{"0.0.0.0:8080", "http://localhost:8080"},
		{"[::]:8080", "http://localhost:8080"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"192.168.1.50:8080", "http://192.168.1.50:8080"},
		{"[::1]:8080", "http://[::1]:8080"},
		{"astrocam:8080", "http://astrocam:8080"},
		{"garbage", "http://garbage"},
	}
	for _, c := range cases {
		if got := displayURL(c.addr); got != c.want {
			t.Errorf("displayURL(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}
