package eta

import (
	"math"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

func ptr[T any](v T) *T { return &v }

func destination() *flight.Route {
	return &flight.Route{Destination: flight.Airport{
		Latitude: ptr(0.0), Longitude: ptr(0.0), ElevationFeet: ptr(0.0),
	}}
}

func observation(now time.Time) flight.Aircraft {
	return flight.Aircraft{
		Hex: "abc001", Callsign: "TEST123", Latitude: ptr(0.0), Longitude: ptr(-5.0),
		AltitudeFeet: ptr(35000.0), GroundSpeed: ptr(450.0), Track: ptr(90.0),
		VerticalRate: ptr(0.0), LastContact: &now, PositionUpdated: &now,
	}
}

func TestFlightPhasesAndPlausibleTimeBudgets(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		phase                                 string
		lon, altitude, speed, rate, low, high float64
	}{
		{"cruise", -5, 35000, 450, 0, 50, 58},
		{"descent", -1, 15000, 300, -1200, 15, 22},
		{"approach", -0.1, 1800, 140, -700, 2, 5},
		{"climb", -5, 15000, 350, 1200, 60, 70},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			a := observation(now)
			a.Longitude, a.AltitudeFeet, a.GroundSpeed, a.VerticalRate = &tc.lon, &tc.altitude, &tc.speed, &tc.rate
			var estimator Estimator
			got := estimator.Update(now, destination(), []flight.Aircraft{a})
			if got.Status != "estimated" || got.Quality != "rough" || got.Phase != tc.phase || got.TouchdownAt == nil || got.MinutesRemaining == nil {
				t.Fatalf("expected rough %s estimate, got %+v", tc.phase, got)
			}
			if *got.MinutesRemaining < tc.low || *got.MinutesRemaining > tc.high {
				t.Errorf("time budget %.1f outside plausible scenario range %.1f–%.1f min", *got.MinutesRemaining, tc.low, tc.high)
			}
			// The model must allow more than current-speed, straight-line travel.
			straightLine := *got.DistanceNM / tc.speed * 60
			if *got.MinutesRemaining <= straightLine {
				t.Errorf("missing approach allowance: got %.1f min vs %.1f min direct travel", *got.MinutesRemaining, straightLine)
			}
		})
	}
}

func TestUnavailableEstimates(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, reason string
		change       func(*flight.Aircraft, *flight.Route)
	}{
		{"stale position", "stale", func(a *flight.Aircraft, r *flight.Route) { a.PositionUpdated = ptr(now.Add(-91 * time.Second)) }},
		{"clock skew", "ahead", func(a *flight.Aircraft, r *flight.Route) { a.PositionUpdated = ptr(now.Add(time.Minute)) }},
		{"missing fix time", "timestamp unavailable", func(a *flight.Aircraft, r *flight.Route) { a.PositionUpdated = nil }},
		{"missing coordinates", "position unavailable", func(a *flight.Aircraft, r *flight.Route) { a.Latitude = nil }},
		{"invalid destination", "destination coordinates", func(a *flight.Aircraft, r *flight.Route) { r.Destination.Latitude = ptr(91.0) }},
		{"missing elevation", "elevation unavailable", func(a *flight.Aircraft, r *flight.Route) { r.Destination.ElevationFeet = nil }},
		{"missing altitude", "altitude", func(a *flight.Aircraft, r *flight.Route) { a.AltitudeFeet = nil }},
		{"no speed", "ground speed", func(a *flight.Aircraft, r *flight.Route) { a.GroundSpeed = nil }},
		{"nonfinite speed", "ground speed", func(a *flight.Aircraft, r *flight.Route) { a.GroundSpeed = ptr(math.NaN()) }},
		{"taxi speed", "ground speed", func(a *flight.Aircraft, r *flight.Route) { a.GroundSpeed = ptr(20.0) }},
		{"surface report", "on ground", func(a *flight.Aircraft, r *flight.Route) { a.OnGround = ptr(true) }},
		{"stale telemetry", "telemetry timestamp", func(a *flight.Aircraft, r *flight.Route) { a.LastContact = ptr(now.Add(-2 * time.Minute)) }},
		{"away heading", "track is away", func(a *flight.Aircraft, r *flight.Route) { a.Track = ptr(270.0) }},
		{"no direction", "more position history", func(a *flight.Aircraft, r *flight.Route) { a.Track = nil }},
		{"go around", "climbing near", func(a *flight.Aircraft, r *flight.Route) {
			a.Longitude, a.AltitudeFeet, a.VerticalRate = ptr(-0.1), ptr(2000.0), ptr(1000.0)
		}},
		{"old final fix", "projected touchdown has passed", func(a *flight.Aircraft, r *flight.Route) {
			a.Longitude, a.AltitudeFeet, a.GroundSpeed = ptr(-0.001), ptr(50.0), ptr(130.0)
			a.PositionUpdated = ptr(now.Add(-60 * time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, route := observation(now), destination()
			tc.change(&a, route)
			var estimator Estimator
			got := estimator.Update(now, route, []flight.Aircraft{a})
			if got.Status != "unavailable" || got.TouchdownAt != nil || got.MinutesRemaining != nil || !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("expected unavailable reason %q, got %+v", tc.reason, got)
			}
		})
	}
	var estimator Estimator
	for _, tc := range []struct {
		route    *flight.Route
		aircraft []flight.Aircraft
		reason   string
	}{
		{destination(), nil, "no current"},
		{nil, []flight.Aircraft{observation(now)}, "direct tracking"},
		{destination(), []flight.Aircraft{observation(now), observation(now)}, "multiple aircraft"},
	} {
		got := estimator.Update(now, tc.route, tc.aircraft)
		if got.Status != "unavailable" || got.TouchdownAt != nil || !strings.Contains(got.Reason, tc.reason) {
			t.Errorf("unexpected unavailable estimate: %+v", got)
		}
	}
}

func TestCachedPollsDoNotAdvanceTouchdown(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := observation(now)
	var estimator Estimator
	first := estimator.Update(now, destination(), []flight.Aircraft{a})
	next := estimator.Update(now.Add(30*time.Second), destination(), []flight.Aircraft{a})
	if first.TouchdownAt == nil || next.TouchdownAt == nil || !first.TouchdownAt.Equal(*next.TouchdownAt) {
		t.Fatalf("cached poll changed ETA: first=%+v, next=%+v", first, next)
	}
	if math.Abs((*first.MinutesRemaining-*next.MinutesRemaining)-0.5) > 0.001 {
		t.Error("remaining time must count down even when the fix is unchanged")
	}
	later := estimator.Update(now.Add(2*time.Minute), destination(), []flight.Aircraft{a})
	if later.TouchdownAt != nil || later.Status != "unavailable" {
		t.Error("cached estimate must expire when its source position becomes stale")
	}
}

func TestSmoothingAndHistoryDirection(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var estimator Estimator
	for i, speed := range []float64{450, 460, 700} {
		a := observation(now.Add(time.Duration(i) * 30 * time.Second))
		a.Longitude, a.GroundSpeed = ptr(-5+float64(i)*0.06), ptr(speed)
		got := estimator.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
		if got.Status != "estimated" {
			t.Fatalf("normal progress should support an estimate: %+v", got)
		}
		if i == 2 && *got.SmoothedGroundSpeed != 460 {
			t.Errorf("speed spike was not smoothed: %v", *got.SmoothedGroundSpeed)
		}
	}
	// Even with a toward-destination track, sustained position movement away
	// from the destination must stop the estimate.
	a := observation(now.Add(90 * time.Second))
	a.Longitude = ptr(-5.5)
	away := estimator.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
	if away.Status != "unavailable" || !strings.Contains(away.Reason, "moving away") {
		t.Fatalf("expected movement-based rejection: %+v", away)
	}
	// A different aircraft must not inherit the previous aircraft's history.
	a.Hex = "def002"
	replacement := estimator.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
	if replacement.Status != "estimated" {
		t.Fatalf("history leaked across aircraft: %+v", replacement)
	}
}

func TestHoldingAndTracklessProgress(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var estimator Estimator
	var last *flight.LandingEstimate
	// A small circuit about 60 nm north of the airport, with no net progress.
	for i, coordinate := range [][2]float64{{1, -0.03}, {1.03, 0}, {1, 0.03}, {0.97, 0}, {1, -0.03}} {
		a := observation(now.Add(time.Duration(i) * 30 * time.Second))
		a.Latitude, a.Longitude, a.Track = ptr(coordinate[0]), ptr(coordinate[1]), nil
		last = estimator.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
	}
	if last.TouchdownAt != nil || !strings.Contains(last.Reason, "holding or vectoring") {
		t.Fatalf("expected holding detection, got %+v", last)
	}
	var straight Estimator
	for i := 0; i < 2; i++ {
		a := observation(now.Add(time.Duration(i) * 30 * time.Second))
		a.Track, a.Longitude = nil, ptr(-5+float64(i)*0.06)
		last = straight.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
	}
	if last.Status != "estimated" {
		t.Fatalf("position history should support direction when track is missing: %+v", last)
	}
}

func TestAirportElevationAndAltitudeTimeBudget(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := observation(now)
	a.Longitude, a.AltitudeFeet, a.GroundSpeed = ptr(-0.3), ptr(7000.0), ptr(140.0)
	var sea, elevated Estimator
	low := sea.Update(now, destination(), []flight.Aircraft{a})
	route := destination()
	route.Destination.ElevationFeet = ptr(5000.0)
	high := elevated.Update(now, route, []flight.Aircraft{a})
	if low.Phase != "approach" || high.Phase != "approach" {
		t.Fatal("both observations should be within approach thresholds")
	}
	// At short distance, high altitude should increase the time budget, while
	// elevation must be subtracted instead of treating sea-level altitude as AGL.
	a.Longitude = ptr(-0.01)
	sea, elevated = Estimator{}, Estimator{}
	low = sea.Update(now, destination(), []flight.Aircraft{a})
	high = elevated.Update(now, route, []flight.Aircraft{a})
	if low.MinutesRemaining == nil || high.MinutesRemaining == nil || *low.MinutesRemaining <= *high.MinutesRemaining+2 {
		t.Fatalf("airport elevation was not included: sea=%+v, elevated=%+v", low, high)
	}
}

func TestGreatCircleAcrossDateLine(t *testing.T) {
	distance := geo.DistanceNM(0, 179, 0, -179)
	if math.Abs(distance-120.08) > 0.1 {
		t.Errorf("incorrect date-line distance: %.2f nm", distance)
	}
}

func TestCallsignChangesDoNotResetAircraftSpeedHistory(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var estimator Estimator
	var last *flight.LandingEstimate
	for i, speed := range []float64{450, 460, 700} {
		a := observation(now.Add(time.Duration(i) * 30 * time.Second))
		a.Longitude, a.GroundSpeed = ptr(-5+float64(i)*0.06), ptr(speed)
		a.Callsign = []string{"TEST123", "OTHER456", ""}[i]
		last = estimator.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
	}
	if last.Status != "estimated" || last.SmoothedGroundSpeed == nil || *last.SmoothedGroundSpeed != 460 {
		t.Fatalf("callsign changes reset the same aircraft's observation history: %+v", last)
	}
}
