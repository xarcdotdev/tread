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
	"strings"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/atomicfile"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/drivers"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

type identity struct {
	Device string
	Driver string
	MAC    string
}

type state struct {
	Version  int           `json:"version"`
	Identity identity      `json:"identity"`
	Boot     string        `json:"boot"`
	Mode     string        `json:"mode,omitempty"`
	Speed    int           `json:"speed,omitempty"`
	InFlight bool          `json:"in_flight,omitempty"`
	Pending  []event.Event `json:"pending,omitempty"`
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
	var s state
	b, err := os.ReadFile(StatePath(c.Config))
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: 1}, nil
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		err = dec.Decode(&s)
		if err == nil {
			var extra any
			if trailing := dec.Decode(&extra); trailing != io.EOF {
				err = errors.New("trailing data in control journal")
			}
		}
	}
	// Speed is checked against driver levels where relative commands use it.
	if err == nil && (s.Version != 1 || (s.Mode != "" && s.Mode != "running" && s.Mode != "paused" && s.Mode != "stopped") || s.Speed < 0) {
		err = errors.New("invalid version or knowledge")
	}
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

// Execute durably records an unknown in-flight state before sending. A successful
// send is committed together with its event, then transferred to the local queue.
// The caller may snapshot after Motion, then deliver outside the control lock.
func (c *Controller) Execute(action string, level int) (Result, error) {
	s, err := c.recover()
	if err != nil {
		return Result{}, err
	}
	if action == "reset-state" {
		s.Mode, s.Speed, s.InFlight = "", 0, false
		if err := c.save(s); err != nil {
			return Result{}, err
		}
		if err := c.clearMarker(); err != nil {
			return Result{}, err
		}
		return Result{Message: "Local control state reset; queued events preserved."}, nil
	}
	mac, err := net.ParseMAC(c.Config.Treadmill.MAC)
	if err != nil || len(mac) != 6 {
		return Result{}, errors.New("set a valid treadmill.mac in config first")
	}
	driver, err := drivers.Lookup(c.Config.Treadmill.Driver)
	if err != nil {
		return Result{}, err
	}
	bootFn := c.BootID
	if bootFn == nil {
		bootFn = LinuxBootID
	}
	boot, err := bootFn()
	if err != nil || strings.TrimSpace(boot) == "" {
		return Result{}, fmt.Errorf("cannot determine boot ID; no command sent: %v", err)
	}
	id := identity{c.Config.Device, c.Config.Treadmill.Driver, mac.String()}
	if s.Identity != id || s.Boot != boot || s.InFlight {
		s.Mode, s.Speed = "", 0
	}
	s.Identity, s.Boot = id, boot
	// Validate speed commands against the driver's published range.
	var levels observed.LevelRange
	if action == "speed" || action == "speed-up" || action == "speed-down" {
		levels, err = device.SpeedLevels(driver)
		if err != nil {
			return Result{}, fmt.Errorf("speed unsupported: %w", err)
		}
	}
	switch action {
	case "toggle":
		switch s.Mode {
		case "running":
			action = "pause"
		default:
			// Unknown mode starts: start is a no-op on a running belt and establishes mode.
			action = "start"
		}
	case "speed-up", "speed-down":
		// A speed command can start a stopped belt, so require known running mode.
		if s.Mode != "running" {
			return Result{}, errors.New("speed adjustment requires known running state; use toggle or start first")
		}
		if s.Speed == 0 {
			if action == "speed-up" {
				return Result{}, errors.New("speed-up requires known speed; use speed-down or an explicit speed first")
			}
			// Unknown speed only ever slows down, to the lowest level.
			level, action = levels.Min, "speed"
			break
		}
		var bounded bool
		level, bounded, err = relativeLevel(levels, s.Speed, action)
		if err != nil {
			return Result{}, err
		}
		if bounded {
			return Result{Message: fmt.Sprintf("Speed already at level %d; no command sent.", s.Speed)}, nil
		}
		action = "speed"
	}
	if action == "speed" && !levels.Contains(level) {
		return Result{}, fmt.Errorf("speed requires a level %s", levels)
	}
	packet, err := driver.Packet(action, level)
	if err != nil {
		return Result{}, err
	}
	if c.Send == nil {
		return Result{}, errors.New("control transport is unavailable")
	}
	if action == "status" {
		if err := c.Send(mac.String(), driver.WriteUUID(), packet); err != nil {
			return Result{}, err
		}
		return Result{Message: "Status query sent. Device telemetry is not decoded."}, nil
	}
	eventAction := event.Action(action)
	if action == "resume" {
		eventAction = event.Start
	}
	confirmed := event.New(c.Config.Device, eventAction, level)
	if err := confirmed.Validate(time.Now()); err != nil {
		return Result{}, fmt.Errorf("no command sent; invalid usage event: %w", err)
	}
	marker := s
	marker.Mode, marker.Speed, marker.InFlight = "", 0, true
	if err := c.save(marker); err != nil {
		return Result{}, fmt.Errorf("no command sent; local preflight failed: %w", err)
	}
	// Separate marker survives even a failed directory fsync after commit rename.
	if err := writeState(markerPath(c.Config), marker); err != nil {
		return Result{}, fmt.Errorf("no command sent; in-flight marker failed: %w", err)
	}
	if err := c.Send(mac.String(), driver.WriteUUID(), packet); err != nil {
		return Result{}, fmt.Errorf("transport failed; control state is unknown (do not automatically retry): %w", err)
	}
	s.InFlight = false
	switch action {
	case "start", "resume":
		s.Mode = "running"
	case "pause":
		s.Mode = "paused"
	case "stop":
		s.Mode = "stopped"
	case "speed":
		s.Speed = level
	}
	s.Pending = []event.Event{confirmed}
	if err := c.save(s); err != nil {
		return Result{}, fmt.Errorf("command sent; local recording failed: %w; do not automatically retry", err)
	}
	if err := c.clearMarker(); err != nil {
		return Result{}, fmt.Errorf("command sent; confirmed event journaled, in-flight cleanup failed: %w", err)
	}
	if _, err := c.recover(); err != nil {
		return Result{}, fmt.Errorf("command sent; confirmed event journaled, queue recovery failed: %w", err)
	}
	return Result{Message: fmt.Sprintf("%s sent; usage queued", action), Motion: true}, nil
}

// relativeLevel steps one level within the driver's range; bounded means no change.
func relativeLevel(r observed.LevelRange, speed int, action string) (level int, bounded bool, err error) {
	if !r.Contains(speed) {
		return 0, false, errors.New("known speed is outside driver level metadata; use an explicit speed level")
	}
	if action == "speed-up" {
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
