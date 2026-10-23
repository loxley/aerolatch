// Package kinematics calculates missing measurements from distinct, fresh
// observations. It never changes feed measurements, altitude, or surface flags.
package kinematics

import (
	"math"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

const (
	maxAge          = 90 * time.Second
	minWindow       = 10 * time.Second
	maxSpeed        = 1200.0  // Knots; reject location jumps before adding a sample.
	maxVerticalRate = 12000.0 // ft/min; reject spikes, not just implausible final regressions.
)

type sample struct {
	at       time.Time
	lat, lon float64
	altitude *float64
}

type Processor struct {
	hex     string
	mode    int // -1 unknown, 0 airborne, 1 surface; transitions break continuity.
	lastFix time.Time
	history []sample
}

// Break discards numeric continuity while retaining the timestamp watermark so
// recovery with the same cached fix cannot create a new baseline observation.
func (p *Processor) Break() { p.history = nil }

// Update returns a separate slice so earlier/provider-owned observations remain
// unchanged. No-result polls add no evidence; ambiguity discards the history.
func (p *Processor) Update(now time.Time, aircraft []flight.Aircraft) []flight.Aircraft {
	if len(aircraft) == 0 {
		p.Break()
		return aircraft
	}
	result := append([]flight.Aircraft(nil), aircraft...)
	for i := range result {
		result[i].Calculated = nil
	}
	if len(result) != 1 {
		p.Break()
		return result
	}
	result[0] = p.enrich(now, result[0])
	return result
}

func (p *Processor) enrich(now time.Time, a flight.Aircraft) flight.Aircraft {
	calculated := &flight.CalculatedKinematics{Unavailable: map[string]string{}}
	missing := func(reason string) flight.Aircraft {
		if a.GroundSpeed == nil {
			calculated.Unavailable["ground_speed"] = reason
		}
		if a.Track == nil {
			calculated.Unavailable["ground_track"] = reason
		}
		if a.VerticalRate == nil {
			verticalReason := reason
			if a.OnGround != nil && *a.OnGround {
				verticalReason = "vertical trend is not calculated on the surface"
			}
			calculated.Unavailable["vertical_rate"] = verticalReason
		}
		if len(calculated.Unavailable) > 0 {
			a.Calculated = calculated
		}
		return a
	}
	hex, err := flight.NewTarget(flight.ByHex, a.Hex)
	if err != nil {
		*p = Processor{}
		return missing("a unique ICAO aircraft address is required")
	}
	mode := -1
	if a.OnGround != nil {
		mode = 0
		if *a.OnGround {
			mode = 1
		}
	}
	transition := p.hex == hex.Value && p.mode != mode
	if p.hex != hex.Value {
		*p = Processor{hex: hex.Value, mode: mode}
	} else if transition {
		last := p.lastFix
		*p = Processor{hex: hex.Value, mode: mode, lastFix: last}
	}
	if !geo.ValidCoordinates(a.Latitude, a.Longitude) || a.PositionUpdated == nil {
		p.history = nil
		return missing("valid position and position timestamp required")
	}
	fix := a.PositionUpdated.UTC()
	if now.Sub(fix) > maxAge || fix.After(now.Add(5*time.Second)) {
		p.Break()
		return missing("position is stale or ahead of the local clock")
	}
	if a.LastContact != nil && (now.Sub(*a.LastContact) > maxAge || a.LastContact.After(now.Add(5*time.Second))) {
		p.Break()
		return missing("telemetry timestamp is stale or ahead of the local clock")
	}
	if fix.Before(p.lastFix) {
		p.Break()
		return missing("out-of-order position ignored")
	}
	for len(p.history) > 0 && p.history[0].at.Before(now.Add(-2*time.Minute)) {
		p.history = p.history[1:]
	}
	if transition && !fix.After(p.lastFix) {
		return missing("air/ground transition; waiting for a fresh position")
	}
	if fix.Equal(p.lastFix) && len(p.history) > 0 {
		last := p.history[len(p.history)-1]
		if last.lat != *a.Latitude || last.lon != *a.Longitude {
			p.Break()
			return missing("position changed without a new timestamp")
		}
	}
	if fix.After(p.lastFix) {
		current := sample{at: fix, lat: *a.Latitude, lon: *a.Longitude}
		if finite(a.AltitudeFeet) {
			v := *a.AltitudeFeet
			current.altitude = &v
		}
		if len(p.history) > 0 {
			last := p.history[len(p.history)-1]
			gap := fix.Sub(last.at)
			if gap > maxAge {
				p.history = nil
			} else {
				speed := geo.DistanceNM(last.lat, last.lon, current.lat, current.lon) / gap.Hours()
				if !number(speed) || speed > maxSpeed {
					p.lastFix, p.history = fix, []sample{current}
					return missing("implausible position jump; collecting new history")
				}
				if current.altitude != nil {
					// Find the last actual altitude sample, not an interpolated value.
					for i := len(p.history) - 1; i >= 0; i-- {
						if previous := p.history[i]; previous.altitude != nil {
							rate := (*current.altitude - *previous.altitude) / fix.Sub(previous.at).Minutes()
							if !number(rate) || math.Abs(rate) > maxVerticalRate {
								for j := range p.history {
									p.history[j].altitude = nil
								}
							}
							break
						}
					}
				}
			}
		}
		p.lastFix = fix
		p.history = append(p.history, current)
		if len(p.history) > 64 {
			p.history = p.history[len(p.history)-64:]
		}
	}
	// A cached fix may reuse its original calculation window, but cannot
	// manufacture new samples or continue using anchors that have gone stale.
	anchor := -1
	for i := len(p.history) - 2; i >= 0; i-- {
		past := p.history[i]
		if now.Sub(past.at) <= maxAge && fix.Sub(past.at) >= minWindow && fix.Sub(past.at) <= maxAge {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return missing("collecting distinct fresh fixes spanning 10–90 seconds")
	}
	points := p.history[anchor:]
	first, last := points[0], points[len(points)-1]
	distance := 0.0
	for i := 1; i < len(points); i++ {
		distance += geo.DistanceNM(points[i-1].lat, points[i-1].lon, points[i].lat, points[i].lon)
	}
	speed := distance / last.at.Sub(first.at).Hours()
	if !number(speed) || speed > maxSpeed {
		return missing("implausible calculated ground speed")
	}
	measure := func(value float64, unit, method string) *flight.CalculatedMeasurement {
		return &flight.CalculatedMeasurement{Value: value, Unit: unit, WindowStart: first.at, WindowEnd: last.at, Samples: len(points), Method: method}
	}
	if a.GroundSpeed == nil {
		calculated.GroundSpeed = measure(speed, "kt", "position_path_distance / elapsed_time")
	}
	if a.Track == nil {
		displacement := geo.DistanceNM(first.lat, first.lon, last.lat, last.lon)
		if displacement >= 0.03 && speed >= 5 && (!finite(a.GroundSpeed) || *a.GroundSpeed >= 5) {
			// Final great-circle bearing estimates track at the latest endpoint.
			track := math.Mod(geo.BearingDegrees(last.lat, last.lon, first.lat, first.lon)+180, 360)
			calculated.GroundTrack = measure(track, "deg", "final_bearing_of_recent_displacement")
		} else {
			calculated.Unavailable["ground_track"] = "movement too small or slow to establish ground track"
		}
	}
	if a.VerticalRate == nil {
		calculated.VerticalRate, calculated.Unavailable["vertical_rate"] = p.vertical(now, fix, a.AltitudeFeet)
		if calculated.Unavailable["vertical_rate"] == "" {
			delete(calculated.Unavailable, "vertical_rate")
		}
	}
	if calculated.GroundSpeed != nil || calculated.GroundTrack != nil || calculated.VerticalRate != nil || len(calculated.Unavailable) > 0 {
		a.Calculated = calculated
	}
	return a
}

func (p *Processor) vertical(now, fix time.Time, currentAltitude *float64) (*flight.CalculatedMeasurement, string) {
	if p.mode == 1 {
		return nil, "vertical trend is not calculated on the surface"
	}
	if !finite(currentAltitude) {
		return nil, "current barometric altitude unavailable"
	}
	var points []sample
	for _, s := range p.history {
		if s.altitude != nil && now.Sub(s.at) <= maxAge && fix.Sub(s.at) <= maxAge {
			points = append(points, s)
		}
	}
	if len(points) == 0 || !points[len(points)-1].at.Equal(fix) {
		return nil, "waiting for altitude associated with a new position observation"
	}
	if len(points) < 3 || points[len(points)-1].at.Sub(points[0].at) < 30*time.Second {
		return nil, "collecting at least 3 altitude samples over 30–90 seconds"
	}
	start, end := points[0].at, points[len(points)-1].at
	// Center times/altitudes to keep the least-squares fit independent of epoch.
	meanTime, meanAltitude := 0.0, 0.0
	for _, s := range points {
		meanTime += s.at.Sub(start).Seconds()
		meanAltitude += *s.altitude
	}
	meanTime /= float64(len(points))
	meanAltitude /= float64(len(points))
	numerator, denominator := 0.0, 0.0
	for _, s := range points {
		dt := s.at.Sub(start).Seconds() - meanTime
		numerator += dt * (*s.altitude - meanAltitude)
		denominator += dt * dt
	}
	rate := numerator / denominator * 60
	if !number(rate) || math.Abs(rate) > maxVerticalRate {
		return nil, "implausible barometric altitude trend"
	}
	return &flight.CalculatedMeasurement{Value: rate, Unit: "ft/min", WindowStart: start, WindowEnd: end, Samples: len(points), Method: "least_squares_barometric_altitude_trend"}, ""
}

func finite(value *float64) bool { return value != nil && number(*value) }
func number(value float64) bool  { return !math.IsNaN(value) && !math.IsInf(value, 0) }
