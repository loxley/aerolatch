// Package arrival assesses possible landings after approach coverage loss and
// likely landings after surface observations, without an operational status feed.
package arrival

import (
	"math"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

const (
	maxAge         = 90 * time.Second
	airborneWindow = 10 * time.Minute
	// Reject timestamp jitter without requiring twenty seconds of ground
	// coverage, which often ends within a few seconds of touchdown.
	corroborationDelay = 3 * time.Second
	landingRadiusNM    = 3
)

type Detector struct {
	hex             string
	lastFix         time.Time
	lastAirborne    *time.Time
	firstSurface    *time.Time
	lastSurface     *time.Time
	surfaceReports  int
	assessment      flight.ArrivalAssessment
	lastResult      *flight.ArrivalAssessment
	approachHistory []approachSample
	approach        *flight.ApproachEvidence
	missingSince    *time.Time
	lastEmpty       time.Time
	emptyResponses  int
}

// Unavailable retains an already established inference as explicitly historical.
// Request failures never create or advance an inference and break the sequence
// of successful empty responses used by the approach-based fallback.
func (d *Detector) Unavailable(reason string) *flight.ArrivalAssessment {
	d.resetMissing()
	return d.retained(reason)
}

func (d *Detector) retained(reason string) *flight.ArrivalAssessment {
	if d.assessment.Status == "likely_landed" || d.assessment.Status == "possible_landing" {
		copy := d.assessment
		copy.Historical = true
		copy.Reason = "previous landing inference retained; " + reason
		return &copy
	}
	if d.assessment.Status == "on_ground_near_destination" {
		copy := d.assessment
		copy.Historical = true
		copy.Reason = "previous surface observation retained; " + reason + "; arrival remains unconfirmed"
		return &copy
	}
	return &flight.ArrivalAssessment{Status: "unknown", Reason: reason}
}

func (d *Detector) Update(now time.Time, destination *flight.Airport, aircraft []flight.Aircraft) (result *flight.ArrivalAssessment) {
	defer func() {
		copy := *result
		d.lastResult = &copy
	}()
	if len(aircraft) == 0 {
		return d.missing(now)
	}
	d.resetMissing()
	if len(aircraft) != 1 {
		*d = Detector{}
		return d.Unavailable("multiple aircraft match; arrival cannot be attributed uniquely")
	}
	a := aircraft[0]
	hex := strings.ToLower(strings.TrimSpace(a.Hex))
	if hex == "" {
		*d = Detector{}
		return d.Unavailable("aircraft identity unavailable")
	}
	if hex != d.hex {
		*d = Detector{hex: hex}
	}
	if destination == nil || !geo.ValidCoordinates(destination.Latitude, destination.Longitude) {
		return d.Unavailable("destination coordinates unavailable")
	}
	if !geo.ValidCoordinates(a.Latitude, a.Longitude) || a.PositionUpdated == nil {
		d.resetApproach()
		return d.Unavailable("position or position timestamp unavailable")
	}
	fix := *a.PositionUpdated
	if now.Sub(fix) > maxAge || fix.After(now.Add(5*time.Second)) {
		return d.Unavailable("position is stale or ahead of the local clock")
	}
	if a.LastContact != nil && (now.Sub(*a.LastContact) > maxAge || a.LastContact.After(now.Add(5*time.Second))) {
		return d.Unavailable("telemetry timestamp is stale or ahead of the local clock")
	}
	if fix.Before(d.lastFix) {
		return d.Unavailable("out-of-order position ignored")
	}
	if fix.Equal(d.lastFix) {
		if d.lastResult == nil {
			return d.Unavailable("waiting for a distinct position observation")
		}
		copy := *d.lastResult
		return &copy
	}
	d.lastFix = fix
	distance := geo.DistanceNM(*a.Latitude, *a.Longitude, *destination.Latitude, *destination.Longitude)
	if a.OnGround == nil {
		d.resetSurface()
		d.resetApproach()
		return d.Unavailable("air/ground state unavailable")
	}
	if !*a.OnGround {
		// Any fresh airborne fix supersedes a prior landing inference, including
		// a go-around or a new departure. Only a low, nearby fix arms detection.
		d.resetSurface()
		d.lastAirborne = nil
		d.assessment = flight.ArrivalAssessment{}
		if distance <= 25 && finite(a.AltitudeFeet) && finite(destination.ElevationFeet) {
			height := *a.AltitudeFeet - *destination.ElevationFeet
			if height >= -1500 && height <= 8000 {
				d.lastAirborne = &fix
			}
		}
		d.observeApproach(a, *destination, distance, fix)
		return d.save("airborne", "airborne observation; no landing inferred", distance, fix, nil)
	}
	if distance > landingRadiusNM || (a.GroundSpeed != nil && (!finite(a.GroundSpeed) || *a.GroundSpeed < 0 || *a.GroundSpeed > 80)) {
		d.resetSurface()
		d.resetApproach()
		return d.Unavailable("surface report is outside the destination radius or above 80 kt")
	}
	if d.assessment.Status == "likely_landed" {
		copy := d.assessment
		copy.Historical = false
		copy.LastObservedAt, copy.DistanceNM = &fix, &distance
		copy.LastSurfaceAt = &fix
		copy.SurfaceReports++
		d.firstSurface, d.lastSurface, d.surfaceReports = copy.FirstSurfaceAt, &fix, copy.SurfaceReports
		d.assessment = copy
		return &copy
	}
	if d.firstSurface == nil || d.lastSurface == nil || fix.Sub(*d.lastSurface) > maxAge {
		d.firstSurface = &fix
		d.surfaceReports = 0
	}
	d.lastSurface = &fix
	d.surfaceReports++
	// An established surface-based possibility keeps its airborne transition
	// evidence even if ground coverage later becomes intermittent.
	surfaceInferred := d.assessment.Status == "possible_landing" && d.assessment.FirstSurfaceAt != nil
	if !surfaceInferred && (d.lastAirborne == nil || fix.Sub(*d.lastAirborne) > airborneWindow) {
		return d.save("on_ground_near_destination", "on ground near destination; arrival unconfirmed (no recent airborne observation in this session)", distance, fix, nil)
	}
	assessedAt := &now
	if surfaceInferred && d.assessment.AssessedAt != nil {
		assessedAt = d.assessment.AssessedAt
	}
	status, reason := "possible_landing", "recent airborne observation followed by a fresh surface report near destination; waiting for distinct surface corroboration (3-second debounce)"
	var corroborated *time.Time
	if d.surfaceReports >= 2 && fix.Sub(*d.firstSurface) >= corroborationDelay {
		status, reason, corroborated = "likely_landed", "airborne observation followed by distinct fresh surface reports near destination; inferred, not an airline status", &fix
	}
	result = d.save(status, reason, distance, fix, corroborated)
	result.AssessedAt = assessedAt
	d.assessment = *result
	return result
}

func (d *Detector) resetSurface() {
	d.firstSurface, d.lastSurface, d.surfaceReports = nil, nil, 0
}

func (d *Detector) save(status, reason string, distance float64, fix time.Time, corroborated *time.Time) *flight.ArrivalAssessment {
	result := flight.ArrivalAssessment{
		Status: status, Reason: reason, AircraftHex: d.hex, DistanceNM: &distance,
		LastAirborneAt: d.lastAirborne, FirstSurfaceAt: d.firstSurface,
		LastSurfaceAt: d.lastSurface, SurfaceReports: d.surfaceReports,
		CorroboratedAt: corroborated, LastObservedAt: &fix,
		Approach: d.approach,
	}
	d.assessment = result
	return &result
}

func finite(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}
