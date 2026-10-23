package arrival

import (
	"math"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

const (
	approachWindow = 2 * time.Minute
	missingDelay   = 2 * time.Minute
	approachExpiry = 10 * time.Minute
)

type approachSample struct {
	at                                time.Time
	lat, lon, distance, height, speed float64
	calculatedInputs                  []string
	speedSource                       string
}

func (d *Detector) resetMissing() {
	d.missingSince, d.lastEmpty, d.emptyResponses = nil, time.Time{}, 0
}

func (d *Detector) resetApproach() {
	d.approachHistory, d.approach = nil, nil
}

// Called only for a distinct, fresh, explicitly airborne position. The two-minute
// history requires measurable descent and progress, not just a single low fix.
func (d *Detector) observeApproach(a flight.Aircraft, destination flight.Airport, distance float64, fix time.Time) {
	speed, track, verticalRate := a.EffectiveGroundSpeed(), a.EffectiveTrack(), a.EffectiveVerticalRate()
	if !finite(a.AltitudeFeet) || !finite(destination.ElevationFeet) || !finite(speed) {
		d.resetApproach()
		return
	}
	height := *a.AltitudeFeet - *destination.ElevationFeet
	if distance > 10 || height < -1500 || height > 5000 || *speed < 80 || *speed > 200 || (finite(verticalRate) && *verticalRate > 300) {
		d.resetApproach()
		return
	}
	if finite(track) && *track >= 0 && *track < 360 && distance > 0.5 {
		bearing := geo.BearingDegrees(*a.Latitude, *a.Longitude, *destination.Latitude, *destination.Longitude)
		if math.Abs(math.Remainder(*track-bearing, 360)) > 100 {
			d.resetApproach()
			return
		}
	}
	if n := len(d.approachHistory); n > 0 {
		previous := d.approachHistory[n-1]
		gap := fix.Sub(previous.at)
		if gap > maxAge || distance > previous.distance+0.25 || height > previous.height+150 || geo.DistanceNM(previous.lat, previous.lon, *a.Latitude, *a.Longitude)/gap.Hours() > 350 {
			d.resetApproach()
		}
	}
	for len(d.approachHistory) > 0 && fix.Sub(d.approachHistory[0].at) > approachWindow {
		d.approachHistory = d.approachHistory[1:]
	}
	speedSource := "reported"
	if a.GroundSpeed == nil {
		speedSource = "calculated"
	}
	d.approachHistory = append(d.approachHistory, approachSample{
		at: fix, lat: *a.Latitude, lon: *a.Longitude, distance: distance, height: height, speed: *speed,
		speedSource: speedSource, calculatedInputs: a.CalculatedInputs(),
	})
	if len(d.approachHistory) > 64 {
		d.approachHistory = d.approachHistory[len(d.approachHistory)-64:]
	}
	d.approach = nil
	first, last := d.approachHistory[0], d.approachHistory[len(d.approachHistory)-1]
	if last.at.Sub(first.at) < 30*time.Second || last.distance > landingRadiusNM || last.height > 1500 || first.height-last.height < 100 || first.distance-last.distance < 0.5 {
		return
	}
	d.approach = &flight.ApproachEvidence{
		StartedAt: first.at, LastPositionAt: last.at, ObservationCount: len(d.approachHistory),
		LastDistanceNM: last.distance, LastHeightFeet: last.height, LastSpeedKnots: last.speed,
		AltitudeDropFeet: first.height - last.height, ProgressNM: first.distance - last.distance,
		SpeedSource: last.speedSource,
	}
	for _, name := range []string{"ground_speed", "ground_track", "vertical_rate"} {
		found := false
		for _, point := range d.approachHistory {
			for _, input := range point.calculatedInputs {
				if input == name {
					found = true
				}
			}
		}
		if found {
			d.approach.CalculatedInputs = append(d.approach.CalculatedInputs, name)
		}
	}
}

// Only successful, empty lookups reach this method. Cached aircraft fixes,
// malformed responses, transport errors, and ambiguous matches cannot advance it.
func (d *Detector) missing(now time.Time) *flight.ArrivalAssessment {
	if d.assessment.Status == "likely_landed" || d.assessment.FirstSurfaceAt != nil {
		// Surface evidence is already stronger than an approach-only gap. Keep
		// it intact; empty responses cannot corroborate or erase a surface report.
		return d.retained("no current live aircraft observation")
	}
	if d.approach == nil {
		return d.retained("no current live aircraft observation")
	}
	age := now.Sub(d.approach.LastPositionAt)
	if age < 0 || age > approachExpiry {
		d.resetMissing()
		return d.retained("no current live aircraft observation; approach evidence expired")
	}
	if d.lastEmpty.IsZero() || now.After(d.lastEmpty) {
		d.emptyResponses++
		d.lastEmpty = now
		if d.missingSince == nil {
			d.missingSince = &now
		}
	}
	if age < missingDelay || d.emptyResponses < 2 {
		if d.assessment.Status == "possible_landing" {
			return d.retained("waiting for renewed successful empty-response evidence")
		}
		return &flight.ArrivalAssessment{
			Status: "unknown", Reason: "low descending approach observed; waiting for two successful empty responses and two minutes since the last position",
			AircraftHex: d.hex, LastAirborneAt: d.lastAirborne, LastObservedAt: &d.approach.LastPositionAt,
			DistanceNM: &d.approach.LastDistanceNM, Approach: d.approach, MissingSince: d.missingSince, EmptyResponses: d.emptyResponses,
		}
	}
	assessedAt := &now
	if d.assessment.Status == "possible_landing" {
		assessedAt = d.assessment.AssessedAt
	}
	result := flight.ArrivalAssessment{
		Status: "possible_landing", Reason: "low descending approach followed by sustained successful empty responses; surface confirmation unavailable",
		AircraftHex: d.hex, LastAirborneAt: d.lastAirborne, LastObservedAt: &d.approach.LastPositionAt,
		DistanceNM: &d.approach.LastDistanceNM, Approach: d.approach, MissingSince: d.missingSince,
		EmptyResponses: d.emptyResponses, AssessedAt: assessedAt,
	}
	d.assessment = result
	return &result
}
