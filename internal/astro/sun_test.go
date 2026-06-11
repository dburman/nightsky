package astro

import (
	"testing"
	"time"
)

// A restart at 01:00 UTC must resolve the session to the previous evening's
// dusk: mid-latitude, Greenwich meridian, civil twilight.
func TestMostRecentDusk_AfterMidnight(t *testing.T) {
	const lat, lon, angle = 45.0, 0.0, -6.0
	now := time.Date(2026, 6, 11, 1, 0, 0, 0, time.UTC)

	if !IsNight(now, lat, lon, angle) {
		t.Fatal("test premise broken: expected night at 01:00 UTC")
	}

	dusk, ok := MostRecentDusk(now, lat, lon, angle)
	if !ok {
		t.Fatal("expected a dusk within 24h")
	}
	if got := dusk.UTC().Format("2006-01-02"); got != "2026-06-10" {
		t.Errorf("dusk date = %s, want 2026-06-10", got)
	}
	// dusk must sit just inside the night side of the crossing.
	if !IsNight(dusk, lat, lon, angle) {
		t.Error("returned dusk is not night")
	}
	if IsNight(dusk.Add(-5*time.Minute), lat, lon, angle) {
		t.Error("instant before returned dusk should still be day")
	}
}

func TestMostRecentDusk_DaytimeReturnsFalse(t *testing.T) {
	noon := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if _, ok := MostRecentDusk(noon, 45.0, 0.0, -6.0); ok {
		t.Error("expected ok=false during daytime")
	}
}

// Polar day: no dusk exists — must not loop forever or return a bogus time.
func TestMostRecentDusk_PolarNoTransition(t *testing.T) {
	// Svalbard in June: midnight sun, never night at civil twilight angle.
	tm := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	if _, ok := MostRecentDusk(tm, 78.0, 15.0, -6.0); ok {
		t.Error("expected ok=false under midnight sun")
	}
}
