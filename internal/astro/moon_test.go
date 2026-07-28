package astro

import (
	"testing"
	"time"
)

// The total lunar eclipse of 2000-01-21 04:44 UT is a hard reference: the
// Moon was full (illuminated ≈ 1.0) and, for the US east coast, high in the
// sky near local midnight. The truncated theory carries ~2-3% error in the
// illuminated fraction, hence the 0.95 bound.
func TestMoonPhase_FullAtEclipse(t *testing.T) {
	tm := time.Date(2000, 1, 21, 4, 44, 0, 0, time.UTC)
	if p := MoonPhase(tm); p < 0.95 {
		t.Errorf("illuminated fraction at total lunar eclipse = %.3f, want > 0.95", p)
	}
}

// New moon 2000-02-05 13:03 UT.
func TestMoonPhase_NewMoon(t *testing.T) {
	tm := time.Date(2000, 2, 5, 13, 3, 0, 0, time.UTC)
	if p := MoonPhase(tm); p > 0.03 {
		t.Errorf("illuminated fraction at new moon = %.3f, want < 0.03", p)
	}
}

// During the 2000-01-21 eclipse the full Moon must be well above the horizon
// for the US east coast (it was near local midnight), and the Sun well below.
func TestMoonPosition_EclipseGeometry(t *testing.T) {
	tm := time.Date(2000, 1, 21, 4, 44, 0, 0, time.UTC)
	const lat, lon = 40.0, -75.0

	moon := MoonPosition(tm, lat, lon)
	if moon.Altitude < 20 {
		t.Errorf("moon altitude = %.1f, want well above horizon (>20)", moon.Altitude)
	}
	if moon.Azimuth < 0 || moon.Azimuth >= 360 {
		t.Errorf("azimuth out of range: %v", moon.Azimuth)
	}

	sun := GetSunPosition(tm, lat, lon)
	if sun.Altitude > -30 {
		t.Errorf("sun altitude = %.1f, want deep below horizon at local midnight", sun.Altitude)
	}
}

// A full moon is up essentially all night.
func TestMoonUpFraction_FullMoonNight(t *testing.T) {
	start := time.Date(2000, 1, 21, 0, 0, 0, 0, time.UTC)  // ~19:00 local
	end := time.Date(2000, 1, 21, 10, 0, 0, 0, time.UTC)   // ~05:00 local
	if f := MoonUpFraction(start, end, 40.0, -75.0); f < 0.8 {
		t.Errorf("full-moon night up-fraction = %.2f, want > 0.8", f)
	}
	if f := MoonUpFraction(end, start, 40.0, -75.0); f != 0 {
		t.Errorf("inverted interval should return 0, got %v", f)
	}
}

// Mid-latitude summer night: dusk in the late evening, dawn in the early
// morning, roughly 5-8 hours long at civil twilight.
func TestNightWindow_MidLatitudeSummer(t *testing.T) {
	date := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	start, end, ok := NightWindow(date, 45.0, 0.0, -6.0)
	if !ok {
		t.Fatal("expected a night window")
	}
	if start.Hour() < 19 || start.Hour() > 22 {
		t.Errorf("dusk at %s, expected 19:00-22:59 UT", start.Format("15:04"))
	}
	if end.Hour() < 2 || end.Hour() > 5 {
		t.Errorf("dawn at %s, expected 02:00-05:59 UT", end.Format("15:04"))
	}
	night := end.Sub(start)
	if night < 4*time.Hour || night > 9*time.Hour {
		t.Errorf("night length = %v, expected 4-9h", night)
	}
}

// Svalbard midsummer: no night at civil twilight — must report ok=false.
func TestNightWindow_PolarDay(t *testing.T) {
	date := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	if _, _, ok := NightWindow(date, 78.0, 15.0, -6.0); ok {
		t.Error("expected no night window under midnight sun")
	}
}
