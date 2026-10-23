package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func ptr[T any](v T) *T { return &v }

type boardsFunc func(context.Context, string, string, string) (flight.FlightBoard, error)

func (f boardsFunc) Board(c context.Context, a, d, date string) (flight.FlightBoard, error) {
	return f(c, a, d, date)
}

func sample(at time.Time) flight.ScheduledFlight {
	return flight.ScheduledFlight{FlightID: "D84487", Callsign: "NSZ87C", Registration: "SERTG", Origin: flight.Airport{IATA: "ARN", ICAO: "ESSA"}, Destination: flight.Airport{IATA: "BUD", ICAO: "LHBP"}, Departure: flight.FlightTimes{Scheduled: &at}, DepartureDate: ptr(time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)), StatusCode: "SCH", FetchedAt: at.Add(-time.Hour)}
}

func TestDateMatchingCodesharesDeletedAndAmbiguousLegs(t *testing.T) {
	f := sample(time.Now().UTC())
	f.Codeshares = []string{"SK0007"}
	for _, query := range []string{"D84487", "NSZ87C", "SK7"} {
		got, state := Match([]flight.ScheduledFlight{f}, MatchRequest{Query: query})
		if got == nil || state != "matched" {
			t.Fatalf("query %s failed: %s", query, state)
		}
	}
	deleted := f
	deleted.StatusCode = "DEL"
	if got, state := Match([]flight.ScheduledFlight{deleted}, MatchRequest{Query: f.FlightID}); got != nil || state != "not_found" {
		t.Fatal("deleted row became a live flight or cancellation")
	}
	other := f
	other.Destination = flight.Airport{IATA: "CPH", ICAO: "EKCH"}
	if _, state := Match([]flight.ScheduledFlight{f, other}, MatchRequest{Query: f.FlightID}); state != "ambiguous" {
		t.Fatal("different legs were arbitrarily selected")
	}
	got, state := Match([]flight.ScheduledFlight{f, other}, MatchRequest{Query: f.FlightID, Destination: "LHBP"})
	if state != "matched" || got.Destination.IATA != "BUD" {
		t.Fatal("explicit route hint did not disambiguate")
	}
}

func TestWatcherFutureGatingRefreshAndRegistrationConflict(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	f := sample(now.Add(24 * time.Hour))
	calls := 0
	fail := false
	w := Watcher{Query: f.FlightID, OriginHint: "ARN", Date: "2026-10-21", ExplicitDate: true, Client: boardsFunc(func(context.Context, string, string, string) (flight.FlightBoard, error) {
		calls++
		if fail {
			return flight.FlightBoard{}, errors.New("offline")
		}
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{f}}, nil
	})}
	update := w.Update(context.Background(), now, nil)
	if !update.Waiting || update.Target == nil || update.Target.Value != "NSZ87C" || update.Route.Destination.IATA != "BUD" {
		t.Fatalf("future service not held or resolved: %+v", update)
	}
	w.Update(context.Background(), now.Add(time.Minute), nil)
	if calls != 1 {
		t.Fatal("watcher polled the board at live-position cadence")
	}
	near := *f.Departure.Scheduled
	near = near.Add(-20 * time.Minute)
	update = w.Update(context.Background(), near, &flight.Aircraft{Registration: "SE-RTG"})
	if update.Waiting || update.Report.Match != "matched" {
		t.Fatal("near-departure aircraft normalization failed")
	}
	conflict := w.Update(context.Background(), near.Add(time.Second), &flight.Aircraft{Registration: "LN-NIB"})
	if conflict.Report.Match != "identity_conflict" {
		t.Fatal("different assigned aircraft was trusted")
	}
	fail = true
	stale := w.Update(context.Background(), near.Add(time.Hour), nil)
	if stale.Report.Match != "unavailable" || !stale.Report.Stale || stale.Report.Flight == nil || !stale.Waiting {
		t.Fatal("airport failure erased prior data or allowed unsafe dated acquisition")
	}
}

func TestWatcherMergesBothCoveredAirportsAndPreservesUserSelection(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	f := sample(now)
	f.Destination = flight.Airport{IATA: "GOT", ICAO: "ESGG"}
	f.Arrival = flight.FlightTimes{}
	other := f
	other.Arrival = flight.FlightTimes{Scheduled: ptr(now.Add(time.Hour)), Actual: ptr(now.Add(50 * time.Minute))}
	other.StatusCode = "LAN"
	w := Watcher{Query: f.FlightID, OriginHint: "ARN", DestinationHint: "ESGG", Client: boardsFunc(func(_ context.Context, a, d, date string) (flight.FlightBoard, error) {
		if a == "GOT" {
			return flight.FlightBoard{Flights: []flight.ScheduledFlight{other}}, nil
		}
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{f}}, nil
	})}
	got := w.Update(context.Background(), now, nil)
	if got.Report.Flight.StatusCode != "LAN" || got.Report.Flight.Departure.Scheduled == nil || got.Report.Flight.Arrival.Actual == nil {
		t.Fatalf("arrival/departure information was not merged: %+v", got.Report)
	}
	if !got.Waiting {
		t.Fatal("already landed dated service could acquire a reused callsign")
	}
}

func TestWatcherPinsFlightLegInsteadOfFollowingReusedCallsign(t *testing.T) {
	now := time.Now().UTC()
	f := sample(now)
	records := []flight.ScheduledFlight{f}
	w := Watcher{Query: f.Callsign, OriginHint: "ARN", Client: boardsFunc(func(context.Context, string, string, string) (flight.FlightBoard, error) {
		return flight.FlightBoard{Flights: records}, nil
	})}
	w.Update(context.Background(), now, nil)
	replacement := f
	replacement.FlightID = "D89999"
	replacement.Destination.IATA = "CPH"
	records = []flight.ScheduledFlight{replacement}
	got := w.Update(context.Background(), now.Add(6*time.Minute), nil)
	if got.Report.Match != "not_found" || !got.Report.Stale || got.Report.Flight.FlightID != "D84487" {
		t.Fatal("reused callsign replaced the selected flight leg")
	}
}

func TestTerminalRecordRemainsGuardedDuringOutageAndDeletedIsDistinct(t *testing.T) {
	now := time.Now().UTC()
	f := sample(now)
	f.StatusCode = "DEL"
	fail := false
	w := Watcher{Query: f.FlightID, OriginHint: "ARN", Client: boardsFunc(func(context.Context, string, string, string) (flight.FlightBoard, error) {
		if fail {
			return flight.FlightBoard{}, errors.New("offline")
		}
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{f}}, nil
	})}
	got := w.Update(context.Background(), now, nil)
	if got.Report.Match != "matched" || got.Report.Flight.StatusCode != "DEL" || !got.Waiting || StatusName(got.Report.Flight.StatusCode) == "CANCELLED" {
		t.Fatal("deleted airport record was lost, cancelled, or eligible for acquisition")
	}
	fail = true
	stale := w.Update(context.Background(), now.Add(2*time.Hour), nil)
	if !stale.Waiting || !stale.Report.Stale {
		t.Fatal("airport outage released a previously guarded flight")
	}
}

func TestMissingMatchBacksOffDespiteUnchangedLiveAircraft(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	w := Watcher{Query: "NO123", OriginHint: "ARN", Client: boardsFunc(func(context.Context, string, string, string) (flight.FlightBoard, error) {
		calls++
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{}}, nil
	})}
	a := &flight.Aircraft{Hex: "abc001", Callsign: "NO123"}
	w.Update(context.Background(), now, a)
	initial := calls
	for i := 1; i < 30; i++ {
		w.Update(context.Background(), now.Add(time.Duration(i)*time.Second), a)
	}
	if calls != initial {
		t.Fatal("missing airport matches polled at live-data cadence")
	}
}
