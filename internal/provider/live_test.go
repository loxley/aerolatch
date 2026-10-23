package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelectedProvidersParseAirborneAndSurfaceReports(t *testing.T) {
	for _, tc := range []struct {
		id, name, baseURL, prefix string
	}{
		{"adsb.lol", "ADSB.lol", "https://api.adsb.lol", ""},
		{"adsb.fi", "ADSB.fi", "https://opendata.adsb.fi/api", "/api"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.prefix+"/v2/callsign/RYR9UJ" {
					t.Errorf("wrong API path: %s", r.URL.Path)
				}
				fmt.Fprint(w, `{"ac":[{"hex":"abc001","flight":"RYR9UJ ","alt_baro":3000,"lat":38.5,"lon":-0.8,"gs":170,"seen":0,"seen_pos":2},{"hex":"abc002","flight":"RYR9UJ","alt_baro":"ground","lat":38.28,"lon":-0.55,"seen":1,"seen_pos":1}],"now":1700000000000,"msg":"No error"}`)
			}))
			defer server.Close()
			live, err := NewLive(tc.id, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if live.Name() != tc.name || live.BaseURL != tc.baseURL {
				t.Fatalf("incorrect selected provider: %+v", live)
			}
			live.BaseURL = server.URL + tc.prefix
			aircraft, err := live.Lookup(context.Background(), callsign("RYR9UJ"))
			if err != nil || len(aircraft) != 2 {
				t.Fatalf("aircraft=%+v, error=%v", aircraft, err)
			}
			if aircraft[0].OnGround == nil || *aircraft[0].OnGround || aircraft[0].PositionUpdated == nil {
				t.Fatalf("airborne report lost evidence required for arrival detection: %+v", aircraft[0])
			}
			if aircraft[1].OnGround == nil || !*aircraft[1].OnGround || aircraft[1].GroundSpeed != nil || aircraft[1].PositionUpdated == nil {
				t.Fatalf("surface report lost evidence or unknown values: %+v", aircraft[1])
			}
		})
	}
}

func TestSelectedProviderLabelsInvalidResponses(t *testing.T) {
	for _, body := range []string{
		`invalid JSON`,
		`{"now":1700000000000}`,
		`{"ac":[],"now":1700000000000,"msg":"Invalid query"}`,
		`{"ac":[{"flight":"RYR9UJ","alt_baro":"invalid"}],"now":1700000000000}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		live, err := NewLive("adsb.fi", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		live.BaseURL = server.URL
		_, err = live.Lookup(context.Background(), callsign("RYR9UJ"))
		server.Close()
		if err == nil || !strings.HasPrefix(err.Error(), "ADSB.fi:") {
			t.Errorf("incorrect response error attribution for %s: %v", body, err)
		}
	}
}
