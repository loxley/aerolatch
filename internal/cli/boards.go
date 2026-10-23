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

	"aerolatch/internal/provider"
	"aerolatch/internal/schedule"
)

func boardCommand(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	date := fs.String("date", "", "Swedish airport local date, YYYY-MM-DD (default today)")
	jsonOutput := fs.Bool("json", false, "print a JSON flight board")
	all := fs.Bool("all", false, "include deleted records (DEL is not cancelled)")
	timeout := fs.Duration("timeout", 15*time.Second, "timeout per API request")
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: aerolatch %s AIRPORT [--date YYYY-MM-DD] [--json] [--all]\nDates use Europe/Stockholm. Requires SWEDAVIA_API_KEY.\n", args[0])
		fs.PrintDefaults()
	}
	flags := append([]string(nil), args[1:]...)
	if len(flags) > 0 && !strings.HasPrefix(flags[0], "-") {
		flags = append(flags[1:], flags[0])
	}
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("supply one Swedavia airport, e.g. 'aerolatch departures ARN --date 2026-10-21'")
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 5m")
	}
	if _, err := provider.SwedaviaDate(*date, time.Now()); err != nil {
		return err
	}
	key := os.Getenv("SWEDAVIA_API_KEY")
	if strings.TrimSpace(key) == "" {
		return errors.New("set SWEDAVIA_API_KEY to use airport flight boards")
	}
	board, err := provider.NewSwedavia(&http.Client{Timeout: *timeout}, key).Board(ctx, fs.Arg(0), args[0], *date)
	if err != nil {
		return err
	}
	if !*all {
		kept := board.Flights[:0]
		for _, f := range board.Flights {
			if f.StatusCode != "DEL" {
				kept = append(kept, f)
			}
		}
		board.Flights = kept
	}
	if *jsonOutput {
		return json.NewEncoder(out).Encode(board)
	}
	fmt.Fprintf(out, "%s %s · %s (%s)\nFlight data from Swedavia — https://www.swedavia.se\nFetched: %s\n", board.Airport, board.Direction, board.Date, board.TimeZone, board.FetchedAt.Format(time.RFC3339))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SCHEDULED LOCAL\tSCHEDULED UTC\tEST/ACT UTC\tFLIGHT\tROUTE\tSTATUS\tGATE\tCALLSIGN\tAIRCRAFT")
	for _, f := range board.Flights {
		at := f.Scheduled(board.Direction)
		times := f.Arrival
		if board.Direction == "departures" {
			times = f.Departure
		}
		updated := "—"
		if times.Estimated != nil {
			updated = "EST " + times.Estimated.UTC().Format("15:04Z")
		}
		if times.Actual != nil {
			updated = "ACT " + times.Actual.UTC().Format("15:04Z")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s → %s\t%s\t%s\t%s\t%s\n", at.In(provider.Stockholm()).Format("15:04 MST"), at.UTC().Format("15:04Z"), updated, f.FlightID, f.Origin.IATA, f.Destination.IATA, schedule.StatusName(f.StatusCode), text(f.Gate), text(f.Callsign), text(f.Registration))
	}
	return w.Flush()
}
