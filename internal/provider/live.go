package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
)

type Live struct {
	Client  *http.Client
	BaseURL string
	Service string
}

// NewLive selects a public aircraft API. Both feeds use the same v2 schema.
func NewLive(name string, client *http.Client) (Live, error) {
	live := Live{Client: client}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "adsb.lol":
		live.BaseURL, live.Service = "https://api.adsb.lol", "ADSB.lol"
	case "adsb.fi":
		live.BaseURL, live.Service = "https://opendata.adsb.fi/api", "ADSB.fi"
	default:
		return Live{}, fmt.Errorf("unknown live-data provider %q; choose adsb.lol or adsb.fi", name)
	}
	return live, nil
}

func (l Live) Name() string {
	if l.Service == "" {
		return "ADSB.lol"
	}
	return l.Service
}

func (l Live) Lookup(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
	target, err := flight.NewTarget(target.Kind, target.Value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", l.Name(), err)
	}
	endpoint := strings.TrimRight(l.BaseURL, "/") + "/v2/" + string(target.Kind) + "/" + url.PathEscape(target.Value)
	return l.fetch(ctx, endpoint, target.Matches, 2<<20)
}

// LookupNearby uses each provider's supported area endpoint. ADSB.fi's v2
// area endpoint is deprecated; v3 uses the same aircraft schema as v2 lookups.
func (l Live) LookupNearby(ctx context.Context, lat, lon float64, radius int) ([]flight.Aircraft, error) {
	if !geo.ValidCoordinates(&lat, &lon) || radius < 1 || radius > 250 {
		return nil, fmt.Errorf("%s: valid coordinates and radius between 1 and 250 nm required", l.Name())
	}
	a, b := strconv.FormatFloat(lat, 'f', -1, 64), strconv.FormatFloat(lon, 'f', -1, 64)
	path := "/v2/point/" + a + "/" + b + "/" + strconv.Itoa(radius)
	if l.Name() == "ADSB.fi" {
		path = "/v3/lat/" + a + "/lon/" + b + "/dist/" + strconv.Itoa(radius)
	}
	return l.fetch(ctx, strings.TrimRight(l.BaseURL, "/")+path, nil, 16<<20)
}

func (l Live) fetch(ctx context.Context, endpoint string, matches func(flight.Aircraft) bool, limit int64) ([]flight.Aircraft, error) {
	var data struct {
		Aircraft *[]struct {
			Hex          string          `json:"hex"`
			Callsign     string          `json:"flight"`
			Registration string          `json:"r"`
			Type         string          `json:"t"`
			Altitude     json.RawMessage `json:"alt_baro"`
			Latitude     *float64        `json:"lat"`
			Longitude    *float64        `json:"lon"`
			Speed        *float64        `json:"gs"`
			Track        *float64        `json:"track"`
			VerticalRate *float64        `json:"baro_rate"`
			Seen         *float64        `json:"seen"`
			SeenPosition *float64        `json:"seen_pos"`
		} `json:"ac"`
		Now     float64 `json:"now"`
		Message string  `json:"msg"`
	}
	if err := getJSONLimit(ctx, l.Client, endpoint, l.Name(), &data, limit); err != nil {
		return nil, err
	}
	if data.Message != "" && data.Message != "No error" {
		return nil, fmt.Errorf("%s: %s", l.Name(), data.Message)
	}
	if data.Aircraft == nil || data.Now <= 0 {
		return nil, fmt.Errorf("%s: response is missing aircraft data or feed timestamp", l.Name())
	}
	feedTime := time.UnixMilli(int64(data.Now)).UTC()
	result := make([]flight.Aircraft, 0, len(*data.Aircraft))
	for _, raw := range *data.Aircraft {
		// Hex queries deliberately accept missing or changed callsigns, but never
		// an aircraft with a different address. Each lookup has an exact key.
		if matches != nil && !matches(flight.Aircraft{Hex: raw.Hex, Callsign: raw.Callsign, Registration: raw.Registration}) {
			continue
		}
		aircraft := flight.Aircraft{
			Hex: strings.ToLower(strings.TrimSpace(raw.Hex)), Callsign: strings.ToUpper(strings.TrimSpace(raw.Callsign)),
			Registration: strings.ToUpper(strings.TrimSpace(raw.Registration)), Type: raw.Type,
			Latitude: raw.Latitude, Longitude: raw.Longitude,
			GroundSpeed: raw.Speed, Track: raw.Track, VerticalRate: raw.VerticalRate,
			LastContact: observedAt(feedTime, raw.Seen),
		}
		if raw.Latitude != nil && raw.Longitude != nil {
			aircraft.PositionUpdated = observedAt(feedTime, raw.SeenPosition)
		}
		if len(raw.Altitude) > 0 && string(raw.Altitude) != "null" {
			if string(raw.Altitude) == `"ground"` {
				ground := true
				aircraft.OnGround = &ground
			} else {
				var altitude float64
				if err := json.Unmarshal(raw.Altitude, &altitude); err != nil {
					return nil, fmt.Errorf("%s: unexpected barometric altitude for %s", l.Name(), raw.Hex)
				}
				ground := false
				aircraft.OnGround = &ground
				aircraft.AltitudeFeet = &altitude
			}
		}
		result = append(result, aircraft)
	}
	return result, nil
}

func observedAt(feedTime time.Time, seconds *float64) *time.Time {
	if seconds == nil || *seconds < 0 || math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds > float64(365*24*60*60) {
		return nil
	}
	at := feedTime.Add(-time.Duration(*seconds * float64(time.Second)))
	return &at
}
