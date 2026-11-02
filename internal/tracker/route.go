package tracker

import (
	"math"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

type referenceObservation struct {
	at       time.Time
	aircraft []flight.Aircraft
	failed   bool
}

func usableAirport(airport *flight.Airport) bool {
	return airport != nil && geo.ValidCoordinates(airport.Latitude, airport.Longitude) && airport.ElevationFeet != nil &&
		!math.IsNaN(*airport.ElevationFeet) && !math.IsInf(*airport.ElevationFeet, 0)
}

// routeInputs is the common destination selection for startup and later
// reference updates. An explicit destination retains priority over a reference.
func routeInputs(options Options, reference *flight.Route) (*flight.Route, *flight.Destination, *flight.Airport) {
	effective := reference
	var destination *flight.Destination
	if options.Destination != nil {
		destination = &flight.Destination{Airport: *options.Destination, Source: "user_override", DataSource: "OurAirports"}
		route := flight.Route{Callsign: options.Callsign}
		if reference != nil {
			route = *reference
		}
		route.Destination = *options.Destination
		effective = &route
	} else if reference != nil {
		destination = &flight.Destination{Airport: reference.Destination, Source: "adsbdb", DataSource: "adsbdb"}
	}
	if destination == nil {
		return effective, nil, nil
	}
	return effective, destination, &destination.Airport
}

func routeCallsign(route *flight.Route) string {
	if route == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(route.Callsign))
}

func sameAirport(a, b *flight.Airport) bool {
	if a == nil || b == nil {
		return a == b
	}
	equal := func(a, b *float64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
	return strings.EqualFold(a.IATA, b.IATA) && strings.EqualFold(a.ICAO, b.ICAO) &&
		equal(a.Latitude, b.Latitude) && equal(a.Longitude, b.Longitude) && equal(a.ElevationFeet, b.ElevationFeet)
}
