package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
)

func p[T any](value T) *T { return &value }

func TestCallsignLocksHexAndKeepsSurfaceArrivalEvidence(t *testing.T) {
	base := time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	paths := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(requests.Add(1))
		paths <- r.URL.Path
		if n == 5 {
			w.WriteHeader(503)
			return
		}
		now := base.Add(time.Duration(n-1) * 30 * time.Second)
		if n == 4 {
			fmt.Fprintf(w, `{"now":%d,"ac":[]}`, now.UnixMilli())
			return
		}
		altitude, callsign := `"ground"`, `""`
		if n == 1 {
			altitude, callsign = `1500`, `"NSZ4366"`
		}
		if n == 3 {
			callsign = `"NSZ9999"`
		}
		fmt.Fprintf(w, `{"now":%d,"ac":[{"hex":"4aca87","flight":%s,"r":"SE-RTG","alt_baro":%s,"lat":0,"lon":0,"gs":20,"seen":0,"seen_pos":0}]}`, now.UnixMilli(), callsign, altitude)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var snapshots []flight.Snapshot
	airport := &flight.Airport{Latitude: p(0.0), Longitude: p(0.0), ElevationFeet: p(0.0)}
	err := run(ctx, provider.Live{Client: server.Client(), BaseURL: server.URL}, Options{Callsign: "NSZ4366", Destination: airport, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == 6 {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(requests.Load()-1) * 30 * time.Second) })
	if !errors.Is(err, context.Canceled) || len(snapshots) != 6 {
		t.Fatalf("snapshots=%d error=%v", len(snapshots), err)
	}
	for n := 0; n < 6; n++ {
		want := "/v2/hex/4aca87"
		if n == 0 {
			want = "/v2/callsign/NSZ4366"
		}
		if path := <-paths; path != want {
			t.Errorf("request %d: %s, want %s", n, path, want)
		}
		s := snapshots[n]
		if s.Tracking == nil || s.Tracking.LockedHex != "4aca87" || s.Tracking.LockedAt == nil || !s.Tracking.LockedAt.Equal(base) {
			t.Fatalf("identity lock missing or moved: %+v", s.Tracking)
		}
		if s.Callsign != "NSZ4366" {
			t.Errorf("original flight callsign changed: %s", s.Callsign)
		}
	}
	if snapshots[1].Aircraft[0].Callsign != "" || snapshots[2].Aircraft[0].Callsign != "NSZ9999" {
		t.Fatal("broadcast callsign was discarded or replaced with the old flight callsign")
	}
	if snapshots[2].Arrival.Status != "likely_landed" {
		t.Fatalf("surface evidence was lost on callsign changes: %+v", snapshots[2].Arrival)
	}
	if snapshots[3].Status != "not_visible" || snapshots[4].Status != "retrying" || !snapshots[4].Arrival.Historical {
		t.Fatal("gaps/errors must preserve the locked aircraft and previous arrival evidence")
	}
}

func TestAcquisitionRequiresUniqueRecentIdentity(t *testing.T) {
	for _, first := range []string{"ambiguous", "stale", "invalid hex"} {
		t.Run(first, func(t *testing.T) {
			base := time.Now().UTC()
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := lookupFunc(func(_ context.Context, target flight.Target) ([]flight.Aircraft, error) {
				calls++
				kind := flight.ByCallsign
				if calls == 3 {
					kind = flight.ByHex
				}
				if target.Kind != kind {
					t.Errorf("request %d used %s, want %s", calls, target.Kind, kind)
				}
				a := flight.Aircraft{Hex: "4aca87", Callsign: "NSZ4366", LastContact: &base}
				if calls == 1 {
					switch first {
					case "ambiguous":
						return []flight.Aircraft{a, {Hex: "abc001", Callsign: "NSZ4366", LastContact: &base}}, nil
					case "stale":
						a.LastContact = p(base.Add(-2 * time.Minute))
					case "invalid hex":
						a.Hex = "~4aca87"
					}
				}
				return []flight.Aircraft{a}, nil
			})
			var snapshots []flight.Snapshot
			err := run(ctx, source, Options{Callsign: "NSZ4366", Interval: time.Nanosecond}, func(s flight.Snapshot) error {
				snapshots = append(snapshots, s)
				if len(snapshots) == 3 {
					cancel()
				}
				return nil
			}, func() time.Time { return base })
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if snapshots[0].Tracking.LockedHex != "" || snapshots[1].Tracking.LockedHex != "4aca87" {
				t.Fatal("unsafe or missing identity acquisition")
			}
		})
	}
}

func TestRegistrationAndDirectHexAcquisition(t *testing.T) {
	for _, kind := range []flight.TargetKind{flight.ByRegistration, flight.ByHex} {
		t.Run(string(kind), func(t *testing.T) {
			value := "SE-RTG"
			if kind == flight.ByHex {
				value = "4aca87"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := time.Now().UTC()
			calls := 0
			source := lookupFunc(func(_ context.Context, target flight.Target) ([]flight.Aircraft, error) {
				calls++
				want := kind
				if calls > 1 {
					want = flight.ByHex
				}
				if target.Kind != want {
					t.Errorf("lookup %d: %s, want %s", calls, target.Kind, want)
				}
				return []flight.Aircraft{{Hex: "4aca87", Registration: "SE-RTG", LastContact: &base}}, nil
			})
			err := run(ctx, source, Options{Target: flight.Target{Kind: kind, Value: value}, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
				if calls == 2 {
					cancel()
				}
				return nil
			}, func() time.Time { return base })
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestSubsequentDepartureDisablesPreviousFlightContext(t *testing.T) {
	base := time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC)
	times := []int{0, 30}
	for seconds := 90; seconds <= 630; seconds += 60 {
		times = append(times, seconds)
	}
	times = append(times, 660, 690)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	source := lookupFunc(func(_ context.Context, target flight.Target) ([]flight.Aircraft, error) {
		index := calls
		calls++
		if index == len(times)-1 {
			return nil, errors.New("provider offline")
		}
		at := base.Add(time.Duration(times[index]) * time.Second)
		ground := index > 0 && index < len(times)-2
		a := flight.Aircraft{Hex: "4aca87", Callsign: "NSZ4366", OnGround: &ground, Latitude: p(0.0), Longitude: p(0.0), AltitudeFeet: p(1000.0), GroundSpeed: p(20.0), PositionUpdated: &at, LastContact: &at}
		if !ground {
			a.GroundSpeed = p(140.0)
		}
		return []flight.Aircraft{a}, nil
	})
	airport := &flight.Airport{Latitude: p(0.0), Longitude: p(0.0), ElevationFeet: p(0.0)}
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Callsign: "NSZ4366", Destination: airport, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		if len(snapshots) == len(times) {
			cancel()
		}
		return nil
	}, func() time.Time { return base.Add(time.Duration(times[calls-1]) * time.Second) })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if snapshots[len(snapshots)-3].Arrival.Status != "likely_landed" {
		t.Fatal("surface observations should establish an arrival before departure")
	}
	for _, s := range snapshots[len(snapshots)-2:] {
		if s.Tracking.FlightState != "subsequent_departure" || s.LandingEstimate.TouchdownAt != nil || s.Arrival.Status != "unknown" {
			t.Fatalf("later departure reused the original arrival context: %+v", s)
		}
	}
	if len(snapshots[len(snapshots)-2].Aircraft) != 1 {
		t.Fatal("physical aircraft tracking should continue after a subsequent departure")
	}
}

func TestGroundDwellRejectsCachedFixesAndGaps(t *testing.T) {
	for _, mode := range []string{"cached", "gap", "go-around"} {
		t.Run(mode, func(t *testing.T) {
			base := time.Now().UTC()
			id := newIdentity(Options{Target: flight.Target{Kind: flight.ByHex, Value: "4aca87"}})
			airport := &flight.Airport{Latitude: p(0.0), Longitude: p(0.0)}
			ground := flight.Aircraft{Hex: "4aca87", OnGround: p(true), Latitude: p(0.0), Longitude: p(0.0), PositionUpdated: &base, LastContact: &base}
			id.observe(base, airport, []flight.Aircraft{ground})
			later := base.Add(10 * time.Minute)
			if mode == "go-around" {
				later = base.Add(30 * time.Second)
			}
			if mode != "cached" {
				ground.PositionUpdated, ground.LastContact = &later, &later
			}
			id.observe(later, airport, []flight.Aircraft{ground})
			at := later.Add(30 * time.Second)
			ground.PositionUpdated, ground.LastContact, ground.OnGround = &at, &at, p(false)
			id.observe(at, airport, []flight.Aircraft{ground})
			if id.departed {
				t.Fatal("cached/gapped surface fixes or a short ground interval implied a subsequent flight")
			}
		})
	}
}
