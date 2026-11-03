package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aerolatch/internal/flight"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func nearbyFixture(now time.Time) flight.NearbySnapshot {
	return flight.NearbySnapshot{CheckedAt: now, Location: flight.Location{Name: "Stockholm", Country: "Sweden", Latitude: 59.33, Longitude: 18.07, Source: "city"}, RadiusNM: 25, Source: "ADSB.fi", Status: "visible",
		Aircraft: []flight.NearbyAircraft{
			{Aircraft: flight.Aircraft{Hex: "abc001", Callsign: "SAS123", Type: "A320", Latitude: ptr(59.34), Longitude: ptr(18.07), PositionUpdated: &now, AltitudeFeet: ptr(8400.0)}, DistanceNM: 0.6, Direction: "N"},
			{Aircraft: flight.Aircraft{Hex: "abc002", Callsign: "NSZ4366", Type: "B38M", Latitude: ptr(59.36), Longitude: ptr(18.07), PositionUpdated: &now, AltitudeFeet: ptr(12100.0)}, DistanceNM: 1.8, Direction: "N"},
		}}
}

func updateNearby(m nearbyModel, msg tea.Msg) nearbyModel {
	next, _ := m.Update(msg)
	return next.(nearbyModel)
}

func TestNearbySelectionFollowsHexAndNeverSwitchesSilently(t *testing.T) {
	now := time.Now().UTC()
	s := nearbyFixture(now)
	m := newNearbyModel(s, 10*time.Second, io.Discard)
	m = updateNearby(m, nearbyMsg(s))
	m = updateNearby(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.selected != "abc002" {
		t.Fatal("failed to select second aircraft")
	}
	s.Aircraft[0], s.Aircraft[1] = s.Aircraft[1], s.Aircraft[0]
	m = updateNearby(m, nearbyMsg(s))
	if m.selected != "abc002" || m.index(m.rows()) != 0 {
		t.Fatal("selection moved with row index instead of aircraft identity")
	}
	s.Aircraft = s.Aircraft[1:]
	m = updateNearby(m, nearbyMsg(s))
	m = updateNearby(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.selected != "" || m.picked != "" || !m.selectionLost {
		t.Fatal("removed aircraft silently selected/followed another aircraft")
	}
	m = updateNearby(m, tea.KeyMsg{Type: tea.KeyDown})
	m = updateNearby(m, tickMsg(now.Add(91*time.Second)))
	m = updateNearby(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.rows()) != 0 || m.picked != "" {
		t.Fatal("expired fixes remained selectable between polls")
	}
}

func TestNearbyLayoutsAndHistoricalResults(t *testing.T) {
	now := time.Now().UTC()
	s := nearbyFixture(now)
	m := newNearbyModel(s, 10*time.Second, io.Discard)
	m = updateNearby(m, nearbyMsg(s))
	for _, size := range [][2]int{{110, 32}, {80, 24}, {60, 24}, {38, 14}, {30, 10}} {
		m = updateNearby(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > size[1] {
			t.Errorf("layout overflow at %v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("width overflow at %v: %q", size, line)
			}
		}
		if size[0] >= 60 && !strings.Contains(ansi.Strip(view), "SAS123") {
			t.Errorf("aircraft missing at %v: %s", size, view)
		}
	}
	s.Status, s.Error, s.LastObserved, s.Aircraft = "retrying", "rate limited", s.Aircraft, nil
	m = updateNearby(m, nearbyMsg(s))
	m = updateNearby(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = updateNearby(m, tea.WindowSizeMsg{Width: 110, Height: 32})
	if m.picked != "" || !strings.Contains(ansi.Strip(m.View()), "NOT CURRENT") {
		t.Fatal("retained observations presented as current/selectable")
	}
}

func TestDetailEscapeClosesHelpThenReturnsToNearby(t *testing.T) {
	m := newModel(flight.Snapshot{}, time.Second, io.Discard)
	m.canReturn, m.showHelp = true, true
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.returnToNearby || m.showHelp {
		t.Fatal("Escape did not just close help first")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.returnToNearby {
		t.Fatal("Escape did not request return to nearby")
	}
}

func TestNonBlockingThemeHints(t *testing.T) {
	for _, tc := range []struct {
		hint string
		dark bool
	}{{"", true}, {"15;0", true}, {"0;15", false}, {"0;7", false}, {"invalid", true}} {
		if got := darkBackground(tc.hint); got != tc.dark {
			t.Errorf("hint %q: dark=%v", tc.hint, got)
		}
	}
}

func TestSelectedObservationIsVisibleWithoutInventingFreshData(t *testing.T) {
	s := nearbyFixture(time.Now().UTC().Add(-20 * time.Second))
	m := browserModel{nearby: newNearbyModel(s, 10*time.Second, io.Discard), initial: s,
		interval: 10 * time.Second, output: io.Discard, generation: 1, transition: func(browserRequest) tea.Cmd { return nil }}
	next, _ := m.Update(browserData{generation: 1, snapshot: s})
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(browserModel)
	if m.detail.received || len(m.detail.snapshot.Aircraft) != 0 || len(m.detail.snapshot.LastObserved) != 1 {
		t.Fatal("selection seed was promoted to a fresh detail response")
	}
	if !m.detail.snapshot.LastObserved[0].PositionUpdated.Equal(*s.Aircraft[0].PositionUpdated) {
		t.Fatal("seed timestamp was refreshed")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"SAS123", "last known", "8400 ft", "NOT CURRENT", "CONNECTING"} {
		if !strings.Contains(view, want) {
			t.Errorf("initial detail missing %q: %s", want, view)
		}
	}
}

func TestBrowserDiscardsQueuedSnapshotsFromPreviousView(t *testing.T) {
	s := nearbyFixture(time.Now().UTC())
	m := browserModel{nearby: newNearbyModel(s, 10*time.Second, io.Discard), initial: s,
		interval: 10 * time.Second, output: io.Discard, generation: 1,
		transition: func(browserRequest) tea.Cmd { return nil }}
	update := func(msg tea.Msg) {
		next, _ := m.Update(msg)
		m = next.(browserModel)
	}
	update(browserData{generation: 1, snapshot: s})
	update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.detailMode || m.generation != 2 {
		t.Fatal("detail view not entered")
	}
	update(browserData{generation: 1, snapshot: flight.Snapshot{Query: "WRONG AIRCRAFT"}})
	update(browserDone{generation: 1, err: errors.New("old poll failed")})
	if m.detail.received || m.detail.finished {
		t.Fatal("old worker overwrote or stopped the new view")
	}
	update(browserData{generation: 2, snapshot: flight.Snapshot{Query: "abc001", CheckedAt: time.Now()}})
	if !m.detail.received || m.detail.snapshot.Query != "abc001" {
		t.Fatal("current worker snapshot not delivered")
	}
	update(tea.KeyMsg{Type: tea.KeyEsc})
	update(browserDone{generation: 2, err: errors.New("late detail error")})
	if m.detailMode || m.generation != 3 || m.nearby.finished || m.nearby.received {
		t.Fatal("return did not start an independent fresh nearby view")
	}
}

func TestBrowseJoinsEachPollerBeforeChangingViews(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input, keys := io.Pipe()
	defer input.Close()
	defer keys.Close()
	events := make(chan string, 10)
	scan := func(ctx context.Context, emit func(flight.NearbySnapshot) error) error {
		if err := emit(nearbyFixture(time.Now().UTC())); err != nil {
			return err
		}
		events <- "scan-ready"
		<-ctx.Done()
		events <- "scan-stopped"
		return ctx.Err()
	}
	track := func(ctx context.Context, selection flight.Selection, emit func(flight.Snapshot) error) error {
		events <- "track:" + selection.Aircraft.Hex
		<-ctx.Done()
		events <- "track-stopped"
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		result <- Browse(ctx, input, io.Discard, nearbyFixture(time.Now()), 10*time.Second, scan, track)
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("lifecycle event %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	expect("scan-ready")
	if _, err := io.WriteString(keys, "j\r"); err != nil {
		t.Fatal(err)
	}
	expect("scan-stopped")
	expect("track:abc002")
	if _, err := io.WriteString(keys, "\x1b"); err != nil {
		t.Fatal(err)
	}
	expect("track-stopped")
	expect("scan-ready")
	if _, err := io.WriteString(keys, "q"); err != nil {
		t.Fatal(err)
	}
	expect("scan-stopped")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("browsing did not return after quit")
	}
}

func TestPlacePickerKeepsDistinctLocationIDs(t *testing.T) {
	m := placeModel{places: []flight.Location{{ID: 1}, {ID: 2}}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selected := next.(placeModel)
	if !selected.chosen || selected.places[selected.index].ID != 2 {
		t.Fatal("picker selected the wrong place")
	}
}

// Opt-in fixture for real PTY navigation, screen cleanup, and signal checks.
func TestNearbyTerminalHelper(t *testing.T) {
	mode := os.Getenv("AEROLATCH_TUI_TEST")
	if mode != "nearby" && mode != "nearby-cancel" {
		t.Skip("PTY smoke helper")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "nearby-cancel" {
		go func() { time.Sleep(600 * time.Millisecond); cancel() }()
	}
	var scans, details atomic.Int32
	err := Browse(ctx, os.Stdin, os.Stdout, nearbyFixture(time.Now()), 10*time.Second,
		func(ctx context.Context, emit func(flight.NearbySnapshot) error) error {
			scans.Add(1)
			if err := emit(nearbyFixture(time.Now().UTC())); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}, func(ctx context.Context, selection flight.Selection, emit func(flight.Snapshot) error) error {
			details.Add(1)
			s := fixture(time.Now().UTC())
			s.Query = selection.Aircraft.Hex
			if err := emit(s); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		})
	if mode == "nearby-cancel" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "NEARBY_LIFECYCLE_OK scan=%d detail=%d\n", scans.Load(), details.Load())
}
