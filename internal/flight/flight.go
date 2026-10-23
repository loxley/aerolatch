// Package flight contains provider-independent flight and observation models.
package flight

import "time"

type Airport struct {
	IATA          string   `json:"iata_code"`
	ICAO          string   `json:"icao_code"`
	Name          string   `json:"name"`
	Latitude      *float64 `json:"latitude"`
	Longitude     *float64 `json:"longitude"`
	ElevationFeet *float64 `json:"elevation"` // adsbdb airport elevations are in feet.
}

// Route is reference data, not a date-specific schedule or confirmed live route.
type Route struct {
	FlightNumber string  `json:"flight_number,omitempty"`
	Callsign     string  `json:"callsign"`
	Airline      string  `json:"airline,omitempty"`
	Origin       Airport `json:"origin"`
	Destination  Airport `json:"destination"`
}

// Destination preserves the selected airport independently of reference data.
type Destination struct {
	Airport    Airport `json:"airport"`
	Source     string  `json:"source"`
	DataSource string  `json:"data_source"`
}

// ArrivalAssessment describes observations within this session, not an airline
// status. Its timestamps delimit the evidence; none is an exact touchdown time.
type ArrivalAssessment struct {
	Status         string            `json:"status"`
	Reason         string            `json:"reason"`
	AircraftHex    string            `json:"aircraft_hex,omitempty"`
	Historical     bool              `json:"historical"`
	DistanceNM     *float64          `json:"distance_nm"`
	LastAirborneAt *time.Time        `json:"last_airborne_at,omitempty"`
	FirstSurfaceAt *time.Time        `json:"first_surface_at,omitempty"`
	LastSurfaceAt  *time.Time        `json:"last_surface_at,omitempty"`
	SurfaceReports int               `json:"surface_reports,omitempty"`
	CorroboratedAt *time.Time        `json:"corroborated_at,omitempty"`
	LastObservedAt *time.Time        `json:"last_observed_at,omitempty"`
	Approach       *ApproachEvidence `json:"approach_evidence,omitempty"`
	MissingSince   *time.Time        `json:"missing_since,omitempty"`
	EmptyResponses int               `json:"empty_responses,omitempty"`
	AssessedAt     *time.Time        `json:"assessed_at,omitempty"`
}

// ApproachEvidence summarizes previously fresh fixes. It is neither a current
// position nor a touchdown observation, even when attached to a new assessment.
type ApproachEvidence struct {
	StartedAt        time.Time `json:"started_at"`
	LastPositionAt   time.Time `json:"last_position_at"`
	ObservationCount int       `json:"observation_count"`
	LastDistanceNM   float64   `json:"last_distance_nm"`
	LastHeightFeet   float64   `json:"last_height_ft"`
	LastSpeedKnots   float64   `json:"last_ground_speed_kt"`
	SpeedSource      string    `json:"speed_source,omitempty"`
	CalculatedInputs []string  `json:"calculated_inputs,omitempty"`
	AltitudeDropFeet float64   `json:"altitude_drop_ft"`
	ProgressNM       float64   `json:"progress_nm"`
}

type Aircraft struct {
	Hex             string                `json:"hex"`
	Callsign        string                `json:"callsign"`
	Registration    string                `json:"registration,omitempty"`
	Type            string                `json:"type,omitempty"`
	Latitude        *float64              `json:"latitude"`
	Longitude       *float64              `json:"longitude"`
	AltitudeFeet    *float64              `json:"altitude_ft"`
	OnGround        *bool                 `json:"on_ground"`
	GroundSpeed     *float64              `json:"ground_speed_kt"`
	Track           *float64              `json:"ground_track_deg"`
	VerticalRate    *float64              `json:"vertical_rate_ft_min"`
	LastContact     *time.Time            `json:"last_contact"`
	PositionUpdated *time.Time            `json:"position_updated"`
	Calculated      *CalculatedKinematics `json:"calculated,omitempty"`
}

// LandingEstimate is an uncalibrated touchdown heuristic, not an operational ETA.
// An unavailable estimate retains its explanation but never a previous ETA.
type LandingEstimate struct {
	Status              string     `json:"status"`
	Reason              string     `json:"reason,omitempty"`
	Quality             string     `json:"quality,omitempty"`
	Phase               string     `json:"phase,omitempty"`
	DistanceNM          *float64   `json:"distance_nm"`
	TouchdownAt         *time.Time `json:"touchdown_at"`
	MinutesRemaining    *float64   `json:"minutes_remaining"`
	BasedOnPositionAt   *time.Time `json:"based_on_position_at"`
	SmoothedGroundSpeed *float64   `json:"smoothed_ground_speed_kt"`
	CalculatedInputs    []string   `json:"calculated_inputs,omitempty"`
}

// Snapshot is one polling result. LastObserved is retained only within this run
// and is deliberately separate from the current aircraft list.
type Snapshot struct {
	CheckedAt       time.Time          `json:"checked_at"`
	Query           string             `json:"query"`
	Callsign        string             `json:"callsign"`
	Route           *Route             `json:"reference_route,omitempty"`
	Source          string             `json:"source"`
	Status          string             `json:"status"`
	Aircraft        []Aircraft         `json:"aircraft"`
	LastObserved    []Aircraft         `json:"last_observed,omitempty"`
	LastSeenAt      *time.Time         `json:"last_seen_at,omitempty"`
	Error           string             `json:"error,omitempty"`
	RetryAt         *time.Time         `json:"retry_at,omitempty"`
	LandingEstimate *LandingEstimate   `json:"landing_estimate,omitempty"`
	Destination     *Destination       `json:"destination,omitempty"`
	Arrival         *ArrivalAssessment `json:"arrival_assessment,omitempty"`
	Tracking        *TrackingInfo      `json:"tracking,omitempty"`
	AirportReport   *AirportReport     `json:"airport_report,omitempty"`
	RouteSource     string             `json:"route_source,omitempty"`
}
