// Package nearby polls an area without treating multiple aircraft as ambiguous.
package nearby

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/geo"
	"aerolatch/internal/provider"
)

type Source interface {
	LookupNearby(context.Context, float64, float64, int) ([]flight.Aircraft, error)
	Name() string
}

type Options struct {
	Location flight.Location
	RadiusNM int
	Interval time.Duration
	Once     bool
}

// Fresh excludes fixes that cannot establish a current horizontal distance.
func Fresh(a flight.Aircraft, now time.Time) bool {
	if !geo.ValidCoordinates(a.Latitude, a.Longitude) || a.PositionUpdated == nil {
		return false
	}
	validTime := func(at time.Time) bool { return now.Sub(at) <= 90*time.Second && at.Sub(now) <= 5*time.Second }
	return validTime(*a.PositionUpdated) && (a.LastContact == nil || validTime(*a.LastContact))
}

func Rows(aircraft []flight.Aircraft, location flight.Location, radius int, now time.Time) []flight.NearbyAircraft {
	unique := make(map[string]flight.Aircraft)
	for _, a := range aircraft {
		target, err := flight.NewTarget(flight.ByHex, a.Hex)
		if err != nil || !Fresh(a, now) {
			continue
		}
		a.Hex = target.Value
		if previous, ok := unique[a.Hex]; ok && !a.PositionUpdated.After(*previous.PositionUpdated) {
			continue
		}
		unique[a.Hex] = a
	}
	rows := make([]flight.NearbyAircraft, 0, len(unique))
	for _, a := range unique {
		distance := geo.DistanceNM(location.Latitude, location.Longitude, *a.Latitude, *a.Longitude)
		if distance > float64(radius) {
			continue
		}
		bearing := geo.BearingDegrees(location.Latitude, location.Longitude, *a.Latitude, *a.Longitude)
		rows = append(rows, flight.NearbyAircraft{Aircraft: a, DistanceNM: distance, BearingDegrees: bearing, Direction: Compass(bearing)})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].DistanceNM == rows[j].DistanceNM {
			return rows[i].Hex < rows[j].Hex
		}
		return rows[i].DistanceNM < rows[j].DistanceNM
	})
	return rows
}

func Compass(bearing float64) string {
	return [...]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[int(math.Round(bearing/45))%8]
}

func Run(ctx context.Context, source Source, options Options, emit func(flight.NearbySnapshot) error) error {
	return run(ctx, source, options, emit, pause)
}

func run(ctx context.Context, source Source, options Options, emit func(flight.NearbySnapshot) error, wait func(context.Context, time.Duration) error) error {
	l := options.Location
	if !geo.ValidCoordinates(&l.Latitude, &l.Longitude) || options.RadiusNM < 1 || options.RadiusNM > 250 || options.Interval < 5*time.Second {
		return fmt.Errorf("nearby: invalid location, radius, or polling interval")
	}
	var previous []flight.NearbyAircraft
	var lastSeen *time.Time
	backoff := options.Interval
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		aircraft, err := source.LookupNearby(ctx, l.Latitude, l.Longitude, options.RadiusNM)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := time.Now().UTC()
		s := flight.NearbySnapshot{CheckedAt: now, Location: l, RadiusNM: options.RadiusNM, Source: source.Name(), Aircraft: []flight.NearbyAircraft{}}
		delay := options.Interval
		fatal := false
		if err != nil {
			s.Status, s.Error, s.LastObserved, s.LastSeenAt = "retrying", err.Error(), previous, lastSeen
			fatal = options.Once || !provider.Retryable(err)
			if fatal {
				s.Status = "error"
			} else {
				delay = max(backoff, provider.RetryAfter(err))
				backoff = min(backoff*2, max(5*time.Minute, options.Interval))
				retryAt := now.Add(delay)
				s.RetryAt = &retryAt
			}
		} else {
			backoff = options.Interval
			s.Aircraft = Rows(aircraft, l, options.RadiusNM, now)
			s.Excluded = len(aircraft) - len(s.Aircraft)
			s.Status = "visible"
			if len(s.Aircraft) == 0 {
				s.Status = "empty"
			}
			// A successful empty response clears earlier rows, unlike a request error.
			previous, lastSeen = s.Aircraft, &now
		}
		if outputErr := emit(s); outputErr != nil {
			return outputErr
		}
		if fatal {
			return err
		}
		if options.Once {
			return nil
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
