package stats

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

type Metrics struct {
	Hours        float64 `json:"hours"`
	DistanceKM   float64 `json:"distance_km"`
	AverageSpeed float64 `json:"average_speed_kmh"`
	Steps        int64   `json:"steps"`
	Calories     float64 `json:"calories"`
}

type Bucket struct {
	Date string `json:"date"`
	Metrics
}

type Pace struct {
	Level int     `json:"level"`
	KMH   float64 `json:"kmh"`
	Hours float64 `json:"hours"`
}

type Report struct {
	Period            string    `json:"period"`
	From              time.Time `json:"from"`
	To                time.Time `json:"to"`
	Totals            Metrics   `json:"totals"`
	Days              []Bucket  `json:"days"`
	Sessions          []Session `json:"sessions"`
	Pace              []Pace    `json:"pace"`
	Warnings          []string  `json:"warnings"`
	DistanceAvailable bool      `json:"distance_available"`
	StepsAvailable    bool      `json:"steps_available"`
	CaloriesAvailable bool      `json:"calories_available"`
}

// CalendarBoundary resolves a civil date to its first valid instant in loc.
// A missing midnight uses the forward transition; a repeated midnight uses its
// first occurrence. A wholly skipped date resolves to the next valid date.
func CalendarBoundary(year int, month time.Month, day int, loc *time.Location) time.Time {
	civil := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	// IANA offsets are less than 24 hours. Walk zone intervals in chronological
	// order rather than relying on time.Date's choice for gaps and overlaps.
	for cursor := civil.Add(-48 * time.Hour).In(loc); ; {
		_, offset := cursor.Zone()
		_, end := cursor.ZoneBounds()
		if !end.IsZero() && !end.After(cursor) {
			// Go's POSIX-rule extrapolation can report Jan 1 + 365 days
			// as the end of the year's final zone, even in a leap year.
			// Repair that synthetic bound to the actual UTC year end.
			// Keep cursor unchanged so a boundary within Dec 31 is not lost.
			end = time.Date(cursor.UTC().Year()+1, 1, 1, 0, 0, 0, 0, time.UTC).In(loc)
		}
		candidate := civil.Add(-time.Duration(offset) * time.Second).In(loc)
		if candidate.Before(cursor) {
			candidate = cursor
		}
		if end.IsZero() || candidate.Before(end) {
			return candidate
		}
		cursor = end
	}
}

func PeriodStart(period string, now time.Time, loc *time.Location) (time.Time, error) {
	n := now.In(loc)
	// Do date arithmetic in UTC, then resolve the intended local boundary once.
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	resolve := func(d time.Time) time.Time { return CalendarBoundary(d.Year(), d.Month(), d.Day(), loc) }
	switch period {
	case "all":
		return CalendarBoundary(1970, 1, 1, loc), nil
	case "3y":
		return resolve(subtractMonths(day, 36)), nil
	case "1y":
		return resolve(subtractMonths(day, 12)), nil
	case "3m":
		return resolve(subtractMonths(day, 3)), nil
	case "1m":
		return resolve(subtractMonths(day, 1)), nil
	case "1w":
		return resolve(day.AddDate(0, 0, -6)), nil
	case "1d":
		return n.Add(-24 * time.Hour), nil
	}
	return time.Time{}, fmt.Errorf("period must be all, 3y, 1y, 3m, 1m, 1w or 1d")
}

// MET values for level treadmill walking, 2024 Adult Compendium.
// https://pacompendium.com/walking/
func met(kmh float64) float64 {
	limits := []float64{1.6, 1.9, 3.2, 4, 4.8, 5.6, 6.4, 7.2, 8, 9}
	values := []float64{2.1, 2.3, 2.8, 3, 3.5, 3.8, 4.8, 5.8, 6.8, 8.3}
	for i, v := range limits {
		if kmh < v {
			return values[i]
		}
	}
	return 8.3
}

// Resting kcal/hour: use Mifflin-St Jeor when the full profile is supplied,
// otherwise use the conventional MET baseline of 1 kcal/kg/hour.
func resting(p config.Personal, now time.Time) float64 {
	if p.WeightKG <= 0 {
		return 0
	}
	age, valid := p.AgeAt(now)
	if p.HeightCM > 0 && valid && age > 0 && (p.Gender == "male" || p.Gender == "female") {
		offset := 5.
		if p.Gender == "female" {
			offset = -161
		}
		return math.Max(0, (10*p.WeightKG+6.25*p.HeightCM-5*float64(age)+offset)/24)
	}
	return p.WeightKG
}

func Build(events []event.Event, now, from time.Time, period string, cfg config.Config) Report {
	r := Report{Period: period, From: from, To: now, Days: []Bucket{}, Sessions: []Session{}, Pace: []Pace{}, Warnings: []string{}, DistanceAvailable: true}
	opt := DefaultOptions()
	opt.InitialLevel = cfg.Treadmill.InitialLevel
	opt.KmhPerLevel = func(level int) float64 { return cfg.Treadmill.SpeedKMH[strconv.Itoa(level)] }
	// Controllers of the same treadmill must share an identifier. Other devices
	// have separate state so their start/stop events cannot alter this treadmill.
	groups := map[string][]event.Event{}
	for _, e := range events {
		groups[e.Device] = append(groups[e.Device], e)
	}
	dayMap := map[string]*Bucket{}
	paceMap := map[int]*Pace{}
	stepLength := cfg.Personal.StepLengthM
	if stepLength == 0 && cfg.Personal.HeightCM > 0 {
		stepLength = cfg.Personal.HeightCM / 100 * 0.414
	}
	// Use the report date's age for the entire history, like the current profile
	// and calibration. Do not age the profile separately for each walking day.
	rest := resting(cfg.Personal, now.In(cfg.Location()))
	var steps float64
	for _, es := range groups {
		for _, s := range Sessions(es, now, opt) {
			clipped := Session{Start: s.Start, End: s.End, Device: s.Device}
			if clipped.Start.Before(from) {
				clipped.Start = from
			}
			if !s.End.After(from) {
				continue
			}
			for _, span := range s.Spans {
				a, b := span.Start, span.End
				if a.Before(from) {
					a = from
				}
				if b.After(now) {
					b = now
				}
				for a.Before(b) {
					local := a.In(cfg.Location())
					boundary := CalendarBoundary(local.Year(), local.Month(), local.Day()+1, cfg.Location())
					date := local.Format(time.DateOnly)
					if period == "1d" {
						// Elapsed-hour intervals anchored at from, not civil hours:
						// DST gaps and overlaps still produce exactly 24 buckets.
						start := from.Add(a.Sub(from).Truncate(time.Hour))
						boundary = start.Add(time.Hour)
						date = start.UTC().Format(time.RFC3339Nano)
					}
					end := b
					if boundary.Before(end) {
						end = boundary
					}
					h := end.Sub(a).Hours()
					km := h * span.KMH
					bucket := dayMap[date]
					if bucket == nil {
						bucket = &Bucket{Date: date}
						dayMap[date] = bucket
					}
					bucket.Hours += h
					bucket.DistanceKM += km
					if span.KMH <= 0 {
						r.DistanceAvailable = false
					} else {
						bucket.Calories += met(span.KMH) * rest * h
					}
					if stepLength > 0 {
						steps += km * 1000 / stepLength
						bucket.Steps = int64(math.Round(float64(bucket.Steps) + km*1000/stepLength))
					}
					pace := paceMap[span.Level]
					if pace == nil {
						pace = &Pace{Level: span.Level, KMH: span.KMH}
						paceMap[span.Level] = pace
					}
					pace.Hours += h
					clipped.Walking += end.Sub(a)
					clipped.Distance += km
					a = end
				}
			}
			if clipped.Walking > 0 {
				r.Sessions = append(r.Sessions, clipped)
			}
		}
	}
	appendBucket := func(key string) {
		bucket := dayMap[key]
		if bucket == nil {
			bucket = &Bucket{Date: key}
		}
		if bucket.Hours > 0 {
			bucket.AverageSpeed = bucket.DistanceKM / bucket.Hours
		}
		r.Days = append(r.Days, *bucket)
		r.Totals.Hours += bucket.Hours
		r.Totals.DistanceKM += bucket.DistanceKM
		r.Totals.Calories += bucket.Calories
	}
	if period == "1d" {
		for d := from; d.Before(now); d = d.Add(time.Hour) {
			appendBucket(d.UTC().Format(time.RFC3339Nano))
		}
	} else {
		// Include zero days so gaps are visible in the activity graph.
		start := from
		if period == "all" {
			start = now
			for _, d := range dayMap {
				civil, _ := time.Parse(time.DateOnly, d.Date)
				t := CalendarBoundary(civil.Year(), civil.Month(), civil.Day(), cfg.Location())
				if t.Before(start) {
					start = t
				}
			}
		}
		n := start.In(cfg.Location())
		start = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
		for d := start; ; d = d.AddDate(0, 0, 1) {
			boundary := CalendarBoundary(d.Year(), d.Month(), d.Day(), cfg.Location())
			if boundary.After(now) {
				break
			}
			key := d.Format(time.DateOnly)
			if boundary.Format(time.DateOnly) != key {
				continue // A wholly skipped local date has no bucket.
			}
			appendBucket(key)
		}
	}
	if r.Totals.Hours > 0 {
		r.Totals.AverageSpeed = r.Totals.DistanceKM / r.Totals.Hours
	}
	r.Totals.Steps = int64(math.Round(steps))
	for _, p := range paceMap {
		r.Pace = append(r.Pace, *p)
	}
	sort.Slice(r.Pace, func(i, j int) bool { return r.Pace[i].Level < r.Pace[j].Level })
	sort.Slice(r.Sessions, func(i, j int) bool { return r.Sessions[i].Start.After(r.Sessions[j].Start) })
	r.StepsAvailable = r.DistanceAvailable && stepLength > 0
	r.CaloriesAvailable = r.DistanceAvailable && rest > 0
	if !cfg.Treadmill.Calibrated {
		r.Warnings = append(r.Warnings, "Speed calibration is unconfirmed. Measure belt speeds, fill treadmill.speed_kmh and set calibrated = true.")
	}
	if !r.DistanceAvailable {
		r.Warnings = append(r.Warnings, "Some used levels have no speed calibration. Distance, average speed, steps and calories are incomplete.")
	}
	if stepLength == 0 {
		r.Warnings = append(r.Warnings, "Set personal.step_length_m or height_cm to estimate steps.")
	} else if cfg.Personal.StepLengthM == 0 {
		r.Warnings = append(r.Warnings, "Steps use an approximate step length of 41.4% of height. Set step_length_m for a better estimate.")
	}
	if rest == 0 {
		r.Warnings = append(r.Warnings, "Set personal.weight_kg to estimate calories.")
	}
	return r
}

func subtractMonths(day time.Time, months int) time.Time {
	first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location()).AddDate(0, -months, 0)
	last := first.AddDate(0, 1, -1).Day()
	d := min(day.Day(), last)
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, day.Location())
}
