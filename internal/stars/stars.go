// Package stars detects point sources (stars) in night-sky frames and
// estimates their sharpness. The count is a per-frame sky-quality /
// transparency metric; the mean FWHM is a focus metric — smaller is sharper.
package stars

import (
	"image"
	"math"
	"slices"

	imgutil "github.com/dburman/nightsky/internal/image"
)

// Result holds star statistics for one frame.
type Result struct {
	// Count of detected point sources in the sky region.
	Count int
	// MeanFWHM is the mean full-width-half-maximum of detected stars in
	// pixels (0 when no stars were found). Lower = better focus.
	MeanFWHM float64
}

const (
	// detectionSigma scales the noise-robust threshold above background.
	detectionSigma = 6.0
	// minThresholdMargin is the floor above background, so a flat frame
	// (zero noise spread) never yields detections.
	minThresholdMargin = 8
	// minSeparation suppresses duplicate maxima closer than this (pixels).
	minSeparation = 4
	// fwhmMaxWalk bounds the half-max search radius; blobs wider than this
	// (clouds, moon) are not stars and are dropped.
	fwhmMaxWalk = 10
	// fwhmSampleCap bounds how many stars contribute to the FWHM average.
	fwhmSampleCap = 200
)

// Detect finds stars in the inner 50%-radius circle (the same sky region the
// cloud metric and center metering use) and returns their count and mean FWHM.
func Detect(img image.Image) Result {
	rgba := imgutil.ToRGBA(img)
	w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if w < 8 || h < 8 {
		return Result{}
	}

	// Luminance plane.
	lum := make([]uint8, w*h)
	pix := rgba.Pix
	for i := 0; i < w*h; i++ {
		p := i * 4
		lum[i] = uint8((299*int(pix[p]) + 587*int(pix[p+1]) + 114*int(pix[p+2])) / 1000)
	}

	background, spread := backgroundStats(lum, w, h)
	threshold := background + detectionSigma*spread
	if threshold < background+minThresholdMargin {
		threshold = background + minThresholdMargin
	}

	cx, cy := float64(w)/2, float64(h)/2
	radius := float64(min(w, h)) * 0.5

	type peak struct{ x, y int }
	var peaks []peak

	// Local maxima above threshold inside the sky circle. Plateau ties are
	// broken by requiring strict inequality against the trailing neighbours.
	for y := 1; y < h-1; y++ {
		dy := float64(y) - cy
		for x := 1; x < w-1; x++ {
			dx := float64(x) - cx
			if dx*dx+dy*dy > radius*radius {
				continue
			}
			v := lum[y*w+x]
			if float64(v) <= threshold {
				continue
			}
			if v < lum[(y-1)*w+x-1] || v < lum[(y-1)*w+x] || v < lum[(y-1)*w+x+1] ||
				v < lum[y*w+x-1] ||
				v <= lum[y*w+x+1] ||
				v <= lum[(y+1)*w+x-1] || v <= lum[(y+1)*w+x] || v <= lum[(y+1)*w+x+1] {
				continue
			}
			peaks = append(peaks, peak{x, y})
		}
	}

	// Minimum-separation suppression (peaks arrive in scan order; a simple
	// reverse check against recently accepted peaks suffices at this scale).
	var accepted []peak
	for _, p := range peaks {
		ok := true
		for i := len(accepted) - 1; i >= 0; i-- {
			a := accepted[i]
			if p.y-a.y > minSeparation {
				break // accepted is scan-ordered; all earlier rows are far enough
			}
			dx, dy := p.x-a.x, p.y-a.y
			if dx*dx+dy*dy < minSeparation*minSeparation {
				ok = false
				break
			}
		}
		if ok {
			accepted = append(accepted, p)
		}
	}

	// FWHM: half-max width along x and y from each peak, averaged. Blobs
	// wider than fwhmMaxWalk are rejected as non-stellar (and removed from
	// the count — a cloud edge is not a star).
	var fwhms []float64
	count := 0
	for _, p := range accepted {
		peakV := float64(lum[p.y*w+p.x])
		half := background + (peakV-background)/2
		wx := halfMaxWidth(lum, w, h, p.x, p.y, 1, 0, half) + halfMaxWidth(lum, w, h, p.x, p.y, -1, 0, half)
		wy := halfMaxWidth(lum, w, h, p.x, p.y, 0, 1, half) + halfMaxWidth(lum, w, h, p.x, p.y, 0, -1, half)
		if wx > 2*fwhmMaxWalk-1 || wy > 2*fwhmMaxWalk-1 {
			continue // too wide to be a star
		}
		count++
		if len(fwhms) < fwhmSampleCap {
			fwhms = append(fwhms, (float64(wx)+float64(wy))/2)
		}
	}

	var meanFWHM float64
	if len(fwhms) > 0 {
		var sum float64
		for _, f := range fwhms {
			sum += f
		}
		meanFWHM = sum / float64(len(fwhms))
	}

	return Result{Count: count, MeanFWHM: meanFWHM}
}

// halfMaxWidth walks from (x,y) in direction (dx,dy) until luminance drops
// below half, returning the distance walked (capped at fwhmMaxWalk).
func halfMaxWidth(lum []uint8, w, h, x, y, dx, dy int, half float64) int {
	for i := 1; i <= fwhmMaxWalk; i++ {
		nx, ny := x+i*dx, y+i*dy
		if nx < 0 || nx >= w || ny < 0 || ny >= h {
			return i
		}
		if float64(lum[ny*w+nx]) < half {
			return i
		}
	}
	return fwhmMaxWalk + fwhmMaxWalk // signals "too wide" to the caller
}

// backgroundStats returns a robust background level and spread (median and
// scaled MAD) from a subsample of the luminance plane.
func backgroundStats(lum []uint8, w, h int) (median, spread float64) {
	var samples []uint8
	for y := 0; y < h; y += 8 {
		for x := 0; x < w; x += 8 {
			samples = append(samples, lum[y*w+x])
		}
	}
	if len(samples) == 0 {
		return 0, 0
	}
	slices.Sort(samples)
	median = float64(samples[len(samples)/2])

	devs := make([]float64, len(samples))
	for i, s := range samples {
		devs[i] = math.Abs(float64(s) - median)
	}
	slices.Sort(devs)
	// 1.4826 scales MAD to the standard deviation of a normal distribution.
	spread = 1.4826 * devs[len(devs)/2]
	return median, spread
}
