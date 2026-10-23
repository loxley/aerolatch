package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"aerolatch/internal/flight"
)

type Resolver struct {
	Client  *http.Client
	BaseURL string
}

func (r Resolver) Resolve(ctx context.Context, number string) (*flight.Route, error) {
	var data struct {
		Response struct {
			Route *struct {
				CallsignICAO string `json:"callsign_icao"`
				CallsignIATA string `json:"callsign_iata"`
				Airline      *struct {
					Name string `json:"name"`
				} `json:"airline"`
				Origin      flight.Airport `json:"origin"`
				Destination flight.Airport `json:"destination"`
			} `json:"flightroute"`
		} `json:"response"`
	}
	endpoint := strings.TrimRight(r.BaseURL, "/") + "/v0/callsign/" + url.PathEscape(number)
	if err := getJSON(ctx, r.Client, endpoint, "adsbdb", &data); err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("flight %s is unknown to adsbdb; if you know its operational callsign, use --callsign CALLSIGN", number)
		}
		return nil, err
	}
	route := data.Response.Route
	if route == nil || strings.TrimSpace(route.CallsignICAO) == "" {
		return nil, fmt.Errorf("adsbdb did not provide an operational callsign for %s; use --callsign CALLSIGN if known", number)
	}
	result := &flight.Route{
		FlightNumber: route.CallsignIATA,
		Callsign:     strings.ToUpper(strings.TrimSpace(route.CallsignICAO)),
		Origin:       route.Origin,
		Destination:  route.Destination,
	}
	if route.Airline != nil {
		result.Airline = route.Airline.Name
	}
	return result, nil
}
