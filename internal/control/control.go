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

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
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

// Controller's injected transport must send exactly once, without retries.
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
	if err == nil && (s.Version != 1 || (s.Mode != "" && s.Mode != "running" && s.Mode != "paused" && s.Mode != "stopped") || s.Speed < 0 || s.Speed > 8) {
		err = errors.New("invalid version or knowledge")
	}
	if err != nil {
		return s, fmt.Errorf("control state %s: %w; preserve this file and repair/recover its pending events before reset", StatePath(c.Config), err)
	}
	if _, err := os.Stat(StatePath(c.Config) + ".in-flight"); err == nil {
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

// Execute preserves the message-only interface for local control callers.
func (c *Controller) Execute(action string, level int) (string, error) {
	r, err := c.ExecuteResult(action, level)
	return r.Message, err
}

// Execute durably records an unknown in-flight state before sending. A successful
// send is committed together with its event, then transferred to the local queue.
// The caller may snapshot after Motion, then deliver outside the control lock.
func (c *Controller) ExecuteResult(action string, level int) (result Result, err error) {
	message, err := c.execute(action, level, &result.Motion)
	result.Message = message
	return result, err
}

func (c *Controller) execute(action string, level int, motion *bool) (string, error) {
	s, err := c.recover()
	if err != nil {
		return "", err
	}
	if action == "reset-state" {
		s.Mode, s.Speed, s.InFlight = "", 0, false
		if err := c.save(s); err != nil {
			return "", err
		}
		if err := c.clearMarker(); err != nil {
			return "", err
		}
		return "Local control state reset; queued events preserved.", nil
	}
	mac, err := net.ParseMAC(c.Config.Treadmill.MAC)
	if err != nil || len(mac) != 6 {
		return "", errors.New("set a valid treadmill.mac in config first")
	}
	driver, err := device.Lookup(c.Config.Treadmill.Driver)
	if err != nil {
		return "", err
	}
	bootFn := c.BootID
	if bootFn == nil {
		bootFn = LinuxBootID
	}
	boot, err := bootFn()
	if err != nil || strings.TrimSpace(boot) == "" {
		return "", fmt.Errorf("cannot determine boot ID; no command sent: %v", err)
	}
	id := identity{c.Config.Device, c.Config.Treadmill.Driver, mac.String()}
	if s.Identity != id || s.Boot != boot || s.InFlight {
		s.Mode, s.Speed = "", 0
	}
	s.Identity, s.Boot = id, boot
	switch action {
	case "toggle":
		switch s.Mode {
		case "running":
			action = "pause"
		case "paused", "stopped":
			action = "start"
		default:
			return "", errors.New("running state unknown; use explicit start, pause, or stop first")
		}
	case "speed-up", "speed-down":
		if s.Mode != "running" || s.Speed == 0 {
			return "", errors.New("speed adjustment requires known running state and speed; use explicit start and speed first")
		}
		level = s.Speed
		if action == "speed-up" {
			level++
		} else {
			level--
		}
		if level < 1 || level > 8 {
			return fmt.Sprintf("Speed already at level %d; no command sent.", s.Speed), nil
		}
		action = "speed"
	}
	packet, err := driver.Packet(action, level)
	if err != nil {
		return "", err
	}
	if c.Send == nil {
		return "", errors.New("control transport is unavailable")
	}
	if action == "status" {
		if err := c.Send(mac.String(), driver.WriteUUID(), packet); err != nil {
			return "", err
		}
		return "Status query sent. Device telemetry is not decoded.", nil
	}
	eventAction := event.Action(action)
	if action == "resume" {
		eventAction = event.Start
	}
	confirmed := event.New(c.Config.Device, eventAction, level)
	if err := confirmed.Validate(time.Now()); err != nil {
		return "", fmt.Errorf("no command sent; invalid usage event: %w", err)
	}
	marker := s
	marker.Mode, marker.Speed, marker.InFlight = "", 0, true
	if err := c.save(marker); err != nil {
		return "", fmt.Errorf("no command sent; local preflight failed: %w", err)
	}
	// Separate marker survives even a failed directory fsync after commit rename.
	if err := writeState(StatePath(c.Config)+".in-flight", marker); err != nil {
		return "", fmt.Errorf("no command sent; in-flight marker failed: %w", err)
	}
	if err := c.Send(mac.String(), driver.WriteUUID(), packet); err != nil {
		return "", fmt.Errorf("transport failed; control state is unknown (do not automatically retry): %w", err)
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
		return "", fmt.Errorf("command sent; local recording failed: %w; do not automatically retry", err)
	}
	if err := c.clearMarker(); err != nil {
		return "", fmt.Errorf("command sent; confirmed event journaled, in-flight cleanup failed: %w", err)
	}
	if _, err := c.recover(); err != nil {
		return "", fmt.Errorf("command sent; confirmed event journaled, queue recovery failed: %w", err)
	}
	*motion = true
	return fmt.Sprintf("%s sent; usage queued", action), nil
}

func (c *Controller) clearMarker() error {
	path := StatePath(c.Config) + ".in-flight"
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writeState(path string, s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".control-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
