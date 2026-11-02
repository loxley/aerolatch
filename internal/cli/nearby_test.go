package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/provider"
)

func TestNearbyCLIResolvesOnceAndUsesProviderAreaEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, host, path string
		args             []string
		locationRequests int
	}{
		{"city lol", "api.adsb.lol", "/v2/point/59.33/18.07/25", []string{"--city", "Stockholm", "--country", "SE"}, 1},
		{"city fi", "opendata.adsb.fi", "/api/v3/lat/59.33/lon/18.07/dist/25", []string{"--city", "Stockholm", "--provider", "adsb.fi"}, 1},
		{"coordinates", "api.adsb.lol", "/v2/point/0/0/25", []string{"--lat", "0", "--lon", "0"}, 0},
		{"selected place", "api.adsb.lol", "/v2/point/59.33/18.07/25", []string{"--place-id", "42"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			locations, areas := 0, 0
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := ""
				if r.URL.Host == "geocoding-api.open-meteo.com" {
					locations++
					body = `{"id":42,"name":"Stockholm","admin1":"Stockholm County","country":"Sweden","country_code":"SE","latitude":59.33,"longitude":18.07}`
					if r.URL.Path == "/v1/search" {
						if r.URL.Query().Get("name") != "Stockholm" {
							t.Errorf("incorrect city query: %s", r.URL)
						}
						body = `{"results":[` + body + `]}`
					} else if r.URL.Path != "/v1/get" || r.URL.Query().Get("id") != "42" {
						t.Errorf("unexpected geocoding request: %s", r.URL)
					}
				} else {
					areas++
					if r.URL.Host != tc.host || r.URL.Path != tc.path {
						t.Errorf("unexpected area request: %s", r.URL)
					}
					lat, lon := 59.33, 18.07
					if tc.name == "coordinates" {
						lat, lon = 0, 0
					}
					body = fmt.Sprintf(`{"ac":[{"hex":"abc001","flight":"TEST123 ","r":"SE-ABC","t":"A320","lat":%f,"lon":%f,"alt_baro":8000,"gs":0,"seen":0,"seen_pos":0}],"now":%d,"msg":"No error"}`, lat, lon, time.Now().UnixMilli())
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})
			args := append([]string{"nearby"}, tc.args...)
			args = append(args, "--once", "--json")
			var out, stderr bytes.Buffer
			if err := Run(context.Background(), args, &out, &stderr); err != nil {
				t.Fatal(err)
			}
			var snapshot flight.NearbySnapshot
			if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
				t.Fatalf("JSON polluted by interactive output: %q: %v", out.String(), err)
			}
			if locations != tc.locationRequests || areas != 1 || len(snapshot.Aircraft) != 1 || snapshot.Status != "visible" || snapshot.RadiusNM != 25 {
				t.Fatalf("incorrect lookups or snapshot: locations=%d, areas=%d, %+v", locations, areas, snapshot)
			}
			if snapshot.Aircraft[0].GroundSpeed == nil || *snapshot.Aircraft[0].GroundSpeed != 0 {
				t.Fatalf("reported zero speed lost: %v", snapshot.Aircraft[0].GroundSpeed)
			}
			if distance := snapshot.Aircraft[0].DistanceNM; distance != 0 {
				t.Fatalf("identical coordinates must have zero distance; got %.17g nm", distance)
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", &stderr)
			}
		})
	}
}

func TestNearbyCLIRejectsInvalidOptionsBeforeNetwork(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("invalid input made request: %s", r.URL)
		return nil, fmt.Errorf("network disabled")
	})
	for _, args := range [][]string{
		{}, {"--lat", "0"}, {"--lon", "0"}, {"--lat", "NaN", "--lon", "0"},
		{"--lat", "91", "--lon", "0"}, {"--city", "X"}, {"--city", "Stockholm", "--country", "Sweden"},
		{"--city", "Stockholm", "--lat", "0", "--lon", "0"}, {"--place-id", "0"},
		{"--place-id", "1", "--country", "SE"}, {"--city", "Stockholm", "--radius", "251"},
		{"--city", "Stockholm", "--radius", "0"}, {"--city", "Stockholm", "--interval", "1s"},
		{"--city", "Stockholm", "--timeout", "0"}, {"--city", "Stockholm", "--provider", "unknown"},
		{"--city", "Stockholm", "--tui", "--json"}, {"--city", "Stockholm", "--tui"},
	} {
		var out, stderr bytes.Buffer
		if err := Run(context.Background(), append([]string{"nearby"}, args...), &out, &stderr); err == nil {
			t.Errorf("accepted invalid args %v", args)
		}
	}
}

func TestNearbyAmbiguityIsActionableAndDoesNotPoll(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	requests := 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "geocoding-api.open-meteo.com" {
			t.Errorf("ambiguous search polled aircraft: %s", r.URL)
		}
		body := `{"results":[{"id":1,"name":"Cambridge","country":"UK","latitude":52.2,"longitude":0.1},{"id":2,"name":"Cambridge","country":"USA","latitude":42.4,"longitude":-71.1}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	var out, stderr bytes.Buffer
	err := Run(context.Background(), []string{"nearby", "--city", "Cambridge", "--once", "--json"}, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--place-id 1") || !strings.Contains(err.Error(), "--place-id 2") || requests != 1 || out.Len() != 0 {
		t.Fatalf("ambiguity not handled safely: %v, output=%q, requests=%d", err, out.String(), requests)
	}
}

func TestNearbyPlainRetainedRowsAreNotCurrent(t *testing.T) {
	now := time.Now().UTC()
	ground := true
	var out bytes.Buffer
	s := flight.NearbySnapshot{CheckedAt: now, Location: flight.Location{Name: "Test city", Source: "city"}, Source: "ADSB.fi", RadiusNM: 25, Status: "retrying", Error: "HTTP 429",
		LastObserved: []flight.NearbyAircraft{{Aircraft: flight.Aircraft{Hex: "abc001", Callsign: "TEST123", OnGround: &ground, PositionUpdated: &now}, DistanceNM: 2, Direction: "N"}}}
	if err := renderNearby(&out, s); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NOT CURRENT", "city reference point", "Open-Meteo / GeoNames", "https://adsb.fi", "on ground", "TEST123"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plain output missing %q: %s", want, out.String())
		}
	}
}

func TestBrowsingSourcePreservesCooldownAcrossViewsAndCancelsWait(t *testing.T) {
	for _, name := range []string{"adsb.lol", "adsb.fi"} {
		live, _ := provider.NewLive(name, nil)
		source := &browsingSource{Live: live}
		source.cooldown(&provider.HTTPError{StatusCode: 429, RetryAfter: 20 * time.Second})
		if time.Until(source.next) < 19*time.Second {
			t.Fatalf("%s lost server cooldown", name)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := source.Lookup(ctx, flight.Target{Kind: flight.ByHex, Value: "abc001"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cooldown was not cancellable: %v", name, err)
		}
	}
}

// A PTY harness can exercise city selection and the complete CLI navigation
// without spending API quota or relying on live aircraft availability.
func TestNearbyCLITerminalHelper(t *testing.T) {
	mode := os.Getenv("AEROLATCH_TUI_TEST")
	swedavia := mode == "nearby-cli-swedavia" || mode == "nearby-cli-swedavia-slow" || mode == "nearby-cli-swedavia-off"
	if mode != "nearby-cli" && mode != "nearby-cli-slow" && mode != "nearby-cli-cooldown" && !swedavia {
		t.Skip("PTY CLI smoke helper")
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	var requests []string
	var mu sync.Mutex
	record := func(name string) { mu.Lock(); requests = append(requests, name); mu.Unlock() }
	fixtureTime := time.Now().UTC()
	if swedavia {
		t.Setenv("SWEDAVIA_API_KEY", "fixture-nearby-airport-key")
	}
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		status, headers := http.StatusOK, make(http.Header)
		if r.Header.Get("Ocp-Apim-Subscription-Key") != "" && r.URL.Host != "api.swedavia.se" {
			t.Error("airport credential sent to another provider")
		}
		if r.URL.Host == "api.swedavia.se" {
			record("airport")
			if !swedavia || r.Header.Get("Ocp-Apim-Subscription-Key") != "fixture-nearby-airport-key" || !strings.Contains(r.URL.Path, "/ARN/arrivals/") {
				t.Error("incorrect selected-aircraft airport request")
			}
			if mode == "nearby-cli-swedavia-slow" {
				t.Log("AIRPORT_REQUEST_PENDING")
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			body = fmt.Sprintf(`{"flights":[{"flightId":"SK1021","airlineOperator":{"name":"Scandinavian Airlines System"},"arrivalTime":{"scheduledUtc":%q,"actualUtc":%q},"locationAndStatus":{"flightLegStatus":"LAN"},"flightLegIdentifier":{"flightId":"SK1021","callsign":"SAS1021","aircraftRegistration":"SE-MKF","departureAirportIata":"SFT","arrivalAirportIata":"ARN","flightDepartureDateUtc":%q}}]}`,
				fixtureTime.Format(time.RFC3339), fixtureTime.Add(-time.Minute).Format(time.RFC3339), fixtureTime.Truncate(24*time.Hour).Format(time.RFC3339))
			return mockResponse(r, 200, body), nil
		}
		switch r.URL.Path {
		case "/v1/search":
			record("search")
			body = `{"results":[{"id":1,"name":"Cambridge","country":"First country","latitude":59,"longitude":18},{"id":2,"name":"Cambridge","country":"Second country","latitude":60,"longitude":18}]}`
		case "/v2/point/60/18/25":
			record("area")
		case "/v2/hex/abc001":
			record("hex")
			if mode == "nearby-cli-slow" {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			if mode == "nearby-cli-cooldown" {
				status = http.StatusTooManyRequests
				headers.Set("Retry-After", "60")
			}
		case "/v2/hex/4ab566":
			record("hex")
			body = fmt.Sprintf(`{"ac":[],"now":%d}`, time.Now().UnixMilli())
		case "/v0/callsign/TEST123":
			record("route")
			if mode == "nearby-cli-slow" {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			body = `{"response":{"flightroute":{"callsign_icao":"TEST123","callsign_iata":"TT123","origin":{"iata_code":"ARN"},"destination":{"iata_code":"LHR","latitude":51.4706,"longitude":-0.46194,"elevation":83},"airline":{"name":"Fixture Air"}}}}`
		case "/v0/callsign/SAS1021":
			record("route")
			body = `{"response":{"flightroute":{"callsign_icao":"SAS1021","callsign_iata":"SK1021","origin":{"iata_code":"SFT"},"destination":{"iata_code":"ARN","icao_code":"ESSA","latitude":59.6519,"longitude":17.9186,"elevation":137}}}}`
		default:
			return nil, fmt.Errorf("unexpected request: %s", r.URL)
		}
		if body == "" {
			if swedavia {
				body = fmt.Sprintf(`{"ac":[{"hex":"4ab566","flight":"SAS1021","r":"SE-MKF","t":"AT76","lat":59.63319,"lon":17.91165,"alt_baro":300,"gs":107,"track":10,"baro_rate":-640,"seen":0,"seen_pos":0}],"now":%d}`, time.Now().UnixMilli())
			} else {
				body = fmt.Sprintf(`{"ac":[{"hex":"abc001","flight":"TEST123","t":"A320","lat":60.01,"lon":18,"alt_baro":8000,"gs":200,"track":240,"baro_rate":-640,"seen":0,"seen_pos":0}],"now":%d,"msg":"No error"}`, time.Now().UnixMilli())
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: headers, Request: r}, nil
	})
	args := []string{"nearby", "--city", "Cambridge", "--tui"}
	if mode == "nearby-cli-swedavia-off" {
		args = append(args, "--swedavia", "off")
	}
	if err := Run(context.Background(), args, os.Stdout, os.Stderr); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, request := range requests {
		counts[request]++
	}
	cycles := 1
	if value := os.Getenv("AEROLATCH_TUI_CYCLES"); value != "" {
		var err error
		cycles, err = strconv.Atoi(value)
		if err != nil || cycles < 1 || cycles > 5 {
			t.Fatal("invalid PTY cycle count")
		}
	}
	wantAreas := cycles + 1
	if os.Getenv("AEROLATCH_TUI_RETURN") == "0" {
		wantAreas = cycles
	}
	if mode == "nearby-cli-cooldown" {
		wantAreas = 1
	}
	if counts["search"] != 1 || counts["area"] != wantAreas || counts["hex"] != cycles || counts["route"] > cycles || (mode == "nearby-cli" && counts["route"] != cycles) {
		t.Fatalf("incorrect location or navigation requests: %v", requests)
	}
	if mode == "nearby-cli-swedavia" && counts["airport"] != 2 {
		t.Fatalf("airport cache was not shared across selections: %v", requests)
	}
	if mode == "nearby-cli-swedavia-slow" && counts["airport"] < 1 {
		t.Fatal("slow airport request was not exercised")
	}
	if mode == "nearby-cli-swedavia-off" && counts["airport"] != 0 {
		t.Fatal("explicit opt-out made airport requests")
	}
	fmt.Fprintln(os.Stdout, "CLI_NEARBY_LIFECYCLE_OK")
}

func TestNearbySwedaviaConfigurationAndAreaOptOut(t *testing.T) {
	t.Setenv("SWEDAVIA_API_KEY", "")
	if config := nearbyInspectionOptions(&http.Client{}, "auto"); config.Airports != nil || config.SwedaviaStatus != "no_key" {
		t.Fatal("missing-key configuration incorrect")
	}
	t.Setenv("SWEDAVIA_API_KEY", "fixture-key")
	if config := nearbyInspectionOptions(&http.Client{}, "off"); config.Airports != nil || config.SwedaviaStatus != "off" {
		t.Fatal("opt-out still configured airport access")
	}
	if config := nearbyInspectionOptions(&http.Client{}, "auto"); config.Airports == nil {
		t.Fatal("exported key did not enable selected-aircraft enrichment")
	}
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.adsb.lol" {
			t.Errorf("area listing called airport provider: %s", r.URL.Host)
		}
		return mockResponse(r, 200, fmt.Sprintf(`{"ac":[],"now":%d}`, time.Now().UnixMilli())), nil
	})
	if err := Run(context.Background(), []string{"nearby", "--lat", "0", "--lon", "0", "--once", "--swedavia", "auto"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"nearby", "--lat", "0", "--lon", "0", "--once", "--swedavia", "invalid"}, io.Discard, io.Discard); err == nil {
		t.Fatal("invalid Swedavia mode accepted")
	}
}
