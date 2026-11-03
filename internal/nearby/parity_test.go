package nearby

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/tracker"
)

func compareDerived(t *testing.T, normal, selected flight.Snapshot) {
	t.Helper()
	if !reflect.DeepEqual(normal.Destination, selected.Destination) || normal.RouteSource != selected.RouteSource {
		t.Fatalf("destination/provenance mismatch: normal=%+v nearby=%+v", normal.Destination, selected.Destination)
	}
	a, b := normal.LandingEstimate, selected.LandingEstimate
	if a.Status != b.Status || a.Phase != b.Phase || !reflect.DeepEqual(a.DistanceNM, b.DistanceNM) || !reflect.DeepEqual(a.TouchdownAt, b.TouchdownAt) {
		t.Fatalf("ETA differs: normal=%+v nearby=%+v", a, b)
	}
	if a.MinutesRemaining != nil && (b.MinutesRemaining == nil || math.Abs(*a.MinutesRemaining-*b.MinutesRemaining) > 0.05) {
		t.Fatalf("remaining time differs beyond test scheduling tolerance: %+v %+v", a, b)
	}
	x, y := normal.Arrival, selected.Arrival
	if x.Status != y.Status || x.Historical != y.Historical || x.SurfaceReports != y.SurfaceReports ||
		!reflect.DeepEqual(x.DistanceNM, y.DistanceNM) || !reflect.DeepEqual(x.FirstSurfaceAt, y.FirstSurfaceAt) ||
		!reflect.DeepEqual(x.LastSurfaceAt, y.LastSurfaceAt) || !reflect.DeepEqual(x.CorroboratedAt, y.CorroboratedAt) {
		t.Fatalf("arrival differs: normal=%+v nearby=%+v", x, y)
	}
	if normal.Tracking.LockedHex != selected.Tracking.LockedHex {
		t.Fatal("physical aircraft identity changed")
	}
}

func TestNearbyAndNormalTrackingHaveIdenticalDestinationCalculations(t *testing.T) {
	for _, scenario := range []string{"SAS549 departure screenshot", "approach and surface corroboration"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			route := &flight.Route{Callsign: "SAS549", Origin: flight.Airport{IATA: "CPH"}, Destination: flight.Airport{
				IATA: "AMS", Latitude: ptr(52.3086), Longitude: ptr(4.76389), ElevationFeet: ptr(-11.0)}}
			a := aircraft("4cac79", 55.58881, 12.74131, now.Add(-2*time.Second))
			a.Callsign, a.OnGround, a.AltitudeFeet, a.GroundSpeed, a.Track, a.VerticalRate = "SAS549", ptr(false), ptr(4850.0), ptr(215.0), ptr(157.0), ptr(3072.0)
			observations := []flight.Aircraft{a}
			if scenario == "approach and surface corroboration" {
				route.Destination = flight.Airport{IATA: "CPH", Latitude: ptr(55.6179), Longitude: ptr(12.656), ElevationFeet: ptr(17.0)}
				observations = nil
				for i := 0; i < 4; i++ {
					fix := aircraft("4cac79", 55.60, 12.64, now.Add(time.Duration(-20+i*5)*time.Second))
					fix.Callsign, fix.OnGround = "SAS549", ptr(i > 0)
					fix.AltitudeFeet, fix.GroundSpeed, fix.Track, fix.VerticalRate = ptr(300.0), ptr(125.0), ptr(30.0), ptr(-640.0)
					if i > 0 {
						fix.Latitude, fix.Longitude, fix.GroundSpeed = ptr(55.617), ptr(12.65), ptr(56.0)
					}
					observations = append(observations, fix)
				}
			}
			normalInput := make(chan flight.Aircraft, len(observations))
			for _, fix := range observations {
				normalInput <- fix
			}
			ctxNormal, stopNormal := context.WithCancel(context.Background())
			defer stopNormal()
			var normal []flight.Snapshot
			err := tracker.Run(ctxNormal, inspectionFeed{frames: normalInput}, tracker.Options{
				Target: flight.Target{Kind: flight.ByHex, Value: observations[0].Hex}, Route: route, RouteSource: "adsbdb", Interval: time.Millisecond},
				func(s flight.Snapshot) error {
					normal = append(normal, s)
					if len(normal) == len(observations) {
						stopNormal()
					}
					return nil
				})
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if normal[0].LandingEstimate.Status != "estimated" || normal[0].Arrival.Status != "airborne" {
				t.Fatalf("normal fixture is not estimable: %+v", normal[0])
			}
			if scenario == "SAS549 departure screenshot" && normal[0].LandingEstimate.Phase != "climb" {
				t.Fatal("screenshot must show climb")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			input := make(chan flight.Aircraft, 1)
			input <- observations[0]
			release := make(chan struct{})
			resolver := inspectionResolver(func(ctx context.Context, call string) (*flight.Route, error) {
				select {
				case <-release:
					return route, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			frames, result := make(chan flight.Snapshot, 64), make(chan error, 1)
			go func() {
				result <- Inspect(ctx, inspectionFeed{frames: input}, resolver, flight.Selection{Aircraft: observations[0], ObservedAt: now}, time.Millisecond,
					func(s flight.Snapshot) error {
						select {
						case frames <- s:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
			}()
			waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Status == "visible" && s.Route == nil })
			resolvedIndex := 0
			if scenario == "approach and surface corroboration" {
				// The whole approach and initial surface corroboration happen
				// before the delayed reference lookup finishes.
				for i := 1; i <= 2; i++ {
					fix := observations[i]
					input <- fix
					waitInspection(t, ctx, frames, func(s flight.Snapshot) bool {
						return s.Status == "visible" && len(s.Aircraft) == 1 && s.Aircraft[0].PositionUpdated.Equal(*fix.PositionUpdated)
					})
				}
				resolvedIndex = 2
			}
			close(release)
			selected := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Route != nil && s.Destination != nil })
			compareDerived(t, normal[resolvedIndex], selected)
			if selected.Tracking.LiveLookupPerformed {
				t.Fatal("route notification was mislabelled as another live request")
			}
			for i, fix := range observations[resolvedIndex+1:] {
				input <- fix
				selected = waitInspection(t, ctx, frames, func(s flight.Snapshot) bool {
					return s.Tracking != nil && s.Tracking.LiveLookupPerformed && len(s.Aircraft) == 1 && s.Aircraft[0].PositionUpdated.Equal(*fix.PositionUpdated)
				})
				compareDerived(t, normal[resolvedIndex+i+1], selected)
			}
			if scenario == "approach and surface corroboration" && selected.Arrival.Status != "likely_landed" {
				t.Fatal("nearby did not corroborate normal surface landing")
			}
			cancel()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
