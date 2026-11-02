package tracker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func routePtr[T any](value T) *T { return &value }

func dynamicRoute() *flight.Route {
	return &flight.Route{Callsign: "SAS123", Destination: flight.Airport{IATA: "AAA", Latitude: routePtr(0.0), Longitude: routePtr(0.0), ElevationFeet: routePtr(0.0)}}
}

func TestReferenceNotificationsDoNotPollRefreshFixesOrAddSurfaceEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	now := time.Now().UTC()
	route := dynamicRoute()
	source := lookupFunc(func(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
		calls.Add(1)
		return []flight.Aircraft{{Hex: "abc001", Callsign: "SAS123", Latitude: routePtr(0.0), Longitude: routePtr(0.001), OnGround: routePtr(true), GroundSpeed: routePtr(10.0), PositionUpdated: &now, LastContact: &now}}, nil
	})
	changed := make(chan struct{}, 1)
	var snapshots []flight.Snapshot
	err := Run(ctx, source, Options{Target: flight.Target{Kind: flight.ByHex, Value: "abc001"}, Interval: time.Hour,
		Reference: func() *flight.Route { copy := *route; return &copy }, ReferenceChanged: changed}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) < 4 {
			changed <- struct{}{}
		} else {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 || len(snapshots) != 4 {
		t.Fatalf("notifications changed request count: calls=%d snapshots=%d err=%v", calls.Load(), len(snapshots), err)
	}
	for i, s := range snapshots {
		if !s.CheckedAt.Equal(snapshots[0].CheckedAt) || !s.Aircraft[0].PositionUpdated.Equal(now) || s.Arrival.SurfaceReports != 1 || s.Arrival.Status != "on_ground_near_destination" {
			t.Fatalf("metadata update manufactured evidence: %+v", s)
		}
		if s.Tracking.LiveLookupPerformed != (i == 0) {
			t.Fatal("metadata/live lookup attribution wrong")
		}
	}
}

func TestDestinationChangeResetsInferenceButPreservesHexLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := dynamicRoute()
	second := dynamicRoute()
	second.Destination.IATA, second.Destination.Latitude = "BBB", routePtr(1.0)
	reference := first
	base := time.Now().UTC().Add(-40 * time.Second)
	index := -1
	source := lookupFunc(func(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
		index++
		if target.Kind != flight.ByHex || target.Value != "abc001" {
			t.Errorf("identity changed: %+v", target)
		}
		if index == 3 {
			reference = second
		}
		if index == 4 {
			return nil, errors.New("temporary outage")
		}
		at := base.Add(time.Duration(index*5) * time.Second)
		a := flight.Aircraft{Hex: "abc001", Callsign: "SAS123", Latitude: routePtr(0.0), Longitude: routePtr(0.001), OnGround: routePtr(true), GroundSpeed: routePtr(56.0), PositionUpdated: &at, LastContact: &at}
		if index >= 5 {
			a.Latitude = routePtr(1.0)
		}
		if index == 0 || index == 5 {
			a.OnGround, a.Longitude, a.AltitudeFeet, a.GroundSpeed, a.Track = routePtr(false), routePtr(0.02), routePtr(300.0), routePtr(125.0), routePtr(270.0)
		}
		return []flight.Aircraft{a}, nil
	})
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Target: flight.Target{Kind: flight.ByHex, Value: "abc001"}, Interval: time.Nanosecond, Reference: func() *flight.Route { copy := *reference; return &copy }}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 8 {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(index*5) * time.Second) })
	if !errors.Is(err, context.Canceled) || len(snapshots) != 8 {
		t.Fatal(err)
	}
	for i, want := range []string{"airborne", "possible_landing", "likely_landed", "unknown", "unknown", "airborne", "possible_landing", "likely_landed"} {
		s := snapshots[i]
		if s.Arrival.Status != want {
			t.Fatalf("snapshot %d: got %+v want %s", i, s.Arrival, want)
		}
		if s.Tracking.LockedHex != "abc001" || !s.Tracking.LockedAt.Equal(*snapshots[0].Tracking.LockedAt) {
			t.Fatal("route change restarted hex acquisition")
		}
		if i >= 3 && s.Destination.Airport.IATA != "BBB" {
			t.Fatal("new destination not selected")
		}
	}
	if snapshots[3].Arrival.SurfaceReports != 0 || snapshots[4].Arrival.Historical {
		t.Fatal("old airport evidence leaked after destination change")
	}
}

func TestNewBroadcastCallsignImmediatelyInvalidatesOldReference(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reference := dynamicRoute()
	index := -1
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		index++
		call := "SAS123"
		if index > 0 {
			call = "NEW123"
		}
		if index == 2 {
			copy := *reference
			copy.Callsign = call
			reference = &copy
		}
		now := time.Now().UTC()
		return []flight.Aircraft{{Hex: "abc001", Callsign: call, Latitude: routePtr(0.0), Longitude: routePtr(0.1), OnGround: routePtr(false), PositionUpdated: &now, LastContact: &now}}, nil
	})
	var snapshots []flight.Snapshot
	err := Run(ctx, source, Options{Target: flight.Target{Kind: flight.ByHex, Value: "abc001"}, Interval: time.Nanosecond, Reference: func() *flight.Route { return reference }}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if snapshots[0].Destination == nil || snapshots[1].Destination != nil || snapshots[1].Route != nil || snapshots[2].Destination == nil {
		t.Fatal("stale callsign route was applied to the next flight")
	}
}

func TestUnresolvedHistoryDoesNotCrossIntoAnotherCallsign(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reference *flight.Route
	index := -1
	base := time.Now().UTC().Add(-20 * time.Second)
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		index++
		at := base.Add(time.Duration(index*5) * time.Second)
		a := flight.Aircraft{Hex: "abc001", Callsign: "OLD123", Latitude: routePtr(0.0), Longitude: routePtr(0.001), OnGround: routePtr(index > 0), PositionUpdated: &at, LastContact: &at, GroundSpeed: routePtr(20.0)}
		if index == 0 {
			a.Longitude, a.AltitudeFeet = routePtr(0.02), routePtr(300.0)
		}
		if index == 2 {
			a.Callsign = "NEW123"
			reference = dynamicRoute()
			reference.Callsign = a.Callsign
		}
		return []flight.Aircraft{a}, nil
	})
	var final flight.Snapshot
	err := run(ctx, source, Options{Target: flight.Target{Kind: flight.ByHex, Value: "abc001"}, Interval: time.Nanosecond, Reference: func() *flight.Route { return reference }}, func(s flight.Snapshot) error {
		final = s
		if index == 2 {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(index*5) * time.Second) })
	if !errors.Is(err, context.Canceled) || final.Arrival.Status != "on_ground_near_destination" || final.Arrival.SurfaceReports != 1 {
		t.Fatalf("old callsign's unbound approach/ground evidence leaked: %+v err=%v", final.Arrival, err)
	}
}

func TestAsyncReferenceChangeDoesNotClearSubsequentDepartureGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reference := dynamicRoute()
	index := -1
	base := time.Now().UTC()
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		index++
		at := base.Add(time.Duration(index) * time.Minute)
		a := flight.Aircraft{Hex: "abc001", Callsign: "SAS123", Latitude: routePtr(0.0), Longitude: routePtr(0.001), OnGround: routePtr(index > 0 && index < 12), PositionUpdated: &at, LastContact: &at, GroundSpeed: routePtr(20.0)}
		if index == 0 || index >= 12 {
			a.Longitude, a.AltitudeFeet, a.GroundSpeed, a.Track = routePtr(0.02), routePtr(300.0), routePtr(125.0), routePtr(270.0)
		}
		if index == 13 {
			reference = dynamicRoute()
			reference.Callsign = "NEW123"
			reference.Destination.Latitude = routePtr(1.0)
			a.Callsign = "NEW123"
			a.Latitude = routePtr(1.0)
		}
		return []flight.Aircraft{a}, nil
	})
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Target: flight.Target{Kind: flight.ByHex, Value: "abc001"}, Interval: time.Nanosecond, Reference: func() *flight.Route { return reference }}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if index == 13 {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(index) * time.Minute) })
	if !errors.Is(err, context.Canceled) || len(snapshots) != 14 {
		t.Fatal(err)
	}
	for _, i := range []int{12, 13} {
		if snapshots[i].Tracking.FlightState != "subsequent_departure" || snapshots[i].LandingEstimate.Status != "unavailable" {
			t.Fatalf("reference update bypassed the next-leg guard: %+v", snapshots[i])
		}
	}
}
