// Package capture implements the main capture loop, auto-exposure, and dark frame handling.
package capture

import (
	"log/slog"
	"math"
	"time"
)

// ExposureController implements a mean-brightness auto-exposure algorithm.
// It adjusts exposure and gain to maintain a target mean brightness,
// prioritizing lower gain (less noise) over longer exposure (up to the max).
//
// This replicates the algorithm from allsky's capture_ZWO.cpp / mode_mean.cpp.
type ExposureController struct {
	// Target mean brightness (0-255).
	TargetBrightness float64
	// Current exposure.
	Exposure time.Duration
	// Current gain.
	Gain float64
	// Bounds.
	MinExposure time.Duration
	MaxExposure time.Duration
	MinGain     float64
	MaxGain     float64
	// Aggression controls how aggressively the algorithm adjusts (0.0-1.0).
	// Higher values converge faster but may oscillate.
	Aggression float64

	// Anti-oscillation state.
	lastDirection int // -1 = decreased, +1 = increased, 0 = initial
	dirChanges    int
	logger        *slog.Logger
}

// NewExposureController creates an auto-exposure controller with the given parameters.
func NewExposureController(
	targetBrightness float64,
	initExposure time.Duration,
	initGain float64,
	minExposure, maxExposure time.Duration,
	minGain, maxGain float64,
	logger *slog.Logger,
) *ExposureController {
	return &ExposureController{
		TargetBrightness: targetBrightness,
		Exposure:         initExposure,
		Gain:             initGain,
		MinExposure:      minExposure,
		MaxExposure:      maxExposure,
		MinGain:          minGain,
		MaxGain:          maxGain,
		Aggression:       0.75,
		logger:           logger,
	}
}

// Adjust updates exposure and gain based on the measured mean brightness of the last frame.
// Returns the new exposure and gain to use for the next frame.
func (ec *ExposureController) Adjust(measuredMean float64) (time.Duration, float64) {
	if measuredMean <= 0 {
		measuredMean = 1 // avoid division by zero
	}

	// Compute the raw brightness ratio.
	ratio := ec.TargetBrightness / measuredMean

	// Apply aggression dampening.
	// Move the ratio toward 1.0 based on aggression level.
	dampedRatio := 1.0 + (ratio-1.0)*ec.Aggression

	// Detect oscillation (ping-pong between over/under exposed).
	direction := 0
	if dampedRatio > 1.01 {
		direction = 1 // need more light
	} else if dampedRatio < 0.99 {
		direction = -1 // need less light
	}

	if ec.lastDirection != 0 && direction != 0 && direction != ec.lastDirection {
		ec.dirChanges++
		// Reduce aggression when oscillating to converge.
		if ec.dirChanges >= 3 {
			ec.Aggression *= 0.8
			if ec.Aggression < 0.2 {
				ec.Aggression = 0.2
			}
			ec.dirChanges = 0
			ec.logger.Debug("reducing aggression due to oscillation",
				"aggression", ec.Aggression)
		}
	} else {
		ec.dirChanges = 0
		// Slowly recover aggression when stable.
		if ec.Aggression < 0.75 {
			ec.Aggression += 0.02
		}
	}
	ec.lastDirection = direction

	// Compute the total "exposure level" combining exposure and gain.
	// We work in a unified logarithmic space: level = log2(exposure_us * gain).
	currentLevel := ec.exposureLevel(ec.Exposure, ec.Gain)
	targetLevel := currentLevel + math.Log2(dampedRatio)

	// Decompose target level back into exposure and gain.
	// Strategy: maximize exposure first (less noise), use gain only when exposure is maxed out.
	newExposure, newGain := ec.decompose(targetLevel)

	ec.logger.Debug("auto-exposure adjustment",
		"measured_mean", measuredMean,
		"target_mean", ec.TargetBrightness,
		"ratio", ratio,
		"damped_ratio", dampedRatio,
		"old_exposure", ec.Exposure,
		"old_gain", ec.Gain,
		"new_exposure", newExposure,
		"new_gain", newGain,
	)

	ec.Exposure = newExposure
	ec.Gain = newGain

	return newExposure, newGain
}

// exposureLevel computes a unified logarithmic exposure level.
func (ec *ExposureController) exposureLevel(exposure time.Duration, gain float64) float64 {
	us := float64(exposure.Microseconds())
	if us < 1 {
		us = 1
	}
	if gain < 1 {
		gain = 1
	}
	return math.Log2(us * gain)
}

// decompose converts a target exposure level back into separate exposure and gain values.
// Strategy: use exposure first, then gain.
func (ec *ExposureController) decompose(targetLevel float64) (time.Duration, float64) {
	// Total "light equivalent" in the logarithmic space.
	target := math.Pow(2, targetLevel)
	if target < 1 {
		target = 1
	}

	// Try maximum exposure with minimum gain first.
	maxExpUs := float64(ec.MaxExposure.Microseconds())
	minExpUs := float64(ec.MinExposure.Microseconds())
	if minExpUs < 1 {
		minExpUs = 1
	}

	// exposure_us * gain = target
	// Maximize exposure: exposure = min(target / min_gain, max_exposure)
	minGain := ec.MinGain
	if minGain < 1 {
		minGain = 1
	}

	expUs := target / minGain
	gain := minGain

	if expUs > maxExpUs {
		// Exposure maxed out, increase gain.
		expUs = maxExpUs
		gain = target / expUs
		if gain > ec.MaxGain {
			gain = ec.MaxGain
		}
		if gain < minGain {
			gain = minGain
		}
	} else if expUs < minExpUs {
		// Exposure at minimum, we might need gain < min but clamp it.
		expUs = minExpUs
		gain = target / expUs
		if gain < minGain {
			gain = minGain
		}
		if gain > ec.MaxGain {
			gain = ec.MaxGain
		}
	}

	exposure := time.Duration(expUs) * time.Microsecond

	// Clamp exposure.
	if exposure < ec.MinExposure {
		exposure = ec.MinExposure
	}
	if exposure > ec.MaxExposure {
		exposure = ec.MaxExposure
	}

	return exposure, gain
}

// IsConverged returns true if the measured brightness is within an acceptable
// range of the target (within 10%).
func (ec *ExposureController) IsConverged(measuredMean float64) bool {
	ratio := measuredMean / ec.TargetBrightness
	return ratio > 0.9 && ratio < 1.1
}
