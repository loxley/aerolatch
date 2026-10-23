package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/schedule"
)

func render(out io.Writer, s flight.Snapshot) error {
	var b strings.Builder
	heading := s.Query
	if s.Callsign != "" {
		heading += " → " + s.Callsign
	}
	fmt.Fprintf(&b, "\n%s  %s\n", s.CheckedAt.Format(time.RFC3339), heading)
	if tracking := s.Tracking; tracking != nil {
		fmt.Fprintf(&b, "Lookup: %s %s", tracking.Lookup.Kind, tracking.Lookup.Value)
		if !tracking.LiveLookupPerformed {
			fmt.Fprint(&b, " (not polled)")
		}
		if tracking.LockedHex != "" {
			fmt.Fprintf(&b, " | Following hex %s", tracking.LockedHex)
		}
		fmt.Fprintln(&b)
		if tracking.FlightState == "subsequent_departure" {
			fmt.Fprintf(&b, "Flight context: %s\n", tracking.Reason)
		}
	}
	if s.Route != nil {
		label := "Reference route"
		if s.RouteSource == "Swedavia" {
			label = "Dated route (Swedavia)"
		}
		fmt.Fprintf(&b, "Airline: %s\n%s: %s → %s\n", text(s.Route.Airline), label, airport(s.Route.Origin), airport(s.Route.Destination))
	}
	if s.Destination != nil {
		source := "adsbdb reference"
		if s.Destination.Source == "user_override" {
			source = "user override; airport data: " + s.Destination.DataSource
		}
		if s.Destination.Source == "swedavia" {
			source = "Swedavia dated flight"
		}
		fmt.Fprintf(&b, "Destination used: %s — %s\n", airport(s.Destination.Airport), source)
	}
	switch s.Status {
	case "scheduled", "waiting_for_schedule":
		fmt.Fprintln(&b, "Status: scheduled / waiting for a dated flight or assigned aircraft")
	case "cancelled", "reported_landed", "deleted":
		fmt.Fprintf(&b, "Status: %s — airport reported; no aircraft acquired\n", strings.ReplaceAll(s.Status, "_", " "))
	case "visible":
		fmt.Fprintln(&b, "Status: visible in live feed")
	case "multiple_matches":
		fmt.Fprintf(&b, "Status: multiple matches (%d); showing all aircraft, no unique match\n", len(s.Aircraft))
	case "not_visible":
		fmt.Fprintln(&b, "Status: not currently visible (does not confirm landing or cancellation)")
	case "retrying", "error":
		fmt.Fprintf(&b, "Status: %s — %s\n", s.Status, s.Error)
	}
	if r := s.AirportReport; r != nil {
		fmt.Fprintln(&b, "Airport data: Swedavia — https://www.swedavia.se")
		fmt.Fprintf(&b, "Flight selection: %s %s on %s | %s\n", r.Airport, r.Direction, r.Date, r.Match)
		if r.Flight != nil && (r.Match == "matched" || r.Stale) {
			f := r.Flight
			label := "Airport-reported status"
			if r.Stale {
				label = "Last airport-reported status (stale)"
			}
			fmt.Fprintf(&b, "%s: %s (%s) | %s\n", label, schedule.StatusName(f.StatusCode), f.StatusCode, f.FlightID)
			fmt.Fprintf(&b, "Airport route: %s → %s | Callsign: %s | Assigned registration: %s\n", airport(f.Origin), airport(f.Destination), text(f.Callsign), text(f.Registration))
			for _, item := range []struct {
				name  string
				times flight.FlightTimes
			}{{"Departure", f.Departure}, {"Arrival", f.Arrival}} {
				for _, t := range []struct {
					label string
					at    *time.Time
				}{{"scheduled", item.times.Scheduled}, {"estimated", item.times.Estimated}, {"actual (reported)", item.times.Actual}} {
					if t.at != nil {
						fmt.Fprintf(&b, "%s %s: %s\n", item.name, t.label, t.at.UTC().Format(time.RFC3339))
					}
				}
			}
			fmt.Fprintf(&b, "Airport record fetched: %s | Terminal: %s | Gate: %s\n", f.FetchedAt.UTC().Format(time.RFC3339), text(f.Terminal), text(f.Gate))
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "Swedavia lookup: %s\n", r.Error)
		}
	}
	for _, aircraft := range s.Aircraft {
		renderAircraft(&b, aircraft, s.CheckedAt)
	}
	if assessment := s.Arrival; assessment != nil {
		switch assessment.Status {
		case "likely_landed", "possible_landing":
			status := "likely landed"
			if assessment.Status == "possible_landing" {
				status = "possible landing"
			}
			label := "Arrival assessment: " + status
			if assessment.Historical {
				label = "Last inferred arrival assessment: " + status
			}
			if s.Destination != nil {
				label += " at " + airport(s.Destination.Airport)
			}
			fmt.Fprintln(&b, label)
			fmt.Fprintf(&b, "Evidence: %s\n", assessment.Reason)
			if assessment.FirstSurfaceAt != nil {
				fmt.Fprintf(&b, "First surface observation: %s (not an exact touchdown time)\n", assessment.FirstSurfaceAt.Format(time.RFC3339))
			}
			if assessment.CorroboratedAt != nil {
				fmt.Fprintf(&b, "Corroborating surface observation: %s\n", assessment.CorroboratedAt.Format(time.RFC3339))
			}
			if assessment.SurfaceReports > 0 && assessment.FirstSurfaceAt != nil && assessment.LastSurfaceAt != nil {
				fmt.Fprintf(&b, "Surface evidence: %d distinct reports over %s | Last surface observation: %s\n", assessment.SurfaceReports, assessment.LastSurfaceAt.Sub(*assessment.FirstSurfaceAt).Round(time.Millisecond), assessment.LastSurfaceAt.Format(time.RFC3339))
			}
			if approach := assessment.Approach; approach != nil {
				fmt.Fprintf(&b, "Last approach position: %s (historical evidence, not a current position)\n", approach.LastPositionAt.Format(time.RFC3339))
				fmt.Fprintf(&b, "Approach evidence: %d fixes over %s; descended %.0f ft; last distance %.1f nm, speed %.0f kt\n", approach.ObservationCount, approach.LastPositionAt.Sub(approach.StartedAt), approach.AltitudeDropFeet, approach.LastDistanceNM, approach.LastSpeedKnots)
				if len(approach.CalculatedInputs) > 0 {
					fmt.Fprintf(&b, "Calculated approach inputs: %s\n", strings.Join(approach.CalculatedInputs, ", "))
				}
				if assessment.FirstSurfaceAt == nil {
					fmt.Fprintln(&b, "Surface confirmation: unavailable")
				}
			}
			if assessment.MissingSince != nil {
				fmt.Fprintf(&b, "First successful empty response: %s | Successful empty responses: %d\n", assessment.MissingSince.Format(time.RFC3339), assessment.EmptyResponses)
			}
		case "on_ground_near_destination":
			label := "Arrival assessment"
			if assessment.Historical {
				label = "Last observed arrival state"
			}
			fmt.Fprintf(&b, "%s: %s\n", label, assessment.Reason)
		default:
			label := "Arrival assessment"
			if s.AirportReport != nil {
				label = "ADS-B inference"
			}
			fmt.Fprintf(&b, "%s: %s — %s\n", label, assessment.Status, assessment.Reason)
		}
	}
	if estimate := s.LandingEstimate; estimate != nil {
		if estimate.DistanceNM != nil {
			fmt.Fprintf(&b, "Distance to destination: %.0f nm (straight line)\n", *estimate.DistanceNM)
		}
		if estimate.TouchdownAt != nil && estimate.MinutesRemaining != nil {
			fmt.Fprintf(&b, "Estimated touchdown: ~%s (about %.0f min remaining)\n", estimate.TouchdownAt.UTC().Format("15:04 UTC, 02 Jan"), *estimate.MinutesRemaining)
			fmt.Fprintf(&b, "Estimate: %s | Phase: %s — %s\n", estimate.Quality, estimate.Phase, estimate.Reason)
			if len(estimate.CalculatedInputs) > 0 {
				fmt.Fprintf(&b, "Calculated ETA inputs: %s\n", strings.Join(estimate.CalculatedInputs, ", "))
			}
		} else {
			fmt.Fprintf(&b, "Estimated touchdown: unavailable — %s\n", estimate.Reason)
		}
	}
	if len(s.LastObserved) > 0 {
		fmt.Fprint(&b, "Last observation retained from this session")
		if s.LastSeenAt != nil {
			fmt.Fprintf(&b, " (fetched %s)", s.LastSeenAt.Format(time.RFC3339))
		}
		fmt.Fprintln(&b, "; not a current position:")
		for _, aircraft := range s.LastObserved {
			renderAircraft(&b, aircraft, s.CheckedAt)
		}
	}
	if s.RetryAt != nil {
		fmt.Fprintf(&b, "Next retry: %s\n", s.RetryAt.Format(time.RFC3339))
	}
	if s.Tracking != nil && !s.Tracking.LiveLookupPerformed {
		fmt.Fprintf(&b, "Live provider: %s (selected, not polled)", text(s.Source))
	} else {
		fmt.Fprintf(&b, "Data: %s (live)", text(s.Source))
	}
	if s.Source == "ADSB.fi" {
		fmt.Fprint(&b, " — https://adsb.fi")
	}
	if s.Route != nil {
		if s.RouteSource != "Swedavia" {
			fmt.Fprint(&b, " / adsbdb (reference lookup)")
		}
	}
	fmt.Fprintln(&b)
	_, err := io.WriteString(out, b.String())
	return err
}

func renderAircraft(b *strings.Builder, a flight.Aircraft, now time.Time) {
	fmt.Fprintf(b, "Aircraft: %s | %s | hex %s\n", text(a.Registration), text(a.Type), text(a.Hex))
	fmt.Fprintf(b, "Broadcast callsign: %s\n", text(a.Callsign))
	altitude := number(a.AltitudeFeet, "ft")
	if a.OnGround != nil && *a.OnGround {
		altitude = "on ground (surface report)"
	}
	var c flight.CalculatedKinematics
	if a.Calculated != nil {
		c = *a.Calculated
	}
	fmt.Fprintf(b, "Altitude: %s | Ground speed: %s | Ground track: %s\n", altitude, calculatedNumber(a.GroundSpeed, c.GroundSpeed, "kt"), calculatedNumber(a.Track, c.GroundTrack, "°"))
	fmt.Fprintf(b, "Vertical rate: %s\n", calculatedNumber(a.VerticalRate, c.VerticalRate, "ft/min"))
	for _, item := range []struct {
		name, key string
		reported  *float64
		value     *flight.CalculatedMeasurement
	}{
		{"Ground speed", "ground_speed", a.GroundSpeed, c.GroundSpeed}, {"Ground track", "ground_track", a.Track, c.GroundTrack}, {"Vertical rate", "vertical_rate", a.VerticalRate, c.VerticalRate},
	} {
		if item.reported != nil {
			continue
		}
		if v := item.value; v != nil {
			fmt.Fprintf(b, "%s calculation: %d samples | %s to %s | %s\n", item.name, v.Samples, v.WindowStart.UTC().Format(time.RFC3339), v.WindowEnd.UTC().Format(time.RFC3339), v.Method)
		} else if reason := c.Unavailable[item.key]; reason != "" {
			fmt.Fprintf(b, "%s fallback: %s\n", item.name, reason)
		}
	}
	if a.Latitude != nil && a.Longitude != nil {
		fmt.Fprintf(b, "Position: %.5f, %.5f\nMap: https://www.openstreetmap.org/?mlat=%.5f&mlon=%.5f#map=8/%.5f/%.5f\n", *a.Latitude, *a.Longitude, *a.Latitude, *a.Longitude, *a.Latitude, *a.Longitude)
	} else {
		fmt.Fprintln(b, "Position: unavailable")
	}
	fmt.Fprintf(b, "Last contact: %s\nPosition updated: %s\n", timestamp(a.LastContact, now), timestamp(a.PositionUpdated, now))
}

func calculatedNumber(reported *float64, calculated *flight.CalculatedMeasurement, unit string) string {
	if reported != nil {
		return number(reported, unit)
	}
	if calculated != nil {
		return "~" + number(&calculated.Value, unit) + " (calculated, " + calculated.WindowEnd.Sub(calculated.WindowStart).String() + ")"
	}
	return "unavailable"
}

func timestamp(at *time.Time, now time.Time) string {
	if at == nil {
		return "unavailable"
	}
	if at.After(now) {
		return at.Format(time.RFC3339) + " (ahead of local clock)"
	}
	return fmt.Sprintf("%s (%s ago)", at.Format(time.RFC3339), now.Sub(*at).Round(time.Second))
}

func number(value *float64, unit string) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.0f %s", *value, unit)
}

func text(value string) string {
	if value == "" {
		return "unavailable"
	}
	return value
}

func airport(a flight.Airport) string {
	code := a.IATA
	if code == "" {
		code = a.ICAO
	}
	if a.Name != "" && code != "" {
		return a.Name + " (" + code + ")"
	}
	if code != "" {
		return code
	}
	return text(a.Name)
}
