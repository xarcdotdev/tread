package observed

import (
	"slices"
	"sync"
	"time"
)

const DefaultFreshness = 5 * time.Second

type StoreOptions struct {
	// Freshness defaults to DefaultFreshness when nonpositive.
	Freshness time.Duration
	// Now defaults to time.Now. A custom clock must be safe for concurrent calls.
	Now func() time.Time
}

// Store is a concurrent, in-memory view of one Bluetooth host's live device.
// It does not persist readings or accept remote ingestion.
type Store struct {
	mu         sync.RWMutex
	snapshot   Snapshot
	static     Capabilities
	discovered Capabilities
	freshness  time.Duration
	now        func() time.Time
}

func NewStore(device string, static Capabilities, options StoreOptions) *Store {
	if options.Freshness <= 0 {
		options.Freshness = DefaultFreshness
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	s := &Store{
		static: static.Clone(), freshness: options.Freshness, now: options.Now,
		snapshot: Snapshot{Device: device, Connection: ConnectionDisconnected, Telemetry: TelemetryUnsupported},
	}
	s.invalidate()
	return s
}

// BeginConnection starts a new connection generation and clears all readings
// and discovered capabilities. Capture the returned token in session callbacks.
// The host must explicitly select linking or disabled telemetry for this attempt;
// the default remains unsupported until a telemetry provider is selected.
func (s *Store) BeginConnection() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.Generation++
	s.invalidate()
	s.snapshot.Connection = ConnectionConnecting
	if s.snapshot.Telemetry != TelemetryUnsupported && s.snapshot.Telemetry != TelemetryDisabled {
		s.snapshot.Telemetry = TelemetryLinking
	}
	return s.snapshot.Generation
}

// SetConnection marks a link connected, disabled, disconnected, or faulted.
// Terminal states clear readings/discovery and advance the generation, rejecting
// every subsequent callback from that link. Reconnecting requires BeginConnection.
func (s *Store) SetConnection(generation uint64, status ConnectionStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.snapshot.Generation {
		return false
	}
	switch status {
	case ConnectionConnected:
		if !s.acceptsUpdates() {
			return false
		}
		s.snapshot.Connection = status
	case ConnectionDisabled, ConnectionDisconnected, ConnectionFault:
		s.snapshot.Generation++
		s.invalidate()
		s.snapshot.Connection = status
		if s.snapshot.Telemetry != TelemetryUnsupported && s.snapshot.Telemetry != TelemetryDisabled {
			switch status {
			case ConnectionDisabled:
				s.snapshot.Telemetry = TelemetryDisabled
			case ConnectionFault:
				s.snapshot.Telemetry = TelemetryFault
			default:
				s.snapshot.Telemetry = TelemetryStale
			}
		}
	default:
		return false
	}
	return true
}

func (s *Store) SetTelemetry(generation uint64, status TelemetryStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.snapshot.Generation || !s.acceptsUpdates() {
		return false
	}
	switch status {
	case TelemetryUnsupported, TelemetryDisabled, TelemetryLinking, TelemetryActive, TelemetryStale, TelemetryFault:
		s.snapshot.Telemetry = status
		return true
	default:
		return false
	}
}

// Apply accepts only this connection generation. Each supplied field gets its
// own UTC receipt timestamp; omitted fields do not become fresh again.
func (s *Store) Apply(generation uint64, update Update) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.snapshot.Generation || !s.acceptsUpdates() {
		return false
	}
	now := s.now().UTC()
	if update.State != nil {
		s.snapshot.State = cloneState(*update.State)
		if s.snapshot.State.Value == "" {
			s.snapshot.State.Value = StateUnknown
		}
		s.snapshot.State.Timestamp = &now
		s.snapshot.State.Stale = false
	}
	for field, measurement := range update.Measurements {
		// Unknown keys cannot silently expand the normalized measurement schema.
		if !slices.Contains(MeasurementFields, field) {
			continue
		}
		measurement = cloneMeasurement(measurement)
		if measurement.Unit == "" {
			measurement.Unit = UnitUnknown
		}
		if measurement.UnitConfidence == "" {
			measurement.UnitConfidence = UnitUnknownConfidence
		}
		if measurement.DataConfidence == "" {
			measurement.DataConfidence = DataExperimental
		}
		measurement.Timestamp = &now
		measurement.Stale = false
		s.snapshot.Measurements[field] = measurement
	}
	if update.Capabilities != nil {
		s.discovered = update.Capabilities.Clone()
		s.snapshot.Capabilities = mergeCapabilities(s.static, s.discovered)
	}
	return true
}

// Snapshot returns an independent copy. Staleness is computed at read time,
// including when no further notifications arrive. Active telemetry becomes stale
// when no observed state or measurement with a value is fresh.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now().UTC()
	out := s.snapshot
	out.Capabilities = out.Capabilities.Clone()
	out.State = cloneState(out.State)
	out.State.Stale = s.stale(out.State.Timestamp, now)
	fresh := !out.State.Stale && (out.State.Value != StateUnknown || out.State.RawCode != nil)
	out.Measurements = make(map[Field]Measurement, len(s.snapshot.Measurements))
	for field, measurement := range s.snapshot.Measurements {
		measurement = cloneMeasurement(measurement)
		measurement.Stale = s.stale(measurement.Timestamp, now)
		fresh = fresh || (!measurement.Stale && measurement.Value != nil)
		out.Measurements[field] = measurement
	}
	if out.Telemetry == TelemetryActive && !fresh {
		out.Telemetry = TelemetryStale
	}
	return out
}

func (s *Store) stale(timestamp *time.Time, now time.Time) bool {
	return timestamp == nil || now.Sub(*timestamp) >= s.freshness
}

func (s *Store) acceptsUpdates() bool {
	return s.snapshot.Connection == ConnectionConnecting || s.snapshot.Connection == ConnectionConnected
}

// invalidate requires the write lock, except during construction.
func (s *Store) invalidate() {
	s.discovered = Capabilities{}
	s.snapshot.Capabilities = s.static.Clone()
	s.snapshot.State = State{Value: StateUnknown, Stale: true}
	s.snapshot.Measurements = make(map[Field]Measurement, len(MeasurementFields))
	for _, field := range MeasurementFields {
		s.snapshot.Measurements[field] = Measurement{
			Unit: UnitUnknown, UnitConfidence: UnitUnknownConfidence,
			DataConfidence: DataExperimental, Stale: true,
		}
	}
}

func cloneState(state State) State {
	if state.RawCode != nil {
		v := *state.RawCode
		state.RawCode = &v
	}
	state.Timestamp = cloneTime(state.Timestamp)
	return state
}

func cloneMeasurement(measurement Measurement) Measurement {
	if measurement.Value != nil {
		v := *measurement.Value
		measurement.Value = &v
	}
	measurement.Timestamp = cloneTime(measurement.Timestamp)
	return measurement
}

func cloneTime(timestamp *time.Time) *time.Time {
	if timestamp == nil {
		return nil
	}
	v := timestamp.UTC()
	return &v
}

func mergeCapabilities(static, discovered Capabilities) Capabilities {
	out := static.Clone()
	for _, action := range discovered.Actions {
		if !slices.Contains(out.Actions, action) {
			out.Actions = append(out.Actions, action)
		}
	}
	for _, field := range discovered.ObservedFields {
		if !slices.Contains(out.ObservedFields, field) {
			out.ObservedFields = append(out.ObservedFields, field)
		}
	}
	// Static command limits remain authoritative if discovery also supplies them.
	if out.SpeedLevels == nil && discovered.SpeedLevels != nil {
		v := *discovered.SpeedLevels
		out.SpeedLevels = &v
	}
	return out
}
