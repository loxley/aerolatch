package dashboard

import (
	"fmt"
	"math"
	"strings"
	"time"

	"aerolatch/internal/flight"
)

type trendPoint struct {
	at              time.Time
	altitude, speed *float64
	gap             bool
	speedCalculated bool
}

func (m *model) resetTrends() {
	m.points, m.hex, m.lastFix = nil, "", time.Time{}
	m.lastPhase = ""
}

func (m *model) recordTrends() {
	if len(m.snapshot.Aircraft) > 1 {
		m.resetTrends()
		return
	}
	if len(m.snapshot.Aircraft) != 1 || m.snapshot.Status != "visible" {
		m.addGap()
		return
	}
	a := m.snapshot.Aircraft[0]
	if a.Hex == "" {
		m.resetTrends()
		return
	}
	if !strings.EqualFold(a.Hex, m.hex) {
		m.resetTrends()
		m.hex = a.Hex
		m.addEvent("Aircraft acquired: " + aircraftID(a))
	}
	if a.PositionUpdated == nil || m.now.Sub(*a.PositionUpdated) > 90*time.Second || a.PositionUpdated.After(m.now.Add(5*time.Second)) {
		m.addGap()
		return
	}
	if !a.PositionUpdated.After(m.lastFix) {
		return
	}
	if !m.lastFix.IsZero() && a.PositionUpdated.Sub(m.lastFix) > 90*time.Second {
		m.addGap()
	}
	m.lastFix = *a.PositionUpdated
	m.points = append(m.points, trendPoint{at: m.lastFix, altitude: a.AltitudeFeet, speed: a.EffectiveGroundSpeed(), speedCalculated: a.GroundSpeed == nil && a.EffectiveGroundSpeed() != nil})
	if len(m.points) > 60 {
		m.points = m.points[len(m.points)-60:]
	}
}

func calculatedSpeedSeries(points []trendPoint, width int) bool {
	for _, p := range points[max(0, len(points)-width):] {
		if !p.gap && p.speedCalculated {
			return true
		}
	}
	return false
}

func (m *model) addGap() {
	if len(m.points) > 0 && !m.points[len(m.points)-1].gap {
		m.points = append(m.points, trendPoint{at: m.now, gap: true})
		if len(m.points) > 60 {
			m.points = m.points[len(m.points)-60:]
		}
	}
}

func aircraftID(a flight.Aircraft) string {
	if a.Registration != "" {
		return a.Registration + " · " + a.Hex
	}
	return a.Hex
}

// Points are observations, not uniformly spaced time buckets. A dot marks a
// missing measurement or a coverage gap; no interpolation or zero filling.
func sparkline(points []trendPoint, width int, altitude bool) string {
	if width < 1 {
		return ""
	}
	if len(points) == 0 {
		return "waiting for distinct fixes"
	}
	points = points[max(0, len(points)-width):]
	low, high, count := math.Inf(1), math.Inf(-1), 0
	value := func(p trendPoint) *float64 {
		if p.gap {
			return nil
		}
		if altitude {
			return p.altitude
		}
		return p.speed
	}
	for _, p := range points {
		if v := value(p); validNumber(v) {
			low, high, count = min(low, *v), max(high, *v), count+1
		}
	}
	if count == 0 {
		return "unavailable"
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, p := range points {
		v := value(p)
		if !validNumber(v) {
			b.WriteRune('·')
			continue
		}
		level := 3
		if high > low {
			level = int(math.Round((*v - low) / (high - low) * 7))
		}
		b.WriteRune(levels[level])
	}
	return b.String()
}

func validNumber(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

func trendRange(points []trendPoint) string {
	var first, last time.Time
	for _, p := range points {
		if !p.gap {
			if first.IsZero() {
				first = p.at
			}
			last = p.at
		}
	}
	if first.IsZero() {
		return "no history"
	}
	return fmt.Sprintf("%s–%sZ", first.UTC().Format("15:04:05"), last.UTC().Format("15:04:05"))
}
