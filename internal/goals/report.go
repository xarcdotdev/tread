package goals

import (
	"math"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
)

type Report struct {
	Periods []Period `json:"periods"`
}

// Period covers [From, To), the full local calendar period. Metrics include only
// activity through the Build timestamp, not future events.
type Period struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	FromDate    string    `json:"from_date"`
	ThroughDate string    `json:"through_date"`
	Metrics     []Metric  `json:"metrics"`
	Warnings    []string  `json:"warnings"`
}

// Metric.Current remains uncapped; Progress is a percentage in [0,100].
// Unavailable metrics have zero progress; Current may be a partial estimate.
type Metric struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Current   float64 `json:"current"`
	Target    float64 `json:"target"`
	Unit      string  `json:"unit"`
	Progress  float64 `json:"progress"`
	Available bool    `json:"available"`
	Reason    string  `json:"reason"`
}

// Build returns daily, weekly (Monday start), monthly and yearly periods in that
// order, including empty metrics arrays for unconfigured periods. cfg must have
// a valid timezone. Session reconstruction and availability belong to stats.
func Build(events []event.Event, now time.Time, cfg config.Config, targets Config) Report {
	n := now.In(cfg.Location())
	// UTC values carry civil dates only; resolve every endpoint independently.
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	week := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	month := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
	year := time.Date(n.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	resolve := func(d time.Time) time.Time {
		return stats.CalendarBoundary(d.Year(), d.Month(), d.Day(), cfg.Location())
	}
	r := Report{Periods: make([]Period, 0, 4)}
	for _, p := range []struct {
		id, label string
		from, to  time.Time
		targets   Targets
	}{
		{"daily", "Daily", day, day.AddDate(0, 0, 1), targets.Daily},
		{"weekly", "Weekly", week, week.AddDate(0, 0, 7), targets.Weekly},
		{"monthly", "Monthly", month, month.AddDate(0, 1, 0), targets.Monthly},
		{"yearly", "Yearly", year, year.AddDate(1, 0, 0), targets.Yearly},
	} {
		from, to := resolve(p.from), resolve(p.to)
		s := stats.Build(events, now, from, p.id, cfg)
		out := Period{ID: p.id, Label: p.label, From: from, To: to,
			FromDate: from.Format(time.DateOnly), ThroughDate: to.Add(-time.Nanosecond).Format(time.DateOnly),
			Metrics: []Metric{}, Warnings: s.Warnings}
		add := func(id, label, unit string, current, target float64, available bool, reason string) {
			if target <= 0 {
				return
			}
			m := Metric{ID: id, Label: label, Unit: unit, Current: current, Target: target, Available: available}
			if available {
				m.Progress = math.Min(100, math.Max(0, current/target*100))
			} else {
				m.Reason = reason
			}
			out.Metrics = append(out.Metrics, m)
		}
		stepsReason := "Set personal.step_length_m or height_cm to estimate steps."
		caloriesReason := "Set personal.weight_kg to estimate calories."
		speedReason := "No walking recorded in this period."
		if !s.DistanceAvailable {
			stepsReason = "Some used levels have no speed calibration; steps are incomplete."
			caloriesReason = "Some used levels have no speed calibration; calories are incomplete."
			speedReason = "Some used levels have no speed calibration; average speed is incomplete."
		}
		add("steps", "Steps", "steps", float64(s.Totals.Steps), float64(p.targets.Steps), s.StepsAvailable, stepsReason)
		add("calories", "Calories", "kcal", s.Totals.Calories, p.targets.Calories, s.CaloriesAvailable, caloriesReason)
		add("time", "Walking time", "seconds", s.Totals.Hours*3600, p.targets.Time.Seconds(), true, "")
		add("average_speed_kmh", "Average speed", "km/h", s.Totals.AverageSpeed, p.targets.AverageSpeedKMH, s.DistanceAvailable && s.Totals.Hours > 0, speedReason)
		if p.targets.AverageSpeedKMH > 0 && s.Totals.Hours == 0 {
			out.Warnings = append(out.Warnings, "No walking recorded in this period; average speed is unavailable.")
		}
		r.Periods = append(r.Periods, out)
	}
	return r
}
