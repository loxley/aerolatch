package arrival

import (
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func copenhagen() *flight.Airport {
	return &flight.Airport{IATA: "CPH", Latitude: ptr(55.6179), Longitude: ptr(12.656), ElevationFeet: ptr(17.0)}
}

func copenhagenFix(at time.Time, ground bool, lat, lon, altitude, speed float64) flight.Aircraft {
	a := flight.Aircraft{Hex: "4787a0", Callsign: "NSZ3235", OnGround: ptr(ground), Latitude: ptr(lat), Longitude: ptr(lon), AltitudeFeet: ptr(altitude), GroundSpeed: ptr(speed), VerticalRate: ptr(-640.0), PositionUpdated: &at, LastContact: &at}
	if ground {
		a.AltitudeFeet = nil
	}
	return a
}

func copenhagenApproach(d *Detector) time.Time {
	// Synthetic preceding approach supporting the actual screenshot's ground
	// reports; the screenshot supplies the surface times, identity and final fix.
	base := time.Date(2026, 10, 19, 21, 26, 1, 0, time.UTC)
	for _, a := range []flight.Aircraft{
		copenhagenFix(base.Add(-41*time.Second), false, 55.57, 12.55, 700, 140),
		copenhagenFix(base.Add(-11*time.Second), false, 55.596, 12.602, 200, 130),
	} {
		d.Update(*a.PositionUpdated, copenhagen(), []flight.Aircraft{a})
	}
	return base
}

func TestCopenhagenFiveSecondSurfaceSequenceAndTrackingLoss(t *testing.T) {
	var d Detector
	firstAt := copenhagenApproach(&d)
	firstFix := copenhagenFix(firstAt, true, 55.6049, 12.6232, 0, 56)
	first := d.Update(firstAt.Add(4*time.Second), copenhagen(), []flight.Aircraft{firstFix})
	if first.Status != "possible_landing" || first.SurfaceReports != 1 || first.Approach == nil || first.CorroboratedAt != nil {
		t.Fatalf("first fresh surface report must preserve approach and establish possibility: %+v", first)
	}
	secondAt := firstAt.Add(5 * time.Second) // Screenshot: 21:26:06, 56 kt, on ground.
	secondFix := copenhagenFix(secondAt, true, 55.60594, 12.62481, 0, 56)
	second := d.Update(firstAt.Add(34*time.Second), copenhagen(), []flight.Aircraft{secondFix})
	if second.Status != "likely_landed" || second.SurfaceReports != 2 || second.Approach == nil || second.CorroboratedAt == nil || !second.CorroboratedAt.Equal(secondAt) {
		t.Fatalf("five-second ground coverage should corroborate landing: %+v", second)
	}
	for _, elapsed := range []time.Duration{58 * time.Second, 64 * time.Second} {
		cached := d.Update(firstAt.Add(elapsed), copenhagen(), []flight.Aircraft{secondFix})
		if cached.Status != "likely_landed" || cached.SurfaceReports != 2 {
			t.Fatalf("cached polls added fake surface evidence: %+v", cached)
		}
	}
	for _, elapsed := range []time.Duration{94 * time.Second, 8*time.Minute + 44*time.Second} { // 21:27:35 and 21:34:45.
		lost := d.Update(firstAt.Add(elapsed), copenhagen(), nil)
		if lost.Status != "likely_landed" || !lost.Historical || lost.SurfaceReports != 2 || lost.LastSurfaceAt == nil || !lost.LastSurfaceAt.Equal(secondAt) || lost.LastObservedAt == nil || !lost.LastObservedAt.Equal(secondAt) || lost.Approach == nil {
			t.Fatalf("tracking loss erased or relabeled existing surface evidence: %+v", lost)
		}
	}
	if first.Status != "possible_landing" || first.SurfaceReports != 1 || first.CorroboratedAt != nil {
		t.Fatal("later assessment mutated earlier snapshot")
	}
}

func TestSingleSurfacePossibilitySurvivesLossWithoutBecomingCorroborated(t *testing.T) {
	var d Detector
	at := copenhagenApproach(&d)
	a := copenhagenFix(at, true, 55.60594, 12.62481, 0, 56)
	first := d.Update(at, copenhagen(), []flight.Aircraft{a})
	if first.Status != "possible_landing" {
		t.Fatalf("expected immediate possibility: %+v", first)
	}
	for _, elapsed := range []time.Duration{time.Minute, 3 * time.Minute, 20 * time.Minute} {
		lost := d.Update(at.Add(elapsed), copenhagen(), nil)
		if lost.Status != "possible_landing" || !lost.Historical || lost.CorroboratedAt != nil || lost.SurfaceReports != 1 || lost.FirstSurfaceAt == nil || lost.AssessedAt == nil || !lost.AssessedAt.Equal(*first.AssessedAt) {
			t.Fatalf("elapsed time must neither erase nor corroborate the surface possibility: %+v", lost)
		}
	}
	failed := d.Unavailable("provider timeout")
	if failed.Status != "possible_landing" || !failed.Historical || failed.CorroboratedAt != nil {
		t.Fatal("request failure changed the evidence level")
	}
}

func TestSurfaceOnlyObservationRetainedWithoutInferringArrival(t *testing.T) {
	var d Detector
	at := time.Date(2026, 10, 19, 21, 26, 1, 0, time.UTC)
	for _, offset := range []time.Duration{0, 5 * time.Second} {
		a := copenhagenFix(at.Add(offset), true, 55.60594, 12.62481, 0, 20)
		got := d.Update(*a.PositionUpdated, copenhagen(), []flight.Aircraft{a})
		if got.Status != "on_ground_near_destination" || got.CorroboratedAt != nil {
			t.Fatal("starting on ground implied an arrival")
		}
	}
	lost := d.Update(at.Add(8*time.Minute), copenhagen(), nil)
	if lost.Status != "on_ground_near_destination" || !lost.Historical || lost.SurfaceReports != 2 || lost.CorroboratedAt != nil {
		t.Fatalf("last known ground state should be retained, without an inferred landing: %+v", lost)
	}
}

func TestSurfacePossibilityClearsOnFreshAirborneReappearance(t *testing.T) {
	var d Detector
	at := copenhagenApproach(&d)
	a := copenhagenFix(at, true, 55.60594, 12.62481, 0, 56)
	d.Update(at, copenhagen(), []flight.Aircraft{a})
	d.Update(at.Add(time.Minute), copenhagen(), nil)
	a = copenhagenFix(at.Add(2*time.Minute), false, 55.60, 12.62, 2000, 160)
	a.VerticalRate = ptr(1500.0)
	got := d.Update(*a.PositionUpdated, copenhagen(), []flight.Aircraft{a})
	if got.Status != "airborne" || got.FirstSurfaceAt != nil || got.LastSurfaceAt != nil || got.SurfaceReports != 0 {
		t.Fatalf("fresh airborne report did not clear old landing evidence: %+v", got)
	}
	if lost := d.Update(at.Add(4*time.Minute), copenhagen(), nil); lost.Status != "unknown" {
		t.Fatalf("superseded surface inference reappeared: %+v", lost)
	}
}

func TestCalculatedSpeedCannotOverrideReportedSurfaceEvidence(t *testing.T) {
	var d Detector
	at := copenhagenApproach(&d)
	ground := copenhagenFix(at, true, 55.60594, 12.62481, 0, 56)
	ground.GroundSpeed = nil
	ground.Calculated = &flight.CalculatedKinematics{GroundSpeed: &flight.CalculatedMeasurement{Value: 200, Unit: "kt"}}
	first := d.Update(at, copenhagen(), []flight.Aircraft{ground})
	if first.Status != "possible_landing" {
		t.Fatal("average calculated speed disqualified a valid surface report")
	}
	at = at.Add(5 * time.Second)
	ground.PositionUpdated, ground.LastContact = &at, &at
	second := d.Update(at, copenhagen(), []flight.Aircraft{ground})
	if second.Status != "likely_landed" {
		t.Fatal("calculated speed altered reported-only surface corroboration")
	}
	var rawHigh Detector
	at = copenhagenApproach(&rawHigh)
	ground = copenhagenFix(at, true, 55.60594, 12.62481, 0, 130)
	ground.Calculated = &flight.CalculatedKinematics{GroundSpeed: &flight.CalculatedMeasurement{Value: 20, Unit: "kt"}}
	if got := rawHigh.Update(at, copenhagen(), []flight.Aircraft{ground}); got.Status == "possible_landing" || got.Status == "likely_landed" {
		t.Fatal("calculated low speed overrode a disqualifying reported ground speed")
	}
}
