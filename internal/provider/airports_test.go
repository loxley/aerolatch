package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const airportCSV = `ident,type,name,latitude_deg,longitude_deg,elevation_ft,gps_code,iata_code
LEAL,large_airport,"Alicante, Spain",38.2822,-0.55816,142,LEAL,ALC
EGPH,large_airport,Edinburgh,55.95,-3.37,135,EGPH,EDI
OLD,closed,Old Alicante,38,-0.5,0,OLDA,ALC
`

func TestAirportOverrideLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, airportCSV)
	}))
	defer server.Close()
	for _, code := range []string{"ALC", "LEAL"} {
		airport, err := (Airports{Client: server.Client(), URL: server.URL}).Lookup(context.Background(), code)
		if err != nil {
			t.Fatal(err)
		}
		if airport.IATA != "ALC" || airport.ICAO != "LEAL" || airport.Latitude == nil || *airport.Latitude != 38.2822 || airport.ElevationFeet == nil || *airport.ElevationFeet != 142 {
			t.Fatalf("incorrect airport for %s: %+v", code, airport)
		}
	}
}

func TestAirportLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, reason string
		status                   int
	}{
		{"unknown", airportCSV, "XYZ", "not found", 200},
		{"duplicate", airportCSV + "NEW,large_airport,Duplicate,38,-0.5,142,NEWW,ALC\n", "ALC", "ambiguous", 200},
		{"bad coordinates", strings.ReplaceAll(airportCSV, "38.2822", "NaN"), "ALC", "invalid coordinates", 200},
		{"bad elevation", strings.ReplaceAll(airportCSV, ",142,", ",NaN,"), "ALC", "invalid elevation", 200},
		{"bad header", "error\nnot a dataset\n", "ALC", "missing CSV column", 200},
		{"HTTP failure", "unavailable", "ALC", "HTTP 503", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := (Airports{Client: server.Client(), URL: server.URL}).Lookup(context.Background(), tc.code)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("got %v, want %q", err, tc.reason)
			}
		})
	}
}
