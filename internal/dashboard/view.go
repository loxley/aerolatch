package dashboard

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"aerolatch/internal/flight"
	"aerolatch/internal/schedule"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type palette struct{ border, title, accent, good, warn, bad, dim lipgloss.Style }

func newPalette(r *lipgloss.Renderer) palette {
	style := func(light, dark string) lipgloss.Style {
		return r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: light, Dark: dark})
	}
	return palette{
		border: style("#168052", "#277C5C"), title: style("#007A80", "#63E6DF").Bold(true),
		accent: style("#007A80", "#63E6DF"), good: style("#087A36", "#80ED99").Bold(true),
		warn: style("#996000", "#F5C76B"), bad: style("#B52540", "#FF728A").Bold(true),
		dim: style("#59656D", "#94A7AD"),
	}
}

func clean(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

func fit(value string, width int) string {
	value = ansi.Truncate(value, max(0, width), "…")
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

func (m model) panel(title string, lines []string, width int) string {
	label := ansi.Truncate(clean(title), width-6, "…")
	top := m.styles.border.Render("╭─ ") + m.styles.title.Render(label) + m.styles.border.Render(" "+strings.Repeat("─", max(0, width-5-ansi.StringWidth(label)))+"╮")
	rows := []string{top}
	for _, line := range lines {
		rows = append(rows, m.styles.border.Render("│")+" "+fit(line, width-4)+" "+m.styles.border.Render("│"))
	}
	return strings.Join(append(rows, m.styles.border.Render("╰"+strings.Repeat("─", width-2)+"╯")), "\n")
}

func (m model) badge() string {
	state := m.state()
	style := m.styles.warn
	if state == "LIVE" || state == "LANDED / AIRPORT" {
		style = m.styles.good
	}
	if state == "ERROR" || state == "STOPPED" || state == "CANCELLED" {
		style = m.styles.bad
	}
	dot := "●"
	if state == "LIVE" && !m.liveDotOn {
		dot = " "
	}
	return style.Render(dot + " " + state)
}

func (m model) header(width int) string {
	s := m.snapshot
	identity := clean(s.Query)
	if s.Callsign != "" && s.Callsign != s.Query {
		identity += " → " + clean(s.Callsign)
	}
	badge := m.badge()
	left := m.styles.title.Render(identity) + " · " + clean(s.Source)
	if s.Tracking != nil && s.Tracking.LockedHex != "" {
		left += " · hex " + s.Tracking.LockedHex
	}
	left = fit(left, max(0, width-5-ansi.StringWidth(badge))) + " " + badge
	airline := "Waiting for the first observation…"
	if m.received {
		airline = "Airline unavailable"
	}
	if s.Route != nil && s.Route.Airline != "" {
		airline = clean(s.Route.Airline)
	}
	if r := s.AirportReport; r != nil && r.Match == "matched" && !r.Stale && r.Flight != nil && r.Flight.Airline != "" {
		airline = clean(r.Flight.Airline)
	}
	if a, _ := m.aircraft(); a != nil {
		airline += " · " + clean(aircraftID(*a))
	}
	route := "Destination unavailable; use --destination to enable arrival estimates"
	if s.Route != nil {
		label := "Reference: "
		if s.RouteSource == "Swedavia" {
			label = "Dated route: "
		}
		route = label + airport(s.Route.Origin) + " → " + airport(s.Route.Destination)
	}
	if r := s.AirportReport; r != nil && r.Match == "matched" && !r.Stale && r.Flight != nil {
		route = "Airport route: " + airport(r.Flight.Origin) + " → " + airport(r.Flight.Destination)
		if s.Route != nil && (s.Route.Origin.IATA != r.Flight.Origin.IATA || s.Route.Destination.IATA != r.Flight.Destination.IATA) {
			route += " | ref " + airport(s.Route.Origin) + " → " + airport(s.Route.Destination)
		}
	}
	if s.Destination != nil {
		label := "adsbdb reference"
		if s.Destination.Source == "user_override" {
			label = "override"
		}
		if s.Destination.Source == "swedavia" {
			label = "Swedavia"
		}
		route += " | Using " + airport(s.Destination.Airport) + " (" + label + ")"
		if s.Route == nil {
			route = "Destination: " + airport(s.Destination.Airport) + " (" + label + ")"
		}
	}
	return m.panel("AEROLATCH / RADAR VIEW", []string{left, airline, clean(route)}, width)
}

func (m model) telemetry(width int) string {
	title := "FLIGHT DATA"
	a, retained := m.aircraft()
	altitude, speed, track, rate, aircraft := "unavailable", "unavailable", "unavailable", "unavailable", "Awaiting aircraft"
	if a != nil {
		var c flight.CalculatedKinematics
		if a.Calculated != nil {
			c = *a.Calculated
		}
		altitude, speed, track, rate = measurement(a.AltitudeFeet, "ft"), fallbackMeasurement(a.GroundSpeed, c.GroundSpeed, "kt"), fallbackMeasurement(a.Track, c.GroundTrack, "°"), fallbackMeasurement(a.VerticalRate, c.VerticalRate, "ft/min")
		aircraft = clean(a.Registration + " · " + a.Type)
		if a.Callsign != "" {
			aircraft += " · " + clean(a.Callsign)
		} else {
			aircraft += " · no callsign"
		}
		if a.OnGround != nil && *a.OnGround {
			altitude = "on ground (surface report)"
		}
	}
	if retained {
		title = "LAST OBSERVATION / NOT CURRENT"
	} else if m.state() == "STALE" {
		title = "FLIGHT DATA / STALE"
	}
	if len(m.snapshot.Aircraft) > 1 {
		aircraft = fmt.Sprintf("%d matches; no unique aircraft", len(m.snapshot.Aircraft))
	}
	columns := min(20, max(1, width-9))
	speedLabel := "SPD "
	if calculatedSpeedSeries(m.points, columns) {
		speedLabel = "SPD~"
	}
	return m.panel(title, []string{
		aircraft,
		"Altitude: " + m.styles.accent.Render(altitude),
		"Speed: " + speed + " · Track: " + track,
		"Vertical: " + rate,
		m.styles.dim.Render("Distinct fixes · gap / ~ calc"),
		"ALT " + m.styles.good.Render(sparkline(m.points, columns, true)),
		speedLabel + m.styles.accent.Render(sparkline(m.points, columns, false)),
	}, width)
}

func (m model) arrivalPanel(width int) string {
	s := m.snapshot
	phase, distance, eta := "Phase: unavailable", "Distance: unavailable", "Touchdown: unavailable"
	if m.lastPhase != "" {
		phase = "Last phase: " + human(m.lastPhase)
	}
	if e := s.LandingEstimate; e != nil {
		if e.Phase != "" && m.state() == "LIVE" {
			phase = "Phase: " + human(e.Phase)
		}
		if e.DistanceNM != nil {
			prefix := "Last distance"
			if m.state() == "LIVE" {
				prefix = "Distance"
			}
			distance = fmt.Sprintf("%s: %.1f nm (straight line)", prefix, *e.DistanceNM)
		}
		if e.Status == "estimated" && e.TouchdownAt != nil && m.state() == "LIVE" {
			if e.TouchdownAt.After(m.now) {
				eta = "Touchdown: ~" + e.TouchdownAt.UTC().Format("15:04 UTC") + " (rough)"
			} else {
				eta = "Touchdown: awaiting fresh fix"
			}
		}
	}
	assessment, evidence, surface, reason := "Arrival: unknown", "Gathering observations", "Surface: unavailable", "Waiting for tracking data"
	if m.received && s.Status != "visible" {
		evidence = "No fresh observation"
	}
	if a := s.Arrival; a != nil {
		if a.DistanceNM != nil && (s.LandingEstimate == nil || s.LandingEstimate.DistanceNM == nil) {
			distance = fmt.Sprintf("Last distance: %.1f nm", *a.DistanceNM)
		}
		prefix := "Arrival: "
		if a.Historical {
			prefix = "Last report: "
			if a.Status == "possible_landing" || a.Status == "likely_landed" {
				prefix = "Last inferred: "
			}
		} else if m.state() == "STALE" {
			prefix = "Last report: "
			if a.Status == "possible_landing" || a.Status == "likely_landed" {
				prefix = "Last inferred: "
			}
		}
		style := m.styles.dim
		if a.Status == "possible_landing" {
			style = m.styles.warn
		}
		if a.Status == "likely_landed" {
			style = m.styles.good
		}
		assessment = style.Render(prefix + strings.ToUpper(human(a.Status)))
		reason = clean(a.Reason)
		if a.Approach != nil {
			evidence = fmt.Sprintf("Approach: %d fixes / %s", a.Approach.ObservationCount, a.Approach.LastPositionAt.Sub(a.Approach.StartedAt))
			surface = fmt.Sprintf("Empty replies: %d · surface unknown", a.EmptyResponses)
		}
		if a.FirstSurfaceAt != nil {
			surface = "First surface: " + a.FirstSurfaceAt.UTC().Format("15:04:05Z")
		}
		if a.CorroboratedAt != nil {
			evidence = "Corroborated: " + a.CorroboratedAt.UTC().Format("15:04:05Z")
		}
		if a.SurfaceReports > 0 && a.FirstSurfaceAt != nil && a.LastSurfaceAt != nil {
			prefix := "Surface"
			if a.Historical {
				prefix = "Retained surface"
			}
			evidence = fmt.Sprintf("%s: %d fixes / %s", prefix, a.SurfaceReports, a.LastSurfaceAt.Sub(*a.FirstSurfaceAt).Round(time.Millisecond))
			surface = "Last surface: " + a.LastSurfaceAt.UTC().Format("15:04:05Z")
		}
		if a.Status == "airborne" {
			evidence = "Airborne report; no arrival inferred"
		}
	}
	if s.Tracking != nil && s.Tracking.FlightState == "subsequent_departure" {
		phase, eta, assessment = "New leg / previous route inactive", "Touchdown: new session required", m.styles.warn.Render("SUBSEQUENT DEPARTURE")
		reason = "Continue aircraft tracking; restart for next-leg estimates"
	}
	if r := s.AirportReport; r != nil {
		if f := r.Flight; f != nil && (r.Match == "matched" || r.Stale) {
			prefix := "Airport: "
			if r.Stale {
				prefix = "Last airport: "
			}
			status := m.styles.accent.Render(prefix + schedule.StatusName(f.StatusCode) + " (Swedavia)")
			if f.StatusCode == "LAN" {
				status = m.styles.good.Render(prefix + "LANDED (Swedavia)")
			}
			if f.StatusCode == "CAN" {
				status = m.styles.bad.Render(prefix + "CANCELLED (Swedavia)")
			}
			times, label := f.Arrival, "Arrival"
			if r.Direction == "departures" && f.Arrival.Scheduled == nil {
				times, label = f.Departure, "Departure"
			}
			when := "Time: unavailable"
			if times.Scheduled != nil {
				when = label + " sched: " + times.Scheduled.UTC().Format("02 Jan 15:04Z")
			}
			if times.Estimated != nil {
				when = label + " est: " + times.Estimated.UTC().Format("15:04Z")
			}
			if times.Actual != nil {
				when = label + " actual: " + times.Actual.UTC().Format("15:04:05Z")
			}
			inferred := strings.Replace(assessment, "Arrival: ", "Inferred: ", 1)
			rows := []string{status, when, phase + " · " + distance, eta, inferred, evidence, surface, m.styles.dim.Render("Swedavia fetched " + age(&f.FetchedAt, m.now))}
			return m.panel("ARRIVAL / AIRPORT + INFERRED", rows, width)
		}
		reason = "Swedavia: " + r.Match
	}
	return m.panel("ARRIVAL / INFERRED", []string{phase, distance, m.styles.accent.Render(eta), assessment, evidence, surface, m.styles.dim.Render(reason)}, width)
}

func (m model) position(width int) string {
	position, freshness := "Position unavailable", "Observation timestamps unavailable"
	if a, retained := m.aircraft(); a != nil {
		if a.Latitude != nil && a.Longitude != nil {
			position = fmt.Sprintf("%.5f, %.5f", *a.Latitude, *a.Longitude)
		}
		if retained {
			position += " · retained, not current"
		}
		fix := "unavailable"
		if a.PositionUpdated != nil {
			fix = a.PositionUpdated.UTC().Format("15:04:05Z") + " (" + age(a.PositionUpdated, m.now) + ")"
		}
		freshness = "Fix: " + fix + " · Contact: " + age(a.LastContact, m.now)
	}
	return m.panel("POSITION / ACTUAL OBSERVATION AGE", []string{position, m.styles.dim.Render(freshness)}, width)
}

func (m model) eventPanel(width, count int) string {
	lines := make([]string, 0, count)
	offset := min(m.eventOffset, max(0, len(m.events)-1))
	for i := offset; i < min(len(m.events), offset+count); i++ {
		e := m.events[i]
		lines = append(lines, m.styles.dim.Render(e.at.UTC().Format("15:04:05"))+"  "+e.text)
	}
	if len(lines) == 0 {
		lines = append(lines, "Waiting for tracker events…")
	}
	return m.panel(fmt.Sprintf("EVENTS / %d–%d OF %d", min(offset+1, len(m.events)), min(len(m.events), offset+len(lines)), len(m.events)), lines, width)
}

func (m model) help(width int) string {
	lines := []string{"q / Ctrl+C  Quit and cancel tracking", "↑ / k        Older events", "↓ / j        Newer events", "PgUp/PgDn    Scroll panels in short/narrow terminals", "Home / g     Return to the top and newest events", "? / Esc      Toggle / close this help", "", "Colors: green = live/surface evidence; amber = stale or possible landing.", "Possible landing: airborne-to-surface transition, or approach + coverage loss.", "Likely landed: distinct fresh surface reports with a 3-second debounce.", "Charts show distinct fixes, not evenly spaced time samples; · marks gaps.", "Screen updates do not make API requests. Trend/event history is session-only.", "", "CURRENT DETAILS"}
	if a := m.snapshot.Arrival; a != nil {
		lines = append(lines, "Arrival: "+clean(a.Reason))
		if a.Approach != nil {
			lines = append(lines, "Last approach position: "+a.Approach.LastPositionAt.UTC().Format(time.RFC3339)+" (historical)")
		}
		if a.LastSurfaceAt != nil {
			lines = append(lines, fmt.Sprintf("Surface reports: %d; last surface observation: %s", a.SurfaceReports, a.LastSurfaceAt.UTC().Format(time.RFC3339)))
		}
	}
	if e := m.snapshot.LandingEstimate; e != nil {
		lines = append(lines, "ETA: "+clean(e.Reason))
		if len(e.CalculatedInputs) > 0 {
			lines = append(lines, "Calculated ETA inputs: "+strings.Join(e.CalculatedInputs, ", "))
		}
	}
	if a, _ := m.aircraft(); a != nil {
		lines = append(lines, "MEASUREMENT SOURCES (~ = calculated; SPD~ includes calculated speed points)")
		var c flight.CalculatedKinematics
		if a.Calculated != nil {
			c = *a.Calculated
		}
		for _, item := range []struct {
			name, key, unit string
			reported        *float64
			value           *flight.CalculatedMeasurement
		}{
			{"Ground speed", "ground_speed", "kt", a.GroundSpeed, c.GroundSpeed},
			{"Ground track", "ground_track", "deg", a.Track, c.GroundTrack},
			{"Vertical rate", "vertical_rate", "ft/min", a.VerticalRate, c.VerticalRate},
		} {
			if item.reported != nil {
				lines = append(lines, item.name+": reported by live feed")
				continue
			}
			if item.value != nil {
				v := item.value
				lines = append(lines, fmt.Sprintf("%s: ~%.0f %s; %d samples over %s", item.name, v.Value, item.unit, v.Samples, v.WindowEnd.Sub(v.WindowStart)),
					"Window: "+v.WindowStart.UTC().Format(time.RFC3339)+" to "+v.WindowEnd.UTC().Format(time.RFC3339)+" (end "+age(&v.WindowEnd, m.now)+")",
					"Method: "+clean(v.Method))
			} else {
				reason := c.Unavailable[item.key]
				if reason == "" {
					reason = "not enough suitable observations"
				}
				lines = append(lines, item.name+": unavailable — "+reason)
			}
		}
	}
	if m.snapshot.Error != "" {
		lines = append(lines, "Error: "+clean(m.snapshot.Error))
	}
	if t := m.snapshot.Tracking; t != nil {
		lines = append(lines, "Lookup: "+string(t.Lookup.Kind)+" "+t.Lookup.Value, "Locked hex: "+t.LockedHex, "Flight context: "+clean(t.Reason))
	}
	if r := m.snapshot.AirportReport; r != nil {
		lines = append(lines, "AIRPORT REPORT — Swedavia https://www.swedavia.se", "Matching: "+r.Match+" / "+r.Airport+" "+r.Direction+" "+r.Date)
		lines = append(lines, "Next airport refresh: "+r.NextCheckAt.UTC().Format(time.RFC3339))
		if r.Error != "" {
			lines = append(lines, "Airport lookup: "+clean(r.Error))
		}
		if f := r.Flight; f != nil {
			lines = append(lines, fmt.Sprintf("Flight %s · %s → %s · %s · callsign %s · registration %s", f.FlightID, f.Origin.IATA, f.Destination.IATA, f.StatusCode, f.Callsign, f.Registration))
			for _, item := range []struct {
				name  string
				times flight.FlightTimes
			}{{"Departure", f.Departure}, {"Arrival", f.Arrival}} {
				for _, v := range []struct {
					name string
					at   *time.Time
				}{{"scheduled", item.times.Scheduled}, {"estimated", item.times.Estimated}, {"reported actual", item.times.Actual}} {
					if v.at != nil {
						lines = append(lines, item.name+" "+v.name+": "+v.at.UTC().Format(time.RFC3339))
					}
				}
			}
			lines = append(lines, "Airport record fetched: "+f.FetchedAt.UTC().Format(time.RFC3339)+" (reported status, not aircraft telemetry)")
			lines = append(lines, "Terminal: "+clean(f.Terminal)+" · Gate: "+clean(f.Gate))
		}
	}
	lines = append(lines, "Trend span: "+trendRange(m.points))
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(line, width-4, true), "\n")...)
	}
	return m.panel("HELP / EVIDENCE DETAILS", wrapped, width)
}

func (m model) View() string {
	if m.width < 38 || m.height < 12 {
		return bounded("AEROLATCH\nResize terminal to at least 38×12.\nq / Ctrl+C to quit", m.width, m.height)
	}
	width := min(m.width-2, 140)
	header := m.header(width)
	available := max(1, m.height-lipgloss.Height(header)-2)
	var body string
	if m.showHelp {
		body = m.help(width)
	} else {
		if width >= 76 {
			left := (width - 1) / 2
			body = lipgloss.JoinHorizontal(lipgloss.Top, m.telemetry(left), " ", m.arrivalPanel(width-left-1))
		} else {
			body = m.telemetry(width) + "\n" + m.arrivalPanel(width)
		}
		body += "\n" + m.position(width)
		if m.snapshot.Error != "" {
			lines := strings.Split(ansi.Hardwrap(clean(m.snapshot.Error), width-4, true), "\n")
			body = m.panel("PROVIDER ERROR", lines[:min(2, len(lines))], width) + "\n" + body
		}
		count := max(1, min(6, available-lipgloss.Height(body)-2))
		body += "\n" + m.eventPanel(width, count)
	}
	lines := strings.Split(body, "\n")
	offset := min(m.bodyOffset, max(0, len(lines)-available))
	visible := append([]string(nil), lines[offset:min(len(lines), offset+available)]...)
	for len(visible) < available {
		visible = append(visible, "")
	}
	source := clean(m.snapshot.Source)
	if source == "ADSB.fi" {
		source += " https://adsb.fi"
	}
	if m.snapshot.AirportReport != nil {
		source = "Swedavia https://swedavia.se + " + source
	}
	fetched := "waiting"
	if m.received {
		fetched = age(&m.snapshot.CheckedAt, m.now)
	}
	footer := fmt.Sprintf("%s | %s UTC | fetched %s | poll %s", source, m.now.UTC().Format("15:04:05"), fetched, m.interval)
	if m.snapshot.RetryAt != nil {
		footer = source + " | retry " + m.snapshot.RetryAt.UTC().Format("15:04:05Z")
	}
	if m.finished {
		footer = source + " | TRACKING STOPPED · q to exit"
	}
	keys := "q quit · ↑↓ events · PgUp/PgDn panels · ? help"
	if len(lines) > available {
		keys += fmt.Sprintf(" [%d–%d/%d]", offset+1, min(len(lines), offset+available), len(lines))
	}
	result := header + "\n" + strings.Join(visible, "\n") + "\n" + m.styles.dim.Render(fit(footer, width)) + "\n" + m.styles.dim.Render(fit(keys, width))
	rows := strings.Split(result, "\n")
	for i := range rows {
		rows[i] = " " + fit(rows[i], width)
	}
	return strings.Join(rows, "\n")
}

func age(at *time.Time, now time.Time) string {
	if at == nil {
		return "unavailable"
	}
	if at.After(now) {
		return "ahead of clock"
	}
	return now.Sub(*at).Round(time.Second).String() + " ago"
}

func airport(a flight.Airport) string {
	if a.IATA != "" {
		return clean(a.IATA)
	}
	if a.ICAO != "" {
		return clean(a.ICAO)
	}
	if a.Name != "" {
		return clean(a.Name)
	}
	return "?"
}

func bounded(text string, width, height int) string {
	lines := strings.Split(text, "\n")
	lines = lines[:min(len(lines), max(0, height))]
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(0, width), "")
	}
	return strings.Join(lines, "\n")
}
