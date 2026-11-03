package nearby

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aerolatch/internal/flight"
)

type inspectionFeed struct {
	frames <-chan flight.Aircraft
	until  time.Time
}

func (s inspectionFeed) Name() string             { return "fixture" }
func (s inspectionFeed) NextAllowedAt() time.Time { return s.until }
func (s inspectionFeed) Lookup(ctx context.Context, target flight.Target) ([]flight.Aircraft, error) {
	select {
	case a := <-s.frames:
		return []flight.Aircraft{a}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type inspectionResolver func(context.Context, string) (*flight.Route, error)

func (f inspectionResolver) Resolve(ctx context.Context, call string) (*flight.Route, error) {
	return f(ctx, call)
}

func waitInspection(t *testing.T, ctx context.Context, frames <-chan flight.Snapshot, match func(flight.Snapshot) bool) flight.Snapshot {
	t.Helper()
	for {
		select {
		case frame := <-frames:
			if match(frame) {
				return frame
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for inspection state")
		}
	}
}

func TestInspectionPreservesSeedAndStreamsWhileRouteIsSlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Now().UTC()
	seed := aircraft("abc001", 59, 18, now.Add(-20*time.Second))
	seed.Callsign = "SAS123"
	selection := flight.Selection{Aircraft: seed, ObservedAt: now.Add(-time.Second)}
	feed := make(chan flight.Aircraft, 1)
	live := aircraft("abc001", 59.01, 18, now) // No callsign in the new hex response.
	feed <- live
	release := make(chan struct{})
	var lookups atomic.Int32
	resolver := inspectionResolver(func(ctx context.Context, call string) (*flight.Route, error) {
		lookups.Add(1)
		select {
		case <-release:
			return &flight.Route{Callsign: call, Origin: flight.Airport{IATA: "ARN"}, Destination: flight.Airport{
				IATA: "LHR", Latitude: ptr(51.4706), Longitude: ptr(-0.46194), ElevationFeet: ptr(83.0)}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	frames := make(chan flight.Snapshot, 32)
	result := make(chan error, 1)
	go func() {
		result <- Inspect(ctx, inspectionFeed{frames: feed}, resolver, selection, time.Millisecond,
			func(s flight.Snapshot) error {
				select {
				case frames <- s:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
	}()
	initial := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Status == "connecting" })
	if len(initial.Aircraft) != 0 || len(initial.LastObserved) != 1 || !initial.LastObserved[0].PositionUpdated.Equal(*seed.PositionUpdated) {
		t.Fatal("seed was discarded or promoted into a new observation")
	}
	current := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Status == "visible" })
	if current.Inspection.RouteStatus != "resolving" || current.Aircraft[0].Callsign != "" || current.Callsign != "" || current.Inspection.LastKnownCallsign != "SAS123" || !current.Inspection.CallsignObservedAt.Equal(*seed.LastContact) {
		t.Fatalf("blank broadcast overwritten, last-known identity lost, or telemetry waited for route: %+v", current)
	}
	close(release)
	resolved := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Route != nil })
	if resolved.Route.Callsign != "SAS123" || resolved.Destination == nil || resolved.Destination.Airport.IATA != "LHR" || resolved.Destination.Source != "adsbdb" || resolved.LandingEstimate.DistanceNM == nil || lookups.Load() != 1 {
		t.Fatalf("resolved reference was not supplied to shared destination calculations: %+v", resolved)
	}
	if !resolved.CheckedAt.Equal(current.CheckedAt) {
		t.Fatal("reference lookup refreshed the feed timestamp")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestInspectionIgnoresLateRoutesAfterCallsignChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seed := aircraft("abc001", 59, 18, time.Now())
	seed.Callsign = "OLD123"
	feed := make(chan flight.Aircraft, 2)
	live := seed
	live.Callsign = "NEW123"
	feed <- live
	oldDone := make(chan struct{})
	defer close(oldDone)
	var newLookups atomic.Int32
	resolver := inspectionResolver(func(ctx context.Context, call string) (*flight.Route, error) {
		if call == "OLD123" {
			select {
			case <-oldDone:
			case <-ctx.Done():
			}
		} else {
			newLookups.Add(1)
		}
		return &flight.Route{Callsign: call, Destination: flight.Airport{IATA: call}}, nil
	})
	frames := make(chan flight.Snapshot, 64)
	result := make(chan error, 1)
	go func() {
		result <- Inspect(ctx, inspectionFeed{frames: feed}, resolver, flight.Selection{Aircraft: seed, ObservedAt: time.Now()}, time.Millisecond,
			func(s flight.Snapshot) error {
				if s.Route != nil && s.Route.Callsign != s.Inspection.LastKnownCallsign {
					return errors.New("stale callsign route leaked")
				}
				select {
				case frames <- s:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
	}()
	resolved := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Route != nil && s.Route.Callsign == "NEW123" })
	if resolved.Inspection.LastKnownCallsign != "NEW123" {
		t.Fatal("new callsign identity not retained")
	}
	live.AltitudeFeet = ptr(12345.0)
	feed <- live
	waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return len(s.Aircraft) == 1 && s.Aircraft[0].AltitudeFeet != nil })
	if newLookups.Load() != 1 {
		t.Fatal("cached route fetched again on next poll")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late lookup poisoned inspection: %v", err)
	}
}

func TestInspectionCooldownProgressAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	until := time.Now().Add(time.Hour)
	frames := make(chan flight.Snapshot, 8)
	result := make(chan error, 1)
	go func() {
		result <- Inspect(ctx, inspectionFeed{frames: make(chan flight.Aircraft), until: until}, nil,
			flight.Selection{Aircraft: aircraft("abc001", 59, 18, time.Now()), ObservedAt: time.Now()}, time.Second,
			func(s flight.Snapshot) error {
				select {
				case frames <- s:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
	}()
	waiting := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return s.Inspection.RequestState == "waiting_for_provider" })
	if waiting.Inspection.WaitUntil == nil || !waiting.Inspection.WaitUntil.Equal(until) {
		t.Fatal("provider cooldown not exposed")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for provider cooldown")
	}
}

func TestInspectionReferenceFailureIsCachedAndDoesNotStopTelemetry(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		seed := aircraft("abc001", 59, 18, time.Now())
		seed.Callsign = "SAS123"
		feed := make(chan flight.Aircraft, 2)
		feed <- seed
		updated := seed
		updated.AltitudeFeet = ptr(9999.0)
		var calls atomic.Int32
		resolver := inspectionResolver(func(context.Context, string) (*flight.Route, error) {
			calls.Add(1)
			if mismatch {
				return &flight.Route{Callsign: "WRONG123"}, nil
			}
			return nil, errors.New("reference unavailable")
		})
		frames := make(chan flight.Snapshot, 64)
		result := make(chan error, 1)
		go func() {
			result <- Inspect(ctx, inspectionFeed{frames: feed}, resolver, flight.Selection{Aircraft: seed}, time.Millisecond,
				func(s flight.Snapshot) error {
					select {
					case frames <- s:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
		}()
		waitInspection(t, ctx, frames, func(s flight.Snapshot) bool {
			return s.Status == "visible" && s.Inspection.RouteStatus == "unavailable"
		})
		feed <- updated
		s := waitInspection(t, ctx, frames, func(s flight.Snapshot) bool { return len(s.Aircraft) == 1 && s.Aircraft[0].AltitudeFeet != nil })
		if s.Route != nil || s.Inspection.RouteError == "" || calls.Load() != 1 {
			t.Errorf("failure poisoned data or retried each poll: %+v calls=%d", s, calls.Load())
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected result: %v", err)
		}
	}
}
