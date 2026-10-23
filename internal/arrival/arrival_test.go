package arrival

import (
	"math"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func ptr[T any](v T) *T { return &v }

var start = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func destination() *flight.Airport {
	return &flight.Airport{IATA: "ALC", Latitude: ptr(38.2822), Longitude: ptr(-0.55816), ElevationFeet: ptr(142.0)}
}

func observation(seconds int, ground bool) flight.Aircraft {
	at := start.Add(time.Duration(seconds) * time.Second)
	a := flight.Aircraft{
		Hex: "4cae57", Callsign: "RYR9UJ", Latitude: ptr(38.29), Longitude: ptr(-0.55),
		OnGround: &ground, LastContact: &at, PositionUpdated: &at,
	}
	if !ground {
		a.Latitude, a.AltitudeFeet, a.GroundSpeed = ptr(38.45), ptr(3000.0), ptr(170.0)
	}
	return a
}

func update(d *Detector, a flight.Aircraft) *flight.ArrivalAssessment {
	return d.Update(*a.PositionUpdated, destination(), []flight.Aircraft{a})
}

func landed(t *testing.T) *Detector {
	t.Helper()
	d := &Detector{}
	update(d, observation(0, false))
	update(d, observation(30, true))
	got := update(d, observation(60, true))
	if got.Status != "likely_landed" {
		t.Fatalf("expected landing sequence to be inferred: %+v", got)
	}
	return d
}

func TestLandingRequiresDistinctCorroboratingReports(t *testing.T) {
	var d Detector
	if got := update(&d, observation(0, false)); got.Status != "airborne" {
		t.Fatalf("expected airborne, got %+v", got)
	}
	first := update(&d, observation(30, true))
	if first.Status != "possible_landing" || first.CorroboratedAt != nil || first.SurfaceReports != 1 {
		t.Fatalf("one surface report should establish only a possibility: %+v", first)
	}
	// Repeated API polls of the same position cannot corroborate a landing,
	// regardless of how much wall-clock time has passed.
	duplicate := d.Update(start.Add(55*time.Second), destination(), []flight.Aircraft{observation(30, true)})
	if duplicate.Status == "likely_landed" {
		t.Fatal("cached fix incorrectly corroborated a landing")
	}
	if got := update(&d, observation(32, true)); got.Status == "likely_landed" {
		t.Fatal("reports inside the 3-second debounce must not corroborate")
	}
	got := update(&d, observation(35, true))
	if got.Status != "likely_landed" || got.Historical || got.AircraftHex != "4cae57" {
		t.Fatalf("expected inferred landing despite missing surface speed: %+v", got)
	}
	if got.FirstSurfaceAt == nil || !got.FirstSurfaceAt.Equal(start.Add(30*time.Second)) || got.CorroboratedAt == nil || !got.CorroboratedAt.Equal(start.Add(35*time.Second)) || got.SurfaceReports != 3 {
		t.Fatalf("incorrect evidence timestamps: %+v", got)
	}
	// Earlier emitted snapshots must not change when the detector advances.
	if first.CorroboratedAt != nil || first.Status != "possible_landing" || first.SurfaceReports != 1 {
		t.Fatal("detector mutated an earlier snapshot")
	}
}

func TestStartingOnGroundCannotEstablishArrival(t *testing.T) {
	var d Detector
	for _, seconds := range []int{0, 30, 60, 120} {
		got := update(&d, observation(seconds, true))
		if got.Status != "on_ground_near_destination" || !strings.Contains(got.Reason, "arrival unconfirmed") || got.CorroboratedAt != nil {
			t.Fatalf("ground-only observations must not imply arrival: %+v", got)
		}
	}
}

func TestUnreliableEvidenceDoesNotEstablishArrival(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*flight.Aircraft)
	}{
		{"wrong airport", func(a *flight.Aircraft) { a.Latitude = ptr(40.0) }},
		{"rollout too fast", func(a *flight.Aircraft) { a.GroundSpeed = ptr(120.0) }},
		{"invalid speed", func(a *flight.Aircraft) { a.GroundSpeed = ptr(math.NaN()) }},
		{"unknown air ground state", func(a *flight.Aircraft) { a.OnGround = nil }},
		{"missing coordinates", func(a *flight.Aircraft) { a.Longitude = nil }},
		{"different aircraft", func(a *flight.Aircraft) { a.Hex = "abc001" }},
		{"missing identity", func(a *flight.Aircraft) { a.Hex = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d Detector
			update(&d, observation(0, false))
			update(&d, observation(30, true))
			a := observation(60, true)
			tc.change(&a)
			got := update(&d, a)
			if got.Status == "likely_landed" {
				t.Fatalf("unreliable evidence produced landing: %+v", got)
			}
			// A repeated copy of rejected data must not resurrect earlier state.
			if duplicate := update(&d, a); duplicate.Status == "likely_landed" {
				t.Fatal("rejected cached fix resurrected a landing")
			}
		})
	}
}

func TestStaleMissingAndOutOfOrderData(t *testing.T) {
	var d Detector
	update(&d, observation(0, false))
	update(&d, observation(30, true))
	stale := d.Update(start.Add(3*time.Minute), destination(), []flight.Aircraft{observation(60, true)})
	if stale.Status != "possible_landing" || !stale.Historical || !strings.Contains(stale.Reason, "stale") || stale.SurfaceReports != 1 {
		t.Fatalf("stale evidence should be ignored: %+v", stale)
	}
	old := update(&d, observation(20, true))
	if old.Status != "possible_landing" || !old.Historical || !strings.Contains(old.Reason, "out-of-order") || old.SurfaceReports != 1 {
		t.Fatalf("out-of-order evidence should be ignored: %+v", old)
	}
	for _, got := range []*flight.ArrivalAssessment{
		d.Update(start.Add(time.Minute), destination(), nil),
		d.Unavailable("request failed"),
	} {
		if got.Status != "possible_landing" || !got.Historical || got.CorroboratedAt != nil || got.SurfaceReports != 1 {
			t.Fatalf("loss of data must preserve the possibility without corroborating it: %+v", got)
		}
	}
	if got := update(&d, observation(31, true)); got.Status == "likely_landed" || got.SurfaceReports != 2 {
		t.Fatal("stale/out-of-order samples advanced corroboration")
	}
}

func TestLongGapAndOldAirborneHistory(t *testing.T) {
	var d Detector
	update(&d, observation(0, false))
	update(&d, observation(30, true))
	if got := update(&d, observation(150, true)); got.Status == "likely_landed" {
		t.Fatal("a gap over 90 seconds must restart surface corroboration")
	}
	if got := update(&d, observation(180, true)); got.Status != "likely_landed" {
		t.Fatalf("fresh corroboration should recover: %+v", got)
	}
	var old Detector
	update(&old, observation(0, false))
	update(&old, observation(601, true))
	if got := update(&old, observation(631, true)); got.Status == "likely_landed" {
		t.Fatal("airborne evidence older than ten minutes must not establish arrival")
	}
}

func TestHistoricalInferenceAndNewAirborneObservation(t *testing.T) {
	d := landed(t)
	for _, result := range []*flight.ArrivalAssessment{
		d.Update(start.Add(90*time.Second), destination(), nil),
		d.Unavailable("request failed"),
	} {
		if result.Status != "likely_landed" || !result.Historical || result.FirstSurfaceAt == nil {
			t.Fatalf("inference should survive gaps as explicitly historical: %+v", result)
		}
	}
	resumed := update(d, observation(90, true))
	if resumed.Status != "likely_landed" || resumed.Historical {
		t.Fatalf("fresh surface fix should retain current evidence: %+v", resumed)
	}
	airborne := update(d, observation(120, false))
	if airborne.Status != "airborne" || airborne.FirstSurfaceAt != nil || airborne.CorroboratedAt != nil {
		t.Fatalf("new airborne fix must supersede inferred landing: %+v", airborne)
	}
	if lost := d.Unavailable("signal lost after go-around"); lost.Status == "likely_landed" {
		t.Fatal("superseded inference reappeared after signal loss")
	}
	if got := update(d, observation(150, true)); got.Status == "likely_landed" {
		t.Fatal("new landing needs new surface corroboration")
	}
}

func TestAmbiguityAndDestinationMismatch(t *testing.T) {
	d := landed(t)
	got := d.Update(start.Add(90*time.Second), destination(), []flight.Aircraft{observation(90, true), observation(90, true)})
	if got.Status != "unknown" || d.Unavailable("signal lost").Status == "likely_landed" {
		t.Fatal("ambiguous aircraft should reset the inferred association")
	}
	var wrong Detector
	edi := &flight.Airport{IATA: "EDI", Latitude: ptr(55.95), Longitude: ptr(-3.37), ElevationFeet: ptr(135.0)}
	for _, a := range []flight.Aircraft{observation(0, false), observation(30, true), observation(60, true)} {
		got := wrong.Update(*a.PositionUpdated, edi, []flight.Aircraft{a})
		if got.Status == "likely_landed" {
			t.Fatal("Alicante surface reports must not imply landing in Edinburgh")
		}
	}
}
