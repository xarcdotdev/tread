package stats

import (
	"math"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func TestReportClipsMidnightAndPeriodBoundary(t *testing.T) {
	cfg := config.Default()
	cfg.Timezone = "Europe/Berlin"
	cfg.Personal = config.Personal{WeightKG: 80, StepLengthM: .8}
	cfg.Treadmill.SpeedKMH = map[string]float64{"1": 2, "2": 4}
	start := time.Date(2026, 10, 27, 23, 30, 0, 0, cfg.Location())
	midnight := start.Add(30 * time.Minute)
	stop := start.Add(time.Hour)
	events := []event.Event{{Device: "desk", Action: event.Start, TS: start}, {Device: "desk", Action: event.Speed, Value: 2, TS: midnight}, {Device: "desk", Action: event.Stop, TS: stop}}
	r := Build(events, stop, midnight, "1d", cfg)
	// Only the half hour AFTER midnight belongs to today, at 4 km/h.
	if r.Totals.Hours != .5 || r.Totals.DistanceKM != 2 || r.Totals.AverageSpeed != 4 || r.Totals.Steps != 2500 {
		t.Fatalf("wrong clipped totals: %+v", r.Totals)
	}
	if r.Totals.Calories != 140 {
		t.Fatalf("expected half hour × 3.5 MET × 80 kg, got %v", r.Totals.Calories)
	}
	whole := Build(events, stop, start, "all", cfg)
	if len(whole.Days) != 2 || whole.Days[0].DistanceKM != 1 || whole.Days[1].DistanceKM != 2 {
		t.Fatalf("wrong daily split: %+v", whole.Days)
	}
}
func TestReportSeparatesDevicesAndHandlesMissingCalibration(t *testing.T) {
	cfg := config.Default()
	cfg.Treadmill.SpeedKMH = map[string]float64{"1": 2}
	events := []event.Event{{Device: "a", Action: event.Start, TS: t0}, {Device: "b", Action: event.Start, TS: t0.Add(10 * time.Minute)}, {Device: "a", Action: event.Stop, TS: t0.Add(30 * time.Minute)}, {Device: "b", Action: event.Stop, TS: t0.Add(40 * time.Minute)}}
	r := Build(events, t0.Add(time.Hour), t0, "1d", cfg)
	if len(r.Sessions) != 2 || r.Totals.Hours != 1 || r.Totals.DistanceKM != 2 {
		t.Fatalf("device histories interfere: %+v", r)
	}
	if r.StepsAvailable || r.CaloriesAvailable {
		t.Fatal("unknown personal metrics must not imply zero steps/calories")
	}
	cfg.Treadmill.SpeedKMH = map[string]float64{}
	r = Build(events, t0.Add(time.Hour), t0, "1d", cfg)
	if r.DistanceAvailable || r.Totals.Hours != 1 {
		t.Fatalf("missing calibration must preserve time but disable derived metrics: %+v", r)
	}
}
func TestReportSpeedChangesAndPause(t *testing.T) {
	cfg := config.Default()
	cfg.Treadmill.SpeedKMH = map[string]float64{"1": 2, "2": 4}
	cfg.Personal = config.Personal{WeightKG: 80, HeightCM: 180, Age: 40, Gender: "male", StepLengthM: .75}
	events := []event.Event{ev(0, event.Start, 0), ev(15, event.Speed, 2), ev(30, event.Pause, 0), ev(32, event.Start, 0), ev(47, event.Stop, 0)}
	r := Build(events, t0.Add(time.Hour), t0, "1d", cfg)
	if r.Totals.Hours != .75 || r.Totals.DistanceKM != 2.5 || math.Abs(r.Totals.AverageSpeed-10./3) > 1e-9 {
		t.Fatalf("wrong time-weighted totals: %+v", r.Totals)
	}
	// Personalized resting energy = 1730 kcal/day. 15 min at MET 2.8,
	// then 30 min at MET 3.5. Pause contributes no calories.
	want := (.25*2.8 + .5*3.5) * 1730 / 24
	if math.Abs(r.Totals.Calories-want) > 1e-9 {
		t.Fatalf("calories = %v, want %v", r.Totals.Calories, want)
	}
}
func TestDaylightSavingWalkingDuration(t *testing.T) {
	cfg := config.Default()
	cfg.Timezone = "Europe/Berlin"
	cfg.Treadmill.SpeedKMH = map[string]float64{"1": 2}
	// Europe/Berlin's autumn clock change makes this a three-hour real interval.
	start := time.Date(2026, 10, 25, 1, 30, 0, 0, cfg.Location())
	stop := time.Date(2026, 10, 25, 3, 30, 0, 0, cfg.Location())
	events := []event.Event{{Action: event.Start, TS: start}, {Action: event.Speed, Value: 1, TS: start.Add(90 * time.Minute)}, {Action: event.Stop, TS: stop}}
	r := Build(events, stop, start, "1d", cfg)
	if r.Totals.Hours != 3 || r.Totals.DistanceKM != 6 {
		t.Fatalf("DST should use elapsed time: %+v", r.Totals)
	}
}

func TestMidnightTransitionSplitting(t *testing.T) {
	for _, tc := range []struct {
		zone, start, stop, first, second string
		firstHours                       float64
	}{
		{"America/Santiago", "2024-09-07T23:30:00-04:00", "2024-09-08T01:30:00-03:00", "2024-09-07", "2024-09-08", .5},
		{"Africa/Cairo", "2024-04-25T23:30:00+02:00", "2024-04-26T01:30:00+03:00", "2024-04-25", "2024-04-26", .5},
		{"America/Santiago", "2024-04-06T23:30:00-03:00", "2024-04-07T00:30:00-04:00", "2024-04-06", "2024-04-07", 1.5},
		{"Africa/Cairo", "2024-10-31T23:30:00+03:00", "2024-11-01T00:30:00+02:00", "2024-10-31", "2024-11-01", 1.5},
	} {
		t.Run(tc.zone+tc.start, func(t *testing.T) {
			cfg := config.Default()
			cfg.Timezone = tc.zone
			start, _ := time.Parse(time.RFC3339, tc.start)
			stop, _ := time.Parse(time.RFC3339, tc.stop)
			events := []event.Event{{Action: event.Start, TS: start}, {Action: event.Stop, TS: stop}}
			r := Build(events, stop, start, "all", cfg)
			if len(r.Days) != 2 || r.Days[0].Date != tc.first || r.Days[1].Date != tc.second || r.Days[0].Hours != tc.firstHours || r.Days[1].Hours != .5 || r.Totals.Hours != tc.firstHours+.5 {
				t.Fatalf("wrong midnight split: %+v, totals %+v", r.Days, r.Totals)
			}
		})
	}
}

func TestMidnightRollingStartsAndEmptyDays(t *testing.T) {
	for _, zone := range []string{"America/Santiago", "Africa/Cairo"} {
		t.Run(zone, func(t *testing.T) {
			cfg := config.Default()
			cfg.Timezone = zone
			date := "2024-09-08"
			if zone == "Africa/Cairo" {
				date = "2024-04-26"
			}
			now, _ := time.ParseInLocation("2006-01-02 15:04", date+" 12:00", cfg.Location())
			start, err := PeriodStart("1d", now, cfg.Location())
			if err != nil || start.Format("2006-01-02 15:04") != date+" 01:00" {
				t.Fatalf("%v %v", start, err)
			}
			week, _ := PeriodStart("1w", now, cfg.Location())
			if week.Hour() != 0 || week.Day() != now.Day()-6 {
				t.Fatal(week)
			}
			// Include the next day at 00:30: iterating from 01:00 must not omit it.
			next := CalendarBoundary(now.Year(), now.Month(), now.Day()+1, cfg.Location()).Add(30 * time.Minute)
			r := Build(nil, next, week, "1w", cfg)
			if len(r.Days) != 8 || r.Days[6].Date != date || r.Days[7].Date != next.Format(time.DateOnly) {
				t.Fatal(r.Days)
			}
		})
	}
}

func TestRepeatedMidnightUsesFirstOccurrence(t *testing.T) {
	loc, err := time.LoadLocation("America/Havana")
	if err != nil {
		t.Fatal(err)
	}
	got := CalendarBoundary(2024, 11, 3, loc)
	if got.Format(time.RFC3339) != "2024-11-03T00:00:00-04:00" {
		t.Fatal(got)
	}
}

func TestCalendarMonthRange(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	got, err := PeriodStart("1m", now, time.UTC)
	if err != nil || got.Format(time.DateOnly) != "2026-02-28" {
		t.Fatalf("March month range: %v %v", got, err)
	}
}
