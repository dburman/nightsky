package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"image"
	"image/jpeg"
	_ "image/png"
	_ "golang.org/x/image/webp"
	"log/slog"
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

// ListenAndServe registers routes and starts the HTTP server. The server sets
// explicit timeouts so dead or dawdling connections (flaky WiFi, Slowloris)
// can't accumulate file descriptors over weeks of uptime. WriteTimeout stays
// generous because timelapse MP4s are streamed over slow links.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	s.logger.Info("web UI started", "url", "http://localhost"+s.addr)
	return srv.ListenAndServe()
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
	entries, err := os.ReadDir(s.cfg.Output.Directory)
	if err != nil {
		if os.IsNotExist(err) {
			s.writeJSON(w, []captureEntry{})
			return
		}
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

		dirPath := filepath.Join(s.cfg.Output.Directory, name)
		files, err := os.ReadDir(dirPath)
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

	// Sort newest-first.
	slices.SortFunc(captures, func(a, b captureEntry) int {
		if a.Date > b.Date {
			return -1
		}
		return 1
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
	Video      string           `json:"video,omitempty"`
	Keogram    string           `json:"keogram,omitempty"`
	StarTrails string           `json:"startrails,omitempty"`
}

// loadHighlights reads the night's highlights manifest, returning nil when
// absent or unreadable.
func loadHighlights(dirPath, date string) []highlightEntry {
	data, err := os.ReadFile(filepath.Join(dirPath, "highlights-"+date+".json"))
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
	date := r.PathValue("date")

	dirPath := filepath.Join(s.cfg.Output.Directory, date)
	files, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
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
		if a.Name < b.Name {
			return -1
		}
		return 1
	})

	resp := dateImagesResponse{
		Date:       date,
		Images:     images,
		Highlights: loadHighlights(dirPath, date),
		Video:      videoURL,
		Keogram:    keogramURL,
		StarTrails: startrailsURL,
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

func (s *Server) handleOutput(w http.ResponseWriter, r *http.Request) {
	relPath := r.PathValue("path")

	// Security check: reject path traversal.
	clean := filepath.Clean(relPath)
	if strings.HasPrefix(clean, "..") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	absPath := filepath.Join(s.cfg.Output.Directory, relPath)

	wStr := r.URL.Query().Get("w")
	if wStr != "" {
		width, err := strconv.Atoi(wStr)
		if err == nil && width > 0 && width <= 1920 {
			s.serveThumb(w, absPath, width)
			return
		}
	}

	http.ServeFile(w, r, absPath)
}

func (s *Server) serveThumb(w http.ResponseWriter, absPath string, width int) {
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	ext := filepath.Ext(base)
	baseNoExt := strings.TrimSuffix(base, ext)

	cacheDir := filepath.Join(dir, ".thumbs")
	cachePath := filepath.Join(cacheDir, baseNoExt+"_"+strconv.Itoa(width)+".jpg")

	// Try cache first.
	if data, err := os.ReadFile(cachePath); err == nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
		return
	}

	// Cache miss: decoding holds a full-resolution frame in memory, so bound
	// how many run at once (excess requests queue briefly).
	s.thumbSem <- struct{}{}
	defer func() { <-s.thumbSem }()

	// Open and decode source image.
	f, err := os.Open(absPath)
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
	if err := os.MkdirAll(cacheDir, 0755); err == nil {
		os.WriteFile(cachePath, buf.Bytes(), 0644)
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(buf.Bytes())
}

// handleFocus runs star detection on the most recent frame and returns the
// count and mean FWHM — a live focus aid: adjust the lens to maximize stars
// and minimize FWHM. Results are cached per frame (path + mtime), so polling
// only pays for a decode when a new frame lands.
func (s *Server) handleFocus(w http.ResponseWriter, r *http.Request) {
	path, err := latestImagePath(s.cfg.Output.Directory)
	if err != nil || path == "" {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}
	info, err := os.Stat(path)
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
		f, err := os.Open(path)
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
	path, err := latestImagePath(s.cfg.Output.Directory)
	if err != nil || path == "" {
		http.Error(w, "no images found", http.StatusNotFound)
		return
	}

	wStr := r.URL.Query().Get("w")
	if wStr != "" {
		width, err := strconv.Atoi(wStr)
		if err == nil && width > 0 && width <= 1920 {
			s.serveThumb(w, path, width)
			return
		}
	}

	// No-cache so external dashboards always fetch the freshest frame.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.ServeFile(w, r, path)
}

// latestImagePath returns the absolute path of the most recently modified
// captured image (jpg/png) across all date directories in outputDir.
func latestImagePath(outputDir string) (string, error) {
	dates, err := os.ReadDir(outputDir)
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
		dirPath := filepath.Join(outputDir, date)
		files, err := os.ReadDir(dirPath)
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
				return filepath.Join(dirPath, f.Name()), nil
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
