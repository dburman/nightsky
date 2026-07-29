package web

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dburman/nightsky/internal/config"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Output.Directory = dir
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
