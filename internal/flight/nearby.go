package flight

import (
	"strings"
	"time"
)

// Location is a fixed search centre, not a measurement of the user's position.
type Location struct {
	ID          int64   `json:"id,omitempty"`
	Name        string  `json:"name"`
	Region      string  `json:"region,omitempty"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Source      string  `json:"source"` // city or coordinates
	DataSource  string  `json:"data_source,omitempty"`
}

func (l Location) Label() string {
	parts := []string{l.Name}
	if l.Region != "" && l.Region != l.Name {
		parts = append(parts, l.Region)
	}
	if l.Country != "" {
		parts = append(parts, l.Country)
	}
	return strings.Join(parts, ", ")
}

type NearbyAircraft struct {
	Aircraft
	DistanceNM     float64 `json:"distance_nm"`
	BearingDegrees float64 `json:"bearing_deg"` // From the search centre, true north.
	Direction      string  `json:"direction"`
}

type NearbySnapshot struct {
	CheckedAt    time.Time        `json:"checked_at"`
	Location     Location         `json:"location"`
	RadiusNM     int              `json:"radius_nm"`
	Source       string           `json:"source"`
	Status       string           `json:"status"`
	Aircraft     []NearbyAircraft `json:"aircraft"`
	Excluded     int              `json:"excluded"` // Missing/stale/invalid fixes, duplicates, or outside radius.
	LastObserved []NearbyAircraft `json:"last_observed,omitempty"`
	LastSeenAt   *time.Time       `json:"last_seen_at,omitempty"`
	Error        string           `json:"error,omitempty"`
	RetryAt      *time.Time       `json:"retry_at,omitempty"`
}
