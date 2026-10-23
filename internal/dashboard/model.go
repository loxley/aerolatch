package dashboard

import (
	"fmt"
	"io"
	"strings"
	"time"

	"aerolatch/internal/flight"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type snapshotMsg flight.Snapshot
type tickMsg time.Time
type doneMsg struct{ err error }

type event struct {
	at   time.Time
	text string
}

type model struct {
	snapshot                     flight.Snapshot
	interval                     time.Duration
	now                          time.Time
	width, height                int
	received, finished, showHelp bool
	liveDotOn                    bool
	events                       []event // Newest first; bounded for long-running sessions.
	eventOffset, bodyOffset      int
	points                       []trendPoint
	hex                          string
	lastFix                      time.Time
	lastState, lastPhase         string
	styles                       palette
}

func newModel(initial flight.Snapshot, interval time.Duration, output io.Writer) model {
	return model{snapshot: initial, interval: interval, now: time.Now().UTC(),
		width: 80, height: 24, liveDotOn: true, styles: newPalette(lipgloss.NewRenderer(output))}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(at time.Time) tea.Msg { return tickMsg(at) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.bodyOffset = 0
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.showHelp = !m.showHelp
			m.bodyOffset = 0
		case "esc":
			m.showHelp = false
			m.bodyOffset = 0
		case "up", "k":
			m.eventOffset = min(max(0, len(m.events)-1), m.eventOffset+1)
		case "down", "j":
			m.eventOffset = max(0, m.eventOffset-1)
		case "pgdown", "ctrl+f":
			m.bodyOffset += max(1, m.height-8)
		case "pgup", "ctrl+b":
			m.bodyOffset = max(0, m.bodyOffset-max(1, m.height-8))
		case "home", "g":
			m.bodyOffset, m.eventOffset = 0, 0
		}
	case snapshotMsg:
		m.accept(flight.Snapshot(msg))
	case tickMsg:
		m.now = time.Time(msg).UTC()
		m.liveDotOn = !m.liveDotOn
		m.stateEvent()
		return m, tick()
	case doneMsg:
		m.finished = true
		if msg.err != nil {
			m.addEvent("Tracking stopped: " + msg.err.Error())
		} else {
			m.addEvent("Tracking finished")
		}
		m.stateEvent()
	}
	return m, nil
}

func (m *model) accept(s flight.Snapshot) {
	previous, hadPrevious := m.snapshot, m.received
	m.snapshot, m.received, m.now = s, true, s.CheckedAt
	if hadPrevious && previous.Source != s.Source {
		m.resetTrends()
		m.addEvent("Live feed changed to " + s.Source)
	}
	m.recordTrends()
	m.stateEvent()
	if r := s.AirportReport; r != nil && r.Flight != nil && (r.Match == "matched" || r.Stale) {
		old := previous.AirportReport
		if old == nil || old.Flight == nil || old.Flight.StatusCode != r.Flight.StatusCode || old.Stale != r.Stale {
			label := "Airport report: " + r.Flight.StatusCode + " (Swedavia)"
			if r.Stale {
				label += " — stale"
			}
			m.addEvent(label)
		}
	}
	if s.Tracking != nil {
		if s.Tracking.LockedHex != "" && (previous.Tracking == nil || previous.Tracking.LockedHex != s.Tracking.LockedHex) {
			m.addEvent("Identity locked: " + s.Tracking.LockedHex + "; following hex address")
		}
		if s.Tracking.FlightState == "subsequent_departure" && (previous.Tracking == nil || previous.Tracking.FlightState != s.Tracking.FlightState) {
			m.lastPhase = ""
			m.addEvent("Subsequent departure detected; previous-flight estimates disabled")
		}
	}
	if len(s.Aircraft) == 1 && len(previous.Aircraft) == 1 && s.Aircraft[0].Hex == previous.Aircraft[0].Hex && s.Aircraft[0].Callsign != previous.Aircraft[0].Callsign {
		label := s.Aircraft[0].Callsign
		if label == "" {
			label = "not broadcast"
		}
		m.addEvent("Broadcast callsign: " + label + "; same aircraft")
	}
	if s.LandingEstimate != nil && s.LandingEstimate.Phase != "" && s.LandingEstimate.Phase != m.lastPhase {
		m.lastPhase = s.LandingEstimate.Phase
		m.addEvent("Flight phase: " + human(m.lastPhase))
	}
	if s.Error != "" && (!hadPrevious || previous.Error != s.Error) {
		m.addEvent("Feed error: " + s.Error)
	}
	var old *flight.ArrivalAssessment
	if hadPrevious {
		old = previous.Arrival
	}
	if a := s.Arrival; a != nil && (old == nil || a.Status != old.Status || a.Historical != old.Historical) {
		switch a.Status {
		case "possible_landing", "likely_landed":
			label := human(a.Status)
			if a.Historical {
				label += " (retained inference)"
			}
			m.addEvent(label)
		case "on_ground_near_destination":
			if a.Historical {
				m.addEvent("Previous surface observation retained; arrival remains unconfirmed")
			} else {
				m.addEvent("Surface report near destination; arrival unconfirmed")
			}
		case "airborne":
			if old != nil && (old.Status == "possible_landing" || old.Status == "likely_landed") {
				m.addEvent("Airborne again; previous arrival assessment superseded")
			}
		}
	}
}

func (m *model) addEvent(text string) {
	if m.eventOffset > 0 {
		m.eventOffset++
	}
	m.events = append([]event{{at: m.now, text: clean(text)}}, m.events...)
	if len(m.events) > 100 {
		m.events = m.events[:100]
	}
	m.eventOffset = min(m.eventOffset, len(m.events)-1)
}

func (m *model) stateEvent() {
	state := m.state()
	if state != m.lastState {
		m.lastState = state
		if m.received {
			m.addEvent("Feed: " + strings.ToLower(state))
		}
	}
}

func (m model) state() string {
	if m.finished {
		return "STOPPED"
	}
	if !m.received {
		return "CONNECTING"
	}
	if m.now.Sub(m.snapshot.CheckedAt) > 90*time.Second && (m.snapshot.Status == "visible" || m.snapshot.Status == "not_visible" || m.snapshot.Status == "multiple_matches") {
		return "STALE"
	}
	switch m.snapshot.Status {
	case "scheduled":
		return "SCHEDULED"
	case "waiting_for_schedule":
		return "WAITING / SCHEDULE"
	case "cancelled":
		return "CANCELLED"
	case "reported_landed":
		return "LANDED / AIRPORT"
	case "deleted":
		return "REMOVED / AIRPORT"
	case "visible":
		a, _ := m.aircraft()
		if a == nil || a.PositionUpdated == nil || a.Latitude == nil || a.Longitude == nil {
			return "VISIBLE / NO FIX"
		}
		age := m.now.Sub(*a.PositionUpdated)
		if age < -5*time.Second {
			return "CLOCK SKEW"
		}
		if age > 90*time.Second || m.now.Sub(m.snapshot.CheckedAt) > 90*time.Second {
			return "STALE"
		}
		return "LIVE"
	case "not_visible":
		return "NOT VISIBLE"
	case "multiple_matches":
		return "AMBIGUOUS"
	case "retrying":
		return "RETRYING"
	case "error":
		return "ERROR"
	default:
		return "WAITING"
	}
}

func (m model) aircraft() (*flight.Aircraft, bool) {
	if len(m.snapshot.Aircraft) == 1 {
		return &m.snapshot.Aircraft[0], false
	}
	if len(m.snapshot.Aircraft) == 0 && len(m.snapshot.LastObserved) == 1 {
		return &m.snapshot.LastObserved[0], true
	}
	return nil, false
}

func human(value string) string {
	return strings.ReplaceAll(value, "_", " ")
}

func measurement(value *float64, unit string) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.0f %s", *value, unit)
}

func fallbackMeasurement(reported *float64, calculated *flight.CalculatedMeasurement, unit string) string {
	if reported != nil {
		return measurement(reported, unit)
	}
	if calculated != nil {
		return "~" + measurement(&calculated.Value, unit)
	}
	return "unavailable"
}
