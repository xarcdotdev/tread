// Package observed holds live device readings, never commanded or estimated values.
package observed

import (
	"fmt"
	"time"
)

type ConnectionStatus string

const (
	ConnectionDisabled     ConnectionStatus = "disabled"
	ConnectionDisconnected ConnectionStatus = "disconnected"
	ConnectionConnecting   ConnectionStatus = "connecting"
	ConnectionConnected    ConnectionStatus = "connected"
	ConnectionFault        ConnectionStatus = "fault"
)

type TelemetryStatus string

const (
	TelemetryUnsupported TelemetryStatus = "unsupported"
	TelemetryDisabled    TelemetryStatus = "disabled"
	TelemetryLinking     TelemetryStatus = "linking"
	TelemetryActive      TelemetryStatus = "active"
	TelemetryStale       TelemetryStatus = "stale"
	TelemetryFault       TelemetryStatus = "fault"
)

type RunState string

const (
	StateUnknown RunState = "unknown"
	StateRunning RunState = "running"
	StatePaused  RunState = "paused"
)

// Field names are stable across drivers. State is not a measurement.
type Field string

const (
	FieldState    Field = "state"
	FieldSpeed    Field = "speed"
	FieldDistance Field = "distance"
	FieldElapsed  Field = "elapsed"
	FieldIncline  Field = "incline"
	FieldCalories Field = "calories"
	FieldPulse    Field = "pulse"
)

// MeasurementFields lists every normalized measurement in display order.
var MeasurementFields = []Field{FieldSpeed, FieldDistance, FieldElapsed, FieldIncline, FieldCalories, FieldPulse}

type Unit string

const (
	UnitUnknown    Unit = "unknown"
	UnitKMH        Unit = "km/h"
	UnitMPS        Unit = "m/s"
	UnitMetres     Unit = "m"
	UnitKilometres Unit = "km"
	UnitSeconds    Unit = "s"
	UnitPercent    Unit = "%"
	UnitKCal       Unit = "kcal"
	UnitBPM        Unit = "bpm"
)

type UnitConfidence string

const (
	UnitUnknownConfidence UnitConfidence = "unknown"
	UnitInferred          UnitConfidence = "inferred"
	UnitVerified          UnitConfidence = "verified"
)

type DataConfidence string

const (
	DataExperimental DataConfidence = "experimental"
	DataVerified     DataConfidence = "verified"
)

// LevelRange describes command levels, not physical speed or calibrated km/h.
type LevelRange struct {
	Min  int `json:"min"`
	Max  int `json:"max"`
	Step int `json:"step"`
}

// Valid reports positive levels with a forward step.
func (r LevelRange) Valid() bool { return r.Min >= 1 && r.Max >= r.Min && r.Step >= 1 }

// Contains reports whether level is reachable from Min in whole steps.
func (r LevelRange) Contains(level int) bool {
	return r.Valid() && level >= r.Min && level <= r.Max && (level-r.Min)%r.Step == 0
}

// Levels lists every contained level in ascending order; none if invalid.
func (r LevelRange) Levels() []int {
	var out []int
	for n := r.Min; r.Valid() && n <= r.Max; n += r.Step {
		out = append(out, n)
	}
	return out
}

func (r LevelRange) String() string {
	if r.Step == 1 {
		return fmt.Sprintf("%d–%d", r.Min, r.Max)
	}
	return fmt.Sprintf("%d–%d in steps of %d", r.Min, r.Max, r.Step)
}

type Capabilities struct {
	Actions        []string    `json:"actions"`
	SpeedLevels    *LevelRange `json:"speed_levels"`
	ObservedFields []Field     `json:"observed_fields"`
}

// Clone detaches all mutable values from the original capabilities.
func (c Capabilities) Clone() Capabilities {
	c.Actions = append([]string{}, c.Actions...)
	c.ObservedFields = append([]Field{}, c.ObservedFields...)
	if c.SpeedLevels != nil {
		v := *c.SpeedLevels
		c.SpeedLevels = &v
	}
	return c
}

type State struct {
	Value     RunState   `json:"value"`
	RawCode   *uint16    `json:"raw_code"`
	Timestamp *time.Time `json:"timestamp"`
	Stale     bool       `json:"stale"`
}

type Measurement struct {
	Value          *float64       `json:"value"`
	Unit           Unit           `json:"unit"`
	UnitConfidence UnitConfidence `json:"unit_confidence"`
	DataConfidence DataConfidence `json:"data_confidence"`
	Timestamp      *time.Time     `json:"timestamp"`
	Stale          bool           `json:"stale"`
}

type Snapshot struct {
	Device       string                `json:"device"`
	Generation   uint64                `json:"generation"`
	Connection   ConnectionStatus      `json:"connection"`
	Telemetry    TelemetryStatus       `json:"telemetry"`
	Capabilities Capabilities          `json:"capabilities"`
	State        State                 `json:"state"`
	Measurements map[Field]Measurement `json:"measurements"`
}

// Update is a partial patch. Nil State/Capabilities and absent measurement keys
// leave existing fields untouched. A supplied nil measurement value clears it.
// Capabilities replaces discovered metadata, not the driver's static metadata.
// Store supplies receipt timestamps and staleness; input timestamps are ignored.
// Units and values are retained as reported, without conversion or calibration.
type Update struct {
	State        *State                `json:"state,omitempty"`
	Measurements map[Field]Measurement `json:"measurements,omitempty"`
	Capabilities *Capabilities         `json:"capabilities,omitempty"`
}
