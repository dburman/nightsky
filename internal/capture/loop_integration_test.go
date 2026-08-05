package capture

import (
	"context"
	"image"
	"image/color"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dburman/nightsky/internal/alerts"
	"github.com/dburman/nightsky/internal/astro"
	"github.com/dburman/nightsky/internal/camera"
	"github.com/dburman/nightsky/internal/config"
)

// fakeClock is a deterministic clock shared by the loop and the fake camera.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeCamera implements camera.Camera. Each Capture advances the shared clock
// by one frame interval and renders a synthetic frame via the generator, so a
// whole night passes deterministically at test speed.
type fakeCamera struct {
	clock    *fakeClock
	step     time.Duration
	generate func(n int, t time.Time) *image.RGBA
	failAt   map[int]bool

	mu       sync.Mutex
	captures int
}

func (f *fakeCamera) Open() error  { return nil }
func (f *fakeCamera) Close() error { return nil }
func (f *fakeCamera) Info() camera.CameraInfo {
	return camera.CameraInfo{Name: "fake", Model: "fake", MaxWidth: 64, MaxHeight: 48}
}
func (f *fakeCamera) SetCooler(camera.CoolerSettings) error { return nil }
func (f *fakeCamera) Temperature() (float64, error)         { return 21.0, nil }

func (f *fakeCamera) Capture(ctx context.Context, s camera.CaptureSettings) (*camera.CaptureResult, error) {
	f.mu.Lock()
	f.captures++
	n := f.captures
	f.mu.Unlock()

	f.clock.Advance(f.step)
	ts := f.clock.Now()

	if f.failAt[n] {
		return nil, context.DeadlineExceeded
	}

	img := f.generate(n, ts)
	return &camera.CaptureResult{
		Image: img,
		Meta: camera.CaptureMeta{
			Timestamp:   ts,
			Exposure:    s.Exposure,
			Gain:        s.Gain,
			Temperature: 21.0,
			Width:       img.Bounds().Dx(),
			Height:      img.Bounds().Dy(),
			Binning:     1,
		},
	}, nil
}

// nightFrame renders a synthetic sky: low background noise, a few bright
// stars placed OFF the 4-pixel sampling grid (so the sky-metric sampler sees
// pure background while full-resolution star detection still finds them),
// and an optional aurora-green tint.
func nightFrame(n int, aurora bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			v := uint8((x*7 + y*13 + n) % 7) // deterministic 0..6 noise
			g := v
			if aurora {
				g += 30
			}
			img.SetRGBA(x, y, color.RGBA{v, g, v, 255})
		}
	}
	// Stars inside the centre circle (centre 32,24 radius 24), off-grid.
	for _, p := range [][2]int{{27, 21}, {37, 27}, {31, 30}, {35, 19}} {
		img.SetRGBA(p[0], p[1], color.RGBA{200, 200, 200, 255})
		img.SetRGBA(p[0]+1, p[1], color.RGBA{100, 100, 100, 255})
		img.SetRGBA(p[0], p[1]+1, color.RGBA{100, 100, 100, 255})
	}
	return img
}

func dayFrame() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, color.RGBA{140, 150, 170, 255})
		}
	}
	return img
}

// TestLoop_FullNight drives the real capture loop through a complete
// simulated night — dusk, a full night session including a transient camera
// failure and an aurora event, and dawn with end-of-night processing — using
// a fake camera and a fake clock. This is the system's end-to-end safety net:
// mode transitions, session naming, frame persistence, sky metrics, alerting,
// highlights, and the dawn summary all in one pass.
func TestLoop_FullNight(t *testing.T) {
	outDir := t.TempDir()

	// 2026-06-10 at 45N/0E, civil twilight: dusk ~20:33 UT, dawn ~03:07 UT.
	clk := &fakeClock{t: time.Date(2026, 6, 10, 20, 0, 0, 0, time.UTC)}
	const lat, lon, angle = 45.0, 0.0, -6.0

	// Match the loop's own astronomy so frame content follows the sky.
	isNight := func(ts time.Time) bool {
		return astro.IsNight(ts, lat, lon, angle)
	}

	var frameNo int
	cam := &fakeCamera{
		clock: clk,
		step:  90 * time.Second,
		generate: func(n int, ts time.Time) *image.RGBA {
			frameNo = n
			if !isNight(ts) {
				return dayFrame()
			}
			// A 5-frame aurora surge mid-night (well after the ~10-frame
			// baseline the detector needs).
			return nightFrame(n, n >= 60 && n < 65)
		},
		failAt: map[int]bool{30: true}, // one transient camera failure
	}

	// Webhook receiver for aurora alerts and the dawn summary.
	type note struct{ title, body string }
	notes := make(chan note, 8)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		notes <- note{r.Header.Get("Title"), string(b)}
	}))
	defer hook.Close()

	cfg := &config.Config{}
	cfg.Location.Latitude = lat
	cfg.Location.Longitude = lon
	cfg.Location.Angle = angle
	cfg.Output.Directory = outDir
	cfg.Output.FilenamePrefix = "test"
	for _, m := range []*config.ModeConfig{&cfg.Day, &cfg.Night} {
		m.Exposure = 5 * time.Millisecond
		m.MaxExposure = 100 * time.Millisecond
		m.Gain = 2
		m.MaxGain = 8
		m.AutoExposure = true
		m.TargetBrightness = 90
		m.ImageType = "png"
		m.Quality = 95
		m.SkipFrames = 1
		m.MeteringZone = "center"
	}
	// Defaulted to true by the config loader, which a struct literal bypasses.
	cfg.Output.Highlights.Enabled = true
	cfg.Alerts.WebhookURL = hook.URL
	cfg.Alerts.NightSummary = true
	cfg.Alerts.Aurora = config.AuroraAlertConfig{
		Enabled: true, RatioThreshold: 1.3, MinFrames: 3, MaxCloud: 0.8,
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	loop := NewLoop(cam, cfg, logger)
	loop.now = clk.Now
	loop.Notifier = alerts.NewNotifier(hook.URL, logger)

	nightEnds := make(chan string, 2)
	// Highlights must already be on disk before the synthesis suite starts, so
	// a step that OOMs or times out mid-suite cannot take the night's best
	// frames with it. OnNightEnd stands in for that suite. The channel send
	// orders this write against the test's read below.
	var highlightsBeforeSynthesis bool
	loop.OnNightEnd = func(dir string) {
		_, err := os.Stat(filepath.Join(dir, "highlights-"+filepath.Base(dir)+".json"))
		highlightsBeforeSynthesis = err == nil
		nightEnds <- dir
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	// Collect notifications until the dawn summary arrives (the last event of
	// the night), then stop the loop.
	var auroraSeen, summarySeen bool
	var summaryBody string
	deadline := time.After(60 * time.Second)
	var nightDir string
waitLoop:
	for {
		select {
		case n := <-notes:
			switch {
			case strings.Contains(n.title, "aurora"):
				auroraSeen = true
			case strings.HasPrefix(n.title, "Night summary"):
				summarySeen = true
				summaryBody = n.body
				break waitLoop
			}
		case d := <-nightEnds:
			nightDir = d
		case <-deadline:
			t.Fatalf("timed out waiting for the dawn summary (frames captured: %d)", frameNo)
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}

	// Night-end fired for the dusk-dated session directory.
	if nightDir == "" {
		select {
		case nightDir = <-nightEnds:
		case <-time.After(time.Second):
			t.Fatal("OnNightEnd never fired")
		}
	}
	if filepath.Base(nightDir) != "2026-06-10" {
		t.Errorf("night session dir = %s, want 2026-06-10", filepath.Base(nightDir))
	}

	// Night frames landed in the session dir; post-dawn day frames in 06-11.
	nightFrames, _ := filepath.Glob(filepath.Join(outDir, "2026-06-10", "test-*.png"))
	if len(nightFrames) < 100 {
		t.Errorf("night frames = %d, want >= 100", len(nightFrames))
	}
	dayFrames, _ := filepath.Glob(filepath.Join(outDir, "2026-06-11", "test-*.png"))
	if len(dayFrames) == 0 {
		t.Error("no post-dawn day frames in 2026-06-11")
	}

	// Sky metrics CSV and highlights manifest were written for the night.
	if _, err := os.Stat(filepath.Join(outDir, "2026-06-10", "cloud-2026-06-10.csv")); err != nil {
		t.Errorf("cloud CSV missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "2026-06-10", "highlights-2026-06-10.json")); err != nil {
		t.Errorf("highlights manifest missing: %v", err)
	}
	if !highlightsBeforeSynthesis {
		t.Error("highlights were not written before end-of-night synthesis began")
	}
	// Highlights are protected copies that survive raw pruning.
	copies, _ := filepath.Glob(filepath.Join(outDir, "2026-06-10", "highlight-*-test-*.png"))
	if len(copies) == 0 {
		t.Error("no protected highlight copies written")
	}
	manifest, _ := os.ReadFile(filepath.Join(outDir, "2026-06-10", "highlights-2026-06-10.json"))
	if !strings.Contains(string(manifest), `"file": "highlight-1-`) {
		t.Errorf("manifest does not reference protected copies: %s", manifest)
	}
	if _, err := os.Stat(filepath.Join(outDir, ".metrics.json")); err != nil {
		t.Errorf("live metrics missing: %v", err)
	}

	// Alerting: the mid-night aurora surge fired, and the dawn summary
	// described the right night.
	if !auroraSeen {
		t.Error("aurora alert never delivered")
	}
	if !summarySeen || !strings.Contains(summaryBody, "Night 2026-06-10") {
		t.Errorf("summary missing or wrong: %q", summaryBody)
	}
	if !strings.Contains(summaryBody, "Aurora alerts: 1") {
		t.Errorf("summary should count one aurora event: %q", summaryBody)
	}
}

