package goals

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
)

func testConfig() config.Config {
	c := config.Default()
	c.Timezone = "America/New_York"
	c.Personal.StepLengthM = 0.5
	c.Personal.WeightKG = 70
	c.Treadmill.SpeedKMH = map[string]float64{"1": 2, "2": 6}
	return c
}
func local(c config.Config, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, c.Location())
	if err != nil {
		panic(err)
	}
	return t
}
func allTargets() Targets {
	return Targets{Steps: 100, Calories: 1, Time: time.Minute, AverageSpeedKMH: 1}
}
func ev(c config.Config, ts string, a event.Action, v int) event.Event {
	return event.Event{Device: "tread", TS: local(c, ts), Action: a, Value: v}
}
func closeTo(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCalendarBoundaries(t *testing.T) {
	c := testConfig()
	for _, tc := range []struct {
		now, day, week, month, year string
		hours                       float64
	}{
		{"2025-01-01 12:00", "2025-01-01", "2024-12-30", "2025-01-01", "2025-01-01", 24},
		{"2024-03-10 12:00", "2024-03-10", "2024-03-04", "2024-03-01", "2024-01-01", 23},
		{"2024-11-03 12:00", "2024-11-03", "2024-10-28", "2024-11-01", "2024-01-01", 25},
		{"2024-04-01 00:00", "2024-04-01", "2024-04-01", "2024-04-01", "2024-01-01", 24},
		{"2024-02-29 23:59", "2024-02-29", "2024-02-26", "2024-02-01", "2024-01-01", 24},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now := local(c, tc.now).UTC() // Boundaries must not follow now's timezone.
			r := Build(nil, now, c, Config{})
			for i, start := range []string{tc.day, tc.week, tc.month, tc.year} {
				p := r.Periods[i]
				if p.ID != []string{"daily", "weekly", "monthly", "yearly"}[i] || p.Label == "" {
					t.Fatal(p)
				}
				if !p.From.Equal(local(c, start+" 00:00")) || p.From.Location().String() != c.Timezone {
					t.Fatal(p.From)
				}
				if p.To.Location().String() != c.Timezone {
					t.Fatal(p.To)
				}
				if p.Metrics == nil || len(p.Metrics) != 0 {
					t.Fatal(p.Metrics)
				}
			}
			closeTo(t, r.Periods[0].To.Sub(r.Periods[0].From).Hours(), tc.hours)
			week := r.Periods[1]
			if !week.To.Equal(week.From.AddDate(0, 0, 7)) {
				t.Fatal(week)
			}
			month := r.Periods[2]
			if !month.To.Equal(month.From.AddDate(0, 1, 0)) {
				t.Fatal(month)
			}
			year := r.Periods[3]
			if !year.To.Equal(year.From.AddDate(1, 0, 0)) {
				t.Fatal(year)
			}
		})
	}
}

func TestClippingWeightedAverageAndExceededTargets(t *testing.T) {
	c := testConfig()
	events := []event.Event{
		ev(c, "2024-04-30 23:30", event.Start, 0),
		ev(c, "2024-05-01 00:30", event.Stop, 0),
		ev(c, "2024-05-01 01:00", event.Speed, 2),
		ev(c, "2024-05-01 01:00", event.Start, 0),
		ev(c, "2024-05-01 02:30", event.Stop, 0),
		ev(c, "2024-05-02 01:00", event.Start, 0), // ignored future event
	}
	now := local(c, "2024-05-01 12:00")
	targets := allTargets()
	r := Build(events, now, c, Config{Daily: targets, Weekly: targets, Monthly: targets, Yearly: targets})
	for _, i := range []int{0, 2} {
		p := r.Periods[i]
		closeTo(t, p.Metrics[0].Current, 20000)
		closeTo(t, p.Metrics[2].Current, 7200)
		closeTo(t, p.Metrics[3].Current, 5) // (0.5h*2 + 1.5h*6) / 2h, not 4
	}
	closeTo(t, r.Periods[1].Metrics[2].Current, 9000)
	for _, p := range r.Periods {
		s := stats.Build(events, now, p.From, p.ID, c)
		closeTo(t, p.Metrics[1].Current, s.Totals.Calories)
		for _, m := range p.Metrics {
			if !m.Available || m.Reason != "" || m.Current <= m.Target || m.Progress != m.Current/m.Target*100 {
				t.Fatal(m)
			}
		}
		if len(p.Warnings) != len(s.Warnings) {
			t.Fatal(p.Warnings)
		}
	}
}

func TestUnavailableAndEmpty(t *testing.T) {
	for _, mode := range []string{"no-profile", "partial-calibration", "empty"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig()
			events := []event.Event{
				ev(c, "2024-05-01 00:00", event.Start, 0),
				ev(c, "2024-05-01 00:30", event.Speed, 2),
				ev(c, "2024-05-01 01:00", event.Stop, 0),
			}
			switch mode {
			case "no-profile":
				c.Personal = config.Personal{}
			case "partial-calibration":
				delete(c.Treadmill.SpeedKMH, "2")
			case "empty":
				events = nil
			}
			now := local(c, "2024-05-01 12:00")
			p := Build(events, now, c, Config{Daily: allTargets()}).Periods[0]
			s := stats.Build(events, now, p.From, p.ID, c)
			expected := []bool{s.StepsAvailable, s.CaloriesAvailable, true, s.DistanceAvailable && s.Totals.Hours > 0}
			for i, m := range p.Metrics {
				if m.Available != expected[i] {
					t.Fatal(m)
				}
				if !m.Available && (m.Progress != 0 || m.Reason == "") {
					t.Fatal(m)
				}
			}
			for _, warning := range s.Warnings {
				if !strings.Contains(strings.Join(p.Warnings, "\n"), warning) {
					t.Fatal(p.Warnings)
				}
			}
			if mode == "empty" && !strings.Contains(strings.Join(p.Warnings, "\n"), "No walking") {
				t.Fatal(p.Warnings)
			}
			if mode == "partial-calibration" && p.Metrics[0].Current <= 0 {
				t.Fatal("partial estimates should remain visible")
			}
		})
	}
}

func TestSessionSemantics(t *testing.T) {
	c := testConfig()
	now := local(c, "2024-05-01 12:00")
	cases := []struct {
		name   string
		events []event.Event
		hours  float64
	}{
		{"running timeout", []event.Event{ev(c, "2024-05-01 00:00", event.Start, 0)}, 2},
		{"paused time excluded", []event.Event{
			ev(c, "2024-05-01 00:00", event.Start, 0),
			ev(c, "2024-05-01 00:30", event.Pause, 0),
			ev(c, "2024-05-01 00:32", event.Start, 0),
			ev(c, "2024-05-01 01:02", event.Stop, 0),
		}, 1},
		{"before period excluded", []event.Event{
			ev(c, "2024-04-30 22:00", event.Start, 0),
			ev(c, "2024-04-30 23:00", event.Stop, 0),
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(tc.events, now, c, Config{Daily: Targets{Time: time.Hour}})
			closeTo(t, r.Periods[0].Metrics[0].Current, tc.hours*3600)
		})
	}
}

func TestRunningSessionDSTAndProgress(t *testing.T) {
	c := testConfig()
	now := local(c, "2024-03-10 03:30")
	events := []event.Event{ev(c, "2024-03-10 01:30", event.Start, 0)}
	r := Build(events, now, c, Config{Daily: Targets{Time: 2 * time.Hour}})
	m := r.Periods[0].Metrics[0]
	closeTo(t, m.Current, 3600)
	closeTo(t, m.Progress, 50)
	if m.Unit != "seconds" {
		t.Fatal(m)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"metrics":[]`) || !strings.Contains(string(data), `"reason":""`) {
		t.Fatal(string(data))
	}
}
