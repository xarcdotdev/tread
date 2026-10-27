// Package stats derives walking sessions from control events.
// The DFT200 reports nothing back, so everything here is an estimate
// based on what the controllers sent.
package stats

import (
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

type Options struct {
	// PauseTimeout ends a session after it has been paused this long (the treadmill sleeps).
	PauseTimeout time.Duration
	// MaxRunning caps a running stretch without any events (missed stop, remote control).
	MaxRunning time.Duration
	// KmhPerLevel converts a speed level to km/h. TODO: measure the real DFT200 values.
	KmhPerLevel func(level int) float64
}

func DefaultOptions() Options {
	return Options{
		PauseTimeout: 270 * time.Second,
		MaxRunning:   2 * time.Hour,
		KmhPerLevel:  func(level int) float64 { return float64(level) },
	}
}

type Session struct {
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	Walking  time.Duration `json:"walking_ns"`
	Distance float64       `json:"distance_km"`
}

// Sessions builds sessions from events sorted by TS. now closes a session that is still open.
func Sessions(events []event.Event, now time.Time, opt Options) []Session {
	var (
		out     []Session
		cur     *Session
		running bool
		level   = 1 // the treadmill resumes at the last level; assume 1 when unknown
		since   time.Time
	)

	// advance accounts walking time up to t.
	advance := func(t time.Time) {
		if cur == nil || !running {
			return
		}
		d := t.Sub(since)
		if d > opt.MaxRunning {
			d = opt.MaxRunning
		}
		if d > 0 {
			cur.Walking += d
			cur.Distance += opt.KmhPerLevel(level) * d.Hours()
		}
		since = t
	}
	end := func(t time.Time) {
		advance(t)
		cur.End = t
		out = append(out, *cur)
		cur, running = nil, false
	}
	// expire ends the session if it timed out before t.
	expire := func(t time.Time) {
		if cur == nil {
			return
		}
		switch {
		case !running && t.Sub(since) > opt.PauseTimeout:
			end(since)
		case running && t.Sub(since) > opt.MaxRunning:
			end(since.Add(opt.MaxRunning))
		}
	}

	for _, e := range events {
		expire(e.TS)
		switch e.Action {
		case event.Start:
			if cur == nil {
				cur = &Session{Start: e.TS}
			}
			advance(e.TS)
			running, since = true, e.TS
		case event.Speed:
			advance(e.TS)
			level = e.Value
		case event.Pause:
			advance(e.TS)
			running, since = false, e.TS
		case event.Stop:
			if cur != nil {
				end(e.TS)
			}
		}
	}
	expire(now)
	if cur != nil {
		end(now)
	}
	return out
}

type Day struct {
	Date     string        `json:"date"` // YYYY-MM-DD in loc
	Walking  time.Duration `json:"walking_ns"`
	Distance float64       `json:"distance_km"`
}

// Daily sums sessions per local day (by session start).
func Daily(sessions []Session, loc *time.Location) []Day {
	var out []Day
	for _, s := range sessions {
		date := s.Start.In(loc).Format(time.DateOnly)
		if len(out) == 0 || out[len(out)-1].Date != date {
			out = append(out, Day{Date: date})
		}
		d := &out[len(out)-1]
		d.Walking += s.Walking
		d.Distance += s.Distance
	}
	return out
}
