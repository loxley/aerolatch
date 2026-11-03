package dashboard

import (
	"fmt"
	"io"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/nearby"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type nearbyMsg flight.NearbySnapshot

type nearbyModel struct {
	snapshot                 flight.NearbySnapshot
	interval                 time.Duration
	now                      time.Time
	width, height            int
	selected, picked         string
	received, finished, help bool
	selectionLost            bool
	styles                   palette
}

func newNearbyModel(s flight.NearbySnapshot, interval time.Duration, output io.Writer) nearbyModel {
	return newNearbyWithPalette(s, interval, newPalette(lipgloss.NewRenderer(output)))
}

func newNearbyWithPalette(s flight.NearbySnapshot, interval time.Duration, styles palette) nearbyModel {
	return nearbyModel{snapshot: s, interval: interval, now: time.Now().UTC(), width: 80, height: 24,
		styles: styles}
}

func (m nearbyModel) Init() tea.Cmd { return tick() }

func (m nearbyModel) rows() []flight.NearbyAircraft {
	if !m.received {
		return nil
	}
	if m.snapshot.Error != "" {
		return m.snapshot.LastObserved
	}
	rows := make([]flight.NearbyAircraft, 0, len(m.snapshot.Aircraft))
	for _, row := range m.snapshot.Aircraft {
		if nearby.Fresh(row.Aircraft, m.now) {
			rows = append(rows, row)
		}
	}
	return rows
}

func (m nearbyModel) index(rows []flight.NearbyAircraft) int {
	for i, row := range rows {
		if row.Hex == m.selected {
			return i
		}
	}
	return -1
}

func (m nearbyModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.now = time.Time(msg).UTC()
		return m, tick()
	case nearbyMsg:
		m.snapshot, m.now = flight.NearbySnapshot(msg), msg.CheckedAt
		m.received = true
		rows := m.rows()
		if m.selected != "" && m.index(rows) < 0 {
			m.selected, m.selectionLost = "", true
		}
		if m.selected == "" && !m.selectionLost && len(rows) > 0 {
			m.selected = rows[0].Hex
		}
	case doneMsg:
		m.finished = true
		if msg.err != nil && m.snapshot.Error == "" {
			m.snapshot.Error = msg.err.Error()
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.help = !m.help
		case "esc":
			m.help = false
		case "up", "k", "down", "j", "pgup", "pgdown", "home", "end":
			rows := m.rows()
			if m.help || len(rows) == 0 {
				break
			}
			i := m.index(rows)
			switch msg.String() {
			case "up", "k":
				i = max(0, i-1)
			case "down", "j":
				i = min(len(rows)-1, i+1)
			case "pgup":
				i = max(0, i-max(1, m.height-12))
			case "pgdown":
				i = min(len(rows)-1, max(0, i)+max(1, m.height-12))
			case "home":
				i = 0
			case "end":
				i = len(rows) - 1
			}
			m.selected, m.selectionLost = rows[i].Hex, false
		case "enter":
			if !m.help && !m.finished && m.received && m.snapshot.Status == "visible" && m.snapshot.Error == "" && m.index(m.rows()) >= 0 {
				m.picked = m.selected
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func nearbyCallsign(a flight.Aircraft) string {
	for _, value := range []string{a.Callsign, a.Registration, a.Hex} {
		if strings.TrimSpace(value) != "" {
			return clean(value)
		}
	}
	return "unknown"
}

func (m nearbyModel) View() string {
	if m.width < 38 || m.height < 14 {
		return bounded("AEROLATCH / NEARBY\nResize terminal to at least 38×14.\nq / Ctrl+C to quit", m.width, m.height)
	}
	width := min(m.width-2, 160)
	panel := model{styles: m.styles}.panel
	s := m.snapshot
	state := "CONNECTING"
	rows := m.rows()
	if m.received {
		state = fmt.Sprintf("%d FRESH AIRCRAFT", len(rows))
	}
	if s.Error != "" {
		state = "RETRYING / NOT CURRENT"
	}
	if m.finished {
		state = "STOPPED"
	}
	reference := "Supplied coordinate reference point"
	if s.Location.Source == "city" {
		reference = "City reference point — not your exact position"
	}
	fetched := "waiting"
	if m.received {
		fetched = age(&s.CheckedAt, m.now)
	}
	header := panel("AEROLATCH / NEARBY AIRCRAFT", []string{
		clean(s.Location.Label()) + fmt.Sprintf(" · Radius: %d nm", s.RadiusNM),
		fmt.Sprintf("%.5f, %.5f · %s", s.Location.Latitude, s.Location.Longitude, reference),
		clean(s.Source) + " · " + state + " · fetched " + fetched,
	}, width)
	if m.help {
		body := panel("NEARBY HELP", []string{
			"↑↓ / j k select · PgUp/PgDn page · Enter track · q quit",
			"Esc in detail returns here; close detail help first.",
			"Sorted by horizontal distance from the fixed search centre.",
			"Bearing is from the centre, clockwise from true north.",
			"Not device geolocation, slant range, or visibility prediction.",
			"Only valid ICAO hex and fixes ≤90s old (≤5s ahead) are listed.",
			"Retained results after errors are NOT CURRENT or selectable.",
			"Selected aircraft is followed by hex, not its changing callsign.",
			"Reference routes resolve in the background; not confirmed dated flights.",
			"Open-Meteo https://open-meteo.com · GeoNames https://geonames.org",
			"ADSB.fi https://adsb.fi · ADSB.lol https://adsb.lol",
		}, width)
		return bounded(header+"\n"+body+"\n? / Esc close help · q quit", m.width, m.height)
	}
	columns := "  CALLSIGN  TYPE    DISTANCE  BEARING    ALTITUDE    SPEED  FIX AGE"
	if width < 72 {
		columns = "  CALLSIGN DISTANCE BEARING ALTITUDE"
	}
	lines := []string{m.styles.dim.Render(columns)}
	count := max(1, m.height-12)
	index := m.index(rows)
	offset := max(0, index-count+1)
	for i := offset; i < min(len(rows), offset+count); i++ {
		r := rows[i]
		marker := "  "
		if i == index {
			marker = "▸ "
		}
		altitude := measurement(r.AltitudeFeet, "ft")
		if r.OnGround != nil && *r.OnGround {
			altitude = "GROUND"
		}
		bearing := fmt.Sprintf("%s %03.0f°", r.Direction, r.BearingDegrees)
		line := fmt.Sprintf("%s%s %s %7.1f nm %-8s %10s %8s %7s", marker, fit(nearbyCallsign(r.Aircraft), 9), fit(clean(r.Type), 5), r.DistanceNM, bearing,
			altitude, measurement(r.GroundSpeed, "kt"), strings.TrimSuffix(age(r.PositionUpdated, m.now), " ago"))
		if width < 72 {
			line = fmt.Sprintf("%s%s %5.1fnm %-7s %s", marker, fit(nearbyCallsign(r.Aircraft), 8), r.DistanceNM, bearing, altitude)
		}
		if i == index {
			line = m.styles.accent.Render(line)
		}
		lines = append(lines, line)
	}
	for len(lines) < count+1 {
		line := ""
		if len(lines) == 1 {
			line = "Waiting for aircraft…"
			if m.received {
				line = "No fresh positions in radius; coverage may be incomplete."
			}
		}
		lines = append(lines, line)
	}
	detail := "Select a row to inspect an aircraft"
	if index >= 0 {
		r := rows[index]
		detail = fmt.Sprintf("Hex %s · %s · fix %s", r.Hex, clean(r.Registration), age(r.PositionUpdated, m.now))
	}
	status := fmt.Sprintf("%d excluded fixes · poll %s · rows %d–%d/%d", s.Excluded, m.interval, min(offset+1, len(rows)), min(offset+count, len(rows)), len(rows))
	if m.selectionLost {
		status = "Selected aircraft left the fresh list; use arrows to select again"
	}
	if s.Error != "" {
		status = "NOT CURRENT: " + clean(s.Error)
		if s.RetryAt != nil {
			status += " · retry " + s.RetryAt.Format("15:04:05Z")
		}
	}
	lines = append(lines, m.styles.dim.Render(detail), m.styles.warn.Render(status))
	body := panel("AIRCRAFT / NEAREST FIRST", lines, width)
	attribution := clean(s.Source)
	if s.Source == "ADSB.fi" {
		attribution += " https://adsb.fi"
	}
	if s.Location.Source == "city" {
		attribution += " · Location: Open-Meteo / GeoNames"
	}
	keys := "↑↓ select · Enter track · ? help · q / Ctrl+C quit"
	return bounded(header+"\n"+body+"\n"+fit(attribution, width)+"\n"+fit(keys, width), m.width, m.height)
}
