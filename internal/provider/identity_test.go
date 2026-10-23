package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"aerolatch/internal/flight"
)

func TestIdentityEndpointsAllowChangedOrMissingCallsigns(t *testing.T) {
	for _, feed := range []string{"adsb.lol", "adsb.fi"} {
		for _, tc := range []struct {
			kind             flight.TargetKind
			value, broadcast string
		}{
			{flight.ByHex, "4ACA87", ""},
			{flight.ByHex, "4aca87", "NSZ9999"},
			{flight.ByRegistration, "se-rtg", ""},
			{flight.ByRegistration, "SERTG", ""},
		} {
			t.Run(feed+"/"+string(tc.kind)+"/"+tc.broadcast, func(t *testing.T) {
				target, err := flight.NewTarget(tc.kind, tc.value)
				if err != nil {
					t.Fatal(err)
				}
				prefix := ""
				if feed == "adsb.fi" {
					prefix = "/api"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != prefix+"/v2/"+string(tc.kind)+"/"+target.Value {
						t.Errorf("wrong endpoint: %s", r.URL.Path)
					}
					fmt.Fprintf(w, `{"now":1700000000000,"ac":[{"hex":"4ACA87","flight":%q,"r":"SE-RTG","alt_baro":"ground","seen":0,"seen_pos":0,"lat":59.65,"lon":17.91},{"hex":"4aca88","flight":"NSZ4366","r":"SE-RTGA","alt_baro":"invalid"}]}`, tc.broadcast)
				}))
				defer server.Close()
				live, err := NewLive(feed, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				live.BaseURL = server.URL + prefix
				result, err := live.Lookup(context.Background(), target)
				if err != nil || len(result) != 1 {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				a := result[0]
				if a.Hex != "4aca87" || a.Callsign != tc.broadcast || a.OnGround == nil || !*a.OnGround {
					t.Fatalf("lost aircraft identity or surface report: %+v", a)
				}
			})
		}
	}
}

func TestInvalidLookupKeysCannotReachProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid target made a request") }))
	defer server.Close()
	for _, target := range []flight.Target{
		{Kind: flight.ByHex, Value: "4aca87,abc001"},
		{Kind: flight.ByRegistration, Value: "../hex/4aca87"},
		{Kind: "other", Value: "NSZ4366"},
	} {
		_, err := (Live{Client: server.Client(), BaseURL: server.URL}).Lookup(context.Background(), target)
		if err == nil {
			t.Errorf("invalid lookup accepted: %+v", target)
		}
	}
}
