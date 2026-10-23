package flight

import (
	"strings"
	"time"
)

type FlightTimes struct {
	Scheduled *time.Time `json:"scheduled_utc,omitempty"`
	Estimated *time.Time `json:"estimated_utc,omitempty"`
	Actual    *time.Time `json:"actual_utc,omitempty"`
}

// ScheduledFlight is airport-reported information, never aircraft telemetry.
type ScheduledFlight struct {
	FlightID      string      `json:"flight_id"`
	Callsign      string      `json:"callsign,omitempty"`
	Registration  string      `json:"registration,omitempty"`
	Airline       string      `json:"airline,omitempty"`
	Origin        Airport     `json:"origin"`
	Destination   Airport     `json:"destination"`
	Departure     FlightTimes `json:"departure"`
	Arrival       FlightTimes `json:"arrival"`
	DepartureDate *time.Time  `json:"departure_date_utc,omitempty"`
	Codeshares    []string    `json:"codeshares,omitempty"`
	StatusCode    string      `json:"status_code"`
	StatusText    string      `json:"status_text"`
	Terminal      string      `json:"terminal,omitempty"`
	Gate          string      `json:"gate,omitempty"`
	FetchedAt     time.Time   `json:"fetched_at"`
	DataUpdatedAt *time.Time  `json:"data_updated_at,omitempty"`
}

func (f ScheduledFlight) Scheduled(direction string) *time.Time {
	if direction == "departures" {
		return f.Departure.Scheduled
	}
	return f.Arrival.Scheduled
}

// LegKey excludes mutable assignments and estimates. The operating ID, UTC
// departure day and route identify the leg across refreshes and codeshares.
func (f ScheduledFlight) LegKey() string {
	day := ""
	if f.DepartureDate != nil {
		day = f.DepartureDate.UTC().Format("2006-01-02")
	}
	return f.FlightID + "/" + day + "/" + f.Origin.IATA + "/" + f.Destination.IATA
}

type FlightBoard struct {
	Source    string            `json:"source"`
	Airport   string            `json:"airport"`
	Direction string            `json:"direction"`
	Date      string            `json:"date"`
	TimeZone  string            `json:"time_zone"`
	FetchedAt time.Time         `json:"fetched_at"`
	Flights   []ScheduledFlight `json:"flights"`
}

type AirportReport struct {
	Source      string           `json:"source"`
	Airport     string           `json:"airport"`
	Direction   string           `json:"direction"`
	Date        string           `json:"date"`
	Match       string           `json:"match"` // matched, not_found, ambiguous, unavailable, identity_conflict
	Flight      *ScheduledFlight `json:"flight,omitempty"`
	Stale       bool             `json:"stale"`
	Error       string           `json:"error,omitempty"`
	NextCheckAt time.Time        `json:"next_check_at"`
}

type ScheduleUpdate struct {
	Report  *AirportReport
	Route   *Route
	Target  *Target
	Waiting bool
	Reason  string
}

func RegistrationID(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.Join(strings.Fields(value), ""), "-", ""))
}
