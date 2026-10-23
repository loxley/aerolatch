package schedule

import (
	"context"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
)

type BoardSource interface {
	Board(context.Context, string, string, string) (flight.FlightBoard, error)
}

type Watcher struct {
	Client                                        BoardSource
	Query                                         string
	Target                                        flight.Target
	Date                                          string
	ExplicitDate                                  bool
	OriginHint, DestinationHint                   string
	Reference                                     *flight.Route
	DestinationOverride                           *flight.Airport
	ResolveAirport                                func(context.Context, string) (*flight.Airport, error)
	last                                          flight.ScheduleUpdate
	pinned                                        string
	primaryAirport, primaryDirection, primaryDate string
	geometry                                      map[string]*flight.Airport
	lastIdentity                                  string
}

func (w *Watcher) context(now time.Time) (string, string, string) {
	if w.pinned != "" {
		return w.primaryAirport, w.primaryDirection, w.primaryDate
	}
	airport, direction := "", ""
	if code := provider.SwedaviaAirport(w.OriginHint); code != "" {
		airport, direction = code, "departures"
	} else if code := provider.SwedaviaAirport(w.DestinationHint); code != "" {
		airport, direction = code, "arrivals"
	} else if w.Reference != nil {
		if code := covered(w.Reference.Destination); code != "" {
			airport, direction = code, "arrivals"
		} else if code := covered(w.Reference.Origin); code != "" {
			airport, direction = code, "departures"
		}
	}
	date := w.Date
	if date == "" {
		date = now.In(provider.Stockholm()).Format("2006-01-02")
	}
	return airport, direction, date
}

func covered(a flight.Airport) string {
	if code := provider.SwedaviaAirport(a.IATA); code != "" {
		return code
	}
	return provider.SwedaviaAirport(a.ICAO)
}

func (w *Watcher) HasContext(now time.Time) bool { a, _, _ := w.context(now); return a != "" }

func sameTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

func (w *Watcher) Update(ctx context.Context, now time.Time, aircraft *flight.Aircraft) flight.ScheduleUpdate {
	now = now.UTC()
	airport, direction, date := w.context(now)
	if airport == "" {
		return flight.ScheduleUpdate{}
	}
	// Reevaluate aircraft identity every poll, but refresh airport data on its
	// own much slower cadence. A newly observed identity may resolve ambiguity.
	identityKey := ""
	if aircraft != nil {
		identityKey = aircraft.Hex + "/" + aircraft.Registration + "/" + aircraft.Callsign
	}
	if w.last.Report != nil && now.Before(w.last.Report.NextCheckAt) {
		if w.last.Report.Match == "matched" || aircraft == nil || identityKey == w.lastIdentity {
			return w.evaluate(w.last, now, aircraft)
		}
	}
	w.lastIdentity = identityKey
	report := &flight.AirportReport{Source: "Swedavia", Airport: airport, Direction: direction, Date: date, Match: "not_found", NextCheckAt: now.Add(5 * time.Minute)}
	board, err := w.Client.Board(ctx, airport, direction, date)
	request := MatchRequest{Query: w.Query, Target: w.Target, Origin: w.OriginHint, Destination: w.DestinationHint, PinnedLeg: w.pinned, Aircraft: aircraft}
	var selected *flight.ScheduledFlight
	if err == nil {
		selected, report.Match = Match(board.Flights, request)
	}
	if err == nil && selected == nil && report.Match == "not_found" && !w.ExplicitDate && w.pinned == "" {
		// Near midnight, the live leg may be listed on an adjacent local date.
		// Only consider adjacent records within six hours of the current time.
		local, _ := provider.SwedaviaDate(date, now)
		var adjacent []flight.ScheduledFlight
		for _, offset := range []int{-1, 1} {
			other, e := w.Client.Board(ctx, airport, direction, local.AddDate(0, 0, offset).Format("2006-01-02"))
			if e != nil {
				continue
			}
			for _, f := range other.Flights {
				if at := f.Scheduled(direction); at != nil && at.After(now.Add(-6*time.Hour)) && at.Before(now.Add(6*time.Hour)) {
					adjacent = append(adjacent, f)
				}
			}
		}
		selected, report.Match = Match(adjacent, request)
		if selected != nil {
			date = selected.Scheduled(direction).In(provider.Stockholm()).Format("2006-01-02")
			report.Date = date
		}
	}
	if err == nil && selected == nil && report.Match == "not_found" {
		request.IncludeDeleted = true
		selected, report.Match = Match(board.Flights, request)
	}
	if err != nil {
		report.Match, report.Error, report.Stale = "unavailable", err.Error(), true
		if w.last.Report != nil {
			report.Flight = w.last.Report.Flight
		}
	} else if selected != nil {
		report.Flight = selected
		w.pinned = selected.LegKey()
		w.primaryAirport, w.primaryDirection, w.primaryDate = airport, direction, date
		w.supplement(ctx, report, now)
	} else if w.pinned != "" && w.last.Report != nil {
		report.Flight, report.Stale = w.last.Report.Flight, true
		report.Error = "selected dated flight leg is no longer present in the airport board"
	}
	if report.Match == "not_found" || report.Match == "ambiguous" {
		report.NextCheckAt = now.Add(30 * time.Minute)
	}
	if f := report.Flight; f != nil {
		if at := f.Scheduled(direction); at != nil && at.After(now.Add(24*time.Hour)) {
			report.NextCheckAt = now.Add(time.Hour)
		} else if at != nil && at.After(now.Add(3*time.Hour)) {
			report.NextCheckAt = now.Add(15 * time.Minute)
		}
	} else if at, e := provider.SwedaviaDate(date, now); e == nil && at.After(now.Add(24*time.Hour)) {
		report.NextCheckAt = now.Add(time.Hour)
	}
	if f := report.Flight; f != nil {
		if f.StatusCode == "LAN" || f.StatusCode == "DEL" {
			report.NextCheckAt = now.Add(time.Hour)
		}
		if f.StatusCode == "CAN" {
			report.NextCheckAt = now.Add(15 * time.Minute)
		}
	}
	update := flight.ScheduleUpdate{Report: report}
	if report.Flight != nil {
		f := report.Flight
		route := flight.Route{FlightNumber: f.FlightID, Callsign: f.Callsign, Airline: f.Airline, Origin: f.Origin, Destination: f.Destination}
		w.geometryFor(ctx, &route)
		update.Route = &route
	}
	w.last = update
	return w.evaluate(update, now, aircraft)
}

func (w *Watcher) evaluate(update flight.ScheduleUpdate, now time.Time, aircraft *flight.Aircraft) flight.ScheduleUpdate {
	r := update.Report
	if r == nil {
		return update
	}
	copy := *r
	update.Report = &copy
	r = &copy
	if r.Match != "matched" || r.Stale || r.Flight == nil {
		update.Waiting = w.ExplicitDate || w.pinned != ""
		update.Reason = "waiting for a unique, fresh Swedavia record for the selected date"
		return update
	}
	f := r.Flight
	if aircraft != nil && !w.ExplicitDate && (w.Target.Kind == flight.ByHex || w.Target.Kind == flight.ByRegistration) && aircraft.Callsign != "" && f.Callsign != "" && aircraft.Callsign != f.Callsign {
		r.Match, r.Error = "identity_conflict", "current broadcast callsign does not match this airport flight leg"
		update.Reason = r.Error
		return update
	}
	if aircraft != nil && aircraft.Registration != "" && f.Registration != "" && flight.RegistrationID(aircraft.Registration) != f.Registration {
		r.Match, r.Error = "identity_conflict", "airport aircraft assignment differs from the aircraft being tracked"
		update.Waiting = w.ExplicitDate
		update.Reason = r.Error
		return update
	}
	if f.Callsign != "" {
		if target, err := flight.NewTarget(flight.ByCallsign, f.Callsign); err == nil {
			update.Target = &target
		}
	}
	if f.StatusCode == "CAN" || f.StatusCode == "DEL" || f.StatusCode == "LAN" {
		update.Waiting = true
		update.Reason = "airport reports " + StatusName(f.StatusCode)
		return update
	}
	var acquire time.Time
	if f.Departure.Scheduled != nil {
		departure := f.Departure.Scheduled
		if f.Departure.Estimated != nil && f.Departure.Estimated.After(*departure) {
			departure = f.Departure.Estimated
		}
		acquire = departure.Add(-30 * time.Minute)
	} else if f.DepartureDate != nil {
		acquire = *f.DepartureDate
	} else if f.Arrival.Scheduled != nil {
		acquire = f.Arrival.Scheduled.Add(-24 * time.Hour)
	}
	if now.Before(acquire) {
		update.Waiting = true
		update.Reason = "scheduled; aircraft acquisition opens " + acquire.UTC().Format(time.RFC3339)
	}
	if update.Target == nil && w.Target.Kind != flight.ByHex && w.Target.Kind != flight.ByRegistration {
		update.Waiting = true
		update.Reason = "waiting for an operational callsign from Swedavia"
	}
	return update
}

func StatusName(code string) string {
	if code == "" {
		return "UNKNOWN"
	}
	if name, ok := map[string]string{"SCH": "SCHEDULED", "FPL": "FLIGHT PLAN", "FLS": "SUSPENDED", "SEQ": "SEQUENCED", "ACT": "ACTIVE", "CAN": "CANCELLED", "LAN": "LANDED", "RER": "REROUTED", "DIV": "DIVERTED", "DEL": "DELETED"}[code]; ok {
		return name
	}
	return code
}

func (w *Watcher) geometryFor(ctx context.Context, route *flight.Route) {
	if w.Reference != nil {
		if route.Origin.IATA == w.Reference.Origin.IATA {
			route.Origin.Latitude, route.Origin.Longitude, route.Origin.ElevationFeet = w.Reference.Origin.Latitude, w.Reference.Origin.Longitude, w.Reference.Origin.ElevationFeet
		}
		if route.Destination.IATA == w.Reference.Destination.IATA {
			route.Destination.Latitude, route.Destination.Longitude, route.Destination.ElevationFeet = w.Reference.Destination.Latitude, w.Reference.Destination.Longitude, w.Reference.Destination.ElevationFeet
		}
	}
	if w.DestinationOverride != nil || route.Destination.Latitude != nil || w.ResolveAirport == nil || route.Destination.IATA == "" {
		return
	}
	if w.geometry == nil {
		w.geometry = map[string]*flight.Airport{}
	}
	code := route.Destination.IATA
	a, ok := w.geometry[code]
	if !ok {
		a, _ = w.ResolveAirport(ctx, code)
		if a != nil {
			w.geometry[code] = a
		}
	}
	if a != nil {
		route.Destination.Latitude, route.Destination.Longitude, route.Destination.ElevationFeet = a.Latitude, a.Longitude, a.ElevationFeet
	}
}

func (w *Watcher) supplement(ctx context.Context, r *flight.AirportReport, now time.Time) {
	f := r.Flight
	a, dir := covered(f.Destination), "arrivals"
	if r.Direction == "arrivals" {
		a, dir = covered(f.Origin), "departures"
	}
	if a == "" {
		return
	}
	day, _ := provider.SwedaviaDate(r.Date, now)
	offsets := []int{0, 1}
	if dir == "departures" {
		offsets = []int{0, -1}
	}
	for _, offset := range offsets {
		board, err := w.Client.Board(ctx, a, dir, day.AddDate(0, 0, offset).Format("2006-01-02"))
		if err != nil {
			r.Error = "additional airport board unavailable: " + err.Error()
			return
		}
		other, match := Match(board.Flights, MatchRequest{PinnedLeg: f.LegKey()})
		if match != "matched" {
			continue
		}
		if other.FetchedAt.Before(f.FetchedAt) {
			f.FetchedAt = other.FetchedAt
		}
		if dir == "arrivals" {
			f.Arrival = other.Arrival
			if other.StatusCode == "LAN" || other.StatusCode == "CAN" {
				f.StatusCode, f.StatusText = other.StatusCode, other.StatusText
			}
		} else {
			f.Departure = other.Departure
			if other.StatusCode == "CAN" && f.StatusCode != "LAN" {
				f.StatusCode, f.StatusText = other.StatusCode, other.StatusText
			}
		}
		return
	}
}
