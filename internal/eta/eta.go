// Package eta estimates touchdown using an intentionally simple fixed-wing
// flight model. Its assumptions are heuristics, not calibrated arrival forecasts.
package eta

import (
	"math"
	"sort"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

const (
	maxAge        = 90 * time.Second
	historyWindow = 2 * time.Minute
	maxSamples    = 64
)

type sample struct {
	at       time.Time
	lat, lon float64
	distance float64
	speed    float64
}

// Estimator belongs to one tracking session. History is based on provider
// position timestamps, so repeated cached polls do not create fake progress.
type Estimator struct {
	identity string
	history  []sample
}

func Unavailable(reason string) *flight.LandingEstimate {
	return &flight.LandingEstimate{Status: "unavailable", Reason: reason}
}

func (e *Estimator) Update(now time.Time, route *flight.Route, aircraft []flight.Aircraft) *flight.LandingEstimate {
	if len(aircraft) == 0 {
		return Unavailable("no current live aircraft observation")
	}
	if len(aircraft) != 1 {
		e.history = nil
		return Unavailable("multiple aircraft match; no unique flight to estimate")
	}
	if route == nil {
		return Unavailable("destination unavailable in direct tracking mode; use --destination")
	}
	dest := route.Destination
	if !geo.ValidCoordinates(dest.Latitude, dest.Longitude) {
		return Unavailable("destination coordinates unavailable or invalid")
	}
	a := aircraft[0]
	groundSpeed, track, verticalRate := a.EffectiveGroundSpeed(), a.EffectiveTrack(), a.EffectiveVerticalRate()
	if !geo.ValidCoordinates(a.Latitude, a.Longitude) {
		return Unavailable("aircraft position unavailable or invalid")
	}
	if a.PositionUpdated == nil {
		return Unavailable("position timestamp unavailable")
	}
	age := now.Sub(*a.PositionUpdated)
	if age < -5*time.Second {
		return Unavailable("position timestamp is ahead of the local clock")
	}
	if age > maxAge {
		return Unavailable("position is stale (more than 90 seconds old)")
	}
	result := Unavailable("")
	result.CalculatedInputs = a.CalculatedInputs()
	distance := geo.DistanceNM(*a.Latitude, *a.Longitude, *dest.Latitude, *dest.Longitude)
	result.DistanceNM = &distance
	result.BasedOnPositionAt = a.PositionUpdated
	withhold := func(reason string) *flight.LandingEstimate {
		result.Reason = reason
		return result
	}
	if a.OnGround != nil && *a.OnGround {
		return withhold("aircraft reports on ground; this does not confirm arrival")
	}
	if a.LastContact != nil && (now.Sub(*a.LastContact) > maxAge || a.LastContact.After(now.Add(5*time.Second))) {
		return withhold("telemetry timestamp is stale or ahead of the local clock")
	}
	if !finite(groundSpeed) {
		return withhold("ground speed unavailable")
	}
	if *groundSpeed < 80 || *groundSpeed > 800 {
		return withhold("ground speed outside the fixed-wing model (80–800 kt)")
	}
	if !finite(a.AltitudeFeet) || !finite(dest.ElevationFeet) {
		return withhold("aircraft altitude or destination elevation unavailable")
	}
	height := *a.AltitudeFeet - *dest.ElevationFeet
	if height < -1500 || height > 60000 {
		return withhold("altitude is inconsistent with the destination elevation")
	}
	height = max(0, height)
	identity := strings.ToLower(strings.TrimSpace(a.Hex))
	if identity == "" {
		identity = "callsign:" + a.Callsign
	}
	if identity != e.identity {
		e.identity, e.history = identity, nil
	}
	// Drop old observations even if the most recent poll repeats a cached fix.
	for len(e.history) > 0 && e.history[0].at.Before(now.Add(-historyWindow)) {
		e.history = e.history[1:]
	}
	if len(e.history) > 0 && a.PositionUpdated.Before(e.history[len(e.history)-1].at) {
		return withhold("out-of-order position; waiting for a newer observation")
	}
	if len(e.history) == 0 || a.PositionUpdated.After(e.history[len(e.history)-1].at) {
		e.history = append(e.history, sample{at: *a.PositionUpdated, lat: *a.Latitude, lon: *a.Longitude, distance: distance, speed: *groundSpeed})
		if len(e.history) > maxSamples {
			e.history = e.history[len(e.history)-maxSamples:]
		}
	}
	speeds := make([]float64, 0, len(e.history))
	for _, s := range e.history {
		speeds = append(speeds, s.speed)
	}
	sort.Float64s(speeds)
	speed := speeds[len(speeds)/2]
	if len(speeds)%2 == 0 {
		speed = (speed + speeds[len(speeds)/2-1]) / 2
	}
	result.SmoothedGroundSpeed = &speed
	progressKnown := false
	if len(e.history) > 1 {
		first, last := e.history[0], e.history[len(e.history)-1]
		elapsed := last.at.Sub(first.at)
		if elapsed >= 30*time.Second {
			progressKnown = true
			progress := first.distance - last.distance
			if progress < -0.5 && progress/elapsed.Hours() < -30 {
				return withhold("moving away from the destination")
			}
			if elapsed >= 90*time.Second {
				path := 0.0
				for i := 1; i < len(e.history); i++ {
					p, q := e.history[i-1], e.history[i]
					path += geo.DistanceNM(p.lat, p.lon, q.lat, q.lon)
				}
				if path > 2 && progress/path < 0.2 {
					return withhold("little progress toward destination; possible holding or vectoring")
				}
			}
		}
	}
	if finite(track) && *track >= 0 && *track < 360 {
		bearing := geo.BearingDegrees(*a.Latitude, *a.Longitude, *dest.Latitude, *dest.Longitude)
		difference := math.Abs(math.Remainder(*track-bearing, 360))
		if distance > 1 && difference > 100 {
			return withhold("ground track is away from the destination")
		}
	} else if !progressKnown {
		return withhold("need a ground track or more position history")
	}
	result.Phase = "cruise"
	if finite(verticalRate) && *verticalRate > 500 {
		result.Phase = "climb"
		if distance < 30 && height < 10000 {
			return withhold("climbing near destination; possible go-around or departure")
		}
	} else if distance <= 25 && height <= 8000 {
		result.Phase = "approach"
	} else if height < 20000 || (finite(verticalRate) && *verticalRate < -300) {
		result.Phase = "descent"
	}
	// Reserve up to 20 nm for an approach at at most 160 kt and the preceding
	// 80 nm for descent at at most 280 kt. Only the remaining segment uses the
	// full smoothed ground speed. Near final, current speed gets more weight.
	approachDistance := min(distance, 20)
	descentDistance := min(max(distance-20, 0), 80)
	cruiseDistance := max(distance-100, 0)
	approachSpeed := min(speed, 160)
	if result.Phase == "approach" {
		approachSpeed = min(*groundSpeed, 160)
	}
	minutes := 60 * (cruiseDistance/speed + descentDistance/min(speed, 280) + approachDistance/approachSpeed)
	// Avoid extrapolating the current vertical rate to the runway: descents
	// often level off. A nominal 1500 ft/min imposes an altitude time budget.
	minutes = max(minutes, height/1500)
	// A small routing allowance tapers to zero at the airport, rather than
	// always adding several minutes even on short final.
	minutes += min(2, distance/10)
	// The projection starts at the fix time, not the fetch time. Do not move
	// an unchanged ETA into the future on every repeated cached response.
	touchdown := a.PositionUpdated.Add(time.Duration(minutes * float64(time.Minute)))
	if !touchdown.After(now) {
		return withhold("projected touchdown has passed; waiting for a fresh position")
	}
	remaining := touchdown.Sub(now).Minutes()
	result.Status, result.Quality = "estimated", "rough"
	result.Reason = "heuristic using selected destination; approach and routing may change"
	result.TouchdownAt, result.MinutesRemaining = &touchdown, &remaining
	return result
}

func finite(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}
