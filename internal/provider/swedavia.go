package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Local flight dates must work on hosts without zoneinfo files.

	"aerolatch/internal/flight"
)

const SwedaviaURL = "https://api.swedavia.se/flightinfo/v2"

var swedaviaAirports = map[string]string{
	"ARN": "ESSA", "BMA": "ESSB", "GOT": "ESGG", "MMX": "ESMS", "LLA": "ESPA",
	"UME": "ESNU", "OSD": "ESNZ", "VBY": "ESSV", "RNB": "ESDF", "KRN": "ESNQ",
}

func SwedaviaAirport(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if _, ok := swedaviaAirports[code]; ok {
		return code
	}
	for iata, icao := range swedaviaAirports {
		if code == icao {
			return iata
		}
	}
	return ""
}

var Stockholm = sync.OnceValue(func() *time.Location {
	location, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		panic(err)
	} // Embedded tzdata guarantees this zone exists.
	return location
})

func SwedaviaDate(value string, now time.Time) (time.Time, error) {
	if value == "" {
		value = now.In(Stockholm()).Format("2006-01-02")
	}
	date, err := time.ParseInLocation("2006-01-02", value, Stockholm())
	if err != nil {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD (Swedish airport local date)")
	}
	return date, nil
}

type swedaviaEntry struct {
	flights []flight.ScheduledFlight
	fetched time.Time
	expires time.Time
	err     error
}

// Swedavia caches airport/UTC-day boards and failed requests in memory. The key
// is sent only as a header; redirects and response-body diagnostics are disabled.
type Swedavia struct {
	Client   *http.Client
	BaseURL  string
	Now      func() time.Time
	key      string
	mu       sync.Mutex
	cache    map[string]swedaviaEntry
	disabled error
	cooldown time.Time
}

func NewSwedavia(client *http.Client, key string) *Swedavia {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Swedavia{Client: &clone, BaseURL: SwedaviaURL, Now: time.Now, key: strings.TrimSpace(key), cache: make(map[string]swedaviaEntry)}
}

// Board interprets date as a local Swedish calendar day. Both overlapping UTC
// boards are fetched and filtered by scheduled time, including DST transitions.
func (s *Swedavia) Board(ctx context.Context, airport, direction, date string) (flight.FlightBoard, error) {
	board := flight.FlightBoard{Source: "Swedavia", Direction: direction, TimeZone: "Europe/Stockholm", Flights: []flight.ScheduledFlight{}}
	airport = SwedaviaAirport(airport)
	if airport == "" {
		return board, errors.New("Swedavia: airport must be one of ARN, BMA, GOT, MMX, LLA, UME, OSD, VBY, RNB, KRN (or its ICAO code)")
	}
	if direction != "arrivals" && direction != "departures" {
		return board, errors.New("Swedavia: direction must be arrivals or departures")
	}
	start, err := SwedaviaDate(date, s.Now())
	if err != nil {
		return board, err
	}
	board.Airport, board.Date = airport, start.Format("2006-01-02")
	end := start.AddDate(0, 0, 1)
	utc := start.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for day.Before(end) {
		entry, err := s.day(ctx, airport, direction, day.Format("2006-01-02"))
		if err != nil {
			return board, err
		}
		if board.FetchedAt.IsZero() || entry.fetched.Before(board.FetchedAt) {
			board.FetchedAt = entry.fetched
		}
		for _, f := range entry.flights {
			at := f.Scheduled(direction)
			if at == nil || at.Before(start) || !at.Before(end) {
				continue
			}
			key := f.LegKey() + "/" + at.UTC().Format(time.RFC3339) + "/" + f.StatusCode
			if !seen[key] {
				board.Flights = append(board.Flights, f)
				seen[key] = true
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	sort.SliceStable(board.Flights, func(i, j int) bool {
		return board.Flights[i].Scheduled(direction).Before(*board.Flights[j].Scheduled(direction))
	})
	return board, nil
}

func (s *Swedavia) day(ctx context.Context, airport, direction, date string) (swedaviaEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().UTC()
	if s.key == "" {
		return swedaviaEntry{}, errors.New("Swedavia: set SWEDAVIA_API_KEY to your subscription key")
	}
	if s.disabled != nil {
		return swedaviaEntry{}, s.disabled
	}
	key := airport + "/" + direction + "/" + date
	if entry, ok := s.cache[key]; ok && now.Before(entry.expires) {
		return entry, entry.err
	}
	if now.Before(s.cooldown) {
		return swedaviaEntry{}, errors.New("Swedavia: rate-limit cooldown; retry later")
	}
	entry := swedaviaEntry{fetched: now, expires: now.Add(5 * time.Minute)}
	if date != now.Format("2006-01-02") && date != now.Add(-24*time.Hour).Format("2006-01-02") {
		entry.expires = now.Add(30 * time.Minute)
	}
	flights, err := s.fetch(ctx, airport, direction, date, now)
	entry.flights, entry.err = flights, err
	if err != nil {
		var failure *HTTPError
		if errors.As(err, &failure) {
			if failure.StatusCode == 401 || failure.StatusCode == 403 {
				s.disabled = err
			}
			if failure.StatusCode == 429 {
				s.cooldown = now.Add(max(failure.RetryAfter, 5*time.Minute))
				entry.expires = s.cooldown
			}
		}
	}
	// Bound memory when a process inspects many dates. This never stores keys on disk.
	if len(s.cache) >= 32 {
		for k, v := range s.cache {
			if !now.Before(v.expires) {
				delete(s.cache, k)
			}
		}
		if len(s.cache) >= 32 {
			for k := range s.cache {
				delete(s.cache, k)
				break
			}
		}
	}
	s.cache[key] = entry
	return entry, err
}

type swedaviaRaw struct {
	FlightID        string                                                                   `json:"flightId"`
	Airline         struct{ Name string }                                                    `json:"airlineOperator"`
	OriginName      string                                                                   `json:"departureAirportEnglish"`
	DestinationName string                                                                   `json:"arrivalAirportEnglish"`
	DepartureTime   swedaviaTimes                                                            `json:"departureTime"`
	ArrivalTime     swedaviaTimes                                                            `json:"arrivalTime"`
	Status          struct{ Terminal, Gate, FlightLegStatus, FlightLegStatusEnglish string } `json:"locationAndStatus"`
	Codeshares      []string                                                                 `json:"codeShareData"`
	Leg             struct {
		Callsign, AircraftRegistration, FlightID                                           string
		FlightDepartureDateUTC                                                             *time.Time
		DepartureAirportIata, ArrivalAirportIata, DepartureAirportIcao, ArrivalAirportIcao string
	} `json:"flightLegIdentifier"`
}

type swedaviaTimes struct{ ScheduledUTC, EstimatedUTC, ActualUTC *time.Time }

func (s *Swedavia) fetch(ctx context.Context, airport, direction, date string, now time.Time) ([]flight.ScheduledFlight, error) {
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/" + airport + "/" + direction + "/" + date
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("Swedavia: invalid request URL")
	}
	req.Header.Set("Ocp-Apim-Subscription-Key", s.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "aerolatch-cli/0.1")
	response, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Swedavia: request failed: %s", strings.ReplaceAll(err.Error(), s.key, "[redacted]"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &HTTPError{Service: "Swedavia", StatusCode: response.StatusCode, RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), now)}
	}
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errors.New("Swedavia: could not read flight board")
	}
	if len(body) > limit {
		return nil, errors.New("Swedavia: response exceeds 8 MiB")
	}
	var data struct {
		Flights *[]swedaviaRaw `json:"flights"`
	}
	if err := json.Unmarshal(body, &data); err != nil || data.Flights == nil {
		return nil, errors.New("Swedavia: invalid flight-board response")
	}
	var updated *time.Time
	if at, err := http.ParseTime(response.Header.Get("Last-Modified")); err == nil {
		updated = &at
	} else if at, err := time.Parse(time.RFC3339, response.Header.Get("Last-Modified")); err == nil {
		updated = &at
	}
	result := make([]flight.ScheduledFlight, 0, len(*data.Flights))
	for _, raw := range *data.Flights {
		id := raw.Leg.FlightID
		if id == "" {
			id = raw.FlightID
		}
		f := flight.ScheduledFlight{FlightID: strings.ToUpper(strings.TrimSpace(id)), Callsign: strings.ToUpper(strings.TrimSpace(raw.Leg.Callsign)), Registration: flight.RegistrationID(raw.Leg.AircraftRegistration), Airline: raw.Airline.Name,
			Origin:        flight.Airport{IATA: raw.Leg.DepartureAirportIata, ICAO: raw.Leg.DepartureAirportIcao, Name: raw.OriginName},
			Destination:   flight.Airport{IATA: raw.Leg.ArrivalAirportIata, ICAO: raw.Leg.ArrivalAirportIcao, Name: raw.DestinationName},
			Departure:     flight.FlightTimes{Scheduled: raw.DepartureTime.ScheduledUTC, Estimated: raw.DepartureTime.EstimatedUTC, Actual: raw.DepartureTime.ActualUTC},
			Arrival:       flight.FlightTimes{Scheduled: raw.ArrivalTime.ScheduledUTC, Estimated: raw.ArrivalTime.EstimatedUTC, Actual: raw.ArrivalTime.ActualUTC},
			DepartureDate: raw.Leg.FlightDepartureDateUTC, Codeshares: raw.Codeshares, StatusCode: raw.Status.FlightLegStatus, StatusText: raw.Status.FlightLegStatusEnglish,
			Terminal: raw.Status.Terminal, Gate: raw.Status.Gate, FetchedAt: now, DataUpdatedAt: updated}
		if direction == "departures" {
			f.Origin.IATA = airport
			f.Origin.ICAO = swedaviaAirports[airport]
		} else {
			f.Destination.IATA = airport
			f.Destination.ICAO = swedaviaAirports[airport]
		}
		if raw.FlightID != "" && !strings.EqualFold(raw.FlightID, f.FlightID) {
			f.Codeshares = append(f.Codeshares, raw.FlightID)
		}
		if f.FlightID != "" {
			result = append(result, f)
		}
	}
	return result, nil
}
