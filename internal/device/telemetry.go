package device

import (
	"context"
	"errors"
	"slices"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// CapabilityProvider is optional; Driver remains a command-only interface.
type CapabilityProvider interface {
	Capabilities() observed.Capabilities
}

// CapabilitiesFor returns detached metadata. Unknown drivers get no assumed
// actions, level limits, or observed fields. It never probes a device.
func CapabilitiesFor(driver Driver) observed.Capabilities {
	if provider, ok := driver.(CapabilityProvider); ok {
		return provider.Capabilities().Clone()
	}
	return observed.Capabilities{}.Clone()
}

// SpeedLevels returns the driver's published command levels, never telemetry or
// calibration. Drivers without the speed action or a valid range have none.
func SpeedLevels(driver Driver) (observed.LevelRange, error) {
	caps := CapabilitiesFor(driver)
	if !slices.Contains(caps.Actions, "speed") || caps.SpeedLevels == nil || !caps.SpeedLevels.Valid() {
		return observed.LevelRange{}, errors.New("driver publishes no speed levels")
	}
	return *caps.SpeedLevels, nil
}

// ErrTelemetryDisabled means the optional telemetry protocol was not enabled.
var ErrTelemetryDisabled = errors.New("telemetry disabled")

type TelemetryOptions struct {
	ExperimentalVAN bool
	// ExperimentalVANUnit is the opaque C1 unit byte, default 0. Its unit
	// mapping is unverified. Sending C1 may configure the device's units;
	// this option does not establish units for observed measurements.
	ExperimentalVANUnit byte
}

// TelemetryProvider is optional. Its presence does not imply that telemetry is
// enabled. NewTelemetry must create a fresh session for each connection attempt.
type TelemetryProvider interface {
	NewTelemetry(TelemetryOptions) (TelemetrySession, error)
}

// Link writes through the Bluetooth host's existing connection. Sessions must
// not open another connection or bypass the host's write serialization.
// Cancellation stops admission, but cannot interrupt a synchronous backend write
// already in progress. Sessions wait for that call before returning.
type Link interface {
	Write(context.Context, string, []byte) error
}

// Notification contains a characteristic UUID and an owned copy of its payload.
// Hosts must copy BLE callback buffers before sending them to a session.
type Notification struct {
	UUID string
	Data []byte
}

// TelemetrySession lists notification subscriptions and runs until cancellation,
// a closed notification channel, or failure. Run must honor context cancellation.
// emit is a partial observed patch, not a command acknowledgment or estimate.
// The host binds emit to the Store generation for this connection attempt.
type TelemetrySession interface {
	NotifyUUIDs() []string
	Run(context.Context, Link, <-chan Notification, func(observed.Update)) error
}
