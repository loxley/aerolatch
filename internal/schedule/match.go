// Package schedule matches dated airport records and enriches live tracking.
package schedule

import (
	"regexp"
	"strings"

	"aerolatch/internal/flight"
)

type MatchRequest struct {
	Query               string
	Target              flight.Target
	Origin, Destination string // Explicit hints only; reference routes may be stale.
	PinnedLeg           string
	IncludeDeleted      bool
	Aircraft            *flight.Aircraft
}

var numbered = regexp.MustCompile(`^([A-Z0-9]{2})([0-9]+)([A-Z]?)$`)

func flightID(value string) string {
	value = strings.ToUpper(strings.Join(strings.Fields(value), ""))
	if parts := numbered.FindStringSubmatch(value); parts != nil {
		n := strings.TrimLeft(parts[2], "0")
		if n == "" {
			n = "0"
		}
		return parts[1] + n + parts[3]
	}
	return value
}

func airportMatches(a flight.Airport, hint string) bool {
	return hint == "" || strings.EqualFold(a.IATA, hint) || strings.EqualFold(a.ICAO, hint)
}

// Match never substitutes a nearby date or chooses arbitrarily between legs.
// Deleted records are not cancellations and cannot acquire an aircraft.
func Match(records []flight.ScheduledFlight, r MatchRequest) (*flight.ScheduledFlight, string) {
	best := 0
	matches := map[string]flight.ScheduledFlight{}
	for _, f := range records {
		if (f.StatusCode == "DEL" && !r.IncludeDeleted) || !airportMatches(f.Origin, r.Origin) || !airportMatches(f.Destination, r.Destination) {
			continue
		}
		score := 0
		if r.PinnedLeg != "" {
			if f.LegKey() != r.PinnedLeg {
				continue
			}
			score = 100
		} else {
			query := flightID(r.Query)
			if query != "" && (flightID(f.FlightID) == query || strings.EqualFold(f.Callsign, r.Query)) {
				score = 10
			}
			for _, code := range f.Codeshares {
				if query != "" && flightID(code) == query {
					score = 10
				}
			}
			if r.Target.Kind == flight.ByCallsign && strings.EqualFold(f.Callsign, r.Target.Value) {
				score = 10
			}
			if r.Target.Kind == flight.ByRegistration && flight.RegistrationID(r.Target.Value) == f.Registration && f.Registration != "" {
				score = 3
			}
			if r.Target.Kind == flight.ByHex || r.Target.Kind == flight.ByRegistration {
				if a := r.Aircraft; a != nil {
					if a.Callsign != "" && strings.EqualFold(a.Callsign, f.Callsign) {
						score += 10
					}
					if a.Registration != "" && flight.RegistrationID(a.Registration) == f.Registration {
						score += 3
					}
				}
			}
		}
		if score == 0 || score < best {
			continue
		}
		if score > best {
			matches = map[string]flight.ScheduledFlight{}
			best = score
		}
		key := f.LegKey()
		if old, ok := matches[key]; ok && (old.StatusCode != f.StatusCode || !sameTime(old.Scheduled("arrivals"), f.Scheduled("arrivals")) || !sameTime(old.Scheduled("departures"), f.Scheduled("departures"))) {
			// Conflicting duplicate rows require an explicit selection, not an
			// arbitrary preference for scheduled/landed/cancelled.
			key += "/conflicting-row/" + f.StatusCode
		}
		matches[key] = f
	}
	if len(matches) == 0 {
		return nil, "not_found"
	}
	if len(matches) > 1 {
		return nil, "ambiguous"
	}
	for _, f := range matches {
		return &f, "matched"
	}
	return nil, "not_found"
}
