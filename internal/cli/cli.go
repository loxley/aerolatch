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
	"regexp"
	"strings"
	"time"

	"aerolatch/internal/dashboard"
	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
	"aerolatch/internal/schedule"
	"aerolatch/internal/tracker"

	"github.com/charmbracelet/x/term"
)

const usage = `Usage:
  aerolatch track FLIGHT [options]
  aerolatch track --callsign CALLSIGN [options]
  aerolatch track --hex ICAO24 [options]
  aerolatch track --registration REGISTRATION [options]
  aerolatch departures AIRPORT [--date YYYY-MM-DD] [--json]
  aerolatch arrivals AIRPORT [--date YYYY-MM-DD] [--json]

Examples:
  aerolatch track D85373
  aerolatch track D85373 --once
  aerolatch track D85373 --interval 60s
  aerolatch track D85373 --once --json
  aerolatch track --callsign NSZ5373
  aerolatch track RYR9UJ --destination ALC
  aerolatch track RYR9UJ --destination ALC --provider adsb.fi
  aerolatch track NSZ4366 --provider adsb.fi --tui
  aerolatch track --hex 4aca87 --destination ARN --provider adsb.fi --tui
  aerolatch track --registration SE-RTG --provider adsb.fi --once
  aerolatch departures ARN --date 2026-10-21
  aerolatch track D84487 --origin ARN --date 2026-10-21 --tui

Free live data: adsbdb, ADSB.lol or ADSB.fi. Optional airport data: SWEDAVIA_API_KEY.
Route mappings are reference data; codeshares and operational callsigns may differ.
No live match does not confirm landing or cancellation.
`

var identifier = regexp.MustCompile(`^[A-Z0-9]{2,8}$`)
var airportCode = regexp.MustCompile(`^[A-Z]{3,4}$`)

func Run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	if args[0] != "track" {
		if args[0] == "departures" || args[0] == "arrivals" {
			return boardCommand(ctx, args, out, errOut)
		}
		return fmt.Errorf("unknown command %q; use 'aerolatch --help'", args[0])
	}
	fs := flag.NewFlagSet("track", flag.ContinueOnError)
	fs.SetOutput(errOut)
	interval := fs.Duration("interval", 30*time.Second, "delay between updates (minimum 5s)")
	timeout := fs.Duration("timeout", 15*time.Second, "timeout for each HTTP request")
	once := fs.Bool("once", false, "print one result and exit")
	jsonOutput := fs.Bool("json", false, "emit newline-delimited JSON snapshots")
	tui := fs.Bool("tui", false, "show the interactive radar-style terminal dashboard")
	callsignFlag := fs.String("callsign", "", "track an operational callsign directly, skipping flight lookup")
	hexFlag := fs.String("hex", "", "track a six-digit ICAO hex address directly, skipping flight lookup")
	registrationFlag := fs.String("registration", "", "track an aircraft registration directly, skipping flight lookup")
	destinationFlag := fs.String("destination", "", "override destination with an IATA/ICAO airport code (OurAirports lookup)")
	providerFlag := fs.String("provider", "adsb.lol", "live-data provider: adsb.lol or adsb.fi")
	originFlag := fs.String("origin", "", "explicit origin IATA/ICAO code; discovers a Swedavia departure board")
	dateFlag := fs.String("date", "", "dated Swedavia flight selection, YYYY-MM-DD in Swedish airport local time")
	swedaviaFlag := fs.String("swedavia", "auto", "airport enrichment: auto or off (requires SWEDAVIA_API_KEY)")
	fs.Usage = func() {
		fmt.Fprint(errOut, usage+"\nOptions:\n")
		fs.PrintDefaults()
	}
	flags := append([]string(nil), args[1:]...)
	// Standard flag parsing stops at a positional argument; support the natural
	// 'track D85373 --once' form as well as flags before the flight number.
	if len(flags) > 0 && !strings.HasPrefix(flags[0], "-") {
		flags = append(flags[1:], flags[0])
	}
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *tui && (*jsonOutput || *once) {
		return errors.New("--tui requires continuous interactive tracking; it cannot be combined with --json or --once")
	}
	if *interval < 5*time.Second || *interval > 24*time.Hour {
		return errors.New("--interval must be between 5s and 24h")
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 5m")
	}
	modes := 0
	var kind flight.TargetKind
	var value string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "callsign":
			modes++
			kind, value = flight.ByCallsign, *callsignFlag
		case "hex":
			modes++
			kind, value = flight.ByHex, *hexFlag
		case "registration":
			modes++
			kind, value = flight.ByRegistration, *registrationFlag
		}
	})
	if modes > 1 {
		return errors.New("supply only one of --callsign, --hex, or --registration")
	}
	resolveFlight := modes == 0
	var query, callsign string
	var target flight.Target
	if !resolveFlight {
		if fs.NArg() != 0 {
			return errors.New("supply either a flight number or one direct lookup flag (--callsign, --hex, --registration), not both")
		}
		var err error
		target, err = flight.NewTarget(kind, value)
		if err != nil {
			return fmt.Errorf("--%s: %w", kind, err)
		}
		query = target.Value
		if kind == flight.ByCallsign {
			callsign = target.Value
		}
	} else {
		if fs.NArg() != 1 {
			return errors.New("supply one flight number, e.g. 'aerolatch track D85373'; use --help for options")
		}
		query = normalize(fs.Arg(0))
	}
	if resolveFlight && !identifier.MatchString(query) {
		return errors.New("flight number or callsign must contain 2–8 letters and digits (spaces are ignored)")
	}
	if *swedaviaFlag != "auto" && *swedaviaFlag != "off" {
		return errors.New("--swedavia must be auto or off")
	}
	if *dateFlag != "" {
		if _, err := provider.SwedaviaDate(*dateFlag, time.Now()); err != nil {
			return err
		}
		if *swedaviaFlag == "off" || strings.TrimSpace(os.Getenv("SWEDAVIA_API_KEY")) == "" {
			return errors.New("dated tracking requires SWEDAVIA_API_KEY and --swedavia auto")
		}
		if kind == flight.ByHex {
			return errors.New("for --date, select a flight number, callsign or registration; --hex follows a physical aircraft without dated flight selection")
		}
	}
	originCode := normalize(*originFlag)
	if *originFlag != "" && !airportCode.MatchString(originCode) {
		return errors.New("--origin must be an IATA or ICAO airport code")
	}
	destinationCode := normalize(*destinationFlag)
	if *destinationFlag != "" && !airportCode.MatchString(destinationCode) {
		return errors.New("--destination must be a 3-letter IATA or 4-letter ICAO airport code")
	}
	client := &http.Client{Timeout: *timeout}
	live, err := provider.NewLive(*providerFlag, client)
	if err != nil {
		return err
	}
	if *tui {
		terminal, ok := out.(interface{ Fd() uintptr })
		if !ok || !term.IsTerminal(terminal.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
			return errors.New("--tui requires interactive terminal input and output; use plain output or --json for pipes")
		}
	}
	var destination *flight.Airport
	if destinationCode != "" {
		var err error
		destination, err = (provider.Airports{Client: client}).Lookup(ctx, destinationCode)
		if err != nil {
			return fmt.Errorf("resolve destination %s: %w", destinationCode, err)
		}
	}
	var route *flight.Route
	routeSource := "adsbdb"
	var watcher *schedule.Watcher
	if key := strings.TrimSpace(os.Getenv("SWEDAVIA_API_KEY")); key != "" && *swedaviaFlag == "auto" {
		watcher = &schedule.Watcher{Client: provider.NewSwedavia(client, key), Query: query, Target: target, Date: *dateFlag, ExplicitDate: *dateFlag != "", OriginHint: originCode, DestinationHint: destinationCode, DestinationOverride: destination,
			ResolveAirport: func(ctx context.Context, code string) (*flight.Airport, error) {
				return (provider.Airports{Client: client}).Lookup(ctx, code)
			}}
	}
	var initialSchedule flight.ScheduleUpdate
	if watcher != nil && watcher.HasContext(time.Now()) {
		initialSchedule = watcher.Update(ctx, time.Now(), nil)
	}
	if initialSchedule.Report != nil && initialSchedule.Report.Match == "matched" && initialSchedule.Route != nil {
		route, routeSource = initialSchedule.Route, "Swedavia"
		if resolveFlight && initialSchedule.Target != nil {
			target = *initialSchedule.Target
			callsign = target.Value
		}
	}
	if resolveFlight && route == nil {
		var err error
		route, err = (provider.Resolver{Client: client, BaseURL: "https://api.adsbdb.com"}).Resolve(ctx, query)
		if err != nil {
			if watcher == nil || !watcher.HasContext(time.Now()) || *dateFlag == "" {
				return fmt.Errorf("resolve %s: %w", query, err)
			}
		} else {
			callsign = route.Callsign
			target, err = flight.NewTarget(flight.ByCallsign, callsign)
			if err != nil {
				return fmt.Errorf("resolve %s: invalid operational callsign: %w", query, err)
			}
		}
	}
	if watcher != nil {
		watcher.Reference = route
		if !watcher.HasContext(time.Now()) {
			if *dateFlag != "" {
				return errors.New("dated tracking needs a Swedavia origin/destination; supply --origin ARN or --destination ARN")
			}
			watcher = nil
		}
	}
	options := tracker.Options{Query: query, Callsign: callsign, Target: target, Route: route, RouteSource: routeSource, Destination: destination, Interval: *interval, Once: *once, Dated: *dateFlag != ""}
	if watcher != nil {
		options.Schedule = watcher
	}
	if *tui {
		initial := flight.Snapshot{Query: query, Callsign: callsign, Route: route, Source: live.Name()}
		initial.RouteSource, initial.AirportReport = routeSource, initialSchedule.Report
		initial.Tracking = &flight.TrackingInfo{Lookup: target, FlightState: "tracking"}
		if target.Kind == flight.ByHex {
			initial.Tracking.LockedHex = target.Value
		}
		if destination != nil {
			initial.Destination = &flight.Destination{Airport: *destination, Source: "user_override", DataSource: "OurAirports"}
		} else if route != nil {
			initial.Destination = &flight.Destination{Airport: route.Destination, Source: "adsbdb", DataSource: "adsbdb"}
		}
		return dashboard.Run(ctx, os.Stdin, out, initial, *interval, func(ctx context.Context, emit func(flight.Snapshot) error) error {
			return tracker.Run(ctx, live, options, emit)
		})
	}
	var emit func(flight.Snapshot) error
	if *jsonOutput {
		encoder := json.NewEncoder(out)
		emit = func(s flight.Snapshot) error { return encoder.Encode(s) }
	} else {
		emit = func(s flight.Snapshot) error { return render(out, s) }
	}
	return tracker.Run(ctx, live, options, emit)
}

func normalize(value string) string {
	return strings.ToUpper(strings.Join(strings.Fields(value), ""))
}
