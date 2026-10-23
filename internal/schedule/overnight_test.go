package schedule

import (
	"context"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func TestAdjacentDateForLiveFlightButNeverForExplicitDate(t *testing.T) {
	now := time.Date(2026, 10, 20, 21, 30, 0, 0, time.UTC) // 23:30 local, arrival after midnight.
	f := sample(now)
	f.Origin = flight.Airport{IATA: "HEL"}
	f.Destination = flight.Airport{IATA: "ARN"}
	f.Arrival = flight.FlightTimes{Scheduled: ptr(now.Add(time.Hour))}
	client := boardsFunc(func(_ context.Context, a, d, date string) (flight.FlightBoard, error) {
		if date == "2026-10-21" {
			return flight.FlightBoard{Flights: []flight.ScheduledFlight{f}}, nil
		}
		return flight.FlightBoard{Flights: []flight.ScheduledFlight{}}, nil
	})
	w := Watcher{Query: f.FlightID, DestinationHint: "ARN", Client: client}
	got := w.Update(context.Background(), now, nil)
	if got.Report.Match != "matched" || got.Report.Date != "2026-10-21" {
		t.Fatal("live overnight flight was not found on the adjacent local date")
	}
	explicit := Watcher{Query: f.FlightID, DestinationHint: "ARN", Client: client, Date: "2026-10-20", ExplicitDate: true}
	if selected := explicit.Update(context.Background(), now, nil); selected.Report.Match != "not_found" || !selected.Waiting {
		t.Fatal("explicit date silently selected a different day's flight")
	}
}
