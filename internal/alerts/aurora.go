package alerts

import "slices"

// AuroraConfig tunes the aurora detector.
type AuroraConfig struct {
	// RatioThreshold is how far the green ratio must exceed the rolling
	// baseline to count as anomalous (e.g. 1.3 = 30% green excess).
	RatioThreshold float64
	// MinFrames is how many consecutive anomalous frames arm an alert —
	// debounces single-frame artifacts (planes, sensor glitches).
	MinFrames int
	// MaxCloud suppresses detection above this cloud coverage; clouds over
	// light pollution also skew green.
	MaxCloud float64
}

const (
	// baselineWindow is how many recent quiet frames form the baseline.
	baselineWindow = 30
	// baselineMinSamples must accumulate before detection activates.
	baselineMinSamples = 10
	// rearmFrames of quiet after an alert re-arm the detector, so a
	// long-lasting display produces one alert per surge rather than one
	// per frame.
	rearmFrames = 10
)

// AuroraDetector watches the per-frame green ratio for a sustained excess
// over its rolling baseline. It is not safe for concurrent use; the capture
// loop owns it.
type AuroraDetector struct {
	cfg AuroraConfig

	baseline    []float64 // recent quiet-frame ratios
	streak      int       // consecutive anomalous frames
	quietStreak int       // consecutive quiet frames since last anomaly
	armed       bool
}

// NewAuroraDetector returns a detector with the given tuning.
func NewAuroraDetector(cfg AuroraConfig) *AuroraDetector {
	if cfg.RatioThreshold <= 1 {
		cfg.RatioThreshold = 1.3
	}
	if cfg.MinFrames <= 0 {
		cfg.MinFrames = 3
	}
	if cfg.MaxCloud <= 0 {
		cfg.MaxCloud = 0.5
	}
	return &AuroraDetector{cfg: cfg, armed: true}
}

// Reset clears state at the start of a night session.
func (d *AuroraDetector) Reset() {
	d.baseline = d.baseline[:0]
	d.streak = 0
	d.quietStreak = 0
	d.armed = true
}

// Observe feeds one frame's green ratio and cloud coverage. It returns true
// exactly when an alert should fire: the ratio has exceeded the rolling
// baseline by the threshold for MinFrames consecutive frames under
// acceptably clear skies, and the detector is armed.
func (d *AuroraDetector) Observe(greenRatio, cloudCoverage float64) bool {
	// Cloudy frames neither trigger nor contribute to the baseline.
	if cloudCoverage > d.cfg.MaxCloud {
		d.streak = 0
		return false
	}

	baselineReady := len(d.baseline) >= baselineMinSamples
	anomalous := baselineReady && greenRatio > d.median()*d.cfg.RatioThreshold

	if !anomalous {
		// Quiet frames feed the baseline (anomalous ones must not, or a
		// long aurora would absorb into "normal").
		d.baseline = append(d.baseline, greenRatio)
		if len(d.baseline) > baselineWindow {
			d.baseline = d.baseline[1:]
		}
		d.streak = 0
		d.quietStreak++
		if d.quietStreak >= rearmFrames {
			d.armed = true
		}
		return false
	}

	d.quietStreak = 0
	d.streak++
	if d.armed && d.streak >= d.cfg.MinFrames {
		d.armed = false
		return true
	}
	return false
}

func (d *AuroraDetector) median() float64 {
	s := slices.Clone(d.baseline)
	slices.Sort(s)
	return s[len(s)/2]
}
