package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"aerolatch/internal/dashboard"
	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
	"aerolatch/internal/nearby"
	"aerolatch/internal/provider"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

func nearbyCommand(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("nearby", flag.ContinueOnError)
	fs.SetOutput(errOut)
	city := fs.String("city", "", "city or postal code to search around (Open-Meteo / GeoNames)")
	country := fs.String("country", "", "two-letter country filter for --city, e.g. SE")
	placeID := fs.Int64("place-id", 0, "select an Open-Meteo place ID from an ambiguous city search")
	lat := fs.Float64("lat", 0, "search-centre latitude; requires --lon")
	lon := fs.Float64("lon", 0, "search-centre longitude; requires --lat")
	radius := fs.Int("radius", 25, "horizontal search radius in nautical miles (1–250)")
	interval := fs.Duration("interval", 10*time.Second, "delay between updates (minimum 5s)")
	timeout := fs.Duration("timeout", 15*time.Second, "timeout per HTTP request")
	providerName := fs.String("provider", "adsb.lol", "live-data provider: adsb.lol or adsb.fi")
	swedaviaMode := fs.String("swedavia", "auto", "airport enrichment for the selected aircraft: auto or off (SWEDAVIA_API_KEY)")
	once := fs.Bool("once", false, "print one result and exit")
	jsonOutput := fs.Bool("json", false, "emit newline-delimited JSON nearby snapshots")
	tui := fs.Bool("tui", false, "interactive nearby list; Enter tracks a plane, Esc returns")
	fs.Usage = func() {
		fmt.Fprint(errOut, "Usage: aerolatch nearby (--city CITY [--country CODE] | --place-id ID | --lat LAT --lon LON) [options]\nDistances are measured from the selected reference point, not a detected device position.\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	coordinates := set["lat"] || set["lon"]
	modes := 0
	for _, yes := range []bool{set["city"], set["place-id"], coordinates} {
		if yes {
			modes++
		}
	}
	if fs.NArg() != 0 || modes != 1 {
		return errors.New("choose exactly one location: --city CITY, --place-id ID, or --lat LAT --lon LON")
	}
	if coordinates && (!set["lat"] || !set["lon"] || !geo.ValidCoordinates(lat, lon)) {
		return errors.New("supply both --lat (-90 to 90) and --lon (-180 to 180) as finite coordinates")
	}
	if set["country"] && !set["city"] {
		return errors.New("--country requires --city")
	}
	if set["place-id"] && *placeID <= 0 {
		return errors.New("--place-id must be positive")
	}
	if *radius < 1 || *radius > 250 {
		return errors.New("--radius must be an integer between 1 and 250 nautical miles")
	}
	if *interval < 5*time.Second || *interval > 24*time.Hour {
		return errors.New("--interval must be between 5s and 24h")
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 5m")
	}
	if *swedaviaMode != "auto" && *swedaviaMode != "off" {
		return errors.New("--swedavia must be auto or off")
	}
	if *tui && (*once || *jsonOutput) {
		return errors.New("--tui cannot be combined with --once or --json")
	}
	terminal, ok := out.(*os.File)
	interactive := ok && term.IsTerminal(terminal.Fd()) && term.IsTerminal(os.Stdin.Fd())
	if *tui && !interactive {
		return errors.New("--tui requires interactive terminal input and output")
	}
	client := &http.Client{Timeout: *timeout}
	live, err := provider.NewLive(*providerName, client)
	if err != nil {
		return err
	}
	location := flight.Location{Name: "Supplied coordinates", Latitude: *lat, Longitude: *lon, Source: "coordinates"}
	places := provider.Places{Client: client}
	if set["place-id"] {
		location, err = places.Lookup(ctx, *placeID)
	} else if set["city"] {
		var matches []flight.Location
		matches, err = places.Search(ctx, *city, *country)
		if err == nil {
			if len(matches) == 1 {
				location = matches[0]
			} else if interactive && !*jsonOutput {
				var selected *flight.Location
				selected, err = dashboard.ChoosePlace(ctx, os.Stdin, out, matches)
				if err == nil && selected == nil {
					return nil
				}
				if selected != nil {
					location = *selected
				}
			} else {
				var b strings.Builder
				fmt.Fprintln(&b, "City search is ambiguous; narrow --country or select --place-id ID:")
				for _, match := range matches {
					fmt.Fprintf(&b, "  --place-id %d  %s (%.5f, %.5f)\n", match.ID, nearbyText(match.Label()), match.Latitude, match.Longitude)
				}
				return errors.New(strings.TrimSpace(b.String()))
			}
		}
	}
	if err != nil {
		return err
	}
	options := nearby.Options{Location: location, RadiusNM: *radius, Interval: *interval, Once: *once}
	// One active poller at a time. Preserve ADSB.fi's request spacing when
	// switching between an area search and an individual aircraft lookup.
	source := &browsingSource{Live: live}
	if *tui {
		inspectionOptions := nearbyInspectionOptions(client, *swedaviaMode)
		return dashboard.Browse(ctx, os.Stdin, out,
			flight.NearbySnapshot{Location: location, RadiusNM: *radius, Source: live.Name()}, *interval,
			func(ctx context.Context, emit func(flight.NearbySnapshot) error) error {
				return nearby.Run(ctx, source, options, emit)
			}, func(ctx context.Context, selection flight.Selection, emit func(flight.Snapshot) error) error {
				resolver := provider.Resolver{Client: client, BaseURL: "https://api.adsbdb.com"}
				return nearby.Inspect(ctx, source, resolver, selection, *interval, emit, inspectionOptions)
			})
	}
	if *jsonOutput {
		encoder := json.NewEncoder(out)
		return nearby.Run(ctx, source, options, func(s flight.NearbySnapshot) error { return encoder.Encode(s) })
	}
	return nearby.Run(ctx, source, options, func(s flight.NearbySnapshot) error { return renderNearby(out, s) })
}

// One credential-scoped client/cache per browser session, shared by all selected
// aircraft. Merely listing nearby aircraft never calls airport flight boards.
func nearbyInspectionOptions(client *http.Client, mode string) nearby.InspectionOptions {
	if mode == "off" {
		return nearby.InspectionOptions{SwedaviaStatus: "off"}
	}
	key := strings.TrimSpace(os.Getenv("SWEDAVIA_API_KEY"))
	if key == "" {
		return nearby.InspectionOptions{SwedaviaStatus: "no_key"}
	}
	return nearby.InspectionOptions{Airports: provider.NewSwedavia(client, key), SwedaviaStatus: "waiting_for_route",
		ResolveAirport: func(ctx context.Context, code string) (*flight.Airport, error) {
			return (provider.Airports{Client: client}).Lookup(ctx, code)
		}}
}

type browsingSource struct {
	provider.Live
	next time.Time
}

func (s *browsingSource) NextAllowedAt() time.Time { return s.next }

func (s *browsingSource) wait(ctx context.Context) error {
	if delay := time.Until(s.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if s.Name() == "ADSB.fi" {
		s.next = time.Now().Add(time.Second)
	}
	return ctx.Err()
}

func (s *browsingSource) LookupNearby(ctx context.Context, lat, lon float64, radius int) ([]flight.Aircraft, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	aircraft, err := s.Live.LookupNearby(ctx, lat, lon, radius)
	s.cooldown(err)
	return aircraft, err
}

func (s *browsingSource) Lookup(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	aircraft, err := s.Live.Lookup(ctx, target)
	s.cooldown(err)
	return aircraft, err
}

func (s *browsingSource) cooldown(err error) {
	if next := time.Now().Add(provider.RetryAfter(err)); next.After(s.next) {
		s.next = next
	}
}

func nearbyText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

func renderNearby(out io.Writer, s flight.NearbySnapshot) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s · Nearby %s · %d nm\n", s.CheckedAt.Format(time.RFC3339), nearbyText(s.Location.Label()), s.RadiusNM)
	fmt.Fprintf(&b, "Search centre: %.5f, %.5f (%s reference point; not device geolocation)\n", s.Location.Latitude, s.Location.Longitude, s.Location.Source)
	if s.Location.Source == "city" {
		fmt.Fprintln(&b, "Location data: Open-Meteo / GeoNames — https://open-meteo.com / https://www.geonames.org")
	}
	fmt.Fprintf(&b, "Data: %s", s.Source)
	if s.Source == "ADSB.fi" {
		fmt.Fprint(&b, " — https://adsb.fi")
	}
	fmt.Fprintf(&b, " | Status: %s | %d excluded fixes\n", s.Status, s.Excluded)
	rows := s.Aircraft
	if s.Error != "" {
		fmt.Fprintf(&b, "Feed error: %s\n", nearbyText(s.Error))
		rows = s.LastObserved
		if len(rows) > 0 {
			fmt.Fprintln(&b, "LAST OBSERVATIONS / NOT CURRENT — distances refer to previous fixes")
		}
	}
	if len(rows) == 0 && s.Error == "" {
		fmt.Fprintln(&b, "No aircraft with fresh valid positions within the selected radius; coverage may be incomplete.")
	}
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CALLSIGN\tHEX\tREGISTRATION\tTYPE\tDISTANCE\tDIRECTION\tALTITUDE\tSPEED\tFIX AGE")
	for _, row := range rows {
		altitude := number(row.AltitudeFeet, "ft")
		if row.OnGround != nil && *row.OnGround {
			altitude = "on ground"
		}
		fixAge := "unavailable"
		if row.PositionUpdated != nil {
			fixAge = s.CheckedAt.Sub(*row.PositionUpdated).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.1f nm\t%s %03.0f°\t%s\t%s\t%s\n", nearbyText(text(row.Callsign)), row.Hex,
			nearbyText(text(row.Registration)), nearbyText(text(row.Type)), row.DistanceNM, row.Direction, row.BearingDegrees, altitude, number(row.GroundSpeed, "kt"), fixAge)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if s.RetryAt != nil {
		fmt.Fprintf(&b, "Retry at: %s\n", s.RetryAt.Format(time.RFC3339))
	}
	_, err := io.WriteString(out, b.String())
	return err
}
