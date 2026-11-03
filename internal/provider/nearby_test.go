package provider

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAreaEndpointsShareAircraftDecoder(t *testing.T) {
	for _, tc := range []struct{ provider, path string }{
		{"adsb.lol", "/v2/point/59.33/18.07/25"},
		{"adsb.fi", "/v3/lat/59.33/lon/18.07/dist/25"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path %s, want %s", r.URL.Path, tc.path)
				}
				fmt.Fprint(w, `{"ac":[{"hex":"ABC001","flight":"SAS123  ","alt_baro":8000,"lat":59.34,"lon":18.08,"gs":0,"seen":1,"seen_pos":2},{"hex":"abc002","alt_baro":"ground","lat":59.33,"lon":18.07,"seen":0,"seen_pos":0}],"now":1700000000000,"msg":"No error"}`)
			}))
			defer server.Close()
			live, _ := NewLive(tc.provider, server.Client())
			live.BaseURL = server.URL
			aircraft, err := live.LookupNearby(context.Background(), 59.33, 18.07, 25)
			if err != nil || len(aircraft) != 2 {
				t.Fatalf("aircraft=%+v, err=%v", aircraft, err)
			}
			a, b := aircraft[0], aircraft[1]
			if a.Hex != "abc001" || a.Callsign != "SAS123" || a.GroundSpeed == nil || *a.GroundSpeed != 0 || a.OnGround == nil || *a.OnGround {
				t.Fatalf("airborne/zero telemetry lost: %+v", a)
			}
			if a.PositionUpdated == nil || !a.PositionUpdated.Equal(time.UnixMilli(1700000000000).Add(-2*time.Second)) {
				t.Fatalf("feed-relative position age lost: %+v", a)
			}
			if b.Callsign != "" || b.GroundSpeed != nil || b.OnGround == nil || !*b.OnGround {
				t.Fatalf("missing callsign, unknown speed, or surface evidence changed: %+v", b)
			}
		})
	}
}

func TestAreaValidationAndResponseErrors(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, `{"ac":[],"now":1700000000000}`)
	}))
	defer server.Close()
	live, _ := NewLive("adsb.lol", server.Client())
	live.BaseURL = server.URL
	for _, coordinates := range [][3]float64{{91, 0, 25}, {0, 181, 25}, {math.NaN(), 0, 25}, {0, 0, 0}, {0, 0, 251}} {
		if _, err := live.LookupNearby(context.Background(), coordinates[0], coordinates[1], int(coordinates[2])); err == nil {
			t.Errorf("accepted %v", coordinates)
		}
	}
	if requests != 0 {
		t.Fatal("invalid input made requests")
	}
	aircraft, err := live.LookupNearby(context.Background(), 0, 0, 1)
	if err != nil || aircraft == nil || len(aircraft) != 0 {
		t.Fatalf("empty valid equatorial area rejected: %v %v", aircraft, err)
	}
}
