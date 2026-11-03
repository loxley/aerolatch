package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCitySearchPreservesAmbiguityAndEncodesQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/search" || q.Get("name") != "Åby & city" || q.Get("countryCode") != "SE" || q.Get("count") != "10" {
			t.Errorf("unexpected search: %s", r.URL)
		}
		fmt.Fprint(w, `{"results":[{"id":1,"name":"Åby","admin1":"Östergötland","country":"Sweden","country_code":"SE","latitude":58.6,"longitude":16.2},{"id":2,"name":"Åby","admin1":"Kalmar","country":"Sweden","country_code":"SE","latitude":57.2,"longitude":16.4}]}`)
	}))
	defer server.Close()
	locations, err := (Places{Client: server.Client(), BaseURL: server.URL}).Search(context.Background(), " Åby & city ", "se")
	if err != nil || len(locations) != 2 || locations[0].ID != 1 || locations[1].ID != 2 {
		t.Fatalf("ambiguous places lost: %+v, %v", locations, err)
	}
	if locations[0].Source != "city" || locations[0].DataSource != "Open-Meteo / GeoNames" || !strings.Contains(locations[0].Label(), "Östergötland") {
		t.Fatalf("location provenance lost: %+v", locations[0])
	}
}

func TestPlaceIDAndInvalidGeocodingResponses(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"id":42,"name":"Zero","latitude":0,"longitude":0}`, true},
		{`{"id":41,"name":"Wrong ID","latitude":0,"longitude":0}`, false},
		{`{"id":42,"name":"Missing latitude","longitude":0}`, false},
		{`{"id":42,"name":"Outside bounds","latitude":91,"longitude":0}`, false},
		{`invalid JSON`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/get" || r.URL.Query().Get("id") != "42" {
				t.Errorf("incorrect selected-place request: %s", r.URL)
			}
			fmt.Fprint(w, tc.body)
		}))
		location, err := (Places{Client: server.Client(), BaseURL: server.URL}).Lookup(context.Background(), 42)
		server.Close()
		if (err == nil) != tc.valid || (tc.valid && location.Latitude != 0) {
			t.Errorf("response %s: %+v, %v", tc.body, location, err)
		}
	}
	for _, body := range []string{`{}`, `{"results":[]}`, `{"error":true}`, `{"results":[{"id":1,"name":"Missing coordinates"}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := (Places{Client: server.Client(), BaseURL: server.URL}).Search(context.Background(), "Unknown", "")
		server.Close()
		if err == nil {
			t.Errorf("accepted empty/broken geocoding response: %s", body)
		}
	}
}

func TestCitySearchPrefersExactNamesWithoutGuessingBetweenCities(t *testing.T) {
	for _, otherName := range []string{"Stockholme", "Stockholm"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"results":[{"id":1,"name":"Stockholm","latitude":59.33,"longitude":18.07},{"id":2,"name":%q,"latitude":61.2,"longitude":15.55}]}`, otherName)
		}))
		locations, err := (Places{Client: server.Client(), BaseURL: server.URL}).Search(context.Background(), "Stockholm", "")
		server.Close()
		want := 1
		if otherName == "Stockholm" {
			want = 2
		}
		if err != nil || len(locations) != want {
			t.Errorf("other name %s: %+v, %v", otherName, locations, err)
		}
	}
}
