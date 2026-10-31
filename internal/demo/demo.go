// Package demo generates repeatable control history without touching Bluetooth.
package demo

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

func Config() config.Config {
	c := config.Default()
	c.Personal = config.Personal{WeightKG: 75, Birthdate: "1994-01-01", HeightCM: 178, Gender: "male", StepLengthM: 0.74}
	c.Treadmill.Calibrated = true
	c.Path = "Demo settings (in memory)"
	for _, n := range speedLevels().Levels() {
		c.Treadmill.SpeedKMH[fmt.Sprint(n)] = float64(n) * 0.65
	}
	return c
}

// speedLevels is the demo driver's range; the default driver always has one.
func speedLevels() observed.LevelRange {
	levels, err := config.Default().Treadmill.SpeedLevels()
	if err != nil {
		panic(err)
	}
	return levels
}

func Events(now time.Time, loc *time.Location) []event.Event {
	rng := rand.New(rand.NewSource(42))
	out := []event.Event{}
	today := now.In(loc)
	for day := 1200; day >= 0; day-- {
		d := today.AddDate(0, 0, -day)
		if day != 0 && (d.Weekday() == time.Sunday || rng.Intn(7) == 0) {
			continue
		}
		for n := 0; n < 1+rng.Intn(3); n++ {
			start := time.Date(d.Year(), d.Month(), d.Day(), 8+n*3, rng.Intn(40), 0, 0, loc)
			if day == 0 {
				start = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).Add(time.Duration(n*100) * time.Minute)
			}
			level := 2 + rng.Intn(4)
			duration := time.Duration(25+rng.Intn(65)) * time.Minute
			add := func(t time.Time, a event.Action, v int) {
				if !t.After(now) {
					out = append(out, event.Event{ID: fmt.Sprintf("demo-%d-%d-%s", day, n, a), Device: "dft200", Action: a, Value: v, TS: t.UTC()})
				}
			}
			add(start, event.Start, 0)
			add(start, event.Speed, level)
			add(start.Add(duration/2), event.Pause, 0)
			// Resume gets a distinct ID to preserve both starts in SQLite.
			resume := event.Event{ID: fmt.Sprintf("demo-%d-%d-resume", day, n), Device: "dft200", Action: event.Start, TS: start.Add(duration/2 + 2*time.Minute).UTC()}
			if !resume.TS.After(now) {
				out = append(out, resume)
			}
			add(start.Add(duration+2*time.Minute), event.Stop, 0)
		}
	}
	return out
}
