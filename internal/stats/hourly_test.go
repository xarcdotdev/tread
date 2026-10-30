package stats

import (
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func TestRollingHourlyReport(t *testing.T) {
	for _, stamp := range []string{"2026-10-28T00:30:12.123456789+01:00", "2026-03-29T12:30:00+02:00", "2026-10-25T12:30:00+01:00"} {
		t.Run(stamp, func(t *testing.T) {
			cfg := config.Default()
			cfg.Timezone = "Europe/Berlin"
			cfg.Treadmill.SpeedKMH = map[string]float64{"1": 2}
			cfg.Personal = config.Personal{WeightKG: 80, StepLengthM: .8}
			now, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				t.Fatal(err)
			}
			from, err := PeriodStart("1d", now, cfg.Location())
			if err != nil || now.Sub(from) != 24*time.Hour {
				t.Fatalf("from=%v err=%v", from, err)
			}
			// Clip both ends, split across hourly boundaries, and leave empty hours.
			events := []event.Event{
				{Action: event.Start, TS: from.Add(-time.Hour)},
				{Action: event.Speed, Value: 1, TS: from},
				{Action: event.Stop, TS: from.Add(90 * time.Minute)},
				{Action: event.Start, TS: now.Add(-30 * time.Minute)},
				{Action: event.Stop, TS: now.Add(time.Hour)},
			}
			r := Build(events, now, from, "1d", cfg)
			if len(r.Days) != 24 {
				t.Fatalf("buckets=%d", len(r.Days))
			}
			for i, b := range r.Days {
				key, err := time.Parse(time.RFC3339Nano, b.Date)
				if err != nil || !key.Equal(from.Add(time.Duration(i)*time.Hour)) {
					t.Fatalf("bucket %d: %+v (%v)", i, b, err)
				}
				want := 0.
				switch i {
				case 0:
					want = 1
				case 1, 23:
					want = .5
				}
				if b.Hours != want || b.DistanceKM != want*2 || b.Steps != int64(want*2500) {
					t.Fatalf("bucket %d: %+v", i, b)
				}
			}
			if r.Totals.Hours != 2 || r.Totals.DistanceKM != 4 || r.Totals.Steps != 5000 || r.Totals.AverageSpeed != 2 || r.Totals.Calories != 448 {
				t.Fatalf("totals: %+v", r.Totals)
			}
			if len(r.Sessions) != 2 || r.Sessions[0].Walking != 30*time.Minute || r.Sessions[1].Walking != 90*time.Minute || !r.Sessions[1].Start.Equal(from) || len(r.Pace) != 1 || r.Pace[0].Hours != 2 {
				t.Fatalf("sessions=%+v pace=%+v", r.Sessions, r.Pace)
			}
			empty := Build(nil, now, from, "1d", cfg)
			if len(empty.Days) != 24 || empty.Totals.Hours != 0 {
				t.Fatalf("empty: %+v", empty)
			}
			// A continuous span must survive both DST transitions without merged hours.
			continuous := []event.Event{{Action: event.Start, TS: from}}
			for at := from.Add(90 * time.Minute); at.Before(now); at = at.Add(90 * time.Minute) {
				continuous = append(continuous, event.Event{Action: event.Speed, Value: 1, TS: at})
			}
			full := Build(continuous, now, from, "1d", cfg)
			for _, b := range full.Days {
				if b.Hours != 1 {
					t.Fatalf("full hour: %+v", b)
				}
			}
			if full.Totals.Hours != 24 {
				t.Fatal(full.Totals)
			}
			week, _ := PeriodStart("1w", now, cfg.Location())
			daily := Build(events, now, week, "1w", cfg)
			if len(daily.Days) != 7 {
				t.Fatalf("week buckets=%d", len(daily.Days))
			}
			for _, b := range daily.Days {
				if _, err := time.Parse(time.DateOnly, b.Date); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
