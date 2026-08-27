// Night-session state and the end-of-night pipeline.
//
// A night is a unit of work with its own directory, metric stream, timelapse
// segments and alert history. Keeping it in one value — rather than as five
// fields on Loop that have to be cleared in the right order at dawn — is what
// lets end-of-night processing run in the background on a complete, finished
// record while the capture loop moves straight on to day frames with a fresh
// session.

package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dburman/nightsky/internal/cloud"
)

// nightSession accumulates everything belonging to one night's capture.
// A nil *nightSession means no night is in progress.
type nightSession struct {
	// dir is the date label the night's frames are filed under (e.g.
	// "2026-06-30"), named for the most recent dusk so a restart after
	// midnight rejoins the same night instead of splitting it.
	dir string
	// frameCount counts night frames saved this session, driving iterative
	// timelapse segmentation.
	frameCount int
	// metrics is the per-frame sky metric stream, flushed to the nightly
	// report and used to rank highlights.
	metrics []cloud.Metric
	// auroraEvents counts alerts fired this session, for the dawn summary.
	auroraEvents int
	// segmentWg tracks in-flight timelapse segment encodes. It belongs to the
	// session because end-of-night must drain this night's segments while a
	// new session gets a fresh group.
	segmentWg sync.WaitGroup
}

// newNightSession starts a session filed under the given date label.
func newNightSession(dir string) *nightSession {
	return &nightSession{dir: dir}
}

// path returns the night's output directory beneath outputDir.
func (s *nightSession) path(outputDir string) string {
	return filepath.Join(outputDir, s.dir)
}

// lastMetric returns the most recent frame's metrics, and whether there is one.
func (s *nightSession) lastMetric() (cloud.Metric, bool) {
	if s == nil || len(s.metrics) == 0 {
		return cloud.Metric{}, false
	}
	return s.metrics[len(s.metrics)-1], true
}

// finishNight runs the end-of-night pipeline for a completed session. It is
// called on its own goroutine so day capture starts immediately rather than
// stalling behind timelapse encoding (encodes are niced, so they yield CPU to
// capture). The session is finished by the time it gets here and is not
// touched by the capture loop again.
func (l *Loop) finishNight(ctx context.Context, s *nightSession) {
	nightDir := s.path(l.cfg.Output.Directory)

	// Flush cloud metrics before handing off to OnNightEnd.
	if len(s.metrics) > 0 {
		if err := cloud.WriteReport(nightDir, s.metrics); err != nil {
			l.logger.Error("cloud report write failed", "error", err)
		} else {
			l.logger.Info("cloud coverage report written",
				"dir", nightDir,
				"frames", len(s.metrics),
				"summary", cloud.SummaryLine(s.metrics),
			)
		}
	}

	// Highlights run before the synthesis suite rather than after it. They
	// need only the metrics in hand and the frames already on disk, so there
	// is nothing to wait for — and anything that cuts the hours of keogram,
	// star-trail and encode work below short (OOM kill, watchdog restart, a
	// step timing out) would otherwise take the night's best frames with it.
	// WebP conversion skips highlight-* names, so the protected copies
	// outlive it either way.
	if l.cfg.Output.Highlights.Enabled {
		WriteHighlights(nightDir, s.metrics, l.logger)
	}

	// Drain any in-flight segment encodes before finalizing.
	s.segmentWg.Wait()

	if l.OnNightEnd != nil {
		l.OnNightEnd(nightDir)
	}

	// Summary goes out after processing so the timelapse and keogram it
	// points at already exist.
	if l.cfg.Alerts.NightSummary {
		date := filepath.Base(nightDir)
		msg := nightSummaryMessage(date, s.metrics, s.auroraEvents, l.cfg.Alerts.BaseURL)
		if err := l.Notifier.Send(ctx, "Night summary "+date, msg); err != nil {
			l.logger.Error("night summary delivery failed", "error", err)
		}
	}
}

// highlightCount is how many top frames the nightly manifest records.
const highlightCount = 5

// Highlight is one entry in the nightly best-frames manifest.
type Highlight struct {
	File  string  `json:"file"`
	Time  string  `json:"time"`
	Stars int     `json:"stars"`
	Cloud float64 `json:"cloud"`
}

// WriteHighlights ranks the night's frames by star count weighted by clear
// sky, copies the top picks to protected highlight-N-<file> names that
// survive raw pruning and WebP conversion, and writes the manifest to
// highlights-<date>.json in nightDir.
//
// Runs ahead of the end-of-night synthesis suite: it needs nothing that
// suite produces, and going first keeps the night's best frames out of the
// blast radius of a step that OOMs or times out. Frames already converted by
// an earlier pass resolve via their .webp twin; frames that vanished
// entirely are skipped.
//
// Safe to re-run: `nightsky process` calls it with metrics read back from the
// night's CSV, which regenerates highlights for a night whose live run was
// interrupted.
func WriteHighlights(nightDir string, ms []cloud.Metric, logger *slog.Logger) {
	type scored struct {
		m     cloud.Metric
		score float64
	}
	var candidates []scored
	for _, m := range ms {
		if m.StarCount == 0 {
			continue
		}
		candidates = append(candidates, scored{m, float64(m.StarCount) * (1 - m.Coverage)})
	}
	if len(candidates) == 0 {
		return
	}
	slices.SortFunc(candidates, func(a, b scored) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})

	// Stale protected copies from a previous run of this night are replaced.
	removeOldHighlightCopies(nightDir)

	var picks []Highlight
	for _, c := range candidates {
		if len(picks) >= highlightCount {
			break
		}
		file := resolveFrameFile(nightDir, c.m)
		if file == "" {
			continue
		}

		// Copy to a protected name the raw pruner keeps, so the night's best
		// frames outlive prune_raw_after_days. On copy failure the manifest
		// references the original (works until pruned).
		protected := fmt.Sprintf("highlight-%d-%s", len(picks)+1, file)
		if data, err := os.ReadFile(filepath.Join(nightDir, file)); err == nil {
			if err := writeFileAtomic(filepath.Join(nightDir, protected), data); err == nil {
				file = protected
			} else {
				logger.Warn("highlight copy failed", "file", protected, "error", err)
			}
		}

		picks = append(picks, Highlight{
			File:  file,
			Time:  c.m.Timestamp.Format("15:04:05"),
			Stars: c.m.StarCount,
			Cloud: c.m.Coverage,
		})
	}
	if len(picks) == 0 {
		return
	}

	date := filepath.Base(nightDir)
	data, err := json.MarshalIndent(map[string]any{"date": date, "frames": picks}, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(nightDir, "highlights-"+date+".json")
	if err := writeFileAtomic(path, data); err != nil {
		logger.Error("highlights write failed", "error", err)
		return
	}
	logger.Info("night highlights written", "path", path, "frames", len(picks))
}

// frameExts are the saved frame formats a highlight may point at; .dng is
// excluded because the web UI cannot display it.
var frameExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// resolveFrameFile returns the on-disk filename backing a metric's frame, or
// "" when the frame is gone. It prefers the recorded name, falls back to the
// .webp twin left behind by conversion, and finally matches on the timestamp
// embedded in the filename — the last case backfills nights whose CSV predates
// the file column, where the metric carries no filename at all.
func resolveFrameFile(nightDir string, m cloud.Metric) string {
	if m.File != "" {
		if _, err := os.Stat(filepath.Join(nightDir, m.File)); err == nil {
			return m.File
		}
		webp := strings.TrimSuffix(m.File, filepath.Ext(m.File)) + ".webp"
		if _, err := os.Stat(filepath.Join(nightDir, webp)); err == nil {
			return webp
		}
		return ""
	}

	matches, err := filepath.Glob(filepath.Join(nightDir, "*-"+m.Timestamp.Format("20060102150405")+".*"))
	if err != nil {
		return ""
	}
	slices.Sort(matches)
	for _, p := range matches {
		name := filepath.Base(p)
		// Never pick a previous run's protected copy: doing so would nest
		// highlight- prefixes on every re-run.
		if highlightCopyRe.MatchString(strings.ToLower(name)) {
			continue
		}
		if slices.Contains(frameExts, strings.ToLower(filepath.Ext(name))) {
			return name
		}
	}
	return ""
}

// highlightCopyRe matches protected highlight frame copies (not the
// highlights-<date>.json manifest).
var highlightCopyRe = regexp.MustCompile(`^highlight-\d+-`)

// removeOldHighlightCopies deletes protected copies from a previous
// highlights run so re-processing a night can't accumulate stale picks.
func removeOldHighlightCopies(nightDir string) {
	entries, err := os.ReadDir(nightDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && highlightCopyRe.MatchString(strings.ToLower(e.Name())) {
			os.Remove(filepath.Join(nightDir, e.Name()))
		}
	}
}

// nightSummaryMessage composes the dawn notification from the night's sky
// metrics.
func nightSummaryMessage(date string, ms []cloud.Metric, auroraEvents int, baseURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Night %s: %d frames", date, len(ms))
	if len(ms) > 1 {
		span := ms[len(ms)-1].Timestamp.Sub(ms[0].Timestamp).Round(time.Minute)
		fmt.Fprintf(&b, " over %s", span)
	}
	fmt.Fprintf(&b, ". %s.", cloud.SummaryLine(ms))

	peakStars := 0
	for _, m := range ms {
		if m.StarCount > peakStars {
			peakStars = m.StarCount
		}
	}
	if peakStars > 0 {
		fmt.Fprintf(&b, " Peak stars: %d.", peakStars)
	}
	if auroraEvents > 0 {
		fmt.Fprintf(&b, " Aurora alerts: %d.", auroraEvents)
	}
	if baseURL != "" {
		fmt.Fprintf(&b, " %s", strings.TrimRight(baseURL, "/"))
	}
	return b.String()
}

// activeNightDir returns the in-progress night's date label, or "" during the
// day. Cleanup uses it to avoid deleting the night currently being captured.
func (l *Loop) activeNightDir() string {
	if l.night == nil {
		return ""
	}
	return l.night.dir
}
