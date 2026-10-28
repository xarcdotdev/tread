// Package event defines usage events and a client that reports them to the server.
package event

import (
	"crypto/rand"
	"fmt"
	"time"
)

type Action string

const (
	Start Action = "start"
	Stop  Action = "stop"
	Pause Action = "pause"
	Speed Action = "speed"
)

// Event is one control action sent to the treadmill.
// ID makes resends idempotent; TS is the controller's clock.
type Event struct {
	ID     string    `json:"id"`
	Device string    `json:"device"`
	Action Action    `json:"action"`
	Value  int       `json:"value,omitempty"` // speed level for Speed
	TS     time.Time `json:"ts"`
}

func New(device string, action Action, value int) Event {
	return Event{ID: newID(), Device: device, Action: action, Value: value, TS: time.Now().UTC()}
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (e Event) Validate(now time.Time) error {
	if e.ID == "" || len(e.ID) > 200 || e.Device == "" || len(e.Device) > 200 {
		return fmt.Errorf("id and device are required and must be at most 200 characters")
	}
	if e.TS.Before(time.Unix(0, 0)) || e.TS.After(now.Add(time.Minute)) {
		return fmt.Errorf("invalid event timestamp")
	}
	switch e.Action {
	case Start, Stop, Pause:
		if e.Value != 0 {
			return fmt.Errorf("value is only allowed for speed")
		}
	case Speed:
		if e.Value < 1 || e.Value > 8 {
			return fmt.Errorf("speed must be 1–8")
		}
	default:
		return fmt.Errorf("unknown action %q", e.Action)
	}
	return nil
}
