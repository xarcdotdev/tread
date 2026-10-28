// Package stats derives walking sessions from control events.
// Telemetry is not decoded. Estimates use the commands recorded by controllers.
package stats

import (
	"sort"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

type Options struct {
	// PauseTimeout ends a session after it has been paused this long (the treadmill sleeps).
	PauseTimeout time.Duration
	// MaxRunning caps a running stretch without any events (missed stop, remote control).
	MaxRunning   time.Duration
	InitialLevel int
	// KmhPerLevel converts a speed level using user calibration.
	KmhPerLevel func(level int) float64
}

func DefaultOptions() Options {
	return Options{
		InitialLevel: 1,
		PauseTimeout: 270 * time.Second,
		MaxRunning:   2 * time.Hour,
		KmhPerLevel:  func(level int) float64 { return float64(level) },
	}
}

type Span struct {
	Start, End time.Time
	Level      int
	KMH        float64
}

type Session struct {
	Device   string        `json:"device"`
	Spans    []Span        `json:"-"`
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
		level   = opt.InitialLevel // the treadmill resumes at the last level; assume 1 when unknown
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
			speed := opt.KmhPerLevel(level)
			cur.Distance += speed * d.Hours()
			cur.Spans = append(cur.Spans, Span{Start: since, End: since.Add(d), Level: level, KMH: speed})
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

	events = append([]event.Event(nil), events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].TS.Before(events[j].TS) })
	for _, e := range events {
		if e.TS.After(now) {
			continue
		}
		expire(e.TS)
		switch e.Action {
		case event.Start:
			if cur == nil {
				cur = &Session{Start: e.TS, Device: e.Device}
			}
			advance(e.TS)
			running, since = true, e.TS
		case event.Speed:
			advance(e.TS)
			level = e.Value
		case event.Pause:
			if running {
				advance(e.TS)
				running, since = false, e.TS
			}
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
