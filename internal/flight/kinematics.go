package flight

import "time"

// CalculatedMeasurement is an average/trend over historical observations, not
// an instantaneous transponder measurement. WindowEnd anchors its data age.
type CalculatedMeasurement struct {
	Value       float64   `json:"value"`
	Unit        string    `json:"unit"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Samples     int       `json:"samples"`
	Method      string    `json:"method"`
}

type CalculatedKinematics struct {
	GroundSpeed  *CalculatedMeasurement `json:"ground_speed,omitempty"`
	GroundTrack  *CalculatedMeasurement `json:"ground_track,omitempty"`
	VerticalRate *CalculatedMeasurement `json:"vertical_rate,omitempty"`
	Unavailable  map[string]string      `json:"unavailable,omitempty"`
}

func (a Aircraft) EffectiveGroundSpeed() *float64 {
	if a.GroundSpeed != nil {
		return a.GroundSpeed
	}
	if a.Calculated != nil && a.Calculated.GroundSpeed != nil {
		return &a.Calculated.GroundSpeed.Value
	}
	return nil
}

func (a Aircraft) EffectiveTrack() *float64 {
	if a.Track != nil {
		return a.Track
	}
	if a.Calculated != nil && a.Calculated.GroundTrack != nil {
		return &a.Calculated.GroundTrack.Value
	}
	return nil
}

func (a Aircraft) EffectiveVerticalRate() *float64 {
	if a.VerticalRate != nil {
		return a.VerticalRate
	}
	if a.Calculated != nil && a.Calculated.VerticalRate != nil {
		return &a.Calculated.VerticalRate.Value
	}
	return nil
}

func (a Aircraft) CalculatedInputs() []string {
	var inputs []string
	if a.Calculated == nil {
		return inputs
	}
	if a.GroundSpeed == nil && a.Calculated.GroundSpeed != nil {
		inputs = append(inputs, "ground_speed")
	}
	if a.Track == nil && a.Calculated.GroundTrack != nil {
		inputs = append(inputs, "ground_track")
	}
	if a.VerticalRate == nil && a.Calculated.VerticalRate != nil {
		inputs = append(inputs, "vertical_rate")
	}
	return inputs
}
