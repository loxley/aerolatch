// Package tracker manages polling independently of the API and output format.
package tracker

import (
	"context"
	"time"

	"aerolatch/internal/arrival"
	"aerolatch/internal/eta"
	"aerolatch/internal/flight"
	"aerolatch/internal/kinematics"
	"aerolatch/internal/provider"
)

type Lookup interface {
	Lookup(context.Context, flight.Target) ([]flight.Aircraft, error)
	Name() string
}

type Options struct {
	Query       string
	Callsign    string
	Target      flight.Target
	Route       *flight.Route
	RouteSource string
	Destination *flight.Airport // Optional user override; the reference route is retained.
	Interval    time.Duration
	Once        bool
	Schedule    ScheduleLookup
	Dated       bool
}

func Run(ctx context.Context, source Lookup, options Options, emit func(flight.Snapshot) error) error {
	return run(ctx, source, options, emit, time.Now)
}

func run(ctx context.Context, source Lookup, options Options, emit func(flight.Snapshot) error, clock func() time.Time) error {
	var previous []flight.Aircraft
	var lastSeen *time.Time
	backoff := options.Interval
	var estimator eta.Estimator
	var detector arrival.Detector
	var measurements kinematics.Processor
	identity := newIdentity(options)
	currentCallsign := options.Callsign
	var destination *flight.Destination
	effectiveRoute := options.Route
	if options.Destination != nil {
		destination = &flight.Destination{Airport: *options.Destination, Source: "user_override", DataSource: "OurAirports"}
		route := flight.Route{Callsign: options.Callsign}
		if options.Route != nil {
			route = *options.Route
		}
		route.Destination = *options.Destination
		effectiveRoute = &route
	} else if options.Route != nil {
		destination = &flight.Destination{Airport: options.Route.Destination, Source: "adsbdb", DataSource: "adsbdb"}
	}
	var airport *flight.Airport
	if destination != nil {
		airport = &destination.Airport
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var update flight.ScheduleUpdate
		waiting := false
		if options.Schedule != nil && !identity.departed {
			var observed *flight.Aircraft
			if len(previous) == 1 && identity.hex != "" {
				observed = &previous[0]
			}
			update = options.Schedule.Update(ctx, clock().UTC(), observed)
			if validSchedule(update) {
				effectiveRoute = update.Route
				if options.Destination != nil {
					route := *effectiveRoute
					route.Destination = *options.Destination
					effectiveRoute = &route
				} else {
					destination = &flight.Destination{Airport: effectiveRoute.Destination, Source: "swedavia", DataSource: "Swedavia / airport metadata"}
					airport = &destination.Airport
				}
				if identity.hex == "" && update.Target != nil && options.Target.Kind != flight.ByRegistration && options.Target.Kind != flight.ByHex {
					identity.target = *update.Target
					currentCallsign = update.Target.Value
				}
			}
			physical := options.Target.Kind == flight.ByHex || options.Target.Kind == flight.ByRegistration
			waiting = update.Waiting && identity.lockedAt == nil && (!physical || options.Dated)
		}
		lookup := identity.lookup()
		liveLookupPerformed := !waiting
		var aircraft []flight.Aircraft
		var err error
		if !waiting {
			aircraft, err = source.Lookup(ctx, lookup)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := clock().UTC()
		if err == nil && !waiting {
			var mismatch bool
			aircraft, mismatch = checkAssignment(&update, aircraft, identity.hex != "")
			waiting = mismatch
		}
		if err == nil && !waiting {
			aircraft = measurements.Update(now, aircraft)
			identity.observe(now, airport, aircraft)
		} else {
			measurements.Break()
		}
		snapshot := flight.Snapshot{
			CheckedAt: now, Query: options.Query, Callsign: currentCallsign,
			Route: options.Route, Source: source.Name(), Aircraft: []flight.Aircraft{},
			LandingEstimate: eta.Unavailable("live aircraft data unavailable"),
			Destination:     destination,
			Tracking:        identity.info(lookup),
			AirportReport:   update.Report, RouteSource: options.RouteSource,
		}
		if snapshot.Callsign == "" && len(aircraft) == 1 {
			snapshot.Callsign = aircraft[0].Callsign
		}
		snapshot.Tracking.LiveLookupPerformed = liveLookupPerformed
		if waiting {
			snapshot.Tracking.Reason = update.Reason
		}
		if options.RouteSource == "Swedavia" && effectiveRoute != nil {
			snapshot.Route = effectiveRoute
		}
		delay := options.Interval
		if waiting {
			snapshot.Status = waitingStatus(update)
			snapshot.LandingEstimate = eta.Unavailable(update.Reason)
			snapshot.Arrival = detector.Unavailable(update.Reason)
			snapshot.LastObserved, snapshot.LastSeenAt = previous, lastSeen
		} else if err != nil {
			if reason := scheduleInferenceReason(update); identity.departed || reason != "" {
				if identity.departed {
					reason = subsequentDepartureReason
				}
				snapshot.Arrival = &flight.ArrivalAssessment{Status: "unknown", AircraftHex: identity.hex, Reason: reason}
			} else {
				snapshot.Arrival = detector.Unavailable("live lookup failed")
			}
			snapshot.Status = "retrying"
			snapshot.Error = err.Error()
			snapshot.LastObserved, snapshot.LastSeenAt = previous, lastSeen
			if options.Once || !provider.Retryable(err) {
				snapshot.Status = "error"
				if outputErr := emit(snapshot); outputErr != nil {
					return outputErr
				}
				return err
			}
			delay = max(backoff, provider.RetryAfter(err))
			backoff = min(backoff*2, max(5*time.Minute, options.Interval))
			retryAt := now.Add(delay)
			snapshot.RetryAt = &retryAt
		} else {
			backoff = options.Interval
			if reason := scheduleInferenceReason(update); identity.departed || reason != "" {
				if identity.departed {
					reason = subsequentDepartureReason
				}
				snapshot.LandingEstimate = eta.Unavailable(reason)
				snapshot.Arrival = &flight.ArrivalAssessment{Status: "unknown", AircraftHex: identity.hex, Reason: reason}
			} else {
				snapshot.LandingEstimate = estimator.Update(now, effectiveRoute, aircraft)
				snapshot.Arrival = detector.Update(now, airport, aircraft)
				if r := update.Report; r != nil && (r.Match == "matched" || r.Stale) && r.Flight != nil && (r.Flight.StatusCode == "LAN" || r.Flight.StatusCode == "CAN" || r.Flight.StatusCode == "DEL") {
					status := map[string]string{"LAN": "landed", "CAN": "cancelled", "DEL": "removed"}[r.Flight.StatusCode]
					snapshot.LandingEstimate = eta.Unavailable("airport reports the selected flight " + status + "; see the sourced airport status")
				}
			}
			if aircraft != nil {
				snapshot.Aircraft = aircraft
			}
			switch len(aircraft) {
			case 0:
				snapshot.Status = "not_visible"
				snapshot.LastObserved, snapshot.LastSeenAt = previous, lastSeen
			case 1:
				snapshot.Status = "visible"
				previous, lastSeen = aircraft, &now
			default:
				snapshot.Status = "multiple_matches"
				previous, lastSeen = aircraft, &now
			}
		}
		if err := emit(snapshot); err != nil {
			return err
		}
		if options.Once {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
