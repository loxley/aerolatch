package schedule

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func asyncArrival(at time.Time) (flight.ScheduledFlight, *flight.Route, flight.Aircraft) {
	f := flight.ScheduledFlight{FlightID: "SK1021", Callsign: "SAS1021", Registration: "SEMKF", Origin: flight.Airport{IATA: "SFT"}, Destination: flight.Airport{IATA: "ARN", ICAO: "ESSA"},
		Arrival: flight.FlightTimes{Scheduled: &at}, DepartureDate: ptr(at.Truncate(24 * time.Hour)), StatusCode: "ACT", FetchedAt: at}
	route := &flight.Route{FlightNumber: f.FlightID, Callsign: f.Callsign, Origin: f.Origin, Destination: flight.Airport{IATA: "ARN", Latitude: ptr(59.6519), Longitude: ptr(17.9186), ElevationFeet: ptr(137.0)}}
	return f, route, flight.Aircraft{Hex: "4ab566", Callsign: f.Callsign, Registration: "SE-MKF"}
}

func awaitAirport(t *testing.T, ctx context.Context, w *AsyncWatcher, aircraft *flight.Aircraft, match func(flight.ScheduleUpdate) bool) flight.ScheduleUpdate {
	t.Helper()
	for {
		update := w.Update(ctx, w.Now(), aircraft)
		if match(update) {
			return update
		}
		select {
		case <-ctx.Done():
			t.Fatal("airport update did not arrive")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestAsyncAirportRefreshRetainsIdentityWithoutCurrentADSB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base := time.Date(2026, 11, 2, 20, 0, 0, 0, time.UTC)
	f, route, aircraft := asyncArrival(base)
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	var calls atomic.Int32
	source := boardsFunc(func(_ context.Context, airport, direction, date string) (flight.FlightBoard, error) {
		if airport != "ARN" || direction != "arrivals" {
			t.Errorf("wrong airport context %s %s", airport, direction)
		}
		copy := f
		if calls.Add(1) > 1 {
			copy.StatusCode = "LAN"
			copy.Arrival.Actual = ptr(base.Add(time.Minute))
		}
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{copy}}, nil
	})
	w := NewAsyncWatcher(source, flight.Target{Kind: flight.ByHex, Value: aircraft.Hex}, nil, nil)
	w.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	w.Update(ctx, base, &aircraft)
	w.SetReference(route)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	first := awaitAirport(t, ctx, w, &aircraft, func(u flight.ScheduleUpdate) bool { return u.Report != nil && u.Report.Match == "matched" })
	if first.Report.Flight.StatusCode != "ACT" || calls.Load() != 1 {
		t.Fatal("initial aircraft/airport matching failed")
	}
	for i := 0; i < 10; i++ {
		w.Update(ctx, base.Add(time.Minute), nil)
	}
	if calls.Load() != 1 {
		t.Fatal("airport data refreshed at ADS-B polling cadence")
	}
	clock.Store(base.Add(6 * time.Minute).UnixNano())
	landed := awaitAirport(t, ctx, w, nil, func(u flight.ScheduleUpdate) bool {
		return u.Report != nil && u.Report.Flight != nil && u.Report.Flight.StatusCode == "LAN"
	})
	if landed.Report.Flight.Arrival.Actual == nil || calls.Load() != 2 {
		t.Fatal("airport did not refresh using retained identity after ADS-B loss")
	}
	conflict := aircraft
	conflict.Registration = "SE-OTHER"
	if u := w.Update(ctx, w.Now(), &conflict); u.Report.Match != "identity_conflict" {
		t.Fatal("airport status accepted for a different assignment")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("airport worker did not stop")
	}
}

func TestAsyncAirportRebindCancelsSupersededRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	f, route, aircraft := asyncArrival(time.Now().UTC())
	started, stopped := make(chan struct{}), make(chan struct{})
	source := boardsFunc(func(ctx context.Context, airport, direction, date string) (flight.FlightBoard, error) {
		if airport == "ARN" {
			close(started)
			<-ctx.Done()
			close(stopped)
			return flight.FlightBoard{}, ctx.Err()
		}
		copy := f
		copy.FlightID, copy.Callsign, copy.Destination.IATA, copy.StatusCode = "SK202", "SAS202", "GOT", "LAN"
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{copy}}, nil
	})
	w := NewAsyncWatcher(source, flight.Target{Kind: flight.ByHex, Value: aircraft.Hex}, nil, nil)
	w.Update(ctx, time.Now(), &aircraft)
	w.SetReference(route)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("airport request not started")
	}
	newRoute := *route
	newRoute.Callsign, newRoute.FlightNumber, newRoute.Destination = "SAS202", "SK202", flight.Airport{IATA: "GOT", Latitude: ptr(57.66), Longitude: ptr(12.28), ElevationFeet: ptr(506.0)}
	aircraft.Callsign = "SAS202"
	w.Update(ctx, time.Now(), &aircraft)
	w.SetReference(&newRoute)
	u := awaitAirport(t, ctx, w, &aircraft, func(u flight.ScheduleUpdate) bool { return u.Report != nil && u.Report.Match == "matched" })
	if u.Report.Airport != "GOT" || u.Report.Flight.Callsign != "SAS202" {
		t.Fatal("superseded airport request leaked into new selection")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("old airport request not cancelled before replacement")
	}
	w.SetReference(nil)
	if u := w.Update(ctx, time.Now(), &aircraft); u.Report != nil {
		t.Fatal("cleared context retained old airport report")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("airport worker did not stop")
	}
}
