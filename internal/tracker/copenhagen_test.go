package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func TestCopenhagenArrivalRetainedAfterFiveSecondGroundCoverage(t *testing.T) {
	base := time.Date(2026, 10, 19, 21, 26, 1, 0, time.UTC)
	airport := &flight.Airport{IATA: "CPH", Latitude: p(55.6179), Longitude: p(12.656), ElevationFeet: p(17.0)}
	polls := []int{-41, -11, 4, 34, 58, 94, 524}
	fix := func(offset int, ground bool, lat, lon, altitude, speed float64) flight.Aircraft {
		at := base.Add(time.Duration(offset) * time.Second)
		a := flight.Aircraft{Hex: "4787a0", Callsign: "NSZ3235", Registration: "LN-NIB", OnGround: p(ground), Latitude: p(lat), Longitude: p(lon), AltitudeFeet: p(altitude), GroundSpeed: p(speed), PositionUpdated: &at, LastContact: &at}
		if ground {
			a.AltitudeFeet = nil
		}
		return a
	}
	frames := []flight.Aircraft{
		fix(-41, false, 55.57, 12.55, 700, 140), fix(-11, false, 55.596, 12.602, 200, 130),
		fix(0, true, 55.6049, 12.6232, 0, 56), fix(5, true, 55.60594, 12.62481, 0, 56),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	source := lookupFunc(func(_ context.Context, target flight.Target) ([]flight.Aircraft, error) {
		i := calls
		calls++
		if i > 0 && (target.Kind != flight.ByHex || target.Value != "4787a0") {
			t.Errorf("lost aircraft identity: %+v", target)
		}
		if i >= 5 {
			return nil, nil
		}
		if i == 4 {
			i = 3
		}
		return []flight.Aircraft{frames[i]}, nil
	})
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Callsign: "NSZ3235", Destination: airport, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == len(polls) {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(polls[calls-1]) * time.Second) })
	if !errors.Is(err, context.Canceled) || len(snapshots) != len(polls) {
		t.Fatalf("snapshots=%d error=%v", len(snapshots), err)
	}
	for i, want := range []string{"airborne", "airborne", "possible_landing", "likely_landed", "likely_landed", "likely_landed", "likely_landed"} {
		a := snapshots[i].Arrival
		if a == nil || a.Status != want {
			t.Fatalf("frame %d: want %s, got %+v", i, want, a)
		}
		if i >= 2 && a.Approach == nil {
			t.Errorf("frame %d discarded the preceding approach evidence", i)
		}
		if i >= 3 && (a.SurfaceReports != 2 || a.LastSurfaceAt == nil || !a.LastSurfaceAt.Equal(base.Add(5*time.Second))) {
			t.Fatalf("frame %d lost or invented surface reports: %+v", i, a)
		}
		if a.Historical != (i >= 5) {
			t.Errorf("frame %d incorrectly labels retained evidence", i)
		}
	}
	final := snapshots[len(snapshots)-1]
	if final.Status != "not_visible" || len(final.Aircraft) != 0 || len(final.LastObserved) != 1 {
		t.Fatal("historical landing evidence became current aircraft telemetry")
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	var decoded flight.Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Arrival.SurfaceReports != 2 || decoded.Arrival.LastSurfaceAt == nil || decoded.Arrival.Status != "likely_landed" || !decoded.Arrival.Historical {
		t.Fatalf("JSON lost the landing evidence: %s", encoded)
	}
}
