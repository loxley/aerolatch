package tracker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

type scheduleFunc func(context.Context, time.Time, *flight.Aircraft) flight.ScheduleUpdate

func (f scheduleFunc) Update(ctx context.Context, now time.Time, a *flight.Aircraft) flight.ScheduleUpdate {
	return f(ctx, now, a)
}

func TestScheduledWaitingThenAircraftAcquisition(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	start := now.Add(24 * time.Hour)
	calls := 0
	schedules := scheduleFunc(func(_ context.Context, at time.Time, _ *flight.Aircraft) flight.ScheduleUpdate {
		return flight.ScheduleUpdate{Report: &flight.AirportReport{Source: "Swedavia", Match: "matched", Flight: &flight.ScheduledFlight{FlightID: "D84487", Callsign: "NSZ87C", Registration: "SERTG", StatusCode: "SCH"}}, Route: &flight.Route{Destination: flight.Airport{IATA: "BUD"}}, Target: &flight.Target{Kind: flight.ByCallsign, Value: "NSZ87C"}, Waiting: at.Before(start.Add(-30 * time.Minute)), Reason: "scheduled"}
	})
	source := lookupFunc(func(_ context.Context, target flight.Target) ([]flight.Aircraft, error) {
		calls++
		if target.Value != "NSZ87C" {
			t.Errorf("wrong operational callsign %s", target.Value)
		}
		return []flight.Aircraft{{Hex: "4aca87", Registration: "SE-RTG", LastContact: &now}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var snapshots []flight.Snapshot
	err := run(ctx, source, Options{Query: "D84487", Schedule: schedules, Dated: true, Interval: time.Nanosecond}, func(s flight.Snapshot) error {
		snapshots = append(snapshots, s)
		now = start.Add(-20 * time.Minute)
		if len(snapshots) == 2 {
			cancel()
		}
		return nil
	}, func() time.Time { return now })
	if !errors.Is(err, context.Canceled) || calls != 1 || len(snapshots) != 2 {
		t.Fatalf("calls=%d snapshots=%d err=%v", calls, len(snapshots), err)
	}
	if snapshots[0].Status != "scheduled" || len(snapshots[0].Aircraft) != 0 || snapshots[1].Status != "visible" || snapshots[1].Tracking.LockedHex != "4aca87" {
		t.Fatal("future service acquired an aircraft too early or failed to hand over")
	}
	if snapshots[0].Tracking.LiveLookupPerformed || !snapshots[1].Tracking.LiveLookupPerformed {
		t.Fatal("snapshot did not distinguish a planned lookup from an actual live-data request")
	}
}

func TestReportedTerminalStatusNeverFabricatesTelemetry(t *testing.T) {
	for _, status := range []string{"CAN", "LAN"} {
		source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
			t.Error("terminal dated flight acquired a reused callsign")
			return nil, nil
		})
		schedules := scheduleFunc(func(context.Context, time.Time, *flight.Aircraft) flight.ScheduleUpdate {
			return flight.ScheduleUpdate{Report: &flight.AirportReport{Match: "matched", Flight: &flight.ScheduledFlight{StatusCode: status, Arrival: flight.FlightTimes{Actual: p(time.Now())}}}, Waiting: true, Reason: "airport reports terminal status"}
		})
		var snapshot flight.Snapshot
		if err := Run(context.Background(), source, Options{Callsign: "NSZ2602", Schedule: schedules, Dated: true, Once: true}, func(s flight.Snapshot) error { snapshot = s; return nil }); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Aircraft) != 0 || snapshot.Arrival.Status != "unknown" || snapshot.LandingEstimate.TouchdownAt != nil || snapshot.AirportReport.Flight.StatusCode != status {
			t.Fatalf("airport status became aircraft evidence: %+v", snapshot)
		}
	}
}

func TestScheduledAssignmentMismatchCannotAcquireWrongAircraft(t *testing.T) {
	schedules := scheduleFunc(func(context.Context, time.Time, *flight.Aircraft) flight.ScheduleUpdate {
		return flight.ScheduleUpdate{Report: &flight.AirportReport{Match: "matched", Flight: &flight.ScheduledFlight{Registration: "SERTG", StatusCode: "SCH"}}}
	})
	at := time.Now()
	source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
		return []flight.Aircraft{{Hex: "abc001", Registration: "LN-NIB", LastContact: &at}}, nil
	})
	var snapshot flight.Snapshot
	err := Run(context.Background(), source, Options{Callsign: "NSZ4366", Schedule: schedules, Once: true}, func(s flight.Snapshot) error { snapshot = s; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Tracking.LockedHex != "" || len(snapshot.Aircraft) != 0 || snapshot.Status != "scheduled" || snapshot.Arrival.Status != "unknown" {
		t.Fatal("mismatched aircraft was acquired or advanced arrival inference")
	}
}

func TestDiversionSuspendsOldDestinationInferenceButKeepsPositions(t *testing.T) {
	for _, stale := range []bool{false, true} {
		at := time.Now().UTC()
		schedules := scheduleFunc(func(context.Context, time.Time, *flight.Aircraft) flight.ScheduleUpdate {
			match := "matched"
			if stale {
				match = "unavailable"
			}
			return flight.ScheduleUpdate{Report: &flight.AirportReport{Match: match, Stale: stale, Flight: &flight.ScheduledFlight{StatusCode: "DIV"}}}
		})
		source := lookupFunc(func(context.Context, flight.Target) ([]flight.Aircraft, error) {
			return []flight.Aircraft{{Hex: "4aca87", OnGround: p(false), Latitude: p(0.0), Longitude: p(-0.2), AltitudeFeet: p(3000.0), GroundSpeed: p(150.0), Track: p(90.0), PositionUpdated: &at, LastContact: &at}}, nil
		})
		airport := &flight.Airport{Latitude: p(0.0), Longitude: p(0.0), ElevationFeet: p(0.0)}
		var s flight.Snapshot
		err := Run(context.Background(), source, Options{Callsign: "NSZ4366", Destination: airport, Schedule: schedules, Once: true}, func(snapshot flight.Snapshot) error { s = snapshot; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if s.Status != "visible" || len(s.Aircraft) != 1 || s.LandingEstimate.TouchdownAt != nil || !strings.Contains(s.Arrival.Reason, "diversion") {
			t.Fatal("diversion reused the old arrival context or discarded physical positions")
		}
	}
}
