package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/arrival"
	"aerolatch/internal/flight"
	"aerolatch/internal/kinematics"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func ptr[T any](v T) *T { return &v }

func fixture(at time.Time) flight.Snapshot {
	fix := at.Add(-time.Second)
	eta := at.Add(2 * time.Minute)
	return flight.Snapshot{
		CheckedAt: at, Query: "NSZ4366", Callsign: "NSZ4366", Source: "ADSB.fi", Status: "visible",
		Route:           &flight.Route{Airline: "Norwegian Air Sweden AOC", Origin: flight.Airport{IATA: "VCE"}, Destination: flight.Airport{IATA: "ARN"}},
		Destination:     &flight.Destination{Airport: flight.Airport{IATA: "ARN"}, Source: "adsbdb"},
		Aircraft:        []flight.Aircraft{{Hex: "4aca87", Registration: "SE-RTG", Type: "B38M", Latitude: ptr(59.60828), Longitude: ptr(17.90298), AltitudeFeet: ptr(300.0), GroundSpeed: ptr(125.0), Track: ptr(10.0), VerticalRate: ptr(-640.0), PositionUpdated: &fix, LastContact: &fix}},
		LandingEstimate: &flight.LandingEstimate{Status: "estimated", Phase: "approach", Quality: "rough", DistanceNM: ptr(2.7), TouchdownAt: &eta},
		Arrival:         &flight.ArrivalAssessment{Status: "airborne", Reason: "airborne observation; no landing inferred"},
	}
}

func apply(m model, msg tea.Msg) model {
	updated, _ := m.Update(msg)
	return updated.(model)
}

func TestRadarLayoutsFitTerminalAndExposeCoreData(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {100, 30}, {80, 24}, {60, 24}, {38, 12}, {20, 5}, {1, 1}, {0, 0}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			at := time.Date(2026, 10, 17, 21, 9, 31, 0, time.UTC)
			m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
			m = apply(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = apply(m, snapshotMsg(fixture(at)))
			view := ansi.Strip(m.View())
			lines := strings.Split(view, "\n")
			if view != "" && len(lines) > size[1] {
				t.Fatalf("view height %d exceeds %d", len(lines), size[1])
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("line too wide (%d > %d): %q", ansi.StringWidth(line), size[0], line)
				}
			}
			if size[0] >= 80 && size[1] >= 24 {
				for _, want := range []string{"AEROLATCH", "NSZ4366", "LIVE", "SE-RTG", "300 ft", "125 kt", "Touchdown:", "rough", "Fix:", "EVENTS", "https://adsb.fi"} {
					if !strings.Contains(view, want) {
						t.Errorf("missing %q:\n%s", want, view)
					}
				}
				if size[0] == 80 {
					t.Log("\n" + view)
				}
			}
			// Help and scrolling must fit too, including after an extreme scroll.
			m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
			for i := 0; i < 20; i++ {
				m = apply(m, tea.KeyMsg{Type: tea.KeyPgDown})
			}
			for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal("help overflowed terminal width")
				}
			}
		})
	}
}

func TestFreshnessAndExpiredETAUpdateWithoutAnotherPoll(t *testing.T) {
	at := time.Now().UTC()
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	s := fixture(at)
	s.LandingEstimate.TouchdownAt = ptr(at.Add(5 * time.Second))
	m = apply(m, snapshotMsg(s))
	if m.state() != "LIVE" {
		t.Fatalf("unexpected state %s", m.state())
	}
	m = apply(m, tickMsg(at.Add(10*time.Second)))
	if !strings.Contains(ansi.Strip(m.View()), "awaiting fresh fix") {
		t.Fatal("expired ETA remained on screen as a current estimate")
	}
	m = apply(m, tickMsg(at.Add(100*time.Second)))
	if m.state() != "STALE" || !strings.Contains(ansi.Strip(m.View()), "STALE") {
		t.Fatal("local ticking failed to mark the data stale")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Last phase: approach") || !strings.Contains(ansi.Strip(m.View()), "Last report: AIRBORNE") {
		t.Fatal("stale phase/airborne reports must not be presented as current")
	}
	if len(m.points) != 1 {
		t.Fatal("clock ticks created fake observations")
	}
}

func TestTrendIdentityCachedFixesMissingValuesAndGaps(t *testing.T) {
	at := time.Now().UTC()
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	s := fixture(at)
	m = apply(m, snapshotMsg(s))
	s.CheckedAt = at.Add(30 * time.Second)
	m = apply(m, snapshotMsg(s))
	if len(m.points) != 1 {
		t.Fatal("cached fix created another trend point")
	}
	next := fixture(at.Add(60 * time.Second))
	next.Aircraft[0].AltitudeFeet, next.Aircraft[0].GroundSpeed = ptr(0.0), nil
	m = apply(m, snapshotMsg(next))
	if len(m.points) != 2 || m.points[1].altitude == nil || *m.points[1].altitude != 0 || m.points[1].speed != nil {
		t.Fatal("known zero or missing speed was lost")
	}
	lost := flight.Snapshot{CheckedAt: at.Add(90 * time.Second), Status: "not_visible", Source: "ADSB.fi", LastObserved: next.Aircraft}
	m = apply(m, snapshotMsg(lost))
	lost.CheckedAt = at.Add(120 * time.Second)
	m = apply(m, snapshotMsg(lost))
	if len(m.points) != 3 || !m.points[2].gap {
		t.Fatal("coverage gap should be represented once")
	}
	if !strings.Contains(ansi.Strip(m.View()), "NOT CURRENT") {
		t.Fatal("retained observations were not clearly labeled")
	}
	if chart := sparkline(m.points, 20, false); chart != "▄··" {
		t.Fatalf("missing speed/gap became a fake number: %q", chart)
	}
	newAircraft := fixture(at.Add(150 * time.Second))
	newAircraft.Aircraft[0].Hex = "abc001"
	newAircraft.LandingEstimate = nil
	m = apply(m, snapshotMsg(newAircraft))
	if len(m.points) != 1 || m.hex != "abc001" || m.lastPhase != "" {
		t.Fatal("trend history leaked between aircraft identities")
	}
	newAircraft.Aircraft = append(newAircraft.Aircraft, newAircraft.Aircraft[0])
	newAircraft.Status = "multiple_matches"
	m = apply(m, snapshotMsg(newAircraft))
	if len(m.points) != 0 || m.state() != "AMBIGUOUS" {
		t.Fatal("ambiguous aircraft must not be selected for charting")
	}
}

func TestOutOfOrderFixesAndStaleFixesDoNotExtendTrends(t *testing.T) {
	at := time.Now().UTC()
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	m = apply(m, snapshotMsg(fixture(at)))
	old := fixture(at.Add(-30 * time.Second))
	old.CheckedAt = at.Add(30 * time.Second)
	m = apply(m, snapshotMsg(old))
	if len(m.points) != 1 {
		t.Fatal("out-of-order fix extended the chart")
	}
	old.CheckedAt = at.Add(3 * time.Minute)
	m = apply(m, snapshotMsg(old))
	if len(m.points) != 2 || !m.points[1].gap {
		t.Fatal("stale observation was plotted as current")
	}
}

func TestSparklineHandlesNegativeAltitudesAndFlatZeros(t *testing.T) {
	points := []trendPoint{{altitude: ptr(300.0)}, {altitude: ptr(-75.0)}, {altitude: ptr(-175.0)}}
	if got := sparkline(points, 20, true); got != "█▂▁" {
		t.Errorf("negative barometric altitudes plotted incorrectly: %q", got)
	}
	if got := sparkline([]trendPoint{{speed: ptr(0.0)}, {speed: ptr(0.0)}}, 20, false); got != "▄▄" {
		t.Errorf("zero-speed series is incorrect: %q", got)
	}
}

func TestEventsAreBoundedDeduplicatedAndScrollable(t *testing.T) {
	at := time.Now().UTC()
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	s := fixture(at)
	m = apply(m, snapshotMsg(s))
	count := len(m.events)
	for i := 0; i < 10; i++ {
		m = apply(m, snapshotMsg(s))
	}
	if len(m.events) != count {
		t.Fatal("identical polls duplicated transition events")
	}
	s.Status, s.Aircraft = "not_visible", nil
	s.Arrival = &flight.ArrivalAssessment{Status: "possible_landing", Reason: "low approach followed by coverage loss"}
	m = apply(m, snapshotMsg(s))
	if !strings.Contains(m.events[0].text, "possible landing") {
		t.Fatalf("arrival transition missing: %+v", m.events)
	}
	for i := 0; i < 150; i++ {
		m.addEvent(fmt.Sprintf("Event %d", i))
	}
	if len(m.events) != 100 {
		t.Fatal("event log grew without a bound")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.eventOffset != 1 {
		t.Fatal("older-event scroll failed")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.eventOffset != 0 {
		t.Fatal("newer-event scroll failed")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.bodyOffset == 0 {
		t.Fatal("panel scrolling failed")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyHome})
	if m.bodyOffset != 0 || m.eventOffset != 0 {
		t.Fatal("home did not reset scrolling")
	}
}

func TestArrivalEvidenceAndProviderErrorsInHelp(t *testing.T) {
	at := time.Now().UTC()
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	s := fixture(at)
	s.Status, s.Aircraft, s.Error = "retrying", nil, "ADSB.fi: rate limit reached (HTTP 429)"
	s.Arrival = &flight.ArrivalAssessment{Status: "possible_landing", Historical: true, Reason: "previous landing inference retained; live lookup failed", Approach: &flight.ApproachEvidence{LastPositionAt: at.Add(-3 * time.Minute), ObservationCount: 3}}
	m = apply(m, snapshotMsg(s))
	m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	text := ansi.Strip(m.help(100))
	for _, want := range []string{"rate limit", "previous landing inference retained", "Last approach position:", "historical"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing detail %q: %s", want, text)
		}
	}
	if m.state() != "RETRYING" {
		t.Fatal("retry status was not shown")
	}
	m = apply(m, doneMsg{err: errors.New("forbidden")})
	if m.state() != "STOPPED" {
		t.Fatal("fatal tracker termination was not shown")
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'q'}}, {Type: tea.KeyCtrlC}} {
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatal("exit key did not produce a command")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("exit key did not request a clean exit")
		}
	}
}

func TestProviderStringsCannotInjectTerminalControls(t *testing.T) {
	if got := clean("flight\x1b[2J\n\tname"); got != "flight  name" {
		t.Fatalf("control characters leaked: %q", got)
	}
}

func calculatedFixtures(base time.Time) []flight.Snapshot {
	var processor kinematics.Processor
	var frames []flight.Snapshot
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * 30 * time.Second)
		s := fixture(at)
		s.Aircraft[0].GroundSpeed, s.Aircraft[0].Track, s.Aircraft[0].VerticalRate = nil, nil, nil
		s.Aircraft[0].Latitude, s.Aircraft[0].Longitude, s.Aircraft[0].AltitudeFeet = ptr(0.0), ptr(float64(i)*0.02), ptr(10000-float64(i)*500)
		s.Aircraft[0].OnGround = ptr(false)
		s.Aircraft[0].PositionUpdated, s.Aircraft[0].LastContact = &at, &at
		s.Aircraft = processor.Update(at, s.Aircraft)
		frames = append(frames, s)
	}
	return frames
}

func TestCalculatedTelemetryLabelsChartsAndDetails(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	for _, s := range calculatedFixtures(base) {
		m = apply(m, snapshotMsg(s))
	}
	for _, size := range [][2]int{{80, 24}, {60, 24}, {110, 32}} {
		m = apply(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := ansi.Strip(m.View())
		for _, want := range []string{"~144 kt", "~90 °", "~-1000 ft/min", "SPD~", "~ calc"} {
			if !strings.Contains(view, want) {
				t.Errorf("size %v missing %q:\n%s", size, want, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("calculated telemetry overflowed terminal width")
			}
		}
	}
	help := ansi.Strip(m.help(110))
	for _, want := range []string{"MEASUREMENT SOURCES", "3 samples", "least_squares_barometric_altitude_trend", "2026-10-20T12:01:00Z"} {
		if !strings.Contains(help, want) {
			t.Errorf("missing provenance detail %q:\n%s", want, help)
		}
	}
	a := m.snapshot.Aircraft[0]
	if a.GroundSpeed != nil || a.Track != nil || a.VerticalRate != nil {
		t.Fatal("dashboard mutated the raw feed values")
	}
}

func TestAirportReportedLandingIsSeparateFromAircraftInference(t *testing.T) {
	at := time.Now().UTC()
	actual := at.Add(-time.Hour)
	s := flight.Snapshot{Query: "D82602", Callsign: "NSZ2602", Source: "ADSB.fi", Status: "reported_landed", CheckedAt: at, Aircraft: []flight.Aircraft{}, Arrival: &flight.ArrivalAssessment{Status: "unknown"}, AirportReport: &flight.AirportReport{Source: "Swedavia", Match: "matched", Direction: "arrivals", Flight: &flight.ScheduledFlight{FlightID: "D82602", Airline: "Norwegian", Origin: flight.Airport{IATA: "HEL"}, Destination: flight.Airport{IATA: "ARN"}, StatusCode: "LAN", Arrival: flight.FlightTimes{Actual: &actual}, FetchedAt: at}}}
	m := newModel(s, 30*time.Second, io.Discard)
	m = apply(m, snapshotMsg(s))
	view := ansi.Strip(m.View())
	for _, want := range []string{"Airport: LANDED (Swedavia)", "Arrival actual:", "Inferred: UNKNOWN", "Airport route: HEL → ARN", "Swedavia https://swedavia.se"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "● LIVE") || len(m.points) != 0 {
		t.Fatal("airport report created a live aircraft observation")
	}
	s.Status = "not_visible"
	s.AirportReport.Stale = true
	s.AirportReport.Match = "unavailable"
	m = apply(m, snapshotMsg(s))
	if !strings.Contains(ansi.Strip(m.View()), "Last airport: LANDED") {
		t.Fatal("stale airport status was presented as current")
	}
}

func TestLockedAircraftShowsCallsignChangesAndNewLeg(t *testing.T) {
	at := time.Now().UTC()
	m := newModel(flight.Snapshot{}, 30*time.Second, io.Discard)
	s := fixture(at)
	s.Tracking = &flight.TrackingInfo{Lookup: flight.Target{Kind: flight.ByCallsign, Value: "NSZ4366"}, LockedHex: "4aca87", FlightState: "tracking"}
	m = apply(m, snapshotMsg(s))
	s = fixture(at.Add(30 * time.Second))
	s.Aircraft[0].Callsign = ""
	s.Tracking = &flight.TrackingInfo{Lookup: flight.Target{Kind: flight.ByHex, Value: "4aca87"}, LockedHex: "4aca87", FlightState: "tracking"}
	m = apply(m, snapshotMsg(s))
	if len(m.points) != 2 || !strings.Contains(ansi.Strip(m.View()), "hex 4aca87") {
		t.Fatal("identity lock or trend continuity lost")
	}
	// Fixture's original callsign is empty too, so first establish and then clear
	// a broadcast callsign to check the actual field rather than the query label.
	s = fixture(at.Add(60 * time.Second))
	s.Aircraft[0].Callsign = "NEW123"
	s.Tracking = &flight.TrackingInfo{LockedHex: "4aca87", FlightState: "tracking"}
	m = apply(m, snapshotMsg(s))
	if !strings.Contains(m.events[0].text, "Broadcast callsign: NEW123") {
		t.Fatalf("missing callsign-change event: %+v", m.events)
	}
	s.Tracking = &flight.TrackingInfo{LockedHex: "4aca87", FlightState: "subsequent_departure", Reason: "old route disabled"}
	s.LandingEstimate = &flight.LandingEstimate{Status: "unavailable"}
	m = apply(m, snapshotMsg(s))
	if !strings.Contains(ansi.Strip(m.View()), "SUBSEQUENT DEPARTURE") || m.lastPhase != "" {
		t.Fatal("subsequent departure retained the old flight phase or estimates")
	}
}

func TestCopenhagenSurfaceEvidenceRemainsVisibleAfterTrackingLoss(t *testing.T) {
	base := time.Date(2026, 10, 19, 21, 26, 1, 0, time.UTC)
	airport := &flight.Airport{IATA: "CPH", Latitude: ptr(55.6179), Longitude: ptr(12.656), ElevationFeet: ptr(17.0)}
	var detector arrival.Detector
	var last flight.Aircraft
	var assessment *flight.ArrivalAssessment
	for _, step := range []struct {
		offset                    int
		ground                    bool
		lat, lon, altitude, speed float64
	}{
		{-41, false, 55.57, 12.55, 700, 140}, {-11, false, 55.596, 12.602, 200, 130},
		{0, true, 55.6049, 12.6232, 0, 56}, {5, true, 55.60594, 12.62481, 0, 56},
	} {
		at := base.Add(time.Duration(step.offset) * time.Second)
		last = flight.Aircraft{Hex: "4787a0", Callsign: "NSZ3235", OnGround: ptr(step.ground), Latitude: ptr(step.lat), Longitude: ptr(step.lon), AltitudeFeet: ptr(step.altitude), GroundSpeed: ptr(step.speed), PositionUpdated: &at, LastContact: &at}
		assessment = detector.Update(at, airport, []flight.Aircraft{last})
	}
	if assessment.Status != "likely_landed" {
		t.Fatalf("fixture did not reproduce landing: %+v", assessment)
	}
	now := base.Add(8*time.Minute + 44*time.Second)
	assessment = detector.Update(now, airport, nil)
	s := flight.Snapshot{CheckedAt: now, Query: "NSZ3235", Callsign: "NSZ3235", Source: "ADSB.lol", Status: "not_visible", LastObserved: []flight.Aircraft{last}, Destination: &flight.Destination{Airport: *airport}, Arrival: assessment}
	m := newModel(s, 30*time.Second, io.Discard)
	m = apply(m, snapshotMsg(s))
	for _, size := range [][2]int{{110, 32}, {80, 24}, {60, 24}} {
		m = apply(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := ansi.Strip(m.View())
		for _, want := range []string{"Last inferred: LIKELY LANDED", "Retained surface: 2 fixes / 5s", "Last surface: 21:26:06Z", "NOT CURRENT", "NOT VISIBLE"} {
			if !strings.Contains(view, want) {
				t.Errorf("size %v missing %q:\n%s", size, want, view)
			}
		}
		for _, wrong := range []string{"Gathering observations", "Surface: unavailable", "Arrival: UNKNOWN"} {
			if strings.Contains(view, wrong) {
				t.Errorf("misleading %q remained in dashboard:\n%s", wrong, view)
			}
		}
	}
	if !strings.Contains(ansi.Strip(m.help(110)), "Surface reports: 2; last surface observation: 2026-10-19T21:26:06Z") {
		t.Fatal("help lost exact surface evidence timestamps")
	}
}

func TestRunCancelsTrackerOnQuitAndContextCancellation(t *testing.T) {
	for _, action := range []string{"q", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input, keys := io.Pipe()
			defer input.Close()
			defer keys.Close()
			started, stopped := make(chan struct{}), make(chan struct{})
			result := make(chan error, 1)
			go func() {
				result <- Run(ctx, input, io.Discard, flight.Snapshot{Query: "TEST", Source: "ADSB.fi"}, 30*time.Second,
					func(ctx context.Context, emit func(flight.Snapshot) error) error {
						defer close(stopped)
						close(started)
						<-ctx.Done()
						return ctx.Err()
					})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("tracker failed to start")
			}
			if action == "q" {
				if _, err := io.WriteString(keys, "q"); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-result:
				if action == "q" && err != nil {
					t.Fatalf("quit returned an error: %v", err)
				}
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("context cancellation lost: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("dashboard did not stop")
			}
			select {
			case <-stopped:
			default:
				t.Fatal("dashboard returned before tracker shutdown")
			}
		})
	}
}

// A separate process in a PTY exercises real terminal cleanup without changing
// the parent test runner's terminal. Enabled explicitly by the smoke harness.
func TestDashboardTerminalHelper(t *testing.T) {
	mode := os.Getenv("AEROLATCH_TUI_TEST")
	if mode == "" {
		t.Skip("PTY smoke helper")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "cancel" {
		go func() { time.Sleep(400 * time.Millisecond); cancel() }()
	}
	err := Run(ctx, os.Stdin, os.Stdout, flight.Snapshot{Query: "NSZ4366", Callsign: "NSZ4366", Source: "ADSB.fi"}, 30*time.Second,
		func(ctx context.Context, emit func(flight.Snapshot) error) error {
			frames := []flight.Snapshot{fixture(time.Now().UTC())}
			if mode == "calculated" {
				frames = calculatedFixtures(time.Now().UTC().Add(-time.Minute))
			}
			for _, s := range frames {
				if err := emit(s); err != nil {
					return err
				}
			}
			if mode == "fatal" {
				return errors.New("test provider failure")
			}
			<-ctx.Done()
			return ctx.Err()
		})
	if mode == "fatal" {
		if err == nil || err.Error() != "test provider failure" {
			t.Fatalf("fatal error lost: %v", err)
		}
	} else if mode == "cancel" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error lost: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}
