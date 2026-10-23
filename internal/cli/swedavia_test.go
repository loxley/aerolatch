package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
)

func mockResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}
}

func TestFlightBoardFiltersDeletedAndPreservesCancellation(t *testing.T) {
	t.Setenv("SWEDAVIA_API_KEY", "unit-test-secret")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	date := time.Now().In(provider.Stockholm()).AddDate(0, 0, 1).Format("2006-01-02")
	day, _ := provider.SwedaviaDate(date, time.Now())
	at := day.Add(12 * time.Hour).UTC()
	var rows []string
	for i, status := range []string{"SCH", "CAN", "DEL"} {
		rows = append(rows, fmt.Sprintf(`{"flightId":"D8%d","departureTime":{"scheduledUtc":%q},"locationAndStatus":{"flightLegStatus":%q},"flightLegIdentifier":{"flightId":"D8%d","callsign":"NSZ87C","departureAirportIata":"ARN","arrivalAirportIata":"BUD"}}`, i, at.Format(time.RFC3339), status, i))
	}
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "unit-test-secret" {
			t.Error("missing credential header")
		}
		return mockResponse(r, 200, `{"flights":[`+strings.Join(rows, ",")+`]}`), nil
	})
	for _, all := range []bool{false, true} {
		args := []string{"departures", "ARN", "--date", date, "--json"}
		if all {
			args = append(args, "--all")
		}
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var board flight.FlightBoard
		if err := json.Unmarshal(out.Bytes(), &board); err != nil {
			t.Fatal(err)
		}
		want := 2
		if all {
			want = 3
		}
		if len(board.Flights) != want || board.TimeZone != "Europe/Stockholm" {
			t.Fatalf("board=%s", &out)
		}
		if strings.Contains(out.String(), "unit-test-secret") {
			t.Fatal("key appeared in board output")
		}
	}
}

func TestDatedFutureTrackUsesSwedaviaAndWaitsWithoutLiveLookup(t *testing.T) {
	t.Setenv("SWEDAVIA_API_KEY", "unit-test-key")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	date := time.Now().In(provider.Stockholm()).AddDate(0, 0, 1).Format("2006-01-02")
	day, _ := provider.SwedaviaDate(date, time.Now())
	at := day.Add(12 * time.Hour).UTC()
	apiCalls := 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.swedavia.se":
			apiCalls++
			body := fmt.Sprintf(`{"flights":[{"flightId":"D84487","departureTime":{"scheduledUtc":%q},"locationAndStatus":{"flightLegStatus":"SCH"},"flightLegIdentifier":{"flightId":"D84487","callsign":"NSZ87C","departureAirportIata":"ARN","arrivalAirportIata":"BUD","flightDepartureDateUtc":%q}}]}`, at.Format(time.RFC3339), day.UTC().Format(time.RFC3339))
			return mockResponse(r, 200, body), nil
		case "raw.githubusercontent.com":
			return mockResponse(r, 200, "type,name,latitude_deg,longitude_deg,elevation_ft,gps_code,iata_code\nlarge_airport,Budapest,47.4,19.2,495,LHBP,BUD\n"), nil
		default:
			t.Errorf("future dated service performed an unexpected lookup: %s", r.URL)
			return mockResponse(r, 500, ""), nil
		}
	})
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"track", "D84487", "--origin", "ARN", "--date", date, "--once", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var s flight.Snapshot
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Status != "scheduled" || len(s.Aircraft) != 0 || s.AirportReport == nil || s.AirportReport.Flight.Callsign != "NSZ87C" || s.Destination.Airport.IATA != "BUD" || s.Destination.Source != "swedavia" || apiCalls != 2 {
		t.Fatalf("wrong future flight: calls=%d output=%s", apiCalls, &out)
	}
}

func TestAutomaticAirportAuthFailureFallsBackToLiveFeed(t *testing.T) {
	t.Setenv("SWEDAVIA_API_KEY", "unit-test-key")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	apiCalls, liveCalls := 0, 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.swedavia.se":
			apiCalls++
			return mockResponse(r, 401, `{"message":"unit-test-key"}`), nil
		case "raw.githubusercontent.com":
			return mockResponse(r, 200, "type,name,latitude_deg,longitude_deg,elevation_ft,gps_code,iata_code\nlarge_airport,Arlanda,59.65,17.91,137,ESSA,ARN\n"), nil
		case "api.adsb.lol":
			liveCalls++
			return mockResponse(r, 200, fmt.Sprintf(`{"ac":[],"now":%d}`, time.Now().UnixMilli())), nil
		default:
			t.Errorf("unexpected URL: %s", r.URL)
			return mockResponse(r, 500, ""), nil
		}
	})
	var out bytes.Buffer
	err := Run(context.Background(), []string{"track", "--callsign", "NSZ4366", "--destination", "ARN", "--once", "--json"}, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var s flight.Snapshot
	json.Unmarshal(out.Bytes(), &s)
	if apiCalls != 1 || liveCalls != 1 || s.Status != "not_visible" || s.AirportReport == nil || s.AirportReport.Match != "unavailable" || strings.Contains(out.String(), "unit-test-key") {
		t.Fatalf("fallback or redaction failed: %s", &out)
	}
}

func TestScheduleFlagsAndOptOut(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.adsb.lol" {
			t.Errorf("opt-out called an airport API: %s", r.URL)
		}
		return mockResponse(r, 200, fmt.Sprintf(`{"ac":[],"now":%d}`, time.Now().UnixMilli())), nil
	})
	for _, args := range [][]string{{"departures", "ARN"}, {"track", "D84487", "--date", "2026-12-01"}, {"track", "D84487", "--swedavia", "other"}} {
		if err := Run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Errorf("accepted invalid airport configuration: %v", args)
		}
	}
	t.Setenv("SWEDAVIA_API_KEY", "unit-test-key")
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"track", "--callsign", "NSZ4366", "--swedavia", "off", "--once", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var s flight.Snapshot
	json.Unmarshal(out.Bytes(), &s)
	if s.AirportReport != nil {
		t.Fatal("opt-out still enabled airport enrichment")
	}
}

func TestReferenceRouteAutomaticallyDiscoversSwedaviaAndCorrectsCallsign(t *testing.T) {
	t.Setenv("SWEDAVIA_API_KEY", "unit-test-key")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	date := time.Now().In(provider.Stockholm()).Format("2006-01-02")
	day, _ := provider.SwedaviaDate(date, time.Now())
	actual := day.Add(6 * time.Hour).UTC()
	apiCalls := 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.adsbdb.com":
			return mockResponse(r, 200, `{"response":{"flightroute":{"callsign_icao":"WRONG123","destination":{"iata_code":"ARN","latitude":59.65,"longitude":17.91,"elevation":137}}}}`), nil
		case "api.swedavia.se":
			apiCalls++
			return mockResponse(r, 200, fmt.Sprintf(`{"flights":[{"flightId":"D82602","arrivalTime":{"scheduledUtc":%q,"actualUtc":%q},"locationAndStatus":{"flightLegStatus":"LAN"},"flightLegIdentifier":{"flightId":"D82602","callsign":"NSZ2602","departureAirportIata":"HEL","arrivalAirportIata":"ARN"}}]}`, actual.Format(time.RFC3339), actual.Format(time.RFC3339))), nil
		default:
			t.Errorf("reported landed service should not query live aircraft or redownload known geometry: %s", r.URL)
			return mockResponse(r, 500, ""), nil
		}
	})
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"track", "D82602", "--once", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var s flight.Snapshot
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if apiCalls != 2 || s.Status != "reported_landed" || s.Callsign != "NSZ2602" || s.AirportReport.Flight.StatusCode != "LAN" || s.Tracking.LiveLookupPerformed {
		t.Fatalf("automatic enrichment failed: %s", &out)
	}
}
