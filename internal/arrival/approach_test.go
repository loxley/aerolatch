package arrival

import (
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

var arlandaStart = time.Date(2026, 10, 17, 21, 9, 29, 0, time.UTC)

func arlanda() *flight.Airport {
	return &flight.Airport{IATA: "ARN", Latitude: ptr(59.6519), Longitude: ptr(17.9186), ElevationFeet: ptr(137.0)}
}

// The user's NSZ4366 observations, using actual fix times rather than fetch times.
func arlandaFixes() []flight.Aircraft {
	var result []flight.Aircraft
	for _, s := range []struct {
		seconds                                int
		lat, lon, altitude, speed, track, rate float64
	}{
		{0, 59.60828, 17.90298, 300, 125, 10, -640},
		{30, 59.62550, 17.90909, -75, 126, 11, -640},
		{40, 59.63100, 17.91103, -175, 132, 10, -704},
	} {
		at := arlandaStart.Add(time.Duration(s.seconds) * time.Second)
		result = append(result, flight.Aircraft{
			Hex: "4aca87", Callsign: "NSZ4366", OnGround: ptr(false),
			Latitude: ptr(s.lat), Longitude: ptr(s.lon), AltitudeFeet: ptr(s.altitude),
			GroundSpeed: ptr(s.speed), Track: ptr(s.track), VerticalRate: ptr(s.rate),
			PositionUpdated: &at, LastContact: &at,
		})
	}
	return result
}

func replayApproach(d *Detector, fixes []flight.Aircraft, airport *flight.Airport) {
	for _, a := range fixes {
		d.Update(*a.PositionUpdated, airport, []flight.Aircraft{a})
	}
}

func TestArlandaApproachThenCoverageLoss(t *testing.T) {
	var d Detector
	fixes := arlandaFixes()
	replayApproach(&d, fixes, arlanda())
	// Polls at 21:10:31 and 21:11:01 repeat the same 21:10:09 fix.
	for _, seconds := range []int{62, 92} {
		got := d.Update(arlandaStart.Add(time.Duration(seconds)*time.Second), arlanda(), fixes[2:])
		if got.Status != "airborne" || got.Approach == nil || got.Approach.ObservationCount != 3 {
			t.Fatalf("cached polls must preserve, not extend, approach evidence: %+v", got)
		}
	}
	var waiting *flight.ArrivalAssessment
	for _, seconds := range []int{122, 152} { // 21:11:31, 21:12:01
		waiting = d.Update(arlandaStart.Add(time.Duration(seconds)*time.Second), arlanda(), nil)
		if waiting.Status != "unknown" || waiting.Approach == nil {
			t.Fatalf("must wait two minutes since the last fix: %+v", waiting)
		}
	}
	assessed := arlandaStart.Add(182 * time.Second) // 21:12:31
	got := d.Update(assessed, arlanda(), nil)
	if got.Status != "possible_landing" || got.Historical || got.AircraftHex != "4aca87" || got.FirstSurfaceAt != nil || got.CorroboratedAt != nil {
		t.Fatalf("expected possible landing without invented surface evidence: %+v", got)
	}
	if got.EmptyResponses != 3 || got.AssessedAt == nil || !got.AssessedAt.Equal(assessed) || got.MissingSince == nil || !got.MissingSince.Equal(arlandaStart.Add(122*time.Second)) {
		t.Fatalf("incorrect assessment/absence timing: %+v", got)
	}
	if got.Approach == nil || got.Approach.ObservationCount != 3 || got.Approach.AltitudeDropFeet != 475 || !got.Approach.LastPositionAt.Equal(*fixes[2].PositionUpdated) || got.Approach.ProgressNM < 1 {
		t.Fatalf("incorrect historical approach evidence: %+v", got.Approach)
	}
	if waiting.Status != "unknown" || waiting.EmptyResponses != 2 {
		t.Fatal("a subsequent inference mutated an earlier assessment")
	}
	// Further successful empty polls keep the original assessment time.
	next := d.Update(assessed.Add(30*time.Second), arlanda(), nil)
	if next.Status != "possible_landing" || !next.AssessedAt.Equal(assessed) {
		t.Fatalf("assessment timestamp must not drift: %+v", next)
	}
}

func TestPossibleLandingRequiresTwoDistinctSuccessfulEmptyPolls(t *testing.T) {
	var d Detector
	replayApproach(&d, arlandaFixes(), arlanda())
	now := arlandaStart.Add(3 * time.Minute)
	for i := 0; i < 2; i++ {
		got := d.Update(now, arlanda(), nil)
		if got.Status != "unknown" || got.EmptyResponses != 1 {
			t.Fatalf("a single poll or repeated poll timestamp cannot infer landing: %+v", got)
		}
	}
	if got := d.Update(now.Add(30*time.Second), arlanda(), nil); got.Status != "possible_landing" {
		t.Fatalf("two successful polls after the delay should infer a possibility: %+v", got)
	}
}

func TestFailuresDoNotAdvanceApproachDisappearance(t *testing.T) {
	var d Detector
	replayApproach(&d, arlandaFixes(), arlanda())
	d.Update(arlandaStart.Add(3*time.Minute), arlanda(), nil)
	for i := 0; i < 3; i++ {
		if got := d.Unavailable("request timeout"); got.Status != "unknown" {
			t.Fatalf("failures cannot establish a landing: %+v", got)
		}
	}
	first := d.Update(arlandaStart.Add(4*time.Minute), arlanda(), nil)
	if first.Status != "unknown" || first.EmptyResponses != 1 {
		t.Fatalf("request failures must break empty-response corroboration: %+v", first)
	}
	second := d.Update(arlandaStart.Add(270*time.Second), arlanda(), nil)
	if second.Status != "possible_landing" {
		t.Fatalf("successful responses can establish possibility after recovery: %+v", second)
	}
	failure := d.Unavailable("provider offline")
	if failure.Status != "possible_landing" || !failure.Historical || failure.AssessedAt == nil || !failure.AssessedAt.Equal(*second.AssessedAt) {
		t.Fatalf("existing inference should be explicitly historical during failure: %+v", failure)
	}
}

func TestWeakApproachesDoNotInferLanding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]flight.Aircraft) []flight.Aircraft
	}{
		{"single observation", func(a []flight.Aircraft) []flight.Aircraft { return a[2:] }},
		{"too short", func(a []flight.Aircraft) []flight.Aircraft { return a[1:] }},
		{"no descent", func(a []flight.Aircraft) []flight.Aircraft {
			for i := range a {
				a[i].AltitudeFeet = ptr(300.0)
				a[i].VerticalRate = ptr(0.0)
			}
			return a
		}},
		{"high approach", func(a []flight.Aircraft) []flight.Aircraft {
			for i := range a {
				a[i].AltitudeFeet = ptr(*a[i].AltitudeFeet + 3000)
			}
			return a
		}},
		{"too fast", func(a []flight.Aircraft) []flight.Aircraft {
			for i := range a {
				a[i].GroundSpeed = ptr(250.0)
			}
			return a
		}},
		{"no speed", func(a []flight.Aircraft) []flight.Aircraft {
			for i := range a {
				a[i].GroundSpeed = nil
			}
			return a
		}},
		{"moving away", func(a []flight.Aircraft) []flight.Aircraft {
			for i := range a {
				a[i].Track = ptr(190.0)
			}
			return a
		}},
		{"position jump", func(a []flight.Aircraft) []flight.Aircraft {
			a[0].Latitude = ptr(59.5)
			return a
		}},
		{"climbing", func(a []flight.Aircraft) []flight.Aircraft {
			a[2].VerticalRate = ptr(1000.0)
			return a
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d Detector
			replayApproach(&d, tc.change(arlandaFixes()), arlanda())
			for _, seconds := range []int{180, 210, 240} {
				got := d.Update(arlandaStart.Add(time.Duration(seconds)*time.Second), arlanda(), nil)
				if got.Status != "unknown" {
					t.Fatalf("weak evidence must not infer landing: %+v", got)
				}
			}
		})
	}
}

func TestOldAndCachedApproachesCannotCreateNewEvidence(t *testing.T) {
	var single Detector
	fix := arlandaFixes()[2]
	for _, seconds := range []int{40, 70, 100} {
		single.Update(arlandaStart.Add(time.Duration(seconds)*time.Second), arlanda(), []flight.Aircraft{fix})
	}
	for _, seconds := range []int{180, 210} {
		if got := single.Update(arlandaStart.Add(time.Duration(seconds)*time.Second), arlanda(), nil); got.Status != "unknown" {
			t.Fatal("repeated cached fixes created an approach sequence")
		}
	}
	var expired Detector
	replayApproach(&expired, arlandaFixes(), arlanda())
	for _, seconds := range []int{700, 730} {
		if got := expired.Update(arlandaStart.Add(time.Duration(seconds)*time.Second), arlanda(), nil); got.Status != "unknown" {
			t.Fatal("approach older than ten minutes created an inference")
		}
	}
	var stale Detector
	for _, a := range arlandaFixes() {
		stale.Update(a.PositionUpdated.Add(2*time.Minute), arlanda(), []flight.Aircraft{a})
	}
	stale.Update(arlandaStart.Add(240*time.Second), arlanda(), nil)
	if got := stale.Update(arlandaStart.Add(270*time.Second), arlanda(), nil); got.Status != "unknown" {
		t.Fatal("stale observations created approach evidence")
	}
}

func TestCachedReappearanceBreaksPendingEmptyResponseSequence(t *testing.T) {
	var d Detector
	fixes := arlandaFixes()
	replayApproach(&d, fixes, arlanda())
	d.Update(arlandaStart.Add(100*time.Second), arlanda(), nil)
	// The old fix is still fresh enough to be returned, but contributes no new
	// approach evidence and interrupts the successful-empty-response sequence.
	d.Update(arlandaStart.Add(110*time.Second), arlanda(), fixes[2:])
	first := d.Update(arlandaStart.Add(180*time.Second), arlanda(), nil)
	if first.Status != "unknown" || first.EmptyResponses != 1 || first.Approach == nil || first.Approach.ObservationCount != 3 {
		t.Fatalf("cached reappearance must break absence corroboration: %+v", first)
	}
	if got := d.Update(arlandaStart.Add(210*time.Second), arlanda(), nil); got.Status != "possible_landing" {
		t.Fatalf("new empty responses should support the preserved approach evidence: %+v", got)
	}
}

func TestReappearanceReassessesPossibleLanding(t *testing.T) {
	for _, kind := range []string{"go around", "moving away", "new identity", "ambiguous", "surface confirmation"} {
		t.Run(kind, func(t *testing.T) {
			var d Detector
			fixes := arlandaFixes()
			replayApproach(&d, fixes, arlanda())
			d.Update(arlandaStart.Add(180*time.Second), arlanda(), nil)
			if got := d.Update(arlandaStart.Add(210*time.Second), arlanda(), nil); got.Status != "possible_landing" {
				t.Fatal("test approach did not produce an inference")
			}
			a := fixes[2]
			at := arlandaStart.Add(240 * time.Second)
			a.PositionUpdated, a.LastContact = &at, &at
			switch kind {
			case "go around":
				a.AltitudeFeet, a.VerticalRate = ptr(2500.0), ptr(1500.0)
			case "moving away":
				a.Latitude, a.Longitude = ptr(59.60), ptr(17.90)
			case "new identity":
				a.Hex = "abc001"
			case "surface confirmation":
				a.OnGround, a.GroundSpeed = ptr(true), ptr(25.0)
			case "ambiguous":
				d.Update(at, arlanda(), []flight.Aircraft{a, a})
				if got := d.Unavailable("signal lost"); got.Status != "unknown" {
					t.Fatalf("ambiguity did not reset inference: %+v", got)
				}
				return
			}
			got := d.Update(at, arlanda(), []flight.Aircraft{a})
			if kind == "surface confirmation" {
				if got.Status != "possible_landing" || got.FirstSurfaceAt == nil || !got.FirstSurfaceAt.Equal(at) || got.AssessedAt == nil || !got.AssessedAt.Equal(at) || got.SurfaceReports != 1 || got.EmptyResponses != 0 {
					t.Fatalf("a fresh surface report should reassess the approach-only possibility: %+v", got)
				}
				at = at.Add(30 * time.Second)
				a.PositionUpdated, a.LastContact = &at, &at
				if upgraded := d.Update(at, arlanda(), []flight.Aircraft{a}); upgraded.Status != "likely_landed" || upgraded.FirstSurfaceAt == nil {
					t.Fatalf("fresh surface evidence should upgrade assessment: %+v", upgraded)
				}
			} else {
				if got.Status == "possible_landing" || got.AssessedAt != nil {
					t.Fatalf("fresh airborne observations must supersede old possible landing: %+v", got)
				}
				d.Update(at.Add(30*time.Second), arlanda(), nil)
				if lost := d.Update(at.Add(150*time.Second), arlanda(), nil); lost.Status != "unknown" {
					t.Fatalf("old possible landing reappeared after new observations: %+v", lost)
				}
			}
		})
	}
}

func TestPossibleLandingRetainedAsHistoricalAfterEvidenceExpires(t *testing.T) {
	var d Detector
	replayApproach(&d, arlandaFixes(), arlanda())
	d.Update(arlandaStart.Add(180*time.Second), arlanda(), nil)
	d.Update(arlandaStart.Add(210*time.Second), arlanda(), nil)
	got := d.Update(arlandaStart.Add(20*time.Minute), arlanda(), nil)
	if got.Status != "possible_landing" || !got.Historical || !strings.Contains(got.Reason, "expired") || got.Approach == nil {
		t.Fatalf("an earlier inference must retain its evidence and historical label: %+v", got)
	}
}
