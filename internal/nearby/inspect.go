package nearby

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"aerolatch/internal/flight"
	"aerolatch/internal/schedule"
	"aerolatch/internal/tracker"
)

type RouteResolver interface {
	Resolve(context.Context, string) (*flight.Route, error)
}

type InspectionOptions struct {
	Airports       schedule.BoardSource
	ResolveAirport func(context.Context, string) (*flight.Airport, error)
	SwedaviaStatus string
}

// InitialInspection retains the selected fix with its original timestamps.
// It is deliberately not presented as a new live observation.
func InitialInspection(selection flight.Selection, source string) flight.Snapshot {
	a := selection.Aircraft
	seen := selection.ObservedAt
	if seen.IsZero() && a.PositionUpdated != nil {
		seen = *a.PositionUpdated
	}
	info := &flight.InspectionInfo{UpdatedAt: time.Now().UTC(), LastKnownCallsign: a.Callsign, RouteStatus: "unavailable"}
	if a.Callsign != "" {
		knownAt := seen
		if a.LastContact != nil {
			knownAt = *a.LastContact
		}
		info.CallsignObservedAt = &knownAt
		info.RouteStatus = "resolving"
	}
	return flight.Snapshot{Query: a.Hex, Source: source, Status: "connecting", CheckedAt: info.UpdatedAt,
		Aircraft: []flight.Aircraft{}, LastObserved: []flight.Aircraft{a}, LastSeenAt: &seen, Inspection: info,
		Tracking: &flight.TrackingInfo{Lookup: flight.Target{Kind: flight.ByHex, Value: a.Hex}, LockedHex: a.Hex, FlightState: "tracking"}}
}

type routeReply struct {
	token    uint64
	callsign string
	route    *flight.Route
	err      error
}

type routeCache struct {
	route   *flight.Route
	err     error
	expires time.Time
}

type requestProgress struct {
	started time.Time
	until   time.Time
}

type inspectionFinished struct{ err error }

type inspectionSource struct {
	source tracker.Lookup
	send   func(any) error
}

func (s inspectionSource) Name() string { return s.source.Name() }

func (s inspectionSource) Lookup(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
	p := requestProgress{started: time.Now().UTC()}
	if source, ok := s.source.(interface{ NextAllowedAt() time.Time }); ok {
		p.until = source.NextAllowedAt()
	}
	if err := s.send(p); err != nil {
		return nil, err
	}
	return s.source.Lookup(ctx, target)
}

// Inspect streams telemetry and optional callsign-reference enrichment
// independently. Route lookups never gate the first live request or keyboard
// handling. The shared tracker consumes resolved reference destinations and
// owns all ETA/arrival calculations, just as it does for normal flight tracking.
func Inspect(ctx context.Context, source tracker.Lookup, resolver RouteResolver, selection flight.Selection, interval time.Duration, emit func(flight.Snapshot) error, settings ...InspectionOptions) error {
	target, err := flight.NewTarget(flight.ByHex, selection.Aircraft.Hex)
	if err != nil {
		return err
	}
	selection.Aircraft.Hex = target.Value
	workCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	events := make(chan any, 8)
	send := func(event any) error {
		select {
		case events <- event:
			return nil
		case <-workCtx.Done():
			return workCtx.Err()
		}
	}
	current := InitialInspection(selection, source.Name())
	info := *current.Inspection
	var config InspectionOptions
	if len(settings) > 0 {
		config = settings[0]
	}
	info.SwedaviaStatus = config.SwedaviaStatus
	last := selection.Aircraft
	seen := *current.LastSeenAt
	var reference *flight.Route
	var referenceMu sync.RWMutex
	referenceChanged := make(chan struct{}, 1)
	notifyContext := func() {
		select {
		case referenceChanged <- struct{}{}:
		default:
		}
	}
	var airports *schedule.AsyncWatcher
	airportDisabled := false
	if config.Airports != nil {
		airports = schedule.NewAsyncWatcher(config.Airports, target, config.ResolveAirport, notifyContext)
		airports.Update(workCtx, time.Now().UTC(), &selection.Aircraft)
		workers.Add(1)
		go func() { defer workers.Done(); airports.Run(workCtx) }()
	}
	getReference := func() *flight.Route {
		referenceMu.RLock()
		defer referenceMu.RUnlock()
		return reference
	}
	setReference := func(next *flight.Route) {
		referenceMu.Lock()
		changed := reference != next
		reference = next
		referenceMu.Unlock()
		if airports != nil && !airportDisabled {
			airports.SetReference(next)
		}
		if changed {
			notifyContext()
		}
	}
	publish := func() error {
		if airports != nil {
			switch {
			case airportDisabled:
				info.SwedaviaStatus = "next_leg"
			case getReference() == nil:
				info.SwedaviaStatus = "waiting_for_route"
			case !(&schedule.Watcher{Reference: getReference()}).HasContext(time.Now()):
				info.SwedaviaStatus = "not_covered"
			case current.AirportReport != nil:
				info.SwedaviaStatus = current.AirportReport.Match
			default:
				info.SwedaviaStatus = "checking"
			}
		}
		copy := info
		copy.UpdatedAt = time.Now().UTC()
		current.Inspection = &copy
		if len(current.Aircraft) == 0 && len(current.LastObserved) == 0 {
			retainedAt := seen
			current.LastObserved, current.LastSeenAt = []flight.Aircraft{last}, &retainedAt
		}
		return emit(current)
	}
	cache := make(map[string]routeCache)
	var token uint64
	var routeCancel context.CancelFunc
	requestRoute := func(callsign string) {
		callsign = strings.ToUpper(strings.TrimSpace(callsign))
		if callsign == info.RouteCallsign && info.RouteStatus == "resolving" {
			return
		}
		if routeCancel != nil {
			routeCancel()
		}
		token++
		info.RouteCallsign, info.RouteError = callsign, ""
		if cached, ok := cache[callsign]; ok && time.Now().Before(cached.expires) {
			setReference(cached.route)
			info.RouteStatus = "available"
			if cached.err != nil {
				info.RouteStatus, info.RouteError = "unavailable", cached.err.Error()
			}
			return
		}
		// Revalidating the same callsign must not transiently discard a usable
		// destination and erase its ETA/landing history. A different callsign
		// still invalidates the previous flight context immediately.
		if existing := getReference(); existing == nil || !strings.EqualFold(existing.Callsign, callsign) {
			setReference(nil)
		}
		if _, err := flight.NewTarget(flight.ByCallsign, callsign); err != nil || resolver == nil {
			info.RouteStatus = "unavailable"
			return
		}
		info.RouteStatus = "resolving"
		routeCtx, stop := context.WithCancel(workCtx)
		routeCancel = stop
		requestToken := token
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer stop()
			route, err := resolver.Resolve(routeCtx, callsign)
			if err == nil && (route == nil || !strings.EqualFold(route.Callsign, callsign)) {
				err = fmt.Errorf("adsbdb: reference callsign does not match %s", callsign)
			}
			_ = send(routeReply{token: requestToken, callsign: callsign, route: route, err: err})
		}()
	}
	if info.LastKnownCallsign != "" {
		// Initial RouteStatus is a UI hint, not an already-running lookup.
		info.RouteStatus = "unavailable"
		requestRoute(info.LastKnownCallsign)
	}
	if err := publish(); err != nil {
		return err
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		options := tracker.Options{Query: target.Value, Target: target, Interval: interval, Reference: getReference, ReferenceChanged: referenceChanged}
		if airports != nil {
			options.Schedule, options.ScheduleChanged = airports, referenceChanged
		}
		err := tracker.Run(workCtx, inspectionSource{source: source, send: send}, options,
			func(s flight.Snapshot) error { return send(s) })
		_ = send(inspectionFinished{err: err})
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-events:
			switch e := event.(type) {
			case inspectionFinished:
				return e.err
			case flight.Snapshot:
				current = e
				if airports != nil && e.Tracking != nil && e.Tracking.FlightState == "subsequent_departure" && !airportDisabled {
					airportDisabled = true
					airports.SetReference(nil)
				}
				liveLookup := e.Tracking != nil && e.Tracking.LiveLookupPerformed
				if liveLookup {
					info.RequestState, info.RequestStartedAt, info.WaitUntil = "", nil, nil
				}
				if liveLookup && len(e.Aircraft) == 1 && e.Aircraft[0].Hex == target.Value {
					last, seen = e.Aircraft[0], e.CheckedAt
					if callsign := last.Callsign; callsign != "" {
						knownAt := e.CheckedAt
						if last.LastContact != nil {
							knownAt = *last.LastContact
						}
						info.LastKnownCallsign, info.CallsignObservedAt = callsign, &knownAt
						requestRoute(callsign)
					}
				}
			case requestProgress:
				info.RequestState, info.RequestStartedAt, info.WaitUntil = "fetching", &e.started, nil
				if e.until.After(e.started) {
					info.RequestState, info.WaitUntil = "waiting_for_provider", &e.until
				}
			case routeReply:
				if e.token != token {
					continue // A cancelled, superseded callsign lookup completed late.
				}
				entry := routeCache{route: e.route, err: e.err, expires: time.Now().Add(30 * time.Minute)}
				info.RouteStatus = "available"
				if e.err != nil {
					entry.route, entry.expires = nil, time.Now().Add(5*time.Minute)
					if existing := getReference(); existing != nil && strings.EqualFold(existing.Callsign, e.callsign) {
						entry.route = existing
					}
					info.RouteStatus, info.RouteError = "unavailable", e.err.Error()
				}
				if len(cache) >= 32 {
					clear(cache)
				}
				cache[e.callsign] = entry
				setReference(entry.route)
			}
			if err := publish(); err != nil {
				return err
			}
		}
	}
}
