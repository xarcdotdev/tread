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

// estimates are the profile-derived factors applied to every walking interval.
// A zero factor disables the corresponding metric.
type estimates struct {
	stepLength float64 // metres per step
	rest       float64 // resting kcal per hour
}

func newEstimates(p config.Personal, now time.Time) estimates {
	e := estimates{stepLength: p.StepLengthM, rest: resting(p, now)}
	if e.stepLength == 0 && p.HeightCM > 0 {
		e.stepLength = p.HeightCM / 100 * 0.414
	}
	return e
}

// buckets groups walking by local calendar day, or by elapsed hour from `from`
// for the rolling 1d period.
type buckets struct {
	hourly bool
	from   time.Time
	loc    *time.Location
}

// slot returns the key of the bucket containing t and the first instant after it.
func (b buckets) slot(t time.Time) (key string, end time.Time) {
	if b.hourly {
		// Elapsed-hour intervals anchored at from, not civil hours:
		// DST gaps and overlaps still produce exactly 24 buckets.
		start := b.from.Add(t.Sub(b.from).Truncate(time.Hour))
		return start.UTC().Format(time.RFC3339Nano), start.Add(time.Hour)
	}
	local := t.In(b.loc)
	return local.Format(time.DateOnly), CalendarBoundary(local.Year(), local.Month(), local.Day()+1, b.loc)
}

// keys lists every bucket from start through now, including empty ones so gaps
// are visible in the activity graph. start is ignored for hourly buckets.
func (b buckets) keys(start, now time.Time) []string {
	var keys []string
	if b.hourly {
		for d := b.from; d.Before(now); d = d.Add(time.Hour) {
			keys = append(keys, d.UTC().Format(time.RFC3339Nano))
		}
		return keys
	}
	n := start.In(b.loc)
	for d := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC); ; d = d.AddDate(0, 0, 1) {
		boundary := CalendarBoundary(d.Year(), d.Month(), d.Day(), b.loc)
		if boundary.After(now) {
			return keys
		}
		key := d.Format(time.DateOnly)
		if boundary.Format(time.DateOnly) != key {
			continue // A wholly skipped local date has no bucket.
		}
		keys = append(keys, key)
	}
}

// tally accumulates clipped walking intervals into buckets and pace rows.
type tally struct {
	est          estimates
	days         map[string]*Bucket
	pace         map[int]*Pace
	steps        float64
	uncalibrated bool // some walked level has no speed calibration
}

func newTally(est estimates) *tally {
	return &tally{est: est, days: map[string]*Bucket{}, pace: map[int]*Pace{}}
}

// add accounts hours walked within one span to bucket key and returns the
// distance covered.
func (t *tally) add(key string, span Span, hours float64) float64 {
	km := hours * span.KMH
	bucket := t.days[key]
	if bucket == nil {
		bucket = &Bucket{Date: key}
		t.days[key] = bucket
	}
	bucket.Hours += hours
	bucket.DistanceKM += km
	if span.KMH <= 0 {
		t.uncalibrated = true
	} else {
		bucket.Calories += met(span.KMH) * t.est.rest * hours
	}
	if t.est.stepLength > 0 {
		steps := km * 1000 / t.est.stepLength
		t.steps += steps
		bucket.Steps = int64(math.Round(float64(bucket.Steps) + steps))
	}
	pace := t.pace[span.Level]
	if pace == nil {
		pace = &Pace{Level: span.Level, KMH: span.KMH}
		t.pace[span.Level] = pace
	}
	pace.Hours += hours
	return km
}

// clip restricts s to [from, now], tallies each walking interval into its
// bucket, and returns the clipped session if any walking remains.
func (t *tally) clip(s Session, from, now time.Time, b buckets) (Session, bool) {
	if !s.End.After(from) {
		return Session{}, false
	}
	clipped := Session{Start: s.Start, End: s.End, Device: s.Device}
	if clipped.Start.Before(from) {
		clipped.Start = from
	}
	for _, span := range s.Spans {
		at, until := span.Start, span.End
		if at.Before(from) {
			at = from
		}
		if until.After(now) {
			until = now
		}
		for at.Before(until) {
			key, boundary := b.slot(at)
			end := until
			if boundary.Before(end) {
				end = boundary
			}
			clipped.Distance += t.add(key, span, end.Sub(at).Hours())
			clipped.Walking += end.Sub(at)
			at = end
		}
	}
	return clipped, clipped.Walking > 0
}

// bucket returns the accumulated bucket for key, or an empty one.
func (t *tally) bucket(key string) Bucket {
	if b := t.days[key]; b != nil {
		return *b
	}
	return Bucket{Date: key}
}

// earliest returns the first instant of the earliest walked day, or now.
func (t *tally) earliest(now time.Time, loc *time.Location) time.Time {
	start := now
	for key := range t.days {
		civil, _ := time.Parse(time.DateOnly, key)
		if day := CalendarBoundary(civil.Year(), civil.Month(), civil.Day(), loc); day.Before(start) {
			start = day
		}
	}
	return start
}

func groupByDevice(events []event.Event) map[string][]event.Event {
	groups := map[string][]event.Event{}
	for _, e := range events {
		groups[e.Device] = append(groups[e.Device], e)
	}
	return groups
}

func Build(events []event.Event, now, from time.Time, period string, cfg config.Config) Report {
	r := Report{Period: period, From: from, To: now, Days: []Bucket{}, Sessions: []Session{}, Pace: []Pace{}, Warnings: []string{}}
	loc := cfg.Location()
	opt := DefaultOptions()
	opt.InitialLevel = cfg.Treadmill.InitialLevel
	opt.KmhPerLevel = func(level int) float64 { return cfg.Treadmill.SpeedKMH[strconv.Itoa(level)] }
	// Use the report date's age for the entire history, like the current profile
	// and calibration. Do not age the profile separately for each walking day.
	est := newEstimates(cfg.Personal, now.In(loc))
	t := newTally(est)
	b := buckets{hourly: period == "1d", from: from, loc: loc}
	// Controllers of the same treadmill must share an identifier. Other devices
	// have separate state so their start/stop events cannot alter this treadmill.
	for _, es := range groupByDevice(events) {
		for _, s := range Sessions(es, now, opt) {
			if clipped, ok := t.clip(s, from, now, b); ok {
				r.Sessions = append(r.Sessions, clipped)
			}
		}
	}
	start := from
	if period == "all" {
		start = t.earliest(now, loc)
	}
	for _, key := range b.keys(start, now) {
		bucket := t.bucket(key)
		if bucket.Hours > 0 {
			bucket.AverageSpeed = bucket.DistanceKM / bucket.Hours
		}
		r.Days = append(r.Days, bucket)
		r.Totals.Hours += bucket.Hours
		r.Totals.DistanceKM += bucket.DistanceKM
		r.Totals.Calories += bucket.Calories
	}
	if r.Totals.Hours > 0 {
		r.Totals.AverageSpeed = r.Totals.DistanceKM / r.Totals.Hours
	}
	r.Totals.Steps = int64(math.Round(t.steps))
	for _, p := range t.pace {
		r.Pace = append(r.Pace, *p)
	}
	sort.Slice(r.Pace, func(i, j int) bool { return r.Pace[i].Level < r.Pace[j].Level })
	sort.Slice(r.Sessions, func(i, j int) bool { return r.Sessions[i].Start.After(r.Sessions[j].Start) })
	r.DistanceAvailable = !t.uncalibrated
	r.StepsAvailable = r.DistanceAvailable && est.stepLength > 0
	r.CaloriesAvailable = r.DistanceAvailable && est.rest > 0
	r.Warnings = warnings(cfg, r.DistanceAvailable, est)
	return r
}

func warnings(cfg config.Config, distanceAvailable bool, est estimates) []string {
	w := []string{}
	if !cfg.Treadmill.Calibrated {
		w = append(w, "Speed calibration is unconfirmed. Measure belt speeds, fill treadmill.speed_kmh and set calibrated = true.")
	}
	if !distanceAvailable {
		w = append(w, "Some used levels have no speed calibration. Distance, average speed, steps and calories are incomplete.")
	}
	if est.stepLength == 0 {
		w = append(w, "Set personal.step_length_m or height_cm to estimate steps.")
	} else if cfg.Personal.StepLengthM == 0 {
		w = append(w, "Steps use an approximate step length of 41.4% of height. Set step_length_m for a better estimate.")
	}
	if est.rest == 0 {
		w = append(w, "Set personal.weight_kg to estimate calories.")
	}
	return w
}

func subtractMonths(day time.Time, months int) time.Time {
	first := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location()).AddDate(0, -months, 0)
	last := first.AddDate(0, 1, -1).Day()
	d := min(day.Day(), last)
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, day.Location())
}
