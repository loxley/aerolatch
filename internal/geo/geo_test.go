package geo

import (
	"math"
	"testing"
)

func TestIdenticalCoordinatesHaveExactlyZeroDistance(t *testing.T) {
	for _, point := range [][2]float64{{59.33, 18.07}, {59.32938, 18.06871}, {-33.8688, 151.2093}, {0, 0}, {90, 180}} {
		if distance := DistanceNM(point[0], point[1], point[0], point[1]); distance != 0 {
			t.Errorf("identical point %v has nonzero distance %.17g nm", point, distance)
		}
	}
}

func TestGreatCircleDistances(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		lat1, lon1, lat2, lon2, want float64
	}{
		{"one degree latitude", 0, 0, 1, 0, 60.04046},
		{"dateline crossing", 0, 179.9, 0, -179.9, 12.00809},
		{"antipodes", 0, 0, 0, 180, 10807.28293},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forward := DistanceNM(tc.lat1, tc.lon1, tc.lat2, tc.lon2)
			reverse := DistanceNM(tc.lat2, tc.lon2, tc.lat1, tc.lon1)
			if math.Abs(forward-tc.want) > 0.00001 || math.Abs(forward-reverse) > 1e-9 {
				t.Fatalf("distance %.12f nm, reverse %.12f nm; want approximately %.5f nm", forward, reverse, tc.want)
			}
		})
	}
}
