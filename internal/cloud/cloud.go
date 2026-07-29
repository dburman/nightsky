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

	imgutil "github.com/dburman/nightsky/internal/image"
)

// Metric holds sky statistics for a single frame.
type Metric struct {
	Timestamp time.Time
	Mean      float64 // mean luminance of sky region (0–255)
	StdDev    float64 // standard deviation of luminance (0–255)
	Coverage  float64 // estimated cloud coverage 0.0–1.0
	// GreenRatio is mean G divided by mean (R+B)/2 over the sky region.
	// Aurora (557.7 nm emission) reads as a broad green excess; ~1.0 is
	// neutral. Used by the aurora detector.
	GreenRatio float64
	// StarCount and StarFWHM are filled by the capture loop from the star
	// detector (0 when not computed for this frame).
	StarCount int
	StarFWHM  float64
}

// Estimate computes a cloud coverage metric from img.
// It samples the center 50% radius circle (zenith for all-sky cameras).
func Estimate(img image.Image, ts time.Time) Metric {
	mean, stddev, greenRatio := centerStats(img)
	coverage := estimateCoverage(mean, stddev)
	return Metric{
		Timestamp:  ts,
		Mean:       mean,
		StdDev:     stddev,
		Coverage:   coverage,
		GreenRatio: greenRatio,
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

// centerStats returns the mean and standard deviation of luminance plus the
// green-excess ratio in the inner 50%-radius circle, sampling every 4th pixel
// over the packed RGBA buffer.
func centerStats(img image.Image) (mean, stddev, greenRatio float64) {
	rgba := imgutil.ToRGBA(img)
	w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	cx, cy := float64(w)/2, float64(h)/2
	radius := float64(min(w, h)) * 0.5
	pix := rgba.Pix

	var sum, sumSq, sumR, sumG, sumB float64
	var count int
	for y := 0; y < h; y += 4 {
		dy := float64(y) - cy
		row := y * rgba.Stride
		for x := 0; x < w; x += 4 {
			dx := float64(x) - cx
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			i := row + x*4
			r, g, b := float64(pix[i]), float64(pix[i+1]), float64(pix[i+2])
			lum := 0.299*r + 0.587*g + 0.114*b
			sum += lum
			sumSq += lum * lum
			sumR += r
			sumG += g
			sumB += b
			count++
		}
	}
	if count == 0 {
		return 0, 0, 1
	}
	mean = sum / float64(count)
	variance := sumSq/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	stddev = math.Sqrt(variance)

	denom := (sumR + sumB) / 2
	if denom < 1 {
		greenRatio = 1 // too dark to measure; neutral
	} else {
		greenRatio = sumG / denom
	}
	return mean, stddev, greenRatio
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
		fmt.Fprintln(w, "timestamp,mean,stddev,coverage,green_ratio,stars,fwhm")
	}
	for _, m := range metrics {
		fmt.Fprintf(w, "%s,%.2f,%.2f,%.3f,%.3f,%d,%.2f\n",
			m.Timestamp.Format("2006-01-02T15:04:05"),
			m.Mean, m.StdDev, m.Coverage, m.GreenRatio, m.StarCount, m.StarFWHM,
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
