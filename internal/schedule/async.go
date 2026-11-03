package schedule

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"time"

	"aerolatch/internal/flight"
)

// AsyncWatcher runs the existing dated-flight watcher independently of ADS-B
// requests. Update is a non-blocking, identity-validated snapshot for the tracker.
// A single Run goroutine owns Watcher and its network I/O.
type AsyncWatcher struct {
	Client         BoardSource
	Target         flight.Target
	ResolveAirport func(context.Context, string) (*flight.Airport, error)
	Now            func() time.Time
	Notify         func()
	mu             sync.RWMutex
	reference      *flight.Route
	aircraft       *flight.Aircraft
	identity       string
	binding        string
	revision       uint64
	last           flight.ScheduleUpdate
	cancelRequest  context.CancelFunc
	wake           chan struct{}
}

func NewAsyncWatcher(client BoardSource, target flight.Target, resolve func(context.Context, string) (*flight.Airport, error), notify func()) *AsyncWatcher {
	return &AsyncWatcher{Client: client, Target: target, ResolveAirport: resolve, Now: time.Now, Notify: notify, wake: make(chan struct{}, 1)}
}

func (a *AsyncWatcher) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// SetReference changes discovery context without ever doing network I/O. A
// cancelled/superseded airport request cannot publish into the next flight leg.
func (a *AsyncWatcher) SetReference(route *flight.Route) {
	key := ""
	if route != nil {
		probe := Watcher{Reference: route}
		if probe.HasContext(a.Now()) {
			key = strings.ToUpper(strings.TrimSpace(route.Callsign)) + "/" + route.FlightNumber + "/" + route.Origin.IATA + "/" + route.Origin.ICAO + "/" + route.Destination.IATA + "/" + route.Destination.ICAO
		}
	}
	a.mu.Lock()
	a.reference = route
	changed := key != a.binding
	if changed {
		a.binding = key
		a.revision++
		a.last = flight.ScheduleUpdate{}
		if a.cancelRequest != nil {
			a.cancelRequest()
		}
	}
	a.mu.Unlock()
	if changed {
		a.signal()
		if a.Notify != nil {
			a.Notify()
		}
	}
}

func (a *AsyncWatcher) Update(ctx context.Context, now time.Time, aircraft *flight.Aircraft) flight.ScheduleUpdate {
	a.mu.Lock()
	changed := false
	if aircraft != nil {
		key := aircraft.Hex + "/" + aircraft.Registration + "/" + aircraft.Callsign
		if key != a.identity {
			copy := *aircraft
			a.aircraft, a.identity, changed = &copy, key, true
		}
	}
	update := a.last
	due := update.Report != nil && !now.Before(update.Report.NextCheckAt) && a.cancelRequest == nil
	a.mu.Unlock()
	if changed || due {
		a.signal()
	}
	// Use precisely the existing physical-aircraft assignment checks against
	// this caller's latest observation, including the first changed callsign.
	validator := Watcher{Target: a.Target}
	return validator.evaluate(update, now, aircraft)
}

func (a *AsyncWatcher) Run(ctx context.Context) {
	var watcher *Watcher
	var revision uint64
	for {
		if ctx.Err() != nil {
			return
		}
		a.mu.RLock()
		ref, aircraft, binding, rev := a.reference, a.aircraft, a.binding, a.revision
		a.mu.RUnlock()
		if binding == "" {
			select {
			case <-ctx.Done():
				return
			case <-a.wake:
				continue
			}
		}
		if watcher == nil || revision != rev {
			query := ref.FlightNumber
			if query == "" {
				query = ref.Callsign
			}
			watcher = &Watcher{Client: a.Client, Target: a.Target, Query: query, Reference: ref, ResolveAirport: a.ResolveAirport}
			revision = rev
		}
		watcher.Reference = ref
		requestCtx, stop := context.WithCancel(ctx)
		a.mu.Lock()
		if a.revision != rev {
			a.mu.Unlock()
			stop()
			continue
		}
		a.cancelRequest = stop
		a.mu.Unlock()
		update := watcher.Update(requestCtx, a.Now().UTC(), aircraft)
		cancelled := requestCtx.Err() != nil
		stop()
		a.mu.Lock()
		current := a.revision == rev && !cancelled && ctx.Err() == nil
		changed := current && !reflect.DeepEqual(a.last, update)
		if current {
			a.last = update
		}
		a.cancelRequest = nil // Run owns the sole request, even after rebinding.
		a.mu.Unlock()
		if changed && a.Notify != nil {
			a.Notify()
		}
		if !current {
			continue
		}
		delay := 5 * time.Minute
		if update.Report != nil {
			delay = max(time.Second, update.Report.NextCheckAt.Sub(a.Now()))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
