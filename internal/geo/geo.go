// Package geo provides great-circle geometry in nautical miles and degrees.
package geo

import "math"

func ValidCoordinates(lat, lon *float64) bool {
	return lat != nil && lon != nil && !math.IsNaN(*lat) && !math.IsNaN(*lon) && math.Abs(*lat) <= 90 && math.Abs(*lon) <= 180
}

func DistanceNM(lat1, lon1, lat2, lon2 float64) float64 {
	const radians = math.Pi / 180
	p, q := lat1*radians, lat2*radians
	h := math.Pow(math.Sin((q-p)/2), 2) + math.Cos(p)*math.Cos(q)*math.Pow(math.Sin((lon2-lon1)*radians/2), 2)
	return 3440.065 * 2 * math.Asin(math.Sqrt(min(1, max(0, h))))
}

func BearingDegrees(lat1, lon1, lat2, lon2 float64) float64 {
	const radians = math.Pi / 180
	p, q, d := lat1*radians, lat2*radians, (lon2-lon1)*radians
	y := math.Sin(d) * math.Cos(q)
	x := math.Cos(p)*math.Sin(q) - math.Sin(p)*math.Cos(q)*math.Cos(d)
	return math.Mod(math.Atan2(y, x)/radians+360, 360)
}
