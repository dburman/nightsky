package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"image"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dburman/nightsky/internal/config"
)

//go:embed static/index.html
var indexHTML []byte

// Server is the web UI HTTP server.
type Server struct {
	cfg    *config.Config
	addr   string
	logger *slog.Logger
}

// New creates a new Server.
func New(cfg *config.Config, addr string, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, addr: addr, logger: logger}
}

// ListenAndServe registers routes and starts the HTTP server.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/captures", s.handleCaptures)
	mux.HandleFunc("GET /api/captures/{date}/images", s.handleDateImages)
	mux.HandleFunc("GET /output/{path...}", s.handleOutput)
	s.logger.Info("web UI started", "url", "http://localhost"+s.addr)
	return http.ListenAndServe(s.addr, mux)
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.cfg)
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
			if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") {
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
	sort.Slice(captures, func(i, j int) bool {
		return captures[i].Date > captures[j].Date
	})

	s.writeJSON(w, captures)
}

type imageEntry struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	ThumbURL string `json:"thumb_url"`
	Time     string `json:"time"`
}

type dateImagesResponse struct {
	Date       string       `json:"date"`
	Images     []imageEntry `json:"images"`
	Video      string       `json:"video,omitempty"`
	Keogram    string       `json:"keogram,omitempty"`
	StarTrails string       `json:"startrails,omitempty"`
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

		if strings.HasPrefix(lower, "keogram-") && strings.HasSuffix(lower, ".jpg") {
			keogramURL = "/output/" + date + "/" + name
			continue
		}

		if strings.HasPrefix(lower, "startrails-") && strings.HasSuffix(lower, ".jpg") {
			startrailsURL = "/output/" + date + "/" + name
			continue
		}

		if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") && !strings.HasSuffix(lower, ".png") {
			continue
		}

		imgURL := "/output/" + date + "/" + name
		thumbURL := imgURL + "?w=160"
		timeStr := extractTime(name)

		images = append(images, imageEntry{
			Name:     name,
			URL:      imgURL,
			ThumbURL: thumbURL,
			Time:     timeStr,
		})
	}

	// Sort images by name (which sorts by timestamp).
	sort.Slice(images, func(i, j int) bool {
		return images[i].Name < images[j].Name
	})

	resp := dateImagesResponse{
		Date:       date,
		Images:     images,
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

	scaled := scaleTo(src, width)

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

func scaleTo(src image.Image, targetW int) image.Image {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= targetW {
		return src
	}
	targetH := srcH * targetW / srcW
	if targetH < 1 {
		targetH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	for dy := 0; dy < targetH; dy++ {
		for dx := 0; dx < targetW; dx++ {
			dst.Set(dx, dy, src.At(b.Min.X+dx*srcW/targetW, b.Min.Y+dy*srcH/targetH))
		}
	}
	return dst
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
