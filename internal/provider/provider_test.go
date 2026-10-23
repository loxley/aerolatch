package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

func callsign(value string) flight.Target {
	return flight.Target{Kind: flight.ByCallsign, Value: value}
}

func TestResolveNorwegianFlight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/callsign/D85373" {
			t.Errorf("unexpected lookup path: %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/json" {
			t.Error("missing API request headers")
		}
		fmt.Fprint(w, `{"response":{"flightroute":{"callsign_iata":"D85373","callsign_icao":"NSZ5373","airline":{"name":"Norwegian Air Sweden AOC"},"origin":{"iata_code":"CPH","name":"Copenhagen Kastrup Airport"},"destination":{"iata_code":"ALC","name":"Alicante","latitude":38.2822,"longitude":-0.55816,"elevation":142}}}}`)
	}))
	defer server.Close()
	route, err := (Resolver{Client: server.Client(), BaseURL: server.URL}).Resolve(context.Background(), "D85373")
	if err != nil {
		t.Fatal(err)
	}
	if route.Callsign != "NSZ5373" || route.FlightNumber != "D85373" || route.Origin.IATA != "CPH" || route.Destination.IATA != "ALC" {
		t.Fatalf("incorrect route: %+v", route)
	}
	if route.Destination.Latitude == nil || *route.Destination.Latitude != 38.2822 || route.Destination.Longitude == nil || *route.Destination.Longitude != -0.55816 || route.Destination.ElevationFeet == nil || *route.Destination.ElevationFeet != 142 {
		t.Fatalf("missing destination metadata for ETA: %+v", route.Destination)
	}
}

func TestResolveFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"unknown", `{"response":"unknown callsign"}`, "unknown to adsbdb", 404},
		{"no operational callsign", `{"response":{"flightroute":{"callsign_iata":"D85373"}}}`, "operational callsign", 200},
		{"bad response", `{"response":`, "invalid JSON", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := (Resolver{Client: server.Client(), BaseURL: server.URL}).Resolve(context.Background(), "D85373")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestLiveNullableFieldsAndGroundReports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/callsign/NSZ5373" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"now":1700000000000,"msg":"No error","ac":[
		{"hex":"abc001","flight":"NSZ5373 ","alt_baro":35000,"lat":0,"lon":0,"gs":0,"track":0,"seen":0,"seen_pos":2.5},
		{"hex":"abc002","flight":"NSZ5373","alt_baro":"ground","seen":1},
		{"hex":"abc003","flight":"NSZ5373","alt_baro":null},
		{"hex":"abc004","flight":"NSZ53730","alt_baro":10000}]}`)
	}))
	defer server.Close()
	aircraft, err := (Live{Client: server.Client(), BaseURL: server.URL}).Lookup(context.Background(), callsign("NSZ5373"))
	if err != nil {
		t.Fatal(err)
	}
	if len(aircraft) != 3 {
		t.Fatalf("expected three exact matches, got %d", len(aircraft))
	}
	a := aircraft[0]
	if a.Latitude == nil || *a.Latitude != 0 || a.GroundSpeed == nil || *a.GroundSpeed != 0 || a.Track == nil || *a.Track != 0 {
		t.Fatalf("valid zeros must be preserved: %+v", a)
	}
	if a.AltitudeFeet == nil || *a.AltitudeFeet != 35000 || a.OnGround == nil || *a.OnGround {
		t.Fatalf("incorrect altitude: %+v", a)
	}
	want := time.UnixMilli(1700000000000).Add(-2500 * time.Millisecond)
	if a.PositionUpdated == nil || !a.PositionUpdated.Equal(want) {
		t.Fatalf("incorrect position timestamp: %v", a.PositionUpdated)
	}
	ground := aircraft[1]
	if ground.OnGround == nil || !*ground.OnGround || ground.AltitudeFeet != nil || ground.PositionUpdated != nil || ground.Latitude != nil {
		t.Fatalf("incorrect surface report: %+v", ground)
	}
	unknown := aircraft[2]
	if unknown.OnGround != nil || unknown.AltitudeFeet != nil || unknown.LastContact != nil {
		t.Fatalf("missing fields must remain unknown: %+v", unknown)
	}
}

func TestLiveEmptyVersusInvalidResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"no match", `{"ac":[],"now":1700000000000,"msg":"No error"}`, false},
		{"missing aircraft", `{"now":1700000000000}`, true},
		{"null aircraft", `{"ac":null,"now":1700000000000}`, true},
		{"missing timestamp", `{"ac":[]}`, true},
		{"API error", `{"ac":[],"now":1700000000000,"msg":"Invalid query"}`, true},
		{"invalid altitude", `{"ac":[{"flight":"NSZ5373","alt_baro":"unknown"}],"now":1700000000000}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			result, err := (Live{Client: server.Client(), BaseURL: server.URL}).Lookup(context.Background(), callsign("NSZ5373"))
			if (err != nil) != tc.wantError {
				t.Fatalf("result %v, error %v; want error=%v", result, err, tc.wantError)
			}
			if !tc.wantError && result == nil {
				t.Error("empty aircraft must be an empty slice, not null")
			}
		})
	}
}

func TestRateLimitAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	live := Live{Client: server.Client(), BaseURL: server.URL}
	_, err := live.Lookup(context.Background(), callsign("NSZ5373"))
	if !Retryable(err) || RetryAfter(err) != 2*time.Minute {
		t.Fatalf("rate limit not respected: %v, delay %s", err, RetryAfter(err))
	}
	if Retryable(&HTTPError{StatusCode: 403}) {
		t.Error("403 must not be retried indefinitely")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = live.Lookup(ctx, callsign("NSZ5373"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if got := parseRetryAfter(now.Add(time.Minute).Format(http.TimeFormat), now); got != time.Minute {
		t.Fatalf("HTTP-date Retry-After: got %s", got)
	}
}
