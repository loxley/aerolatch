package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/kinematics"
)

func TestMain(m *testing.M) {
	// Unit tests never spend a developer's real airport API quota. Tests for
	// Swedavia explicitly opt in with a dummy key and a mocked transport.
	os.Unsetenv("SWEDAVIA_API_KEY")
	os.Exit(m.Run())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCLITrackFlightNumberAndDirectCallsign(t *testing.T) {
	// Intercept the transport so the complete CLI path is tested without external
	// network access. These tests deliberately do not run in parallel.
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, tc := range []struct {
		name   string
		args   []string
		want   []string
		source string
	}{
		{"flight first", []string{"track", "d8 5373", "--once", "--json"}, []string{"api.adsbdb.com/v0/callsign/D85373", "api.adsb.lol/v2/callsign/NSZ5373"}, "ADSB.lol"},
		{"flags first", []string{"track", "--once", "--json", "D85373"}, []string{"api.adsbdb.com/v0/callsign/D85373", "api.adsb.lol/v2/callsign/NSZ5373"}, "ADSB.lol"},
		{"direct callsign", []string{"track", "--callsign", "nsz5373", "--once", "--json"}, []string{"api.adsb.lol/v2/callsign/NSZ5373"}, "ADSB.lol"},
		{"explicit lol", []string{"track", "D85373", "--provider", "adsb.lol", "--once", "--json"}, []string{"api.adsbdb.com/v0/callsign/D85373", "api.adsb.lol/v2/callsign/NSZ5373"}, "ADSB.lol"},
		{"fi flight number", []string{"track", "D85373", "--provider", "adsb.fi", "--once", "--json"}, []string{"api.adsbdb.com/v0/callsign/D85373", "opendata.adsb.fi/api/v2/callsign/NSZ5373"}, "ADSB.fi"},
		{"fi direct callsign", []string{"track", "--callsign", "nsz5373", "--provider", "ADSB.FI", "--once", "--json"}, []string{"opendata.adsb.fi/api/v2/callsign/NSZ5373"}, "ADSB.fi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.URL.Host+r.URL.Path)
				body := `{"ac":[],"now":1700000000000,"msg":"No error"}`
				if r.URL.Host == "api.adsbdb.com" {
					body = `{"response":{"flightroute":{"callsign_icao":"NSZ5373","callsign_iata":"D85373"}}}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})
			var out, stderr bytes.Buffer
			if err := Run(context.Background(), tc.args, &out, &stderr); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(requests) != fmt.Sprint(tc.want) {
				t.Fatalf("requests=%v, want %v", requests, tc.want)
			}
			var snapshot flight.Snapshot
			if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
				t.Fatalf("output must be clean JSON: %s: %v", &out, err)
			}
			if snapshot.Status != "not_visible" || snapshot.Callsign != "NSZ5373" || snapshot.Aircraft == nil {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
			if snapshot.Source != tc.source {
				t.Errorf("source=%q, want %q", snapshot.Source, tc.source)
			}
			if stderr.Len() != 0 {
				t.Errorf("unexpected diagnostics: %s", &stderr)
			}
		})
	}
}

func TestProviderAttributionInOutputAndFailures(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, tc := range []struct {
		provider, source, endpoint string
	}{
		{"adsb.lol", "ADSB.lol", "api.adsb.lol/v2/callsign/RYR9UJ"},
		{"adsb.fi", "ADSB.fi", "opendata.adsb.fi/api/v2/callsign/RYR9UJ"},
	} {
		for _, status := range []int{200, 503} {
			for _, asJSON := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/json=%v", tc.provider, status, asJSON), func(t *testing.T) {
					requests := 0
					http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
						requests++
						if got := r.URL.Host + r.URL.Path; got != tc.endpoint {
							t.Errorf("wrong provider endpoint: %s", got)
						}
						return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"ac":[],"now":1700000000000}`)), Header: make(http.Header), Request: r}, nil
					})
					args := []string{"track", "--callsign", "RYR9UJ", "--provider", tc.provider, "--once"}
					if asJSON {
						args = append(args, "--json")
					}
					var out bytes.Buffer
					err := Run(context.Background(), args, &out, io.Discard)
					if status == 503 {
						if err == nil || !strings.Contains(err.Error(), tc.source+": HTTP 503") {
							t.Fatalf("wrong error attribution: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if requests != 1 {
						t.Fatalf("--once should query exactly one provider: %d requests", requests)
					}
					if asJSON {
						var s flight.Snapshot
						if err := json.Unmarshal(out.Bytes(), &s); err != nil {
							t.Fatal(err)
						}
						if s.Source != tc.source {
							t.Errorf("JSON attributed to %s, want %s", s.Source, tc.source)
						}
					} else {
						if !strings.Contains(out.String(), "Data: "+tc.source+" (live)") {
							t.Errorf("missing provider attribution: %s", &out)
						}
						if tc.provider == "adsb.fi" && !strings.Contains(out.String(), "https://adsb.fi") {
							t.Errorf("missing ADSB.fi attribution link: %s", &out)
						}
					}
				})
			}
		}
	}
}

func TestUnknownProviderRejectedBeforeNetworkRequests(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unknown provider must not make a request: %s", r.URL)
		return nil, fmt.Errorf("unexpected request")
	})
	for _, value := range []string{"unknown", "", "https://api.adsb.lol"} {
		err := Run(context.Background(), []string{"track", "RYR9UJ", "--destination", "ALC", "--provider", value, "--once"}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "choose adsb.lol or adsb.fi") {
			t.Errorf("provider %q: unexpected error %v", value, err)
		}
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"track"}, {"track", "D85373", "extra"},
		{"track", "D85373", "--interval", "1s"},
		{"track", "D85373", "--timeout", "0s"},
		{"track", "D85373", "--timeout", "oops"},
		{"track", "D85373", "--callsign", "NSZ5373"},
		{"track", "../flight"},
		{"track", "RYR9UJ", "--destination", "A1C"},
		{"track", "RYR9UJ", "--destination", "Alicante"},
	} {
		if err := Run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Errorf("expected validation error for %v", args)
		}
	}
}

func TestDashboardValidationBeforeNetworkRequests(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("invalid dashboard invocation made a network request: %s", r.URL)
		return nil, fmt.Errorf("unexpected request")
	})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"track", "NSZ4366", "--tui", "--json"}, "cannot be combined"},
		{[]string{"track", "NSZ4366", "--tui", "--once"}, "cannot be combined"},
		{[]string{"track", "NSZ4366", "--tui"}, "interactive terminal"},
	} {
		err := Run(context.Background(), tc.args, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("args=%v: expected %q, got %v", tc.args, tc.want, err)
		}
	}
}

func TestDirectAircraftLookupsSkipRouteResolution(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, feed := range []string{"adsb.lol", "adsb.fi"} {
		for _, tc := range []struct{ flag, value, query string }{
			{"hex", "4ACA87", "4aca87"}, {"registration", "se-rtg", "SE-RTG"},
		} {
			t.Run(feed+"/"+tc.flag, func(t *testing.T) {
				requests := 0
				http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					requests++
					prefix, host := "", "api.adsb.lol"
					if feed == "adsb.fi" {
						prefix, host = "/api", "opendata.adsb.fi"
					}
					if r.URL.Host != host || r.URL.Path != prefix+"/v2/"+tc.flag+"/"+tc.query {
						t.Errorf("unexpected lookup (route resolution must be skipped): %s", r.URL)
					}
					body := fmt.Sprintf(`{"now":%d,"ac":[{"hex":"4aca87","r":"SE-RTG","alt_baro":"ground","seen":0}]}`, time.Now().UnixMilli())
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
				})
				var out bytes.Buffer
				err := Run(context.Background(), []string{"track", "--" + tc.flag, tc.value, "--provider", feed, "--once", "--json"}, &out, io.Discard)
				if err != nil {
					t.Fatal(err)
				}
				var s flight.Snapshot
				if err := json.Unmarshal(out.Bytes(), &s); err != nil {
					t.Fatal(err)
				}
				if requests != 1 || s.Query != tc.query || s.Route != nil || s.Callsign != "" || s.Tracking == nil || s.Tracking.LockedHex != "4aca87" || string(s.Tracking.Lookup.Kind) != tc.flag || len(s.Aircraft) != 1 {
					t.Fatalf("incorrect direct lookup result: requests=%d output=%s", requests, &out)
				}
			})
		}
	}
}

func TestInvalidOrConflictingAircraftSelectorsFailBeforeNetwork(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("invalid selector made request: %s", r.URL)
		return nil, fmt.Errorf("unexpected request")
	})
	for _, args := range [][]string{
		{"--hex", "abcde"}, {"--hex", "zzzzzz"}, {"--hex", ""},
		{"--hex", "4aca87,abc001"}, {"--registration", "SE/RTG"}, {"--registration", ""},
		{"--registration", "SE RTG"}, {"--callsign", ""},
		{"--hex", "4aca87", "--callsign", "NSZ4366"},
		{"--hex", "4aca87", "--registration", "SE-RTG"},
		{"NSZ4366", "--hex", "4aca87"},
	} {
		if err := Run(context.Background(), append([]string{"track"}, args...), io.Discard, io.Discard); err == nil {
			t.Errorf("accepted invalid selector %v", args)
		}
	}
}

func TestPlainOutputDistinguishesLookupAndBroadcastCallsign(t *testing.T) {
	s := flight.Snapshot{Query: "SE-RTG", Callsign: "", Tracking: &flight.TrackingInfo{Lookup: flight.Target{Kind: flight.ByHex, Value: "4aca87"}, LockedHex: "4aca87"}, Aircraft: []flight.Aircraft{{Hex: "4aca87", Registration: "SE-RTG"}}}
	var out bytes.Buffer
	if err := render(&out, s); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Lookup: hex 4aca87", "Following hex 4aca87", "Broadcast callsign: unavailable"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "SE-RTG →") {
		t.Fatal("output invented a callsign for direct aircraft tracking")
	}
}

func TestCalculatedPlainOutputIncludesWindowsAndKeepsRawFields(t *testing.T) {
	base := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var processor kinematics.Processor
	var aircraft []flight.Aircraft
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * 30 * time.Second)
		lat, lon, altitude, ground := 0.0, float64(i)*0.02, 10000-float64(i)*500, false
		aircraft = processor.Update(at, []flight.Aircraft{{Hex: "abc001", Latitude: &lat, Longitude: &lon, AltitudeFeet: &altitude, OnGround: &ground, PositionUpdated: &at, LastContact: &at}})
	}
	var out bytes.Buffer
	if err := render(&out, flight.Snapshot{CheckedAt: base.Add(time.Minute), Aircraft: aircraft}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"~144 kt (calculated, 30s)", "~90 ° (calculated, 30s)", "~-1000 ft/min (calculated, 1m0s)", "Vertical rate calculation: 3 samples", "2026-10-20T12:01:00Z", "Altitude: 9000 ft"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
	if aircraft[0].GroundSpeed != nil || aircraft[0].Track != nil || aircraft[0].VerticalRate != nil {
		t.Fatal("plain renderer changed raw measurements")
	}
}

func TestPlainSurfaceArrivalDoesNotClaimSurfaceEvidenceIsMissing(t *testing.T) {
	first := time.Date(2026, 10, 19, 21, 26, 1, 0, time.UTC)
	last := first.Add(5 * time.Second)
	s := flight.Snapshot{Arrival: &flight.ArrivalAssessment{
		Status: "likely_landed", Historical: true, Reason: "landing inference retained after tracking loss",
		SurfaceReports: 2, FirstSurfaceAt: &first, LastSurfaceAt: &last, CorroboratedAt: &last,
		Approach: &flight.ApproachEvidence{LastPositionAt: first.Add(-11 * time.Second)},
	}}
	var out bytes.Buffer
	if err := render(&out, s); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Last inferred arrival assessment: likely landed", "2 distinct reports over 5s", "Last surface observation: 2026-10-19T21:26:06Z"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, &out)
		}
	}
	if strings.Contains(out.String(), "Surface confirmation: unavailable") {
		t.Fatal("surface evidence was contradicted by the rendered approach explanation")
	}
}

func TestDestinationOverridePreservesReferenceRoute(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, mode := range []string{"flight", "callsign", "hex", "registration"} {
		direct := mode != "flight"
		http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"ac":[{"hex":"4cae57","flight":"RYR9UJ","r":"SE-RTG","lat":38.51495,"lon":-0.85693,"alt_baro":7975,"gs":180,"track":135,"seen":0,"seen_pos":0}],"now":%d}`, time.Now().UnixMilli())
			switch r.URL.Host {
			case "raw.githubusercontent.com":
				body = "type,name,latitude_deg,longitude_deg,elevation_ft,gps_code,iata_code\nlarge_airport,Alicante,38.2822,-0.55816,142,LEAL,ALC\n"
			case "api.adsbdb.com":
				body = `{"response":{"flightroute":{"callsign_icao":"RYR9UJ","destination":{"iata_code":"EDI","name":"Edinburgh","latitude":55.95,"longitude":-3.37,"elevation":135}}}}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
		})
		args := []string{"track", "RYR9UJ", "--destination", "alc", "--once", "--json"}
		if direct {
			value := "RYR9UJ"
			if mode == "hex" {
				value = "4cae57"
			}
			if mode == "registration" {
				value = "SE-RTG"
			}
			args = []string{"track", "--" + mode, value, "--destination", "ALC", "--once", "--json"}
		}
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var s flight.Snapshot
		if err := json.Unmarshal(out.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if s.Destination == nil || s.Destination.Airport.IATA != "ALC" || s.Destination.Source != "user_override" {
			t.Fatalf("missing selected destination: %s", &out)
		}
		if !direct && (s.Route == nil || s.Route.Destination.IATA != "EDI") {
			t.Fatalf("reference destination must be retained: %s", &out)
		}
		if direct && s.Route != nil {
			t.Fatalf("direct mode should not invent a reference route: %s", &out)
		}
		if s.LandingEstimate == nil || s.LandingEstimate.Status != "estimated" || s.LandingEstimate.DistanceNM == nil || *s.LandingEstimate.DistanceNM > 30 {
			t.Fatalf("ETA must use Alicante, including in direct mode: %s", &out)
		}
		if s.Arrival == nil || s.Arrival.Status != "airborne" {
			t.Fatalf("arrival detector must use the override: %s", &out)
		}
	}
}

func TestDestinationLookupFailureStopsBeforeTracking(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	requests := 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "raw.githubusercontent.com" {
			t.Errorf("must not fall back to an unrequested destination: %s", r.URL)
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header), Request: r}, nil
	})
	err := Run(context.Background(), []string{"track", "RYR9UJ", "--destination", "ALC", "--once"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "resolve destination ALC") || requests != 1 {
		t.Fatalf("expected explicit destination failure, got error=%v, requests=%d", err, requests)
	}
}

func TestRenderInferredArrivalWithHistoricalLabel(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	first := now.Add(-30 * time.Second)
	for _, historical := range []bool{false, true} {
		s := flight.Snapshot{
			CheckedAt: now, Destination: &flight.Destination{Airport: flight.Airport{IATA: "ALC", Name: "Alicante"}, Source: "user_override", DataSource: "OurAirports"},
			Arrival: &flight.ArrivalAssessment{Status: "likely_landed", Reason: "airborne then repeated surface reports", Historical: historical, FirstSurfaceAt: &first, CorroboratedAt: &now},
		}
		var out bytes.Buffer
		if err := render(&out, s); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"likely landed at Alicante (ALC)", "user override", "not an exact touchdown time", "Corroborating surface observation"} {
			if !strings.Contains(out.String(), text) {
				t.Errorf("arrival output missing %q: %s", text, &out)
			}
		}
		if historical && !strings.Contains(out.String(), "Last inferred arrival assessment") {
			t.Errorf("historical inference needs an explicit label: %s", &out)
		}
	}
}

func TestPossibleLandingOutputPreservesHistoricalEvidence(t *testing.T) {
	first := time.Date(2026, 10, 17, 21, 9, 29, 0, time.UTC)
	last, missing, assessed := first.Add(40*time.Second), first.Add(122*time.Second), first.Add(182*time.Second)
	for _, historical := range []bool{false, true} {
		s := flight.Snapshot{
			CheckedAt: assessed, Status: "not_visible", Source: "ADSB.fi", Aircraft: []flight.Aircraft{},
			Destination: &flight.Destination{Airport: flight.Airport{IATA: "ARN", Name: "Stockholm-Arlanda"}},
			Arrival: &flight.ArrivalAssessment{
				Status: "possible_landing", Reason: "low descending approach followed by sustained successful empty responses; surface confirmation unavailable",
				Historical: historical, LastObservedAt: &last, MissingSince: &missing, EmptyResponses: 3, AssessedAt: &assessed,
				Approach: &flight.ApproachEvidence{StartedAt: first, LastPositionAt: last, ObservationCount: 3, AltitudeDropFeet: 475, LastDistanceNM: 1.3, LastSpeedKnots: 132},
			},
		}
		var out bytes.Buffer
		if err := render(&out, s); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"possible landing at Stockholm-Arlanda (ARN)", "21:10:09Z", "historical evidence, not a current position", "3 fixes over 40s", "Surface confirmation: unavailable", "Successful empty responses: 3"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q: %s", want, &out)
			}
		}
		if strings.Contains(out.String(), "First surface observation:") || strings.Contains(out.String(), "likely landed") {
			t.Errorf("possible landing output invented surface evidence: %s", &out)
		}
		if historical && !strings.Contains(out.String(), "Last inferred arrival assessment") {
			t.Error("retained possible landing is not marked historical")
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var decoded flight.Snapshot
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Arrival == nil || decoded.Arrival.Status != "possible_landing" || decoded.Arrival.Approach == nil || !decoded.Arrival.Approach.LastPositionAt.Equal(last) || decoded.Arrival.FirstSurfaceAt != nil || len(decoded.Aircraft) != 0 {
			t.Fatalf("JSON lost or relabeled historical evidence: %s", encoded)
		}
	}
}

func TestEstimatedTouchdownTerminalAndJSON(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"ac":[{"hex":"abc001","flight":"NSZ5373","lat":0,"lon":-5,"alt_baro":35000,"gs":450,"track":90,"seen":0,"seen_pos":0}],"now":%d}`, time.Now().UnixMilli())
		if r.URL.Host == "api.adsbdb.com" {
			body = `{"response":{"flightroute":{"callsign_icao":"NSZ5373","destination":{"latitude":0,"longitude":0,"elevation":0}}}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	for _, asJSON := range []bool{false, true} {
		args := []string{"track", "D85373", "--once"}
		if asJSON {
			args = append(args, "--json")
		}
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var s flight.Snapshot
			if err := json.Unmarshal(out.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
			if s.LandingEstimate == nil || s.LandingEstimate.Status != "estimated" || s.LandingEstimate.TouchdownAt == nil || s.LandingEstimate.Quality != "rough" {
				t.Fatalf("missing JSON estimate: %s", &out)
			}
		} else {
			for _, want := range []string{"Estimated touchdown: ~", "Estimate: rough", "Phase: cruise", "straight line", "UTC"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("terminal estimate missing %q: %s", want, &out)
				}
			}
		}
	}
}

func TestRenderUnknownValuesAndLastObservation(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	positionAt := now.Add(-2 * time.Minute)
	zero := 0.0
	s := flight.Snapshot{
		CheckedAt: now, Query: "D85373", Callsign: "NSZ5373", Status: "not_visible",
		LastObserved: []flight.Aircraft{{Hex: "abc001", Latitude: &zero, Longitude: &zero, PositionUpdated: &positionAt}},
	}
	var out bytes.Buffer
	if err := render(&out, s); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"not currently visible", "not a current position", "Altitude: unavailable", "Ground speed: unavailable", "Position: 0.00000, 0.00000", "2m0s ago"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, &out)
		}
	}
}
