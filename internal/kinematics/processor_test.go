package kinematics

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

var epoch = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }
func fix(seconds int, lon, altitude float64) flight.Aircraft {
	at := epoch.Add(time.Duration(seconds) * time.Second)
	return flight.Aircraft{Hex: "abc001", Callsign: "TEST123", Latitude: ptr(0.0), Longitude: ptr(lon), AltitudeFeet: ptr(altitude), OnGround: ptr(false), PositionUpdated: &at, LastContact: &at}
}
func update(p *Processor, a flight.Aircraft) flight.Aircraft {
	return p.Update(*a.PositionUpdated, []flight.Aircraft{a})[0]
}
func near(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) || math.Abs(got-want) > tolerance {
		t.Fatalf("got %.6f, want %.6f ± %.6f", got, want, tolerance)
	}
}

func TestRecoveryUnitsWindowsAndRawJSON(t *testing.T) {
	var p Processor
	first := update(&p, fix(0, 0, 10000))
	if first.EffectiveGroundSpeed() != nil || first.EffectiveTrack() != nil || first.EffectiveVerticalRate() != nil {
		t.Fatal("one fix invented measurements")
	}
	input := fix(30, 0.02, 9500)
	second := update(&p, input)
	near(t, *second.EffectiveGroundSpeed(), 144.1, 0.2)
	near(t, *second.EffectiveTrack(), 90, 0.01)
	if second.EffectiveVerticalRate() != nil {
		t.Fatal("two altitude samples produced a vertical trend")
	}
	third := update(&p, fix(60, 0.04, 9000))
	near(t, *third.EffectiveVerticalRate(), -1000, 0.001)
	if third.GroundSpeed != nil || third.Track != nil || third.VerticalRate != nil || input.Calculated != nil {
		t.Fatal("raw/provider-owned values were overwritten")
	}
	gs, vr := third.Calculated.GroundSpeed, third.Calculated.VerticalRate
	if gs.Unit != "kt" || gs.Samples != 2 || !gs.WindowStart.Equal(epoch.Add(30*time.Second)) || !gs.WindowEnd.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("incorrect horizontal provenance: %+v", gs)
	}
	if vr.Unit != "ft/min" || vr.Samples != 3 || !vr.WindowStart.Equal(epoch) || !vr.WindowEnd.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("incorrect vertical provenance: %+v", vr)
	}
	encoded, err := json.Marshal(third)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"ground_speed_kt":null`, `"ground_track_deg":null`, `"vertical_rate_ft_min":null`, `"calculated":`, `"window_start":`, `"method":`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("JSON missing %s: %s", want, encoded)
		}
	}
}

func TestReportedZeroValuesAlwaysTakePriority(t *testing.T) {
	var p Processor
	update(&p, fix(0, 0, 10000))
	update(&p, fix(30, 0.02, 9500))
	a := fix(60, 0.04, 9000)
	a.GroundSpeed, a.Track, a.VerticalRate = ptr(0.0), ptr(0.0), ptr(0.0)
	got := update(&p, a)
	if *got.EffectiveGroundSpeed() != 0 || *got.EffectiveTrack() != 0 || *got.EffectiveVerticalRate() != 0 || got.Calculated != nil {
		t.Fatal("reported zeros were replaced with calculated values")
	}
	// Even a previously calculated object cannot override an explicit report.
	a.Calculated = &flight.CalculatedKinematics{GroundSpeed: &flight.CalculatedMeasurement{Value: 999}}
	if *a.EffectiveGroundSpeed() != 0 || len(a.CalculatedInputs()) != 0 {
		t.Fatal("reported measurement precedence failed")
	}
}

func TestCachedFixesNeverCreateSamplesOrRefreshCalculationAge(t *testing.T) {
	var p Processor
	update(&p, fix(0, 0, 10000))
	a := fix(30, 0.02, 9500)
	initial := update(&p, a)
	for _, seconds := range []int{40, 60} {
		cached := p.Update(epoch.Add(time.Duration(seconds)*time.Second), []flight.Aircraft{a})[0]
		if cached.Calculated.GroundSpeed.Samples != 2 || !cached.Calculated.GroundSpeed.WindowEnd.Equal(*a.PositionUpdated) || cached.EffectiveVerticalRate() != nil {
			t.Fatal("cached polls changed the original calculation window or sample count")
		}
	}
	aged := p.Update(epoch.Add(110*time.Second), []flight.Aircraft{a})[0]
	if aged.EffectiveGroundSpeed() != nil {
		t.Fatal("a stale anchor was used for a fresh-looking calculation")
	}
	if initial.Calculated.GroundSpeed.Samples != 2 || !initial.Calculated.GroundSpeed.WindowStart.Equal(epoch) {
		t.Fatal("later processing mutated an emitted calculation")
	}
	var single Processor
	a = fix(0, 0, 10000)
	for _, seconds := range []int{0, 30, 60} {
		if g := single.Update(epoch.Add(time.Duration(seconds)*time.Second), []flight.Aircraft{a})[0]; g.EffectiveGroundSpeed() != nil {
			t.Fatal("a repeated single fix produced speed")
		}
	}
}

func TestPositionGapsErrorsAndAmbiguityBreakContinuity(t *testing.T) {
	for _, kind := range []string{"empty", "error", "ambiguous"} {
		t.Run(kind, func(t *testing.T) {
			var p Processor
			update(&p, fix(0, 0, 10000))
			a := fix(30, 0.02, 9500)
			update(&p, a)
			switch kind {
			case "empty":
				p.Update(epoch.Add(40*time.Second), nil)
			case "error":
				p.Break()
			case "ambiguous":
				p.Update(epoch.Add(40*time.Second), []flight.Aircraft{a, a})
			}
			if got := p.Update(epoch.Add(45*time.Second), []flight.Aircraft{a})[0]; got.EffectiveGroundSpeed() != nil {
				t.Fatal("cached recovery revived a pre-gap calculation")
			}
			if got := update(&p, fix(60, 0.04, 9000)); got.EffectiveGroundSpeed() != nil {
				t.Fatal("first new recovery fix bridged a coverage gap")
			}
			if got := update(&p, fix(90, 0.06, 8500)); got.EffectiveGroundSpeed() == nil {
				t.Fatal("two new recovery fixes failed to restore speed")
			}
		})
	}
}

func TestIdentityFollowsHexAcrossCallsignChanges(t *testing.T) {
	var p Processor
	update(&p, fix(0, 0, 10000))
	a := fix(30, 0.02, 9500)
	a.Callsign = "OTHER123"
	if got := update(&p, a); got.EffectiveGroundSpeed() == nil {
		t.Fatal("callsign change discarded aircraft continuity")
	}
	a = fix(60, 0.04, 9000)
	a.Callsign = ""
	if got := update(&p, a); got.EffectiveVerticalRate() == nil {
		t.Fatal("cleared callsign discarded altitude history")
	}
	a = fix(90, 0.06, 8500)
	a.Hex = "abc002"
	if got := update(&p, a); got.EffectiveGroundSpeed() != nil {
		t.Fatal("history leaked across aircraft identities")
	}
}

func TestTouchdownBreaksAveragesAndNeverInventsSurfaceEvidence(t *testing.T) {
	var p Processor
	update(&p, fix(0, 0, 1000))
	update(&p, fix(30, 0.02, 500))
	a := fix(40, 0.025, 0)
	a.OnGround = ptr(true)
	a.AltitudeFeet = nil
	got := update(&p, a)
	if got.EffectiveGroundSpeed() != nil || got.EffectiveVerticalRate() != nil {
		t.Fatal("airborne speed or altitude trend crossed touchdown")
	}
	a = fix(70, 0.026, 0)
	a.OnGround = ptr(true)
	a.AltitudeFeet = nil
	got = update(&p, a)
	if got.EffectiveGroundSpeed() == nil || got.EffectiveVerticalRate() != nil {
		t.Fatal("ground movement was unavailable or a surface vertical rate was invented")
	}
	var unknown Processor
	a = fix(0, 0, 0)
	a.OnGround = nil
	update(&unknown, a)
	a = fix(30, 0, 0)
	a.OnGround = nil
	got = update(&unknown, a)
	if got.OnGround != nil || got.AltitudeFeet == nil || *got.AltitudeFeet != 0 {
		t.Fatal("calculated speed created a surface flag or changed raw altitude")
	}
	var cachedTransition Processor
	update(&cachedTransition, fix(0, 0, 1000))
	update(&cachedTransition, fix(30, 0.02, 500))
	a = fix(30, 0.02, 0)
	a.OnGround, a.AltitudeFeet = ptr(true), nil
	if g := cachedTransition.Update(epoch.Add(31*time.Second), []flight.Aircraft{a})[0]; g.EffectiveGroundSpeed() != nil {
		t.Fatal("a ground flag on a cached airborne position retained approach speed")
	}
	a = fix(60, 0.03, 0)
	a.OnGround, a.AltitudeFeet = ptr(true), nil
	if g := update(&cachedTransition, a); g.EffectiveGroundSpeed() != nil {
		t.Fatal("a cached transition became a ground baseline")
	}
}

func TestStationaryAndTinyMovementHaveNoCalculatedDirection(t *testing.T) {
	for _, lon := range []float64{0, 0.0001} {
		var p Processor
		update(&p, fix(0, 0, 10000))
		got := update(&p, fix(30, lon, 10000))
		if got.EffectiveGroundSpeed() == nil || got.EffectiveTrack() != nil {
			t.Fatal("stationary/jitter direction was fabricated")
		}
		if lon == 0 && *got.EffectiveGroundSpeed() != 0 {
			t.Fatal("known stationary displacement did not preserve zero speed")
		}
	}
}

func TestNorthAndDateLineTrack(t *testing.T) {
	for _, tc := range []struct{ lat1, lon1, lat2, lon2, want float64 }{{-0.01, 0, 0.01, 0, 0}, {0, 179.99, 0, -179.99, 90}, {0, 0, 0, -0.02, 270}} {
		var p Processor
		a, b := fix(0, tc.lon1, 10000), fix(30, tc.lon2, 10000)
		a.Latitude = ptr(tc.lat1)
		b.Latitude = ptr(tc.lat2)
		update(&p, a)
		got := update(&p, b)
		if got.EffectiveTrack() == nil {
			t.Fatal("missing wrapped track")
		}
		near(t, *got.EffectiveTrack(), tc.want, 0.01)
	}
}

func TestRejectsInvalidObservationsAndPositionJumps(t *testing.T) {
	for _, kind := range []string{"stale", "future", "out of order", "invalid coordinates", "changed timestamp data", "invalid identity", "jump"} {
		t.Run(kind, func(t *testing.T) {
			var p Processor
			update(&p, fix(0, 0, 10000))
			update(&p, fix(30, 0.02, 9500))
			a := fix(60, 0.04, 9000)
			now := *a.PositionUpdated
			switch kind {
			case "stale":
				now = epoch.Add(200 * time.Second)
			case "future":
				now = epoch.Add(50 * time.Second)
			case "out of order":
				a = fix(20, 0.01, 9700)
			case "invalid coordinates":
				a.Latitude = ptr(math.NaN())
			case "changed timestamp data":
				a.PositionUpdated = ptr(epoch.Add(30 * time.Second))
			case "invalid identity":
				a.Hex = "~abc001"
			case "jump":
				a.Longitude = ptr(10.0)
			}
			got := p.Update(now, []flight.Aircraft{a})[0]
			if got.EffectiveGroundSpeed() != nil || got.EffectiveTrack() != nil || got.EffectiveVerticalRate() != nil {
				t.Fatalf("invalid observation created calculations: %+v", got.Calculated)
			}
		})
	}
}

func TestVerticalSpikeDoesNotPoisonHorizontalMotionOrLaterTrend(t *testing.T) {
	var p Processor
	update(&p, fix(0, 0, 10000))
	update(&p, fix(30, 0.02, 9500))
	spike := update(&p, fix(60, 0.04, 25000))
	if spike.EffectiveGroundSpeed() == nil || spike.EffectiveVerticalRate() != nil || *spike.AltitudeFeet != 25000 {
		t.Fatal("altitude spike changed raw altitude, poisoned horizontal calculation, or generated a trend")
	}
	update(&p, fix(90, 0.06, 9000))
	update(&p, fix(120, 0.08, 8500))
	recovered := update(&p, fix(150, 0.1, 8000))
	if recovered.EffectiveVerticalRate() == nil {
		t.Fatal("altitude trend did not recover after clean samples")
	}
	near(t, *recovered.EffectiveVerticalRate(), -1000, 0.001)
}

func TestMinimumWindowsAndCurrentAltitudeRequirement(t *testing.T) {
	var p Processor
	update(&p, fix(0, 0, 10000))
	if got := update(&p, fix(5, 0.0033, 9950)); got.EffectiveGroundSpeed() != nil {
		t.Fatal("speed used a sub-10-second window")
	}
	got := update(&p, fix(10, 0.0067, 9900))
	if got.EffectiveGroundSpeed() == nil || got.Calculated.GroundSpeed.Samples != 3 || got.EffectiveVerticalRate() != nil {
		t.Fatal("minimum horizontal/vertical windows were not respected")
	}
	got = update(&p, fix(30, 0.02, 9700))
	if got.EffectiveVerticalRate() == nil {
		t.Fatal("30-second altitude window did not produce a trend")
	}
	a := fix(60, 0.04, 9400)
	a.AltitudeFeet = nil
	if got := update(&p, a); got.EffectiveVerticalRate() != nil {
		t.Fatal("missing current altitude was extrapolated from older samples")
	}
	// A later altitude field on the same cached position cannot create a new
	// altitude/position sample or reuse a regression ending at an older fix.
	a.AltitudeFeet = ptr(9400.0)
	if got := p.Update(epoch.Add(65*time.Second), []flight.Aircraft{a})[0]; got.EffectiveVerticalRate() != nil {
		t.Fatal("cached late altitude manufactured a current vertical trend")
	}
}
