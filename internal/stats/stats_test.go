package stats

import (
	"math"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

var t0 = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func ev(min int, a event.Action, v int) event.Event {
	return event.Event{Action: a, Value: v, TS: t0.Add(time.Duration(min) * time.Minute)}
}

func TestSessions(t *testing.T) {
	tests := []struct {
		name     string
		events   []event.Event
		sessions int
		walking  time.Duration
		distance float64
	}{
		{
			name:     "start speed stop",
			events:   []event.Event{ev(0, event.Start, 0), ev(0, event.Speed, 4), ev(30, event.Stop, 0)},
			sessions: 1, walking: 30 * time.Minute, distance: 2,
		},
		{
			name:     "short pause keeps session",
			events:   []event.Event{ev(0, event.Start, 0), ev(0, event.Speed, 2), ev(30, event.Pause, 0), ev(32, event.Start, 0), ev(62, event.Stop, 0)},
			sessions: 1, walking: 60 * time.Minute, distance: 2,
		},
		{
			name:     "long pause splits sessions",
			events:   []event.Event{ev(0, event.Start, 0), ev(0, event.Speed, 2), ev(30, event.Pause, 0), ev(60, event.Start, 0), ev(90, event.Stop, 0)},
			sessions: 2, walking: 60 * time.Minute, distance: 2,
		},
		{
			name:     "missed stop is capped",
			events:   []event.Event{ev(0, event.Start, 0), ev(0, event.Speed, 1)},
			sessions: 1, walking: 2 * time.Hour, distance: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sessions(tt.events, t0.Add(24*time.Hour), DefaultOptions())
			if len(got) != tt.sessions {
				t.Fatalf("got %d sessions, want %d: %+v", len(got), tt.sessions, got)
			}
			var walking time.Duration
			var distance float64
			for _, s := range got {
				walking += s.Walking
				distance += s.Distance
			}
			if walking != tt.walking {
				t.Errorf("walking = %v, want %v", walking, tt.walking)
			}
			if math.Abs(distance-tt.distance) > 1e-9 {
				t.Errorf("distance = %v, want %v", distance, tt.distance)
			}
		})
	}
}
