package tracker

import (
	"context"
	"time"

	"aerolatch/internal/flight"
)

type ScheduleLookup interface {
	Update(context.Context, time.Time, *flight.Aircraft) flight.ScheduleUpdate
}

func waitingStatus(update flight.ScheduleUpdate) string {
	if r := update.Report; r != nil && r.Match == "matched" && !r.Stale && r.Flight != nil {
		switch r.Flight.StatusCode {
		case "CAN":
			return "cancelled"
		case "LAN":
			return "reported_landed"
		case "DEL":
			return "deleted"
		}
		return "scheduled"
	}
	return "waiting_for_schedule"
}

// checkAssignment does not turn a mismatched aircraft into an empty feed result:
// waiting must never advance the approach/coverage-loss landing heuristic.
func checkAssignment(update *flight.ScheduleUpdate, aircraft []flight.Aircraft, locked bool) ([]flight.Aircraft, bool) {
	r := update.Report
	if r == nil || r.Match != "matched" || r.Stale || r.Flight == nil || r.Flight.Registration == "" || len(aircraft) == 0 {
		return aircraft, false
	}
	expected := flight.RegistrationID(r.Flight.Registration)
	kept := make([]flight.Aircraft, 0, len(aircraft))
	for _, a := range aircraft {
		if a.Registration == "" || flight.RegistrationID(a.Registration) == expected {
			kept = append(kept, a)
		}
	}
	if len(kept) == len(aircraft) {
		return aircraft, false
	}
	if locked {
		copy := *r
		copy.Match, copy.Error = "identity_conflict", "airport aircraft assignment differs from the locked aircraft; reported status is not applied to this aircraft"
		update.Report = &copy
		return aircraft, false
	}
	if len(kept) == 0 {
		update.Reason = "waiting for the aircraft assigned to the selected dated flight"
		return nil, true
	}
	return kept, false
}

func scheduleInferenceReason(update flight.ScheduleUpdate) string {
	r := update.Report
	if r == nil {
		return ""
	}
	if r.Match == "identity_conflict" {
		return r.Error
	}
	if r.Flight != nil && (r.Match == "matched" || r.Stale) && (r.Flight.StatusCode == "DIV" || r.Flight.StatusCode == "RER") {
		return "airport reports a diversion or reroute; previous-destination arrival estimates are suspended"
	}
	return ""
}

func validSchedule(update flight.ScheduleUpdate) bool {
	return update.Report != nil && update.Report.Match == "matched" && !update.Report.Stale && update.Route != nil
}
