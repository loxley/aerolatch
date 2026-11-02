package flight

import "time"

// Selection carries the original area observation into physical-aircraft tracking.
type Selection struct {
	Aircraft   Aircraft
	ObservedAt time.Time
}

// InspectionInfo separates remembered identity, reference data and request
// progress from current reported telemetry. It is only used by nearby detail.
type InspectionInfo struct {
	UpdatedAt          time.Time  `json:"updated_at"`
	LastKnownCallsign  string     `json:"last_known_callsign,omitempty"`
	CallsignObservedAt *time.Time `json:"callsign_observed_at,omitempty"`
	RouteStatus        string     `json:"route_status"`
	RouteCallsign      string     `json:"route_callsign,omitempty"`
	RouteError         string     `json:"route_error,omitempty"`
	RequestState       string     `json:"request_state,omitempty"`
	RequestStartedAt   *time.Time `json:"request_started_at,omitempty"`
	WaitUntil          *time.Time `json:"wait_until,omitempty"`
	SwedaviaStatus     string     `json:"swedavia_status,omitempty"`
}
