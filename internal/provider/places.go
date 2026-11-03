package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

// Places resolves named locations using Open-Meteo's GeoNames-based geocoder.
type Places struct {
	Client  *http.Client
	BaseURL string
}

type place struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Region      string   `json:"admin1"`
	Country     string   `json:"country"`
	CountryCode string   `json:"country_code"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
}

func (p place) location() (flight.Location, error) {
	if p.ID <= 0 || strings.TrimSpace(p.Name) == "" || !geo.ValidCoordinates(p.Latitude, p.Longitude) {
		return flight.Location{}, fmt.Errorf("Open-Meteo: invalid location in response")
	}
	return flight.Location{ID: p.ID, Name: p.Name, Region: p.Region, Country: p.Country,
		CountryCode: p.CountryCode, Latitude: *p.Latitude, Longitude: *p.Longitude,
		Source: "city", DataSource: "Open-Meteo / GeoNames"}, nil
}

func (p Places) base() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	return "https://geocoding-api.open-meteo.com"
}

func (p Places) Search(ctx context.Context, name, country string) ([]flight.Location, error) {
	name, country = strings.TrimSpace(name), strings.ToUpper(strings.TrimSpace(country))
	if len([]rune(name)) < 2 || len(name) > 200 {
		return nil, fmt.Errorf("city must contain 2–200 characters")
	}
	if country != "" && (len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z') {
		return nil, fmt.Errorf("country must be a two-letter code, e.g. SE")
	}
	query := url.Values{"name": {name}, "count": {"10"}, "language": {"en"}, "format": {"json"}}
	if country != "" {
		query.Set("countryCode", country)
	}
	var data struct {
		Results []place `json:"results"`
		Error   bool    `json:"error"`
	}
	if err := getJSON(ctx, p.Client, p.base()+"/v1/search?"+query.Encode(), "Open-Meteo", &data); err != nil {
		return nil, err
	}
	if data.Error {
		return nil, fmt.Errorf("Open-Meteo: location search failed")
	}
	locations := make([]flight.Location, 0, len(data.Results))
	for _, raw := range data.Results {
		location, err := raw.location()
		if err != nil {
			return nil, err
		}
		if country == "" || strings.EqualFold(location.CountryCode, country) {
			locations = append(locations, location)
		}
	}
	if len(locations) == 0 {
		return nil, fmt.Errorf("Open-Meteo: no locations found for %q; try another city or country", name)
	}
	// The API also returns prefix suggestions (Stockholme, Stockholm airports,
	// etc.). Prefer all exact city-name matches, without choosing between two
	// genuinely distinct cities sharing that name.
	city, _, _ := strings.Cut(name, ",")
	var exact []flight.Location
	for _, location := range locations {
		if strings.EqualFold(location.Name, strings.TrimSpace(city)) {
			exact = append(exact, location)
		}
	}
	if len(exact) > 0 {
		return exact, nil
	}
	return locations, nil
}

func (p Places) Lookup(ctx context.Context, id int64) (flight.Location, error) {
	if id <= 0 {
		return flight.Location{}, fmt.Errorf("place ID must be positive")
	}
	var raw place
	if err := getJSON(ctx, p.Client, p.base()+"/v1/get?id="+strconv.FormatInt(id, 10), "Open-Meteo", &raw); err != nil {
		return flight.Location{}, err
	}
	if raw.ID != id {
		return flight.Location{}, fmt.Errorf("Open-Meteo: requested place ID missing from response")
	}
	return raw.location()
}
