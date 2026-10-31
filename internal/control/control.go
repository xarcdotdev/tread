// Package control maintains conservative, boot-scoped knowledge of sent controls.
// Every operation must run under the same per-configuration lock as queue sync.
package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/atomicfile"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/drivers"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// Action names a command Execute accepts. The CLI and the agent protocol use
// the same spelling.
type Action string

const (
	ActionStart      Action = "start"
	ActionResume     Action = "resume"
	ActionPause      Action = "pause"
	ActionStop       Action = "stop"
	ActionStatus     Action = "status"
	ActionSpeed      Action = "speed"
	ActionToggle     Action = "toggle"
	ActionSpeedUp    Action = "speed-up"
	ActionSpeedDown  Action = "speed-down"
	ActionResetState Action = "reset-state"
)

// Actions lists every accepted action.
var Actions = []Action{
	ActionStart, ActionResume, ActionPause, ActionStop, ActionStatus,
	ActionSpeed, ActionToggle, ActionSpeedUp, ActionSpeedDown, ActionResetState,
}

func (a Action) Valid() bool { return slices.Contains(Actions, a) }

// UnknownOutcomeError wraps any failure from the moment a command is handed to
// the transport. The treadmill may have acted on it, so callers must report the
// outcome as unknown and never retry automatically.
type UnknownOutcomeError struct{ Err error }

func (e *UnknownOutcomeError) Error() string { return e.Err.Error() }
func (e *UnknownOutcomeError) Unwrap() error { return e.Err }

// mode is the belt state the last confirmed command implies.
type mode string

const (
	modeUnknown mode = ""
	modeRunning mode = "running"
	modePaused  mode = "paused"
	modeStopped mode = "stopped"
)

func (m mode) valid() bool {
	return m == modeUnknown || m == modeRunning || m == modePaused || m == modeStopped
}

type identity struct {
	Device string
	Driver string
	MAC    string
}

// state is the on-disk control journal. Mode and Speed are knowledge derived
// from confirmed commands; a zero Speed means unknown.
type state struct {
	Version  int           `json:"version"`
	Identity identity      `json:"identity"`
	Boot     string        `json:"boot"`
	Mode     mode          `json:"mode,omitempty"`
	Speed    int           `json:"speed,omitempty"`
	InFlight bool          `json:"in_flight,omitempty"`
	Pending  []event.Event `json:"pending,omitempty"`
}

// forget drops all belt knowledge while keeping identity and pending events.
func (s *state) forget() {
	s.Mode = modeUnknown
	s.Speed = 0
}

// Controller records sent controls. Its injected transport must send exactly
// once, without retries.
type Controller struct {
	Config config.Config
	Queue  *event.Client
	Send   func(mac, uuid string, packet []byte) error
	BootID func() (string, error)
	// write is a fault-injection seam for durability tests.
	write func(string, state) error
}

func StatePath(cfg config.Config) string {
	return filepath.Join(filepath.Dir(cfg.QueuePath()), "control-state.json")
}

// markerPath is a separate in-flight marker beside the control state.
func markerPath(cfg config.Config) string { return StatePath(cfg) + ".in-flight" }

func LinuxBootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

func (c *Controller) save(s state) error {
	if c.write != nil {
		return c.write(StatePath(c.Config), s)
	}
	return writeState(StatePath(c.Config), s)
}

func (c *Controller) load() (state, error) {
	s, err := readState(StatePath(c.Config))
	if err != nil {
		return s, fmt.Errorf("control state %s: %w; preserve this file and repair/recover its pending events before reset", StatePath(c.Config), err)
	}
	if _, err := os.Stat(markerPath(c.Config)); err == nil {
		s.InFlight = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	return s, nil
}

// readState strictly decodes the journal. A missing file is an empty journal.
func readState(path string) (state, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: 1}, nil
	}
	if err != nil {
		return state{}, err
	}
	var s state
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, err
	}
	var extra any
	if trailing := dec.Decode(&extra); trailing != io.EOF {
		return s, errors.New("trailing data in control journal")
	}
	// Speed is checked against driver levels where relative commands use it.
	if s.Version != 1 || !s.Mode.valid() || s.Speed < 0 {
		return s, errors.New("invalid version or knowledge")
	}
	return s, nil
}

// recover transfers confirmed events before changing identity or clearing state.
func (c *Controller) recover() (state, error) {
	s, err := c.load()
	if err != nil {
		return s, err
	}
	if err = c.Queue.Preflight(); err != nil {
		return s, err
	}
	if len(s.Pending) != 0 {
		if err = c.Queue.Enqueue(s.Pending...); err != nil {
			return s, err
		}
		s.Pending = nil
		if err = c.save(s); err != nil {
			return s, err
		}
	}
	return s, nil
}

// Sync recovers and snapshots reporting under the control lock. The caller must
// release that lock before Post, then reacquire it for Acknowledge.
func (c *Controller) Sync() ([]event.Event, error) {
	if _, err := c.recover(); err != nil {
		return nil, err
	}
	return c.Queue.Snapshot()
}

// Result distinguishes recorded motion from status queries and local no-ops.
type Result struct {
	Message string
	Motion  bool
}

// target is the device a command goes to, resolved from configuration.
type target struct {
	driver   device.Driver
	identity identity
	boot     string
}

func (t target) mac() string { return t.identity.MAC }

// Execute durably records an unknown in-flight state before sending. A successful
// send is committed together with its event, then transferred to the local queue.
// The caller may snapshot after Motion, then deliver outside the control lock.
func (c *Controller) Execute(action Action, level int) (Result, error) {
	s, err := c.recover()
	if err != nil {
		return Result{}, err
	}
	if action == ActionResetState {
		return c.resetState(s)
	}
	t, err := c.target()
	if err != nil {
		return Result{}, err
	}
	// Knowledge only survives for the same device, in the same boot, with no
	// command whose outcome was never recorded.
	if s.Identity != t.identity || s.Boot != t.boot || s.InFlight {
		s.forget()
	}
	s.Identity, s.Boot = t.identity, t.boot
	action, level, err = resolve(s, t.driver, action, level)
	if err != nil {
		return Result{}, err
	}
	if action == "" {
		return Result{Message: fmt.Sprintf("Speed already at level %d; no command sent.", s.Speed)}, nil
	}
	packet, err := t.driver.Packet(string(action), level)
	if err != nil {
		return Result{}, err
	}
	if c.Send == nil {
		return Result{}, errors.New("control transport is unavailable")
	}
	if action == ActionStatus {
		if err := c.Send(t.mac(), t.driver.WriteUUID(), packet); err != nil {
			return Result{}, err
		}
		return Result{Message: "Status query sent. Device telemetry is not decoded.", Motion: false}, nil
	}
	return c.sendRecorded(s, t, action, level, packet)
}

// resetState forgets belt knowledge and clears any in-flight marker.
func (c *Controller) resetState(s state) (Result, error) {
	s.forget()
	s.InFlight = false
	if err := c.save(s); err != nil {
		return Result{}, err
	}
	if err := c.clearMarker(); err != nil {
		return Result{}, err
	}
	return Result{Message: "Local control state reset; queued events preserved."}, nil
}

func (c *Controller) target() (target, error) {
	mac, err := net.ParseMAC(c.Config.Treadmill.MAC)
	if err != nil || len(mac) != 6 {
		return target{}, errors.New("set a valid treadmill.mac in config first")
	}
	driver, err := drivers.Lookup(c.Config.Treadmill.Driver)
	if err != nil {
		return target{}, err
	}
	bootFn := c.BootID
	if bootFn == nil {
		bootFn = LinuxBootID
	}
	boot, err := bootFn()
	if err != nil || strings.TrimSpace(boot) == "" {
		return target{}, fmt.Errorf("cannot determine boot ID; no command sent: %v", err)
	}
	return target{
		driver:   driver,
		identity: identity{c.Config.Device, c.Config.Treadmill.Driver, mac.String()},
		boot:     boot,
	}, nil
}

// resolve turns toggle and relative speed actions into the concrete action to
// send, using recorded knowledge, and checks speed levels against the driver.
// An empty action means a bounded speed step: there is nothing to send.
func resolve(s state, driver device.Driver, action Action, level int) (Action, int, error) {
	var levels observed.LevelRange
	if action == ActionSpeed || action == ActionSpeedUp || action == ActionSpeedDown {
		var err error
		if levels, err = device.SpeedLevels(driver); err != nil {
			return "", 0, fmt.Errorf("speed unsupported: %w", err)
		}
	}
	switch action {
	case ActionToggle:
		// Unknown mode starts: start is a no-op on a running belt and establishes mode.
		if s.Mode == modeRunning {
			return ActionPause, 0, nil
		}
		return ActionStart, 0, nil
	case ActionSpeedUp, ActionSpeedDown:
		// A speed command can start a stopped belt, so require known running mode.
		if s.Mode != modeRunning {
			return "", 0, errors.New("speed adjustment requires known running state; use toggle or start first")
		}
		if s.Speed == 0 {
			if action == ActionSpeedUp {
				return "", 0, errors.New("speed-up requires known speed; use speed-down or an explicit speed first")
			}
			// Unknown speed only ever slows down, to the lowest level.
			return ActionSpeed, levels.Min, nil
		}
		level, bounded, err := relativeLevel(levels, s.Speed, action)
		if err != nil {
			return "", 0, err
		}
		if bounded {
			return "", 0, nil
		}
		return ActionSpeed, level, nil
	case ActionSpeed:
		if !levels.Contains(level) {
			return "", 0, fmt.Errorf("speed requires a level %s", levels)
		}
	}
	return action, level, nil
}

// sendRecorded journals an in-flight marker, sends once, then commits the
// confirmed knowledge and usage event. Every failure after the send is an
// UnknownOutcomeError.
func (c *Controller) sendRecorded(s state, t target, action Action, level int, packet []byte) (Result, error) {
	eventAction := event.Action(action)
	if action == ActionResume {
		eventAction = event.Start
	}
	confirmed := event.New(c.Config.Device, eventAction, level)
	if err := confirmed.Validate(time.Now()); err != nil {
		return Result{}, fmt.Errorf("no command sent; invalid usage event: %w", err)
	}
	marker := s
	marker.forget()
	marker.InFlight = true
	if err := c.save(marker); err != nil {
		return Result{}, fmt.Errorf("no command sent; local preflight failed: %w", err)
	}
	// Separate marker survives even a failed directory fsync after commit rename.
	if err := writeState(markerPath(c.Config), marker); err != nil {
		return Result{}, fmt.Errorf("no command sent; in-flight marker failed: %w", err)
	}
	if err := c.Send(t.mac(), t.driver.WriteUUID(), packet); err != nil {
		return Result{}, &UnknownOutcomeError{fmt.Errorf("transport failed; control state is unknown (do not automatically retry): %w", err)}
	}
	s.InFlight = false
	switch action {
	case ActionStart, ActionResume:
		s.Mode = modeRunning
	case ActionPause:
		s.Mode = modePaused
	case ActionStop:
		s.Mode = modeStopped
	case ActionSpeed:
		s.Speed = level
	}
	s.Pending = []event.Event{confirmed}
	if err := c.save(s); err != nil {
		return Result{}, &UnknownOutcomeError{fmt.Errorf("command sent; local recording failed: %w; do not automatically retry", err)}
	}
	if err := c.clearMarker(); err != nil {
		return Result{}, &UnknownOutcomeError{fmt.Errorf("command sent; confirmed event journaled, in-flight cleanup failed: %w", err)}
	}
	if _, err := c.recover(); err != nil {
		return Result{}, &UnknownOutcomeError{fmt.Errorf("command sent; confirmed event journaled, queue recovery failed: %w", err)}
	}
	return Result{Message: fmt.Sprintf("%s sent; usage queued", action), Motion: true}, nil
}

// relativeLevel steps one level within the driver's range; bounded means no change.
func relativeLevel(r observed.LevelRange, speed int, action Action) (level int, bounded bool, err error) {
	if !r.Contains(speed) {
		return 0, false, errors.New("known speed is outside driver level metadata; use an explicit speed level")
	}
	if action == ActionSpeedUp {
		if r.Step > r.Max-speed {
			return speed, true, nil
		}
		return speed + r.Step, false, nil
	}
	if r.Step > speed-r.Min {
		return speed, true, nil
	}
	return speed - r.Step, false, nil
}

func (c *Controller) clearMarker() error {
	path := markerPath(c.Config)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(path))
}

func writeState(path string, s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b)
}
