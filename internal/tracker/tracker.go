// Package tracker manages polling independently of the API and output format.
package tracker

import (
	"context"
	"strings"
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
	// Reference returns the latest immutable asynchronously resolved route.
	// It must be non-blocking and safe to call concurrently with its publisher.
	Reference        func() *flight.Route
	ReferenceChanged <-chan struct{} // Optional notifications; never cause a live-data request.
	// When set, Schedule.Update must be a non-blocking cached snapshot. Airport
	// notifications can then update the dashboard independently of ADS-B polls.
	ScheduleChanged <-chan struct{}
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
	reference, referenceSource := options.Route, options.RouteSource
	effectiveRoute, destination, airport := routeInputs(options, reference)
	observedCallsign := ""
	var pendingReference []referenceObservation
	var activeSchedule flight.ScheduleUpdate
	refreshReference := func() bool {
		if options.Reference == nil && options.ScheduleChanged == nil {
			return false
		}
		next := reference
		if options.Reference != nil {
			next = options.Reference()
		}
		// Reject a superseded reference immediately, before the asynchronous
		// resolver has processed a newly broadcast callsign on this same hex.
		if next != nil && observedCallsign != "" && !strings.EqualFold(strings.TrimSpace(next.Callsign), observedCallsign) {
			next = nil
		}
		wasUsable := usableAirport(airport)
		nextRoute, nextDestination, nextAirport := routeInputs(options, next)
		if options.ScheduleChanged != nil && validSchedule(activeSchedule) {
			nextRoute, nextDestination, nextAirport = routeInputs(options, activeSchedule.Route)
			if options.Destination == nil {
				nextDestination.Source, nextDestination.DataSource = "swedavia", "Swedavia / airport metadata"
			}
		}
		changed := !sameAirport(airport, nextAirport)
		if options.Destination == nil && routeCallsign(reference) != routeCallsign(next) {
			changed = true
		}
		reference, effectiveRoute, destination, airport = next, nextRoute, nextDestination, nextAirport
		if options.Reference != nil {
			referenceSource = ""
			if reference != nil {
				referenceSource = "adsbdb"
			}
		}
		if changed {
			estimator, detector = eta.Estimator{}, arrival.Detector{}
			identity.resetDestinationEvidence()
			if !wasUsable && usableAirport(airport) && len(pendingReference) > 0 {
				// Resolution can finish after several real reports. Replay those
				// reports at their original times through the SAME estimators so
				// a slow route lookup does not lose an approach/landing sequence.
				// Never replay one destination's evidence into another destination.
				replayed := identity
				replayed.lastFix = time.Time{}
				cutoff := clock().UTC().Add(-10 * time.Minute)
				for _, observation := range pendingReference {
					if observation.at.Before(cutoff) {
						continue
					}
					if observation.failed {
						detector.Unavailable("live lookup failed")
						continue
					}
					replayed.observe(observation.at, airport, observation.aircraft)
					estimator.Update(observation.at, effectiveRoute, observation.aircraft)
					detector.Update(observation.at, airport, observation.aircraft)
				}
				if identity.lastFix.After(replayed.lastFix) {
					replayed.lastFix = identity.lastFix
				}
				identity = replayed
			}
			if wasUsable || usableAirport(airport) {
				pendingReference = nil
			}
		}
		return changed
	}
	refreshReference()
	// Both ordinary live polls and asynchronous destination updates use these
	// exact same estimator/detector instances and inference rules.
	assess := func(snapshot *flight.Snapshot, now time.Time, update flight.ScheduleUpdate) {
		if reason := scheduleInferenceReason(update); identity.departed || reason != "" {
			if identity.departed {
				reason = subsequentDepartureReason
			}
			snapshot.LandingEstimate = eta.Unavailable(reason)
			snapshot.Arrival = &flight.ArrivalAssessment{Status: "unknown", AircraftHex: identity.hex, Reason: reason}
			return
		}
		snapshot.LandingEstimate = estimator.Update(now, effectiveRoute, snapshot.Aircraft)
		snapshot.Arrival = detector.Update(now, airport, snapshot.Aircraft)
		applyAirportStatus(snapshot, update)
	}
	referenceChanged := options.ReferenceChanged
	scheduleChanged := options.ScheduleChanged
	var latest *flight.Snapshot
	var latestSchedule flight.ScheduleUpdate
	setContext := func(snapshot *flight.Snapshot, update flight.ScheduleUpdate) {
		snapshot.Route, snapshot.RouteSource, snapshot.Destination = reference, referenceSource, destination
		snapshot.AirportReport = update.Report
		if options.ScheduleChanged != nil && validSchedule(update) {
			snapshot.Route, snapshot.RouteSource = update.Route, "Swedavia"
		} else if options.RouteSource == "Swedavia" && effectiveRoute != nil {
			snapshot.Route = effectiveRoute
		}
	}
	notifyReference := func() error {
		if options.ScheduleChanged != nil && options.Schedule != nil {
			activeSchedule = flight.ScheduleUpdate{}
			var observed *flight.Aircraft
			if len(previous) == 1 {
				observed = &previous[0]
			}
			if !identity.departed {
				activeSchedule = options.Schedule.Update(ctx, clock().UTC(), observed)
			}
			latestSchedule = activeSchedule
		}
		changed := refreshReference()
		if latest == nil {
			return nil
		}
		setContext(latest, latestSchedule)
		if latest.Status == "visible" || identity.departed || scheduleInferenceReason(latestSchedule) != "" {
			// Use the actual last fix, not a manufactured observation. Cached
			// timestamps cannot extend estimator or landing evidence histories.
			assess(latest, clock().UTC(), latestSchedule)
		} else if changed {
			latest.LandingEstimate = eta.Unavailable("destination reference changed; awaiting a current aircraft observation")
			latest.Arrival = detector.Unavailable("destination reference changed; awaiting a current aircraft observation")
		}
		applyAirportStatus(latest, latestSchedule)
		tracking := *latest.Tracking
		tracking.LiveLookupPerformed = false
		latest.Tracking = &tracking
		return emit(*latest)
	}
	if referenceChanged != nil || scheduleChanged != nil {
		// A background airport report can arrive while the first ADS-B request
		// is still pending. Publish it without inventing a live observation.
		latest = &flight.Snapshot{CheckedAt: clock().UTC(), Query: options.Query, Source: source.Name(), Status: "connecting", Aircraft: []flight.Aircraft{},
			Tracking: identity.info(identity.lookup()), LandingEstimate: eta.Unavailable("awaiting a current aircraft observation"), Arrival: detector.Unavailable("awaiting a current aircraft observation")}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var update flight.ScheduleUpdate
		if options.ScheduleChanged != nil && identity.departed {
			activeSchedule = flight.ScheduleUpdate{}
		}
		waiting := false
		if options.Schedule != nil && !identity.departed {
			var observed *flight.Aircraft
			if len(previous) == 1 && identity.hex != "" {
				observed = &previous[0]
			}
			update = options.Schedule.Update(ctx, clock().UTC(), observed)
			if options.ScheduleChanged != nil {
				activeSchedule = update
				refreshReference()
			} else if validSchedule(update) {
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
			if referenceChanged == nil && scheduleChanged == nil {
				aircraft, err = source.Lookup(ctx, lookup)
			} else {
				// Keep destination updates responsive even if the next live-data
				// request is slow. Still own and join exactly one live request.
				requestCtx, stop := context.WithCancel(ctx)
				type response struct {
					aircraft []flight.Aircraft
					err      error
				}
				result := make(chan response, 1)
				go func() { a, err := source.Lookup(requestCtx, lookup); result <- response{a, err} }()
			fetch:
				for {
					select {
					case reply := <-result:
						aircraft, err = reply.aircraft, reply.err
						break fetch
					case <-ctx.Done():
						stop()
						<-result
						return ctx.Err()
					case _, ok := <-referenceChanged:
						if !ok {
							referenceChanged = nil
							continue
						}
						if err := notifyReference(); err != nil {
							stop()
							<-result
							return err
						}
					case _, ok := <-scheduleChanged:
						if !ok {
							scheduleChanged = nil
							continue
						}
						if err := notifyReference(); err != nil {
							stop()
							<-result
							return err
						}
					}
				}
				stop()
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := clock().UTC()
		if options.ScheduleChanged != nil && options.Schedule != nil && !identity.departed {
			var observed *flight.Aircraft
			if len(aircraft) == 1 {
				observed = &aircraft[0]
			} else if len(previous) == 1 {
				observed = &previous[0]
			}
			update = options.Schedule.Update(ctx, now, observed)
			activeSchedule = update
		}
		if err == nil && !waiting {
			var mismatch bool
			aircraft, mismatch = checkAssignment(&update, aircraft, identity.hex != "")
			waiting = mismatch
		}
		if err == nil && !waiting {
			if len(aircraft) == 1 && strings.TrimSpace(aircraft[0].Callsign) != "" {
				call := strings.ToUpper(strings.TrimSpace(aircraft[0].Callsign))
				if observedCallsign != "" && call != observedCallsign {
					pendingReference = nil
				}
				observedCallsign = call
			}
			refreshReference()
			aircraft = measurements.Update(now, aircraft)
			identity.observe(now, airport, aircraft)
		} else {
			measurements.Break()
			if options.ScheduleChanged != nil {
				refreshReference()
			}
		}
		if options.Reference != nil && !waiting && !usableAirport(airport) {
			pendingReference = append(pendingReference, referenceObservation{at: now, aircraft: aircraft, failed: err != nil})
			for len(pendingReference) > 0 && (len(pendingReference) > 128 || pendingReference[0].at.Before(now.Add(-10*time.Minute))) {
				pendingReference = pendingReference[1:]
			}
		}
		snapshot := flight.Snapshot{
			CheckedAt: now, Query: options.Query, Callsign: currentCallsign,
			Route: reference, Source: source.Name(), Aircraft: []flight.Aircraft{},
			LandingEstimate: eta.Unavailable("live aircraft data unavailable"),
			Destination:     destination,
			Tracking:        identity.info(lookup),
			AirportReport:   update.Report, RouteSource: referenceSource,
		}
		if snapshot.Callsign == "" && len(aircraft) == 1 {
			snapshot.Callsign = aircraft[0].Callsign
		}
		snapshot.Tracking.LiveLookupPerformed = liveLookupPerformed
		if waiting {
			snapshot.Tracking.Reason = update.Reason
		}
		setContext(&snapshot, update)
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
			if aircraft != nil {
				snapshot.Aircraft = aircraft
			}
			assess(&snapshot, now, update)
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
		applyAirportStatus(&snapshot, update)
		latest, latestSchedule = &snapshot, update
		if err := emit(snapshot); err != nil {
			return err
		}
		if options.Once {
			return nil
		}
		timer := time.NewTimer(delay)
	waitForPoll:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				break waitForPoll
			case _, ok := <-referenceChanged:
				if !ok {
					referenceChanged = nil
					continue
				}
				if err := notifyReference(); err != nil {
					timer.Stop()
					return err
				}
			case _, ok := <-scheduleChanged:
				if !ok {
					scheduleChanged = nil
					continue
				}
				if err := notifyReference(); err != nil {
					timer.Stop()
					return err
				}
			}
		}
	}
}
