// Package whitebalance analyses captured images to suggest WB red/blue
// settings that would produce a more neutral colour balance.
package whitebalance

import (
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const maxSamples = 100

// ModeSettings carries the current WB configuration for one capture mode.
type ModeSettings struct {
	WBRed      float64
	WBBlue     float64
	AWB        bool
	Label      string // "Night" or "Day"
	CameraType string // "zwo" or "libcamera"
}

// Result holds the analysis output for one set of images.
type Result struct {
	MeanR, MeanG, MeanB float64
	SampleCount         int
	ImageCount          int

	CurrentWBRed  float64
	CurrentWBBlue float64
	AWBEnabled    bool
	CameraType    string

	SuggestedWBRed  float64
	SuggestedWBBlue float64
}

// isLibcamera reports whether the result is for a libcamera backend.
func (r *Result) isLibcamera() bool {
	return r.CameraType == "libcamera"
}

// Analyze samples up to maxSamples images from dir, computes mean R/G/B
// channel values, and derives suggested WB settings based on the provided
// current settings.
func Analyze(dir string, settings ModeSettings) (*Result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		// Skip synthetic outputs.
		if strings.HasPrefix(lower, "keogram-") || strings.HasPrefix(lower, "startrails-") {
			continue
		}
		if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(files)

	if len(files) == 0 {
		return nil, fmt.Errorf("no images found in %s", dir)
	}

	// Pick evenly-spaced sample indices.
	indices := sampleIndices(len(files), maxSamples)

	var sumR, sumG, sumB float64
	sampled := 0

	for _, idx := range indices {
		r, g, b, err := channelMeans(files[idx])
		if err != nil {
			continue
		}
		sumR += r
		sumG += g
		sumB += b
		sampled++
	}

	if sampled == 0 {
		return nil, fmt.Errorf("could not decode any images in %s", dir)
	}

	meanR := sumR / float64(sampled)
	meanG := sumG / float64(sampled)
	meanB := sumB / float64(sampled)

	res := &Result{
		MeanR:         meanR,
		MeanG:         meanG,
		MeanB:         meanB,
		SampleCount:   sampled,
		ImageCount:    len(files),
		CurrentWBRed:  settings.WBRed,
		CurrentWBBlue: settings.WBBlue,
		AWBEnabled:    settings.AWB,
		CameraType:    settings.CameraType,
	}

	// Derive suggestions. When AWB is on the camera is already self-correcting,
	// so we report current values as-is.
	if settings.AWB {
		res.SuggestedWBRed = settings.WBRed
		res.SuggestedWBBlue = settings.WBBlue
	} else {
		clamp := clampWBZWO
		if res.isLibcamera() {
			clamp = clampWBLibcamera
		}
		res.SuggestedWBRed = clamp(settings.WBRed * safeRatio(meanG, meanR))
		res.SuggestedWBBlue = clamp(settings.WBBlue * safeRatio(meanG, meanB))
	}

	return res, nil
}

// ReportPath returns the path where WriteReport will save the analysis file.
func ReportPath(dir string) (string, error) {
	date := filepath.Base(dir)
	return filepath.Join(dir, "wb-analysis-"+date+".txt"), nil
}

// WriteReport writes a human-readable WB analysis report to
// <dir>/wb-analysis-<date>.txt.
func WriteReport(dir string, results map[string]*Result) error {
	date := filepath.Base(dir)
	outPath := filepath.Join(dir, "wb-analysis-"+date+".txt")

	var sb strings.Builder

	sb.WriteString("Nightsky White Balance Analysis\n")
	sb.WriteString("================================\n")
	sb.WriteString(fmt.Sprintf("Generated: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("Session:   %s\n", date))
	sb.WriteString("\n")
	sb.WriteString("Channel means are the average pixel value (0–255) across sampled\n")
	sb.WriteString("images. A neutral image has R ≈ G ≈ B. Suggestions are a linear\n")
	sb.WriteString("approximation — fine-tune to taste.\n")

	for _, label := range []string{"Night", "Day"} {
		res, ok := results[label]
		if !ok {
			continue
		}

		// libcamera uses float gain multipliers (0.0–10.0, 2 dp).
		// ZWO uses integers (0–99).
		wbFmt := "%.0f"
		if res.isLibcamera() {
			wbFmt = "%.2f"
		}

		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("%s Mode\n", label))
		sb.WriteString(strings.Repeat("-", len(label)+5) + "\n")
		sb.WriteString(fmt.Sprintf("Images analysed: %d (sampled from %d)\n", res.SampleCount, res.ImageCount))
		sb.WriteString(fmt.Sprintf("Mean channels:   R=%.1f  G=%.1f  B=%.1f\n", res.MeanR, res.MeanG, res.MeanB))
		sb.WriteString(fmt.Sprintf("Color cast:      %s\n", colorCast(res.MeanR, res.MeanG, res.MeanB)))
		sb.WriteString("\n")

		if res.AWBEnabled {
			sb.WriteString(fmt.Sprintf("Current:   WB Red="+wbFmt+"  WB Blue="+wbFmt+"  AWB=enabled\n", res.CurrentWBRed, res.CurrentWBBlue))
			sb.WriteString("Suggested: no change — AWB is active and handling balance automatically.\n")
		} else {
			sb.WriteString(fmt.Sprintf("Current:   WB Red="+wbFmt+"  WB Blue="+wbFmt+"\n", res.CurrentWBRed, res.CurrentWBBlue))
			sb.WriteString(fmt.Sprintf("Suggested: WB Red="+wbFmt+"  WB Blue="+wbFmt+"\n", res.SuggestedWBRed, res.SuggestedWBBlue))
			if explanation := explain(res); explanation != "" {
				sb.WriteString("\n")
				sb.WriteString(explanation)
				sb.WriteString("\n")
			}
		}
	}

	sb.WriteString("\n")

	return os.WriteFile(outPath, []byte(sb.String()), 0644)
}

// channelMeans decodes the image at path and returns mean R, G, B (0–255).
func channelMeans(path string) (r, g, b float64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return
	}

	rgba := toRGBA(img)
	bounds := rgba.Bounds()
	total := bounds.Dx() * bounds.Dy()
	if total == 0 {
		return
	}

	var sumR, sumG, sumB int64
	pix := rgba.Pix
	for i := 0; i < len(pix); i += 4 {
		sumR += int64(pix[i])
		sumG += int64(pix[i+1])
		sumB += int64(pix[i+2])
	}

	n := float64(total)
	r = float64(sumR) / n
	g = float64(sumG) / n
	b = float64(sumB) / n
	return
}

func toRGBA(src image.Image) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		return r
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// sampleIndices returns up to n evenly-spaced indices into a slice of length total.
func sampleIndices(total, n int) []int {
	if total <= n {
		idx := make([]int, total)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i * total / n
	}
	return idx
}

// safeRatio returns a/b, or 1.0 if b is zero.
func safeRatio(a, b float64) float64 {
	if b <= 0 {
		return 1.0
	}
	return a / b
}

// clampWBZWO rounds and clamps a WB value to the 0–99 integer range used by ZWO cameras.
func clampWBZWO(v float64) float64 {
	return math.Round(math.Max(0, math.Min(99, v)))
}

// clampWBLibcamera rounds to 2 decimal places and clamps to the 0.0–10.0
// gain-multiplier range used by libcamera's --awbgains.
func clampWBLibcamera(v float64) float64 {
	v = math.Max(0, math.Min(10.0, v))
	return math.Round(v*100) / 100
}

// colorCast describes the dominant colour imbalance in plain English.
func colorCast(r, g, b float64) string {
	threshold := 5.0
	rDom := r - g > threshold
	bDom := b - g > threshold
	rLow := g - r > threshold
	bLow := g - b > threshold

	switch {
	case rDom && bDom:
		return "magenta (R and B both elevated)"
	case rDom && bLow:
		return "warm/red (R high, B low)"
	case bDom && rLow:
		return "cool/blue (B high, R low)"
	case rDom:
		return "slightly warm/red"
	case bDom:
		return "slightly cool/blue"
	case rLow && bLow:
		return "green cast (R and B both low)"
	case rLow:
		return "slightly green-blue"
	case bLow:
		return "slightly green-red"
	default:
		return "approximately neutral"
	}
}

// explain returns a note describing the suggested change direction.
func explain(res *Result) string {
	redDiff := res.SuggestedWBRed - res.CurrentWBRed
	blueDiff := res.SuggestedWBBlue - res.CurrentWBBlue

	// Threshold for "no meaningful change" differs by backend precision.
	threshold := 1.0
	diffFmt := "%.0f"
	if res.isLibcamera() {
		threshold = 0.01
		diffFmt = "%.2f"
	}

	noRed := math.Abs(redDiff) < threshold
	noBlue := math.Abs(blueDiff) < threshold

	if noRed && noBlue {
		return "  Current settings look well balanced — no change recommended."
	}

	var parts []string
	if !noRed {
		dir := "increase"
		if redDiff < 0 {
			dir = "decrease"
		}
		parts = append(parts, fmt.Sprintf("%s WB Red by "+diffFmt, dir, math.Abs(redDiff)))
	}
	if !noBlue {
		dir := "increase"
		if blueDiff < 0 {
			dir = "decrease"
		}
		parts = append(parts, fmt.Sprintf("%s WB Blue by "+diffFmt, dir, math.Abs(blueDiff)))
	}

	note := "  Suggested change: " + strings.Join(parts, ", ") + ".\n"
	note += "  Apply gradually — one adjustment per night is recommended."
	return note
}
