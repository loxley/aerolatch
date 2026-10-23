package provider

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

const AirportsURL = "https://raw.githubusercontent.com/davidmegginson/ourairports-data/main/airports.csv"

type Airports struct {
	Client *http.Client
	URL    string
}

// Lookup streams the public-domain OurAirports CSV once at startup when an
// override is requested. Nothing is written to disk; duplicate codes are rejected.
func (a Airports) Lookup(ctx context.Context, code string) (*flight.Airport, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 3 && len(code) != 4 {
		return nil, errors.New("OurAirports: airport code must be 3 or 4 letters")
	}
	for _, char := range code {
		if char < 'A' || char > 'Z' {
			return nil, errors.New("OurAirports: airport code must contain only letters")
		}
	}
	endpoint := a.URL
	if endpoint == "" {
		endpoint = AirportsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("User-Agent", "aerolatch-cli/0.1")
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OurAirports: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Service: "OurAirports", StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	limited := &io.LimitedReader{R: resp.Body, N: (64 << 20) + 1}
	reader := csv.NewReader(limited)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("OurAirports: read CSV header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, column := range header {
		columns[strings.TrimPrefix(column, "\ufeff")] = i
	}
	for _, name := range []string{"type", "name", "latitude_deg", "longitude_deg", "elevation_ft", "iata_code", "gps_code"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("OurAirports: missing CSV column %s", name)
		}
	}
	var match *flight.Airport
	for {
		row, err := reader.Read()
		if limited.N <= 0 {
			return nil, errors.New("OurAirports: airport dataset exceeds 64 MiB")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("OurAirports: invalid CSV: %w", err)
		}
		field := func(name string) string {
			if index, ok := columns[name]; ok {
				return strings.TrimSpace(row[index])
			}
			return ""
		}
		if field("type") == "closed" {
			continue
		}
		iata, icao := field("iata_code"), field("icao_code")
		if icao == "" {
			// OurAirports normally stores ICAO codes in gps_code. Do not use
			// ident as a fallback: it can be an unrelated internal identifier.
			icao = field("gps_code")
		}
		if (len(code) == 3 && iata != code) || (len(code) == 4 && icao != code) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("OurAirports: airport code %s is ambiguous", code)
		}
		lat, latErr := strconv.ParseFloat(field("latitude_deg"), 64)
		lon, lonErr := strconv.ParseFloat(field("longitude_deg"), 64)
		if latErr != nil || lonErr != nil || !geo.ValidCoordinates(&lat, &lon) {
			return nil, fmt.Errorf("OurAirports: invalid coordinates for %s", code)
		}
		match = &flight.Airport{IATA: iata, ICAO: icao, Name: field("name"), Latitude: &lat, Longitude: &lon}
		if value := field("elevation_ft"); value != "" {
			elevation, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(elevation) || math.IsInf(elevation, 0) {
				return nil, fmt.Errorf("OurAirports: invalid elevation for %s", code)
			}
			match.ElevationFeet = &elevation
		}
	}
	if match == nil {
		return nil, fmt.Errorf("OurAirports: airport %s not found", code)
	}
	return match, nil
}
