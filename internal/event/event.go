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
