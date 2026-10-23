package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSwedaviaLocalDayFilteringDSTAndCache(t *testing.T) {
	for _, date := range []string{"2026-10-20", "2026-10-25", "2026-03-29"} {
		t.Run(date, func(t *testing.T) {
			start, _ := SwedaviaDate(date, time.Time{})
			end := start.AddDate(0, 0, 1)
			now := start.Add(12 * time.Hour).UTC()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Ocp-Apim-Subscription-Key") != "test-key" {
					t.Error("missing subscription header")
				}
				if !strings.HasPrefix(r.URL.Path, "/ARN/departures/") {
					t.Errorf("unexpected URL %s", r.URL.Path)
				}
				fmt.Fprint(w, `{"flights":[`)
				for i, at := range []time.Time{start.Add(-time.Minute), start, start.Add(time.Hour), end.Add(-time.Minute), end} {
					if i > 0 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, `{"flightId":"SK%d","departureTime":{"scheduledUtc":%q},"locationAndStatus":{"flightLegStatus":"SCH"},"flightLegIdentifier":{"flightId":"SK%d","departureAirportIata":"ARN","arrivalAirportIata":"CPH","callsign":"SAS%d","aircraftRegistration":"SE-RTG"}}`, i, at.UTC().Format(time.RFC3339), i, i)
				}
				fmt.Fprint(w, `]}`)
			}))
			defer server.Close()
			client := NewSwedavia(server.Client(), "test-key")
			client.BaseURL = server.URL
			client.Now = func() time.Time { return now }
			board, err := client.Board(context.Background(), "ESSA", "departures", date)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 || len(board.Flights) != 3 {
				t.Fatalf("UTC calls=%d flights=%d", calls.Load(), len(board.Flights))
			}
			if !board.Flights[0].Departure.Scheduled.Equal(start) || !board.Flights[2].Departure.Scheduled.Equal(end.Add(-time.Minute)) {
				t.Fatal("local calendar-day bounds or sorting incorrect")
			}
			if board.Flights[0].Registration != "SERTG" {
				t.Fatal("registration normalization failed")
			}
			_, err = client.Board(context.Background(), "ARN", "departures", date)
			if err != nil || calls.Load() != 2 {
				t.Fatalf("cache did not reuse UTC boards: calls=%d err=%v", calls.Load(), err)
			}
			now = now.Add(6 * time.Minute)
			_, err = client.Board(context.Background(), "ARN", "departures", date)
			if err != nil || calls.Load() != 4 {
				t.Fatalf("active/previous UTC-day cache failed to refresh: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestSwedaviaAuthenticationFailureIsNotRepeatedOrLeaked(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"super-secret-unit-key"}`)
	}))
	defer server.Close()
	client := NewSwedavia(server.Client(), "super-secret-unit-key")
	client.BaseURL = server.URL
	for i := 0; i < 3; i++ {
		_, err := client.Board(context.Background(), "ARN", "arrivals", "2026-10-21")
		if err == nil || strings.Contains(err.Error(), "super-secret") {
			t.Fatalf("bad authentication diagnostic: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid credentials were retried: %d", calls.Load())
	}
}

func TestSwedaviaRateLimitCooldownAndRedirectProtection(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	var limited atomic.Bool
	limited.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if limited.Load() {
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"flights":[]}`)
	}))
	defer server.Close()
	client := NewSwedavia(server.Client(), "test-key")
	client.BaseURL = server.URL
	client.Now = func() time.Time { return now }
	client.Board(context.Background(), "ARN", "arrivals", "2026-10-20")
	client.Board(context.Background(), "GOT", "arrivals", "2026-10-20")
	if calls.Load() != 1 {
		t.Fatal("429 cooldown failed across airports")
	}
	now = now.Add(21 * time.Minute)
	limited.Store(false)
	if _, err := client.Board(context.Background(), "ARN", "arrivals", "2026-10-20"); err != nil || calls.Load() != 3 {
		t.Fatalf("cooldown recovery failed: calls=%d err=%v", calls.Load(), err)
	}
	var forwarded atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer redirect.Close()
	protected := NewSwedavia(redirect.Client(), "test-key")
	protected.BaseURL = redirect.URL
	if _, err := protected.Board(context.Background(), "ARN", "arrivals", "2026-10-20"); err == nil || forwarded.Load() {
		t.Fatal("authenticated request followed a redirect")
	}
}

func TestSwedaviaDecodesReportedTimesStatusesAndCodeshares(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", "2026-10-20T07:00:00Z")
		fmt.Fprint(w, `{"flights":[{"flightId":"D82602","codeShareData":["SK007"],"arrivalTime":{"scheduledUtc":"2026-10-20T05:05:00Z","actualUtc":"2026-10-20T05:11:54Z"},"locationAndStatus":{"flightLegStatus":"LAN","flightLegStatusEnglish":"Landed 07:11","gate":"A1"},"flightLegIdentifier":{"flightId":"D82602","callsign":"NSZ2602","aircraftRegistration":"SERTB","flightDepartureDateUtc":"2026-10-20T00:00:00Z","departureAirportIata":"HEL","arrivalAirportIata":"ARN"}}]}`)
	}))
	defer server.Close()
	client := NewSwedavia(server.Client(), "test-key")
	client.BaseURL = server.URL
	board, err := client.Board(context.Background(), "ARN", "arrivals", "2026-10-20")
	if err != nil || len(board.Flights) != 1 {
		t.Fatalf("board=%+v err=%v", board, err)
	}
	f := board.Flights[0]
	if f.StatusCode != "LAN" || f.Arrival.Actual == nil || f.Arrival.Actual.UTC().Format("15:04:05") != "05:11:54" || f.Departure.Actual != nil || f.Codeshares[0] != "SK007" || f.DataUpdatedAt == nil {
		t.Fatalf("airport report lost semantics: %+v", f)
	}
}
