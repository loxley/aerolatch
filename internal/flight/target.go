package flight

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type TargetKind string

const (
	ByCallsign     TargetKind = "callsign"
	ByHex          TargetKind = "hex"
	ByRegistration TargetKind = "registration"
)

// Target names the actual lookup key, independently of the passenger flight
// number in Snapshot.Query and of the aircraft's changing broadcast callsign.
type Target struct {
	Kind  TargetKind `json:"kind"`
	Value string     `json:"value"`
}

var (
	validCallsign     = regexp.MustCompile(`^[A-Z0-9]{2,8}$`)
	validHex          = regexp.MustCompile(`^[0-9a-f]{6}$`)
	validRegistration = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,10}[A-Z0-9]$`)
)

func NewTarget(kind TargetKind, value string) (Target, error) {
	value = strings.TrimSpace(value)
	switch kind {
	case ByCallsign:
		value = strings.ToUpper(strings.Join(strings.Fields(value), ""))
		if !validCallsign.MatchString(value) {
			return Target{}, fmt.Errorf("callsign must contain 2–8 letters and digits")
		}
	case ByHex:
		value = strings.ToLower(value)
		if !validHex.MatchString(value) {
			return Target{}, fmt.Errorf("hex address must contain exactly 6 hexadecimal digits")
		}
	case ByRegistration:
		value = strings.ToUpper(value)
		if !validRegistration.MatchString(value) {
			return Target{}, fmt.Errorf("registration must contain 2–12 letters, digits, or internal hyphens")
		}
	default:
		return Target{}, fmt.Errorf("unknown lookup kind %q", kind)
	}
	return Target{Kind: kind, Value: value}, nil
}

func (t Target) Matches(a Aircraft) bool {
	var value string
	switch t.Kind {
	case ByCallsign:
		value = a.Callsign
	case ByHex:
		value = a.Hex
	case ByRegistration:
		return RegistrationID(a.Registration) == RegistrationID(t.Value)
	default:
		return false
	}
	return strings.EqualFold(strings.TrimSpace(value), t.Value)
}

type TrackingInfo struct {
	Lookup              Target     `json:"lookup"` // Request used for this snapshot; the next poll may use the acquired hex.
	LockedHex           string     `json:"locked_hex,omitempty"`
	LockedAt            *time.Time `json:"locked_at,omitempty"`
	FlightState         string     `json:"flight_state"`
	Reason              string     `json:"reason"`
	LiveLookupPerformed bool       `json:"live_lookup_performed"`
}
