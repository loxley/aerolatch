package tracker

import (
	"math"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

const subsequentDepartureReason = "aircraft departed after at least 10 minutes of observed surface activity at the selected destination; previous-flight arrival estimates disabled; start a new session for the next leg"

type identity struct {
	target                           flight.Target
	hex                              string
	lockedAt                         *time.Time
	groundStart, lastGround, lastFix time.Time
	groundDwell                      bool
	departed                         bool
}

func newIdentity(options Options) identity {
	target := options.Target
	if target.Kind == "" {
		target = flight.Target{Kind: flight.ByCallsign, Value: options.Callsign}
	}
	i := identity{target: target}
	if target.Kind == flight.ByHex {
		i.hex = target.Value
	}
	return i
}

func (i identity) lookup() flight.Target {
	if i.hex != "" {
		return flight.Target{Kind: flight.ByHex, Value: i.hex}
	}
	return i.target
}

func recent(at *time.Time, now time.Time) bool {
	return at != nil && now.Sub(*at) <= 90*time.Second && !at.After(now.Add(5*time.Second))
}

func (i *identity) observe(now time.Time, airport *flight.Airport, aircraft []flight.Aircraft) {
	if len(aircraft) != 1 {
		return
	}
	a := aircraft[0]
	contact := a.LastContact
	if contact == nil {
		contact = a.PositionUpdated
	}
	if i.lockedAt == nil && recent(contact, now) {
		if hex, err := flight.NewTarget(flight.ByHex, a.Hex); err == nil && (i.hex == "" || i.hex == hex.Value) {
			i.hex, i.lockedAt = hex.Value, &now
		}
	}
	if i.hex == "" || a.Hex != i.hex || !recent(a.PositionUpdated, now) || (a.LastContact != nil && !recent(a.LastContact, now)) || !a.PositionUpdated.After(i.lastFix) {
		return
	}
	i.lastFix = *a.PositionUpdated
	if a.OnGround == nil {
		i.groundStart, i.lastGround = time.Time{}, time.Time{}
		return
	}
	if !*a.OnGround {
		if i.groundDwell {
			i.departed = true
		}
		i.groundStart, i.lastGround = time.Time{}, time.Time{}
		return
	}
	if airport == nil || !geo.ValidCoordinates(airport.Latitude, airport.Longitude) || !geo.ValidCoordinates(a.Latitude, a.Longitude) {
		i.groundStart, i.lastGround = time.Time{}, time.Time{}
		return
	}
	if geo.DistanceNM(*a.Latitude, *a.Longitude, *airport.Latitude, *airport.Longitude) > 3 || (a.GroundSpeed != nil && (math.IsNaN(*a.GroundSpeed) || math.IsInf(*a.GroundSpeed, 0) || *a.GroundSpeed < 0 || *a.GroundSpeed > 80)) {
		i.groundStart, i.lastGround = time.Time{}, time.Time{}
		return
	}
	if i.groundStart.IsZero() || a.PositionUpdated.Sub(i.lastGround) > 90*time.Second {
		i.groundStart = *a.PositionUpdated
	}
	i.lastGround = *a.PositionUpdated
	if i.lastGround.Sub(i.groundStart) >= 10*time.Minute {
		i.groundDwell = true
	}
}

func (i identity) info(lookup flight.Target) *flight.TrackingInfo {
	reason := "searching for a unique, recently observed aircraft ICAO hex address"
	if i.hex != "" {
		reason = "following this aircraft by hex address, including missing or changed callsigns"
	}
	state := "tracking"
	if i.departed {
		state, reason = "subsequent_departure", subsequentDepartureReason
	}
	return &flight.TrackingInfo{Lookup: lookup, LockedHex: i.hex, LockedAt: i.lockedAt, FlightState: state, Reason: reason}
}
