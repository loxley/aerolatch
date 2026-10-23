package tracker

import (
	"context"
	"errors"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
)

type lookupFunc func(context.Context, flight.Target) ([]flight.Aircraft, error)

func (f lookupFunc) Name() string { return "Test feed" }

func (f lookupFunc) Lookup(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
	return f(ctx, target)
}

func TestTrackingVisibilityGapsAndRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	source := lookupFunc(func(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
		if target.Value != "NSZ5373" || target.Kind != flight.ByCallsign {
			t.Errorf("wrong callsign target: %+v", target)
		}
		count++
		switch count {
		case 1:
			return []flight.Aircraft{{Hex: "abc001"}}, nil
		case 2:
			return nil, nil
		case 3, 4:
			return nil, errors.New("temporary network failure")
		default:
			return []flight.Aircraft{{Hex: "abc001"}, {Hex: "abc002"}}, nil
		}
	})
	var snapshots []flight.Snapshot
	err := Run(ctx, source, Options{Callsign: "NSZ5373", Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 5 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	want := []string{"visible", "not_visible", "retrying", "retrying", "multiple_matches"}
	if len(snapshots) != len(want) {
		t.Fatalf("expected five snapshots, got %d", len(snapshots))
	}
	for i, status := range want {
		if snapshots[i].Status != status {
			t.Errorf("snapshot %d: got %s, want %s", i, snapshots[i].Status, status)
		}
	}
	for _, i := range []int{1, 2, 3} {
		if len(snapshots[i].Aircraft) != 0 || len(snapshots[i].LastObserved) != 1 || snapshots[i].LastSeenAt == nil {
			t.Errorf("snapshot %d did not separate last observation from current data: %+v", i, snapshots[i])
		}
	}
	for i, want := range map[int]time.Duration{2: time.Nanosecond, 3: 2 * time.Nanosecond} {
		if got := snapshots[i].RetryAt.Sub(snapshots[i].CheckedAt); got != want {
			t.Errorf("snapshot %d backoff = %s, want %s", i, got, want)
		}
	}
	if len(snapshots[4].LastObserved) != 0 {
		t.Error("recovered snapshot should not include old observations")
	}
}

func TestRateLimitDelayAndCancelWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		calls++
		return nil, &provider.HTTPError{StatusCode: 429, RetryAfter: time.Hour}
	})
	err := Run(ctx, source, Options{Interval: 30 * time.Second}, func(s flight.Snapshot) error {
		if s.Status != "retrying" || s.RetryAt.Sub(s.CheckedAt) != time.Hour {
			t.Errorf("rate limit not honored: %+v", s)
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("waiting must stop immediately on cancellation: calls=%d, error=%v", calls, err)
	}
}

func TestOnceAndFatalFailures(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		once         bool
		lookupErr    error
	}{
		{"no match", "not_visible", true, nil},
		{"once network error", "error", true, errors.New("network down")},
		{"forbidden", "error", false, &provider.HTTPError{StatusCode: 403}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, emissions := 0, 0
			source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
				calls++
				return nil, tc.lookupErr
			})
			err := Run(context.Background(), source, Options{Once: tc.once}, func(s flight.Snapshot) error {
				emissions++
				if s.Aircraft == nil {
					t.Error("empty current aircraft must serialize as [], not null")
				}
				if s.Status != tc.status {
					t.Errorf("status = %s, want %s", s.Status, tc.status)
				}
				return nil
			})
			if !errors.Is(err, tc.lookupErr) || calls != 1 || emissions != 1 {
				t.Fatalf("calls=%d emissions=%d error=%v", calls, emissions, err)
			}
		})
	}
}

func TestOutputFailureStopsPolling(t *testing.T) {
	want := errors.New("broken pipe")
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) { return nil, nil })
	err := Run(context.Background(), source, Options{}, func(flight.Snapshot) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want output error", err)
	}
}

func TestEstimatesNotRetainedDuringOutages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	zero, lon, altitude, speed, track := 0.0, -5.0, 35000.0, 450.0, 90.0
	route := &flight.Route{Destination: flight.Airport{Latitude: &zero, Longitude: &zero, ElevationFeet: &zero}}
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		calls++
		if calls == 2 {
			return nil, nil
		}
		if calls == 3 {
			return nil, errors.New("network down")
		}
		now := time.Now().UTC()
		return []flight.Aircraft{{Hex: "abc001", Latitude: &zero, Longitude: &lon, AltitudeFeet: &altitude, GroundSpeed: &speed, Track: &track, PositionUpdated: &now}}, nil
	})
	emissions := 0
	err := Run(ctx, source, Options{Route: route, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		emissions++
		if s.LandingEstimate == nil {
			t.Fatal("missing landing estimate status")
		}
		if emissions == 1 && s.LandingEstimate.TouchdownAt == nil {
			t.Errorf("expected initial estimate: %+v", s.LandingEstimate)
		}
		if emissions > 1 && (s.LandingEstimate.Status != "unavailable" || s.LandingEstimate.TouchdownAt != nil) {
			t.Errorf("old ETA must not survive an outage: %+v", s.LandingEstimate)
		}
		if emissions == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || emissions != 3 {
		t.Fatalf("unexpected polling result: emissions=%d, error=%v", emissions, err)
	}
}

func TestArrivalInferenceSurvivesGapsAndResetsOnAirborne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := time.Now().UTC()
	zero, far, near, altitude := 0.0, 50.0, 0.2, 3000.0
	airport := &flight.Airport{IATA: "ALC", Latitude: &zero, Longitude: &zero, ElevationFeet: &zero}
	reference := &flight.Route{Destination: flight.Airport{IATA: "EDI", Latitude: &far, Longitude: &zero, ElevationFeet: &zero}}
	calls := 0
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		calls++
		if calls == 4 {
			return nil, nil
		}
		if calls == 5 {
			return nil, errors.New("network failed")
		}
		at, ground := base, true
		if calls == 1 {
			at, ground = base.Add(-60*time.Second), false
		} else if calls == 2 {
			at = base.Add(-30 * time.Second)
		} else if calls == 6 {
			at, ground = base.Add(time.Second), false
		}
		a := flight.Aircraft{Hex: "4cae57", OnGround: &ground, Latitude: &zero, Longitude: &zero, PositionUpdated: &at, LastContact: &at}
		if !ground {
			a.Longitude, a.AltitudeFeet = &near, &altitude
		}
		return []flight.Aircraft{a}, nil
	})
	var snapshots []flight.Snapshot
	err := Run(ctx, source, Options{Route: reference, Destination: airport, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 6 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(snapshots) != 6 {
		t.Fatalf("unexpected result: error=%v, snapshots=%d", err, len(snapshots))
	}
	for i, status := range []string{"airborne", "possible_landing", "likely_landed", "likely_landed", "likely_landed", "airborne"} {
		s := snapshots[i]
		if s.Arrival == nil || s.Arrival.Status != status {
			t.Errorf("snapshot %d: expected %s, got %+v", i, status, s.Arrival)
		}
		if s.Arrival != nil && s.Arrival.Historical != (i == 3 || i == 4) {
			t.Errorf("snapshot %d: incorrect historical label", i)
		}
		if s.Destination == nil || s.Destination.Airport.IATA != "ALC" || s.Route.Destination.IATA != "EDI" {
			t.Errorf("snapshot %d: selected/reference destination mismatch", i)
		}
	}
	if reference.Destination.IATA != "EDI" {
		t.Fatal("reference route was mutated by the override")
	}
}

func TestApproachFallbackThroughTrackerAndRequestFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Date(2026, 10, 17, 21, 9, 29, 0, time.UTC)
	lat, lon, elevation := 59.6519, 17.9186, 137.0
	airport := &flight.Airport{IATA: "ARN", Latitude: &lat, Longitude: &lon, ElevationFeet: &elevation}
	var fixes []flight.Aircraft
	for i, s := range [][4]float64{
		{59.60828, 17.90298, 300, 125},
		{59.62550, 17.90909, -75, 126},
		{59.63100, 17.91103, -175, 132},
	} {
		at := start.Add([]time.Duration{0, 30 * time.Second, 40 * time.Second}[i])
		ground, track, rate := false, 10.0, -640.0
		fixes = append(fixes, flight.Aircraft{Hex: "4aca87", OnGround: &ground,
			Latitude: &s[0], Longitude: &s[1], AltitudeFeet: &s[2], GroundSpeed: &s[3],
			Track: &track, VerticalRate: &rate, PositionUpdated: &at, LastContact: &at})
	}
	pollSeconds := []int{2, 32, 62, 92, 122, 152, 182, 212, 242, 272, 302}
	calls := 0
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		index := calls
		calls++
		switch {
		case index < 3:
			return fixes[index : index+1], nil
		case index == 3:
			return fixes[2:], nil // Cached, not another approach observation.
		case index == 7:
			return nil, errors.New("provider timeout")
		case index == 10:
			a := fixes[2]
			at, height, climb := start.Add(302*time.Second), 2500.0, 1500.0
			a.PositionUpdated, a.LastContact, a.AltitudeFeet, a.VerticalRate = &at, &at, &height, &climb
			return []flight.Aircraft{a}, nil
		default:
			return nil, nil
		}
	})
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Destination: airport, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == len(pollSeconds) {
			cancel()
		}
		return nil
	}, func() time.Time { return start.Add(time.Duration(pollSeconds[calls-1]) * time.Second) })
	if !errors.Is(err, context.Canceled) || len(snapshots) != len(pollSeconds) {
		t.Fatalf("unexpected tracker result: snapshots=%d, error=%v", len(snapshots), err)
	}
	for i, status := range []string{"airborne", "airborne", "airborne", "airborne", "unknown", "unknown", "possible_landing", "possible_landing", "possible_landing", "possible_landing", "airborne"} {
		s := snapshots[i]
		if s.Arrival == nil || s.Arrival.Status != status {
			t.Fatalf("snapshot %d: expected %s, got %+v", i, status, s.Arrival)
		}
		if s.Arrival.Historical != (i == 7 || i == 8) {
			t.Errorf("snapshot %d: incorrect retained-assessment label", i)
		}
	}
	possible := snapshots[6]
	if possible.Status != "not_visible" || len(possible.Aircraft) != 0 || possible.Arrival.Approach == nil || possible.Arrival.Approach.ObservationCount != 3 || possible.Arrival.EmptyResponses != 3 {
		t.Fatalf("fallback must preserve the separation from live positions: %+v", possible)
	}
	if possible.Arrival.AssessedAt == nil || !possible.Arrival.AssessedAt.Equal(start.Add(182*time.Second)) {
		t.Fatal("Arlanda should first be assessed at 21:12:31 UTC")
	}
	if snapshots[7].Status != "retrying" || snapshots[9].Arrival.AssessedAt == nil || !snapshots[9].Arrival.AssessedAt.Equal(*possible.Arrival.AssessedAt) {
		t.Fatal("request failure advanced or replaced the original inference")
	}
}
