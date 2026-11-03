package nearby

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
)

func ptr[T any](value T) *T { return &value }

func aircraft(hex string, lat, lon float64, at time.Time) flight.Aircraft {
	return flight.Aircraft{Hex: hex, Latitude: &lat, Longitude: &lon, PositionUpdated: &at, LastContact: &at}
}

func TestRowsFreshnessRadiusDeduplicationAndOrdering(t *testing.T) {
	now := time.Now().UTC()
	items := []flight.Aircraft{
		aircraft("abc002", 0.2, 0, now), aircraft("ABC001", 0.1, 0, now),
		aircraft("abc001", 0.3, 0, now.Add(-time.Second)),
		aircraft("abc003", 1, 0, now), // outside 25 nm
		aircraft("abc004", 0.1, 0, now.Add(-91*time.Second)),
		aircraft("abc005", 0.1, 0, now.Add(6*time.Second)),
		aircraft("~abc06", 0.1, 0, now),
		aircraft("abc007", math.NaN(), 0, now),
		{Hex: "abc008", Latitude: ptr(0.0), Longitude: ptr(0.0)},
		aircraft("abc010", 0.1, 0, now.Add(-time.Second)),
		aircraft("abc010", 1, 0, now), // Newest fix outside radius supersedes old inside fix.
	}
	rows := Rows(items, flight.Location{}, 25, now)
	if len(rows) != 2 || rows[0].Hex != "abc001" || rows[1].Hex != "abc002" {
		t.Fatalf("incorrect current unique distance order: %+v", rows)
	}
	if math.Abs(rows[0].DistanceNM-6.004) > 0.01 || rows[0].BearingDegrees != 0 || rows[0].Direction != "N" {
		t.Fatalf("incorrect observer-relative geometry: %+v", rows[0])
	}
	if Fresh(rows[0].Aircraft, now.Add(91*time.Second)) {
		t.Fatal("screen ticks must expire old fixes")
	}
	a := aircraft("abc009", 0, 0, now)
	a.LastContact = ptr(now.Add(-91 * time.Second))
	if Fresh(a, now) {
		t.Fatal("stale contact cannot establish a current aircraft")
	}
}

func TestDatelineAndCompassBearings(t *testing.T) {
	now := time.Now()
	rows := Rows([]flight.Aircraft{aircraft("abc001", 0, -179.9, now)}, flight.Location{Longitude: 179.9}, 25, now)
	if len(rows) != 1 || rows[0].Direction != "E" || math.Abs(rows[0].DistanceNM-12.008) > 0.01 {
		t.Fatalf("dateline geometry incorrect: %+v", rows)
	}
	for bearing, want := range map[float64]string{0: "N", 22.5: "NE", 90: "E", 180: "S", 270: "W", 359: "N"} {
		if got := Compass(bearing); got != want {
			t.Errorf("bearing %.1f: %s, want %s", bearing, got, want)
		}
	}
}

type fakeSource struct {
	lookup func(context.Context) ([]flight.Aircraft, error)
}

func (s fakeSource) Name() string { return "fixture" }
func (s fakeSource) LookupNearby(ctx context.Context, _, _ float64, _ int) ([]flight.Aircraft, error) {
	return s.lookup(ctx)
}

func TestPollingRetainsOnlyOnErrorsAndHonoursRetryAfter(t *testing.T) {
	calls := 0
	fatal := &provider.HTTPError{StatusCode: http.StatusForbidden}
	source := fakeSource{lookup: func(context.Context) ([]flight.Aircraft, error) {
		calls++
		switch calls {
		case 1:
			return []flight.Aircraft{aircraft("abc001", 0, 0, time.Now())}, nil
		case 2:
			return nil, &provider.HTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 20 * time.Second}
		case 3:
			return nil, nil
		default:
			return nil, fatal
		}
	}}
	var snapshots []flight.NearbySnapshot
	var delays []time.Duration
	err := run(context.Background(), source, Options{RadiusNM: 25, Interval: 5 * time.Second}, func(s flight.NearbySnapshot) error {
		snapshots = append(snapshots, s)
		return nil
	}, func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil })
	if !errors.Is(err, fatal) || len(snapshots) != 4 || len(delays) != 3 || delays[1] != 20*time.Second || delays[2] != 5*time.Second {
		t.Fatalf("retry/fatal handling: %v, %+v, %v", err, snapshots, delays)
	}
	if snapshots[1].Status != "retrying" || len(snapshots[1].Aircraft) != 0 || len(snapshots[1].LastObserved) != 1 || snapshots[1].RetryAt == nil {
		t.Fatalf("failed request confused current/historical: %+v", snapshots[1])
	}
	if snapshots[2].Status != "empty" || len(snapshots[3].LastObserved) != 0 {
		t.Fatal("successful empty response did not clear previous results")
	}
}

func TestPollingCancellationAndOutputErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := fakeSource{lookup: func(context.Context) ([]flight.Aircraft, error) { return nil, nil }}
	err := Run(ctx, source, Options{RadiusNM: 25, Interval: 5 * time.Second}, func(flight.NearbySnapshot) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	want := errors.New("output closed")
	err = Run(context.Background(), source, Options{RadiusNM: 25, Interval: 5 * time.Second}, func(flight.NearbySnapshot) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("output error lost: %v", err)
	}
}
