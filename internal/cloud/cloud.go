// Package cloud computes a simple cloud-coverage metric from captured frames.
// The metric is based on mean luminance and standard deviation of the sky
// region (center metering zone). Clear skies produce a dark, high-contrast
// image (low mean, high stddev from point sources); cloudy skies produce a
// bright, uniform glow (higher mean, lower stddev).
package cloud

import (
	"bufio"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Metric holds cloud coverage statistics for a single frame.
type Metric struct {
	Timestamp  time.Time
	Mean       float64 // mean luminance of sky region (0–255)
	StdDev     float64 // standard deviation of luminance (0–255)
	Coverage   float64 // estimated cloud coverage 0.0–1.0
}

// Estimate computes a cloud coverage metric from img.
// It samples the center 50% radius circle (zenith for all-sky cameras).
func Estimate(img image.Image, ts time.Time) Metric {
	mean, stddev := centerStats(img)
	coverage := estimateCoverage(mean, stddev)
	return Metric{
		Timestamp: ts,
		Mean:      mean,
		StdDev:    stddev,
		Coverage:  coverage,
	}
}

// estimateCoverage maps mean/stddev to a 0–1 cloud fraction.
// Heuristic: higher mean and lower stddev → more cloud cover.
// The combined score blends both signals and clamps to [0, 1].
func estimateCoverage(mean, stddev float64) float64 {
	// Normalise mean to [0,1] — a mean >80 is likely cloud-affected.
	normMean := math.Min(mean/80.0, 1.0)
	// Normalise stddev inversely — clear skies have stddev ≥ 20 from stars.
	normStdDev := math.Max(0, 1.0-stddev/20.0)
	score := 0.6*normMean + 0.4*normStdDev
	return math.Min(score, 1.0)
}

// centerStats returns the mean and standard deviation of luminance in the
// inner 50%-radius circle. Samples every 4th pixel for speed.
func centerStats(img image.Image) (mean, stddev float64) {
	b := img.Bounds()
	cx := float64(b.Min.X+b.Max.X) / 2
	cy := float64(b.Min.Y+b.Max.Y) / 2
	r := float64(min(b.Dx(), b.Dy())) * 0.5

	var sum, sumSq float64
	var count int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		dy := float64(y) - cy
		for x := b.Min.X; x < b.Max.X; x += 4 {
			dx := float64(x) - cx
			if math.Sqrt(dx*dx+dy*dy) > r {
				continue
			}
			rv, g, bl, _ := img.At(x, y).RGBA()
			lum := 0.299*float64(rv>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			sum += lum
			sumSq += lum * lum
			count++
		}
	}
	if count == 0 {
		return 0, 0
	}
	mean = sum / float64(count)
	variance := sumSq/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	stddev = math.Sqrt(variance)
	return mean, stddev
}

// WriteReport appends metrics to a CSV report file in dir.
// The file is named cloud-YYYY-MM-DD.csv and has a header row if new.
func WriteReport(dir string, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	date := metrics[0].Timestamp.Format("2006-01-02")
	path := filepath.Join(dir, "cloud-"+date+".csv")

	needHeader := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		needHeader = true
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open cloud report: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	if needHeader {
		fmt.Fprintln(w, "timestamp,mean,stddev,coverage")
	}
	for _, m := range metrics {
		fmt.Fprintf(w, "%s,%.2f,%.2f,%.3f\n",
			m.Timestamp.Format("2006-01-02T15:04:05"),
			m.Mean, m.StdDev, m.Coverage,
		)
	}
	return w.Flush()
}

// SummaryLine returns a human-readable summary of the night's cloud coverage.
func SummaryLine(metrics []Metric) string {
	if len(metrics) == 0 {
		return "no data"
	}
	var sum float64
	var clear, cloudy int
	for _, m := range metrics {
		sum += m.Coverage
		if m.Coverage < 0.3 {
			clear++
		} else {
			cloudy++
		}
	}
	avg := sum / float64(len(metrics))
	pctClear := 100 * clear / len(metrics)
	return fmt.Sprintf("avg coverage %.0f%%, %d%% of frames clear",
		avg*100, pctClear)
}

// AppendSummary writes a one-line summary to a text summary file alongside
// the CSV so humans can read it at a glance.
func AppendSummary(dir string, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	date := metrics[0].Timestamp.Format("2006-01-02")
	path := filepath.Join(dir, "cloud-"+date+".csv")
	summary := SummaryLine(metrics)

	// Append the summary as a comment line at the end of the CSV.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "# %s\n", strings.TrimSpace(summary))
	return err
}
