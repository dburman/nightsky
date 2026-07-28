package astro

import (
	"math"
	"time"
)

// Moon calculations use the low-precision geocentric theory from Schlyter's
// "Computing planetary positions" (itself a condensation of Meeus), accurate
// to roughly 1° in position and a couple of percent in illuminated fraction —
// ample for exposure decisions and annotations, and dependency-free.

// rev normalizes an angle in degrees to [0, 360).
func rev(deg float64) float64 {
	r := math.Mod(deg, 360)
	if r < 0 {
		r += 360
	}
	return r
}

func daysSinceJ2000(t time.Time) float64 {
	return timeToJulianDate(t) - 2451545.0
}

// sunEcliptic returns the Sun's geocentric ecliptic longitude (degrees) at d
// days since J2000.
func sunEcliptic(d float64) float64 {
	w := 282.9404 + 4.70935e-5*d       // argument of perihelion
	e := 0.016709 - 1.151e-9*d         // eccentricity
	M := rev(356.0470 + 0.9856002585*d) // mean anomaly

	Mr := M * degToRad
	// Eccentric anomaly (one Newton step is plenty at solar eccentricity).
	E := M + e*radToDeg*math.Sin(Mr)*(1.0+e*math.Cos(Mr))
	Er := E * degToRad

	xv := math.Cos(Er) - e
	yv := math.Sqrt(1.0-e*e) * math.Sin(Er)
	v := math.Atan2(yv, xv) * radToDeg // true anomaly
	return rev(v + w)
}

// moonEcliptic returns the Moon's geocentric ecliptic longitude and latitude
// (degrees) and distance (Earth radii) at d days since J2000, including the
// principal perturbation terms.
func moonEcliptic(d float64) (lon, lat, r float64) {
	N := rev(125.1228 - 0.0529538083*d) // longitude of ascending node
	const i = 5.1454                    // inclination
	w := rev(318.0634 + 0.1643573223*d) // argument of perigee
	const a = 60.2666                   // semi-major axis, Earth radii
	const e = 0.054900                  // eccentricity
	M := rev(115.3654 + 13.0649929509*d) // mean anomaly

	// Eccentric anomaly by iteration.
	E := M + e*radToDeg*math.Sin(M*degToRad)*(1.0+e*math.Cos(M*degToRad))
	for range 5 {
		E = E - (E-e*radToDeg*math.Sin(E*degToRad)-M)/(1.0-e*math.Cos(E*degToRad))
	}
	Er := E * degToRad

	xv := a * (math.Cos(Er) - e)
	yv := a * math.Sqrt(1.0-e*e) * math.Sin(Er)
	v := math.Atan2(yv, xv) * radToDeg
	r = math.Sqrt(xv*xv + yv*yv)

	// Ecliptic coordinates.
	Nr := N * degToRad
	vw := (v + w) * degToRad
	ir := i * degToRad
	xe := r * (math.Cos(Nr)*math.Cos(vw) - math.Sin(Nr)*math.Sin(vw)*math.Cos(ir))
	ye := r * (math.Sin(Nr)*math.Cos(vw) + math.Cos(Nr)*math.Sin(vw)*math.Cos(ir))
	ze := r * math.Sin(vw) * math.Sin(ir)

	lon = rev(math.Atan2(ye, xe) * radToDeg)
	lat = math.Atan2(ze, math.Sqrt(xe*xe+ye*ye)) * radToDeg

	// Perturbations (arguments in degrees).
	Ms := rev(356.0470 + 0.9856002585*d)      // Sun's mean anomaly
	Ls := rev(Ms + 282.9404 + 4.70935e-5*d)   // Sun's mean longitude
	Lm := rev(M + w + N)                      // Moon's mean longitude
	D := rev(Lm - Ls)                         // mean elongation
	F := rev(Lm - N)                          // argument of latitude

	s := func(deg float64) float64 { return math.Sin(deg * degToRad) }

	lon += -1.274*s(M-2*D) +
		0.658*s(2*D) -
		0.186*s(Ms) -
		0.059*s(2*M-2*D) -
		0.057*s(M-2*D+Ms) +
		0.053*s(M+2*D) +
		0.046*s(2*D-Ms) +
		0.041*s(M-Ms) -
		0.035*s(D) -
		0.031*s(M+Ms) -
		0.015*s(2*F-2*D) +
		0.011*s(M-4*D)

	lat += -0.173*s(F-2*D) -
		0.055*s(M-F-2*D) -
		0.046*s(M+F-2*D) +
		0.033*s(F+2*D) +
		0.017*s(2*M+F)

	r += -0.58*math.Cos((M-2*D)*degToRad) - 0.46*math.Cos(2*D*degToRad)

	return rev(lon), lat, r
}

// MoonPosition returns the Moon's topocentric altitude and azimuth for the
// given time and location. Azimuth is degrees from north; altitude includes
// the ~1° parallax correction (significant for the nearby Moon).
func MoonPosition(t time.Time, latitude, longitude float64) SunPosition {
	d := daysSinceJ2000(t)
	mlon, mlat, mr := moonEcliptic(d)

	// Ecliptic → equatorial.
	ecl := (23.4393 - 3.563e-7*d) * degToRad
	lonR := mlon * degToRad
	latR := mlat * degToRad
	xe := math.Cos(latR) * math.Cos(lonR)
	ye := math.Cos(latR) * math.Sin(lonR)
	ze := math.Sin(latR)
	xq := xe
	yq := ye*math.Cos(ecl) - ze*math.Sin(ecl)
	zq := ye*math.Sin(ecl) + ze*math.Cos(ecl)

	ra := rev(math.Atan2(yq, xq) * radToDeg)
	dec := math.Atan2(zq, math.Sqrt(xq*xq+yq*yq)) * radToDeg

	// Local sidereal time from the Sun's mean longitude (valid because d
	// carries the time-of-day fraction).
	ut := t.UTC()
	utHours := float64(ut.Hour()) + float64(ut.Minute())/60 + float64(ut.Second())/3600
	Ms := rev(356.0470 + 0.9856002585*d)
	Ls := rev(Ms + 282.9404 + 4.70935e-5*d)
	lst := rev(Ls + 180 + utHours*15 + longitude)

	ha := rev(lst-ra) * degToRad
	latRad := latitude * degToRad
	decR := dec * degToRad

	sinAlt := math.Sin(latRad)*math.Sin(decR) + math.Cos(latRad)*math.Cos(decR)*math.Cos(ha)
	alt := math.Asin(sinAlt)

	az := math.Atan2(math.Sin(ha), math.Cos(ha)*math.Sin(latRad)-math.Tan(decR)*math.Cos(latRad))*radToDeg + 180

	// Topocentric altitude: subtract lunar parallax.
	par := math.Asin(1.0 / mr)
	alt -= par * math.Cos(alt)

	return SunPosition{
		Altitude: alt * radToDeg,
		Azimuth:  rev(az),
	}
}

// MoonPhase returns the Moon's illuminated fraction (0 = new, 1 = full).
func MoonPhase(t time.Time) float64 {
	d := daysSinceJ2000(t)
	slon := sunEcliptic(d)
	mlon, mlat, _ := moonEcliptic(d)

	elong := math.Acos(math.Cos((slon - mlon) * degToRad) * math.Cos(mlat*degToRad))
	phaseAngle := math.Pi - elong
	return (1 + math.Cos(phaseAngle)) / 2
}

// NightWindow returns the start (dusk) and end (dawn) of the night that
// begins on the given calendar date, scanning from local noon in 5-minute
// steps. ok is false when no complete night occurs within 24 hours of noon
// (polar day/night).
func NightWindow(date time.Time, latitude, longitude, angle float64) (start, end time.Time, ok bool) {
	base := time.Date(date.Year(), date.Month(), date.Day(), 12, 0, 0, 0, date.Location())
	const step = 5 * time.Minute

	var duskFound bool
	prevNight := IsNight(base, latitude, longitude, angle)
	for t := base.Add(step); t.Before(base.Add(30 * time.Hour)); t = t.Add(step) {
		night := IsNight(t, latitude, longitude, angle)
		switch {
		case !duskFound && night && !prevNight:
			start = t
			duskFound = true
		case duskFound && !night && prevNight:
			return start, t, true
		}
		prevNight = night
	}
	return time.Time{}, time.Time{}, false
}

// MoonUpFraction returns the fraction of the interval [start, end] during
// which the Moon is above the horizon, sampled every 10 minutes.
func MoonUpFraction(start, end time.Time, latitude, longitude float64) float64 {
	if !end.After(start) {
		return 0
	}
	const step = 10 * time.Minute
	var up, total int
	for t := start; !t.After(end); t = t.Add(step) {
		if MoonPosition(t, latitude, longitude).Altitude > 0 {
			up++
		}
		total++
	}
	if total == 0 {
		return 0
	}
	return float64(up) / float64(total)
}
