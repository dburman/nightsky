package web

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	_ "golang.org/x/image/webp"
	"image"
	"image/jpeg"
	_ "image/png"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dburman/nightsky/internal/config"
	imgutil "github.com/dburman/nightsky/internal/image"
	"github.com/dburman/nightsky/internal/metrics"
	"github.com/dburman/nightsky/internal/stars"
)

//go:embed static/index.html
var indexHTML []byte

// Server is the web UI HTTP server.
type Server struct {
	cfg    *config.Config
	addr   string
	logger *slog.Logger

	// thumbSem bounds concurrent on-demand thumbnail decodes. A cache-miss
	// decode holds a full-resolution frame in memory; without a bound, one
	// phone scrolling the gallery can trigger several simultaneous decodes
	// while capture and an encode are running on a low-memory board.
	thumbSem chan struct{}

	// Focus-aid cache: star detection on the latest frame is recomputed only
	// when the frame changes, so focus-mode polling doesn't re-decode the
	// same image. Guarded by focusMu.
	focusMu   sync.Mutex
	focusPath string
	focusMod  time.Time
	focusRes  stars.Result
}

// New creates a new Server.
func New(cfg *config.Config, addr string, logger *slog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		addr:     addr,
		logger:   logger,
		thumbSem: make(chan struct{}, 2),
	}
}

// routes builds the request mux (separated from ListenAndServe for tests).
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/captures", s.handleCaptures)
	mux.HandleFunc("GET /api/captures/{date}/images", s.handleDateImages)
	mux.HandleFunc("GET /api/focus", s.handleFocus)
	mux.HandleFunc("GET /latest", s.handleLatest)
	mux.HandleFunc("GET /output/{path...}", s.handleOutput)
	return mux
}

// contentSecurityPolicy is written for the single embedded page: everything it
// needs is same-origin, apart from the inline <style> and <script> it is built
// from and the data: URI favicon. 'unsafe-inline' is therefore unavoidable
// without hashing the blocks, but the rest of the policy still does real work
// — it blocks any external load, plugin, or <base> rewrite that an injected
// filename could otherwise reach for, and frame-ancestors 'none' keeps the
// camera's UI out of someone else's frame.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"media-src 'self'; " +
	"connect-src 'self'; " +
	"font-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

// securityHeaders sets the response headers that apply to every route. nosniff
// matters most on /output, which serves file bytes under a Content-Type
// derived from a filename the server did not choose.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// handler returns the fully wrapped HTTP handler.
func (s *Server) handler() http.Handler {
	return securityHeaders(s.routes())
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled, at
// which point in-flight responses are given a grace period to finish — a
// timelapse download shouldn't be severed by a restart.
//
// The server sets explicit timeouts so dead or dawdling connections (flaky
// WiFi, Slowloris) can't accumulate file descriptors over weeks of uptime.
// WriteTimeout stays generous because timelapse MP4s are streamed over slow
// links.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	s.logger.Info("web UI started", "url", displayURL(s.addr))
	if !isLoopback(s.addr) {
		// The UI has no authentication and /api/config reports the camera's
		// coordinates, so binding beyond loopback is a deliberate choice the
		// operator should see in the log.
		s.logger.Warn("web UI is reachable beyond localhost and has no authentication; "+
			"put it behind a reverse proxy or restrict it at the firewall",
			"addr", s.addr)
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Info("shutting down web UI")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	}
}

// displayURL renders a listen address as a URL a person can click. A wildcard
// bind ("", "0.0.0.0", "::") has no single address, so it shows as localhost;
// anything else is shown as bound, with IPv6 literals kept in brackets.
func displayURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// isLoopback reports whether a listen address binds only the loopback
// interface. An empty or wildcard host ("" or ":8080") binds everything.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, map[string]string{
		"version":     config.Version,
		"camera_type": s.cfg.Camera.Type,
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	// Return a sanitized copy — never expose credentials over the API.
	cfg := *s.cfg
	if cfg.Upload.HTTP.Authorization != "" {
		cfg.Upload.HTTP.Authorization = "[redacted]"
	}
	// An ntfy-style webhook URL contains the (secret) topic.
	if cfg.Alerts.WebhookURL != "" {
		cfg.Alerts.WebhookURL = "[redacted]"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap, err := metrics.Read(s.cfg.Output.MetricsDirectory())
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "capture not running", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to read metrics", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	s.writeJSON(w, snap)
}

type captureEntry struct {
	Date       string `json:"date"`
	ImageCount int    `json:"image_count"`
	HasVideo   bool   `json:"has_video"`
}

func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	root, err := s.openOutputRoot()
	if err != nil {
		if os.IsNotExist(err) {
			s.writeJSON(w, []captureEntry{})
			return
		}
		http.Error(w, "failed to read output directory", http.StatusInternalServerError)
		return
	}
	defer root.Close()

	fsys := root.FS()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		http.Error(w, "failed to read output directory", http.StatusInternalServerError)
		return
	}

	var captures []captureEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Match YYYY-MM-DD pattern
		if len(name) != 10 || name[4] != '-' || name[7] != '-' {
			continue
		}

		files, err := fs.ReadDir(fsys, name)
		if err != nil {
			continue
		}

		imageCount := 0
		hasVideo := false
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			lower := strings.ToLower(f.Name())
			if isImageFile(lower) {
				imageCount++
			}
			if strings.HasSuffix(lower, ".mp4") {
				hasVideo = true
			}
		}

		captures = append(captures, captureEntry{
			Date:       name,
			ImageCount: imageCount,
			HasVideo:   hasVideo,
		})
	}

	// Sort newest-first. The comparator is a real three-way compare (0 for
	// equal): slices.SortFunc requires a strict weak ordering, and a
	// comparator that never returns 0 does not provide one.
	slices.SortFunc(captures, func(a, b captureEntry) int {
		return strings.Compare(b.Date, a.Date)
	})

	s.writeJSON(w, captures)
}

type imageEntry struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	ThumbURL string `json:"thumb_url"`
	Time     string `json:"time"`
}

type highlightEntry struct {
	Name     string  `json:"name"`
	URL      string  `json:"url"`
	ThumbURL string  `json:"thumb_url"`
	Time     string  `json:"time"`
	Stars    int     `json:"stars"`
	Cloud    float64 `json:"cloud"`
}

type dateImagesResponse struct {
	Date       string           `json:"date"`
	Images     []imageEntry     `json:"images"`
	Highlights []highlightEntry `json:"highlights,omitempty"`
	// HighlightsEnabled lets the gallery distinguish "none selected for this
	// night" from "the feature is switched off" on an empty Highlights tab.
	HighlightsEnabled bool   `json:"highlights_enabled"`
	Video             string `json:"video,omitempty"`
	Keogram           string `json:"keogram,omitempty"`
	StarTrails        string `json:"startrails,omitempty"`
}

// loadHighlights reads the night's highlights manifest, returning nil when
// absent or unreadable.
func loadHighlights(root *os.Root, date string) []highlightEntry {
	data, err := root.ReadFile(filepath.Join(date, "highlights-"+date+".json"))
	if err != nil {
		return nil
	}
	var manifest struct {
		Frames []struct {
			File  string  `json:"file"`
			Time  string  `json:"time"`
			Stars int     `json:"stars"`
			Cloud float64 `json:"cloud"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil
	}
	var out []highlightEntry
	for _, f := range manifest.Frames {
		url := "/output/" + date + "/" + f.File
		out = append(out, highlightEntry{
			Name:     f.File,
			URL:      url,
			ThumbURL: url + "?w=" + strconv.Itoa(imgutil.ThumbWidth),
			Time:     f.Time,
			Stars:    f.Stars,
			Cloud:    f.Cloud,
		})
	}
	return out
}

func (s *Server) handleDateImages(w http.ResponseWriter, r *http.Request) {
	// The date segment is attacker-controlled and reaches the filesystem, so
	// it is resolved through the output root rather than joined onto it: ".."
	// and an absolute path both fail here instead of listing another
	// directory's contents.
	date := r.PathValue("date")

	root, err := s.openOutputRoot()
	if err != nil {
		http.Error(w, "date not found", http.StatusNotFound)
		return
	}
	defer root.Close()

	files, err := fs.ReadDir(root.FS(), date)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrInvalid) {
			http.Error(w, "date not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read directory", http.StatusInternalServerError)
		return
	}

	var images []imageEntry
	var videoURL string
	var keogramURL string
	var startrailsURL string

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		lower := strings.ToLower(name)

		if strings.HasSuffix(lower, ".mp4") {
			videoURL = "/output/" + date + "/" + name
			continue
		}

		// Protected highlight copies appear on the Highlights tab, not in
		// the main grid (they duplicate frames that may since be pruned).
		if strings.HasPrefix(lower, "highlight") {
			continue
		}

		if strings.HasPrefix(lower, "keogram-") && strings.HasSuffix(lower, ".jpg") {
			keogramURL = "/output/" + date + "/" + name
			continue
		}

		if strings.HasPrefix(lower, "startrails-") && strings.HasSuffix(lower, ".jpg") {
			startrailsURL = "/output/" + date + "/" + name
			continue
		}

		if !isImageFile(lower) {
			continue
		}

		imgURL := "/output/" + date + "/" + name
		thumbURL := imgURL + "?w=" + strconv.Itoa(imgutil.ThumbWidth)
		timeStr := extractTime(name)

		images = append(images, imageEntry{
			Name:     name,
			URL:      imgURL,
			ThumbURL: thumbURL,
			Time:     timeStr,
		})
	}

	// Sort images by name (which sorts by timestamp).
	slices.SortFunc(images, func(a, b imageEntry) int {
		return strings.Compare(a.Name, b.Name)
	})

	resp := dateImagesResponse{
		Date:              date,
		Images:            images,
		Highlights:        loadHighlights(root, date),
		HighlightsEnabled: s.cfg.Output.Highlights.Enabled,
		Video:             videoURL,
		Keogram:           keogramURL,
		StarTrails:        startrailsURL,
	}
	s.writeJSON(w, resp)
}

// extractTime finds a 14-char numeric segment (YYYYMMDDHHMMSS) in the filename
// and returns the time portion as HH:MM:SS. Returns "" if not found.
func extractTime(name string) string {
	// Strip extension.
	base := name
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		base = name[:idx]
	}

	// Find a run of 14 consecutive digits.
	digits := ""
	for _, ch := range base {
		if ch >= '0' && ch <= '9' {
			digits += string(ch)
			if len(digits) == 14 {
				break
			}
		} else {
			digits = ""
		}
	}

	if len(digits) < 14 {
		return ""
	}

	// chars 8-13 are HHMMSS
	hh := digits[8:10]
	mm := digits[10:12]
	ss := digits[12:14]
	return hh + ":" + mm + ":" + ss
}

// openOutputRoot opens the output directory as an os.Root. Every path that
// reaches the filesystem from a request is resolved through it, so a name that
// climbs out with ".." — or a symlink inside the tree pointing outside it —
// fails in the syscall instead of being served. The root is opened per request
// (a single openat) so an output directory that is created, moved, or
// recreated while the server runs needs no restart.
func (s *Server) openOutputRoot() (*os.Root, error) {
	return os.OpenRoot(s.cfg.Output.Directory)
}

func (s *Server) handleOutput(w http.ResponseWriter, r *http.Request) {
	relPath := r.PathValue("path")
	if relPath == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	root, err := s.openOutputRoot()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer root.Close()

	if width, ok := thumbWidth(r); ok {
		s.serveThumb(w, root, relPath, width)
		return
	}

	s.serveFile(w, r, root, relPath)
}

// thumbWidth reports the thumbnail width requested via ?w=N, and whether one
// was present and within range.
func thumbWidth(r *http.Request) (int, bool) {
	wStr := r.URL.Query().Get("w")
	if wStr == "" {
		return 0, false
	}
	width, err := strconv.Atoi(wStr)
	if err != nil || width <= 0 || width > 1920 {
		return 0, false
	}
	return width, true
}

// serveFile serves relPath from inside root. http.ServeContent is used rather
// than http.ServeFile because the file is opened through the root; it still
// honours Range requests, so seeking within a timelapse MP4 works.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, root *os.Root, relPath string) {
	f, err := root.Open(relPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	http.ServeContent(w, r, filepath.Base(relPath), info.ModTime(), f)
}

func (s *Server) serveThumb(w http.ResponseWriter, root *os.Root, relPath string, width int) {
	dir := filepath.Dir(relPath)
	base := filepath.Base(relPath)
	ext := filepath.Ext(base)
	baseNoExt := strings.TrimSuffix(base, ext)

	cacheDir := filepath.Join(dir, ".thumbs")
	cachePath := filepath.Join(cacheDir, baseNoExt+"_"+strconv.Itoa(width)+".jpg")

	// Try cache first.
	if data, err := root.ReadFile(cachePath); err == nil {
		writeThumb(w, data)
		return
	}

	// Cache miss: decoding holds a full-resolution frame in memory, so bound
	// how many run at once (excess requests queue briefly).
	s.thumbSem <- struct{}{}
	defer func() { <-s.thumbSem }()

	// Open and decode source image.
	f, err := root.Open(relPath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		http.Error(w, "failed to decode image", http.StatusInternalServerError)
		return
	}

	scaled := imgutil.ScaleTo(src, width)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 75}); err != nil {
		http.Error(w, "failed to encode thumbnail", http.StatusInternalServerError)
		return
	}

	// Save to cache (best-effort).
	if err := root.MkdirAll(cacheDir, 0755); err == nil {
		root.WriteFile(cachePath, buf.Bytes(), 0644)
	}

	writeThumb(w, buf.Bytes())
}

func writeThumb(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

// handleFocus runs star detection on the most recent frame and returns the
// count and mean FWHM — a live focus aid: adjust the lens to maximize stars
// and minimize FWHM. Results are cached per frame (path + mtime), so polling
// only pays for a decode when a new frame lands.
func (s *Server) handleFocus(w http.ResponseWriter, r *http.Request) {
	root, err := s.openOutputRoot()
	if err != nil {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}
	defer root.Close()

	path, err := latestImagePath(root)
	if err != nil || path == "" {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}
	info, err := root.Stat(path)
	if err != nil {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}

	s.focusMu.Lock()
	cached := s.focusPath == path && s.focusMod.Equal(info.ModTime())
	res := s.focusRes
	s.focusMu.Unlock()

	if !cached {
		// Full-frame decode — bound it like the thumbnail path.
		s.thumbSem <- struct{}{}
		f, err := root.Open(path)
		if err != nil {
			<-s.thumbSem
			http.Error(w, "failed to open image", http.StatusInternalServerError)
			return
		}
		img, _, err := image.Decode(f)
		f.Close()
		<-s.thumbSem
		if err != nil {
			http.Error(w, "failed to decode image", http.StatusInternalServerError)
			return
		}
		res = stars.Detect(img)

		s.focusMu.Lock()
		s.focusPath, s.focusMod, s.focusRes = path, info.ModTime(), res
		s.focusMu.Unlock()
	}

	w.Header().Set("Cache-Control", "no-cache")
	s.writeJSON(w, map[string]any{
		"stars":      res.Count,
		"fwhm":       res.MeanFWHM,
		"file":       filepath.Base(path),
		"frame_time": info.ModTime(),
	})
}

// handleLatest serves the most recently captured image. Useful for embedding a
// live view in external dashboards — poll this endpoint to always show the
// newest frame without knowing its filename.
//
// By default the full-resolution image is returned. Add ?w=N to get a
// resized JPEG (same thumb mechanism as /output).
func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	root, err := s.openOutputRoot()
	if err != nil {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}
	defer root.Close()

	path, err := latestImagePath(root)
	if err != nil || path == "" {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}

	if width, ok := thumbWidth(r); ok {
		s.serveThumb(w, root, path, width)
		return
	}

	// No-cache so external dashboards always fetch the freshest frame.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	s.serveFile(w, r, root, path)
}

// latestImagePath returns the path, relative to the output root, of the newest
// captured frame: the last image by filename in the newest date directory.
// Frames are named with a sortable timestamp, so filename order is capture
// order — no stat of every candidate is needed.
func latestImagePath(root *os.Root) (string, error) {
	fsys := root.FS()
	dates, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return "", err
	}

	// Walk from newest date to oldest; return first image found.
	sorted := make([]string, 0, len(dates))
	for _, d := range dates {
		if d.IsDir() && len(d.Name()) == 10 && d.Name()[4] == '-' {
			sorted = append(sorted, d.Name())
		}
	}
	slices.Sort(sorted)
	slices.Reverse(sorted)

	for _, date := range sorted {
		files, err := fs.ReadDir(fsys, date)
		if err != nil {
			continue
		}

		// Files are already sorted by name (which is by timestamp). Pick the last one.
		for i := len(files) - 1; i >= 0; i-- {
			f := files[i]
			if f.IsDir() {
				continue
			}
			lower := strings.ToLower(f.Name())
			// Skip synthesized outputs — we only want raw captured frames.
			if strings.HasPrefix(lower, "timelapse-") ||
				strings.HasPrefix(lower, "keogram-") ||
				strings.HasPrefix(lower, "startrails-") ||
				strings.HasPrefix(lower, "wb-analysis-") ||
				strings.HasPrefix(lower, "cloud-") ||
				strings.HasPrefix(lower, "highlight") {
				continue
			}
			if isImageFile(lower) {
				return filepath.Join(date, f.Name()), nil
			}
		}
	}
	return "", nil
}

func isImageFile(lower string) bool {
	return strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".png") ||
		strings.HasSuffix(lower, ".webp")
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
