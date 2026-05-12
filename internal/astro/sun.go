// Package astro provides astronomical calculations for day/night detection.
package astro

import (
	"math"
	"time"
)

const (
	// Standard sun altitude thresholds in degrees.
	AngleDaylight        = 0.0
	AngleCivilTwilight   = -6.0
	AngleNauticalTwlight = -12.0
	AngleAstroTwilight   = -18.0

	degToRad = math.Pi / 180.0
	radToDeg = 180.0 / math.Pi
)

// SunPosition contains the sun's position in the sky.
type SunPosition struct {
	// Altitude in degrees above the horizon. Negative = below horizon.
	Altitude float64
	// Azimuth in degrees from north (0-360).
	Azimuth float64
}

// GetSunPosition calculates the sun's position for a given time and location.
// Uses the NOAA solar position algorithm (accurate to ~0.01 degrees).
func GetSunPosition(t time.Time, latitude, longitude float64) SunPosition {
	// Julian date calculation.
	jd := timeToJulianDate(t)
	jc := (jd - 2451545.0) / 36525.0 // Julian century

	// Solar coordinates.
	geomMeanLongSun := math.Mod(280.46646+jc*(36000.76983+0.0003032*jc), 360.0)
	geomMeanAnomSun := 357.52911 + jc*(35999.05029-0.0001537*jc)
	eccentEarthOrbit := 0.016708634 - jc*(0.000042037+0.0000001267*jc)

	sunEqOfCenter := math.Sin(geomMeanAnomSun*degToRad)*(1.914602-jc*(0.004817+0.000014*jc)) +
		math.Sin(2*geomMeanAnomSun*degToRad)*(0.019993-0.000101*jc) +
		math.Sin(3*geomMeanAnomSun*degToRad)*0.000289

	sunTrueLong := geomMeanLongSun + sunEqOfCenter
	sunAppLong := sunTrueLong - 0.00569 - 0.00478*math.Sin((125.04-1934.136*jc)*degToRad)

	meanObliqEcliptic := 23.0 + (26.0+(21.448-jc*(46.815+jc*(0.00059-jc*0.001813)))/60.0)/60.0
	obliqCorr := meanObliqEcliptic + 0.00256*math.Cos((125.04-1934.136*jc)*degToRad)

	sunDeclination := math.Asin(math.Sin(obliqCorr*degToRad) * math.Sin(sunAppLong*degToRad))

	varY := math.Tan(obliqCorr/2.0*degToRad) * math.Tan(obliqCorr/2.0*degToRad)
	eqOfTime := 4.0 * radToDeg * (varY*math.Sin(2.0*geomMeanLongSun*degToRad) -
		2.0*eccentEarthOrbit*math.Sin(geomMeanAnomSun*degToRad) +
		4.0*eccentEarthOrbit*varY*math.Sin(geomMeanAnomSun*degToRad)*math.Cos(2.0*geomMeanLongSun*degToRad) -
		0.5*varY*varY*math.Sin(4.0*geomMeanLongSun*degToRad) -
		1.25*eccentEarthOrbit*eccentEarthOrbit*math.Sin(2.0*geomMeanAnomSun*degToRad))

	// Hour angle.
	_, offset := t.Zone()
	timezone := float64(offset) / 3600.0
	timeFrac := (float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0) / 24.0
	trueSolarTime := math.Mod(timeFrac*1440.0+eqOfTime+4.0*longitude-60.0*timezone, 1440.0)

	var hourAngle float64
	if trueSolarTime/4.0 < 0 {
		hourAngle = trueSolarTime/4.0 + 180.0
	} else {
		hourAngle = trueSolarTime/4.0 - 180.0
	}

	latRad := latitude * degToRad

	// Solar altitude.
	sinAlt := math.Sin(latRad)*math.Sin(sunDeclination) +
		math.Cos(latRad)*math.Cos(sunDeclination)*math.Cos(hourAngle*degToRad)
	altitude := math.Asin(sinAlt) * radToDeg

	// Solar azimuth.
	cosAz := (math.Sin(sunDeclination) - math.Sin(latRad)*sinAlt) /
		(math.Cos(latRad) * math.Cos(altitude*degToRad))
	// Clamp to [-1, 1] to avoid NaN from floating point errors.
	cosAz = math.Max(-1, math.Min(1, cosAz))

	var azimuth float64
	if hourAngle > 0 {
		azimuth = math.Mod(math.Acos(cosAz)*radToDeg+180.0, 360.0)
	} else {
		azimuth = math.Mod(540.0-math.Acos(cosAz)*radToDeg, 360.0)
	}

	return SunPosition{
		Altitude: altitude,
		Azimuth:  azimuth,
	}
}

// IsNight returns true if the sun is below the given angle threshold.
func IsNight(t time.Time, latitude, longitude, angle float64) bool {
	pos := GetSunPosition(t, latitude, longitude)
	return pos.Altitude < angle
}

// IsDaytime returns true if the sun is above the given angle threshold.
func IsDaytime(t time.Time, latitude, longitude, angle float64) bool {
	return !IsNight(t, latitude, longitude, angle)
}

func timeToJulianDate(t time.Time) float64 {
	t = t.UTC()
	y := float64(t.Year())
	m := float64(t.Month())
	d := float64(t.Day()) +
		float64(t.Hour())/24.0 +
		float64(t.Minute())/1440.0 +
		float64(t.Second())/86400.0

	if m <= 2 {
		y--
		m += 12
	}

	a := math.Floor(y / 100.0)
	b := 2.0 - a + math.Floor(a/4.0)

	return math.Floor(365.25*(y+4716.0)) + math.Floor(30.6001*(m+1.0)) + d + b - 1524.5
}
