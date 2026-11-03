package nearby

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

type airportBoards func(context.Context, string, string, string) (flight.FlightBoard, error)

func (f airportBoards) Board(ctx context.Context, a, d, date string) (flight.FlightBoard, error) {
	return f(ctx, a, d, date)
}

type airportFeed struct {
	frames   <-chan []flight.Aircraft
	returned *atomic.Int32
}

func (s airportFeed) Name() string { return "fixture" }
func (s airportFeed) Lookup(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
	select {
	case frames := <-s.frames:
		if s.returned != nil {
			s.returned.Add(1)
		}
		return frames, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func airportFixture() (flight.Selection, *flight.Route, flight.ScheduledFlight) {
	now := time.Now().UTC()
	a := aircraft("4ab566", 59.63319, 17.91165, now.Add(-2*time.Second))
	a.Callsign, a.Registration, a.Type, a.OnGround = "SAS1021", "SE-MKF", "AT76", ptr(false)
	a.AltitudeFeet, a.GroundSpeed, a.Track, a.VerticalRate = ptr(300.0), ptr(107.0), ptr(10.0), ptr(-640.0)
	route := &flight.Route{FlightNumber: "SK1021", Callsign: a.Callsign, Origin: flight.Airport{IATA: "SFT"}, Destination: flight.Airport{IATA: "ARN", ICAO: "ESSA", Latitude: ptr(59.6519), Longitude: ptr(17.9186), ElevationFeet: ptr(137.0)}}
	f := flight.ScheduledFlight{FlightID: "SK1021", Callsign: a.Callsign, Registration: "SEMKF", Origin: route.Origin, Destination: flight.Airport{IATA: "ARN", ICAO: "ESSA"}, Arrival: flight.FlightTimes{Scheduled: ptr(now), Actual: ptr(now.Add(-time.Minute))}, DepartureDate: ptr(now.Truncate(24 * time.Hour)), StatusCode: "LAN", FetchedAt: now}
	return flight.Selection{Aircraft: a, ObservedAt: now}, route, f
}

func TestNearbyAirportArrivalPublishesAfterADSBDisappears(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	selection, route, flightRecord := airportFixture()
	input := make(chan []flight.Aircraft, 1)
	input <- []flight.Aircraft{selection.Aircraft}
	release := make(chan struct{})
	started := make(chan struct{})
	boards := airportBoards(func(ctx context.Context, airport, direction, date string) (flight.FlightBoard, error) {
		if airport != "ARN" || direction != "arrivals" {
			t.Errorf("did not discover SFT -> ARN arrival board: %s %s", airport, direction)
		}
		close(started)
		select {
		case <-release:
			return flight.FlightBoard{Flights: []flight.ScheduledFlight{flightRecord}}, nil
		case <-ctx.Done():
			return flight.FlightBoard{}, ctx.Err()
		}
	})
	var returned atomic.Int32
	frames, result := make(chan flight.Snapshot, 128), make(chan error, 1)
	go func() {
		result <- Inspect(ctx, airportFeed{frames: input, returned: &returned}, inspectionResolver(func(context.Context, string) (*flight.Route, error) { return route, nil }), selection, time.Millisecond,
			func(s flight.Snapshot) error {
				select {
				case frames <- s:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, InspectionOptions{Airports: boards})
	}()
	waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Status == "visible" && s.Destination != nil })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("Swedavia request not started")
	}
	input <- nil
	lost := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Status == "not_visible" })
	close(release)
	landed := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool {
		return s.AirportReport != nil && s.AirportReport.Match == "matched" && s.AirportReport.Flight.StatusCode == "LAN"
	})
	if landed.Status != "not_visible" || returned.Load() != 2 || landed.Tracking.LiveLookupPerformed || len(landed.Aircraft) != 0 || len(landed.LastObserved) != 1 || !landed.CheckedAt.Equal(lost.CheckedAt) {
		t.Fatalf("airport update manufactured a live observation: %+v", landed)
	}
	if landed.Destination.Source != "swedavia" || landed.RouteSource != "Swedavia" || landed.AirportReport.Flight.Arrival.Actual == nil || landed.Inspection.SwedaviaStatus != "matched" {
		t.Fatalf("dated airport information/provenance not shared with tracker: %+v", landed)
	}
	if landed.Arrival.Status == "likely_landed" || landed.Arrival.SurfaceReports != 0 {
		t.Fatal("airport arrival fabricated surface landing evidence")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNearbySlowAirportDoesNotBlockTelemetryOrCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	selection, route, _ := airportFixture()
	started, stopped := make(chan struct{}), make(chan struct{})
	boards := airportBoards(func(ctx context.Context, _, _, _ string) (flight.FlightBoard, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return flight.FlightBoard{}, ctx.Err()
	})
	input := make(chan []flight.Aircraft, 1)
	input <- []flight.Aircraft{selection.Aircraft}
	frames, result := make(chan flight.Snapshot, 64), make(chan error, 1)
	go func() {
		result <- Inspect(ctx, airportFeed{frames: input}, inspectionResolver(func(context.Context, string) (*flight.Route, error) { return route, nil }), selection, time.Hour,
			func(s flight.Snapshot) error {
				select {
				case frames <- s:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, InspectionOptions{Airports: boards})
	}()
	waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Status == "visible" && s.Destination != nil })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("airport request not started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("inspection waited for the airport request")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("airport request left running behind UI")
	}
}

func TestNearbyDoesNotUseUnrelatedAirportAssignments(t *testing.T) {
	for _, mismatch := range []string{"registration", "callsign"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		selection, route, f := airportFixture()
		if mismatch == "registration" {
			f.Registration = "LNOTHER"
		} else {
			f.Callsign = "OTHER123"
		}
		boards := airportBoards(func(context.Context, string, string, string) (flight.FlightBoard, error) {
			return flight.FlightBoard{Flights: []flight.ScheduledFlight{f}}, nil
		})
		input := make(chan []flight.Aircraft, 1)
		input <- []flight.Aircraft{selection.Aircraft}
		frames, result := make(chan flight.Snapshot, 64), make(chan error, 1)
		go func() {
			result <- Inspect(ctx, airportFeed{frames: input}, inspectionResolver(func(context.Context, string) (*flight.Route, error) { return route, nil }), selection, time.Hour,
				func(s flight.Snapshot) error {
					select {
					case frames <- s:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, InspectionOptions{Airports: boards})
		}()
		conflict := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool {
			return s.AirportReport != nil && s.AirportReport.Match == "identity_conflict"
		})
		if conflict.Destination != nil && conflict.Destination.Source == "swedavia" {
			t.Fatal("conflicting airport record selected destination")
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestNearbyDoesNotQuerySwedaviaForUncoveredRoute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	selection, route, _ := airportFixture()
	route.Origin = flight.Airport{IATA: "CPH"}
	route.Destination = flight.Airport{IATA: "AMS", Latitude: ptr(52.3086), Longitude: ptr(4.76389), ElevationFeet: ptr(-11.0)}
	var calls atomic.Int32
	boards := airportBoards(func(context.Context, string, string, string) (flight.FlightBoard, error) {
		calls.Add(1)
		return flight.FlightBoard{}, errors.New("unexpected airport lookup")
	})
	input := make(chan []flight.Aircraft, 1)
	input <- []flight.Aircraft{selection.Aircraft}
	frames, result := make(chan flight.Snapshot, 64), make(chan error, 1)
	go func() {
		result <- Inspect(ctx, airportFeed{frames: input}, inspectionResolver(func(context.Context, string) (*flight.Route, error) { return route, nil }), selection, time.Hour,
			func(s flight.Snapshot) error {
				select {
				case frames <- s:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, InspectionOptions{Airports: boards})
	}()
	waitInspection(t, ctx, frames, func(s flight.Snapshot) bool {
		return s.Destination != nil && s.Inspection.SwedaviaStatus == "not_covered"
	})
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported route spent airport API requests")
	}
}
