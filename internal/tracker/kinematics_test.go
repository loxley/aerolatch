package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func measurementFix(at time.Time, lon, altitude float64) flight.Aircraft {
	return flight.Aircraft{Hex: "abc001", Callsign: "TEST123", Latitude: p(0.0), Longitude: p(lon), AltitudeFeet: p(altitude), OnGround: p(false), PositionUpdated: &at, LastContact: &at}
}

func TestMissingMeasurementsRecoverETAAndApproachWithoutChangingRawValues(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		i := calls
		calls++
		at := base.Add(time.Duration(i) * 30 * time.Second)
		a := measurementFix(at, -0.1+float64(i)*0.02, 2000-float64(i)*500)
		if i == 2 {
			a.Callsign = "OTHER123"
		} // Callsign changes do not sever hex history.
		return []flight.Aircraft{a}, nil
	})
	var snapshots []flight.Snapshot
	dest := &flight.Airport{Latitude: p(0.0), Longitude: p(0.0), ElevationFeet: p(0.0)}
	err := run(ctx, source, Options{Callsign: "TEST123", Destination: dest, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 4 {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(calls-1) * 30 * time.Second) })
	if !errors.Is(err, context.Canceled) || calls != 4 {
		t.Fatalf("unexpected polling: calls=%d err=%v", calls, err)
	}
	if snapshots[0].LandingEstimate.Status != "unavailable" || snapshots[1].LandingEstimate.Status != "estimated" {
		t.Fatal("ETA did not recover once speed and track could be calculated")
	}
	last := snapshots[3]
	a := last.Aircraft[0]
	if a.GroundSpeed != nil || a.Track != nil || a.VerticalRate != nil || a.Calculated == nil || a.Calculated.VerticalRate == nil {
		t.Fatal("raw measurements were overwritten or missing calculations not recovered")
	}
	if len(last.LandingEstimate.CalculatedInputs) != 3 || last.Arrival.Approach == nil || last.Arrival.Approach.SpeedSource != "calculated" || len(last.Arrival.Approach.CalculatedInputs) != 3 {
		t.Fatalf("calculated input provenance missing: ETA=%+v approach=%+v", last.LandingEstimate, last.Arrival.Approach)
	}
	encoded, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"ground_speed_kt":null`) || !strings.Contains(string(encoded), `"calculated_inputs"`) {
		t.Fatal("JSON did not separate raw data and calculation provenance")
	}
}

func TestTrackerErrorBreaksCalculationHistoryAndKeepsHistoricalProvenance(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		i := calls
		calls++
		if i == 2 {
			return nil, errors.New("temporary network error")
		}
		return []flight.Aircraft{measurementFix(base.Add(time.Duration(i)*30*time.Second), -0.1+float64(i)*0.02, 2000-float64(i)*200)}, nil
	})
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Callsign: "TEST123", Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 5 {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(calls-1) * 30 * time.Second) })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if snapshots[1].Aircraft[0].EffectiveGroundSpeed() == nil {
		t.Fatal("pre-error calculation missing")
	}
	retained := snapshots[2].LastObserved[0].Calculated.GroundSpeed
	if len(snapshots[2].Aircraft) != 0 || !retained.WindowEnd.Equal(base.Add(30*time.Second)) {
		t.Fatal("error response fabricated new measurement freshness")
	}
	if snapshots[3].Aircraft[0].EffectiveGroundSpeed() != nil || snapshots[4].Aircraft[0].EffectiveGroundSpeed() == nil {
		t.Fatal("recovery did not require two new distinct fixes")
	}
}
