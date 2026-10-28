package goals

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func TestMidnightCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct {
		zone, now string
		bounds    [4][2]string
	}{
		{"America/Santiago", "2024-09-08 12:00", [4][2]string{
			{"2024-09-08T01:00:00-03:00", "2024-09-09T00:00:00-03:00"},
			{"2024-09-02T00:00:00-04:00", "2024-09-09T00:00:00-03:00"},
			{"2024-09-01T00:00:00-04:00", "2024-10-01T00:00:00-03:00"},
			{"2024-01-01T00:00:00-03:00", "2025-01-01T00:00:00-03:00"},
		}},
		{"Africa/Cairo", "2024-04-26 12:00", [4][2]string{
			{"2024-04-26T01:00:00+03:00", "2024-04-27T00:00:00+03:00"},
			{"2024-04-22T00:00:00+02:00", "2024-04-29T00:00:00+03:00"},
			{"2024-04-01T00:00:00+02:00", "2024-05-01T00:00:00+03:00"},
			{"2024-01-01T00:00:00+02:00", "2025-01-01T00:00:00+02:00"},
		}},
		{"Africa/Cairo", "2024-10-31 12:00", [4][2]string{
			{"2024-10-31T00:00:00+03:00", "2024-11-01T00:00:00+02:00"},
			{"2024-10-28T00:00:00+03:00", "2024-11-04T00:00:00+02:00"},
			{"2024-10-01T00:00:00+03:00", "2024-11-01T00:00:00+02:00"},
			{"2024-01-01T00:00:00+02:00", "2025-01-01T00:00:00+02:00"},
		}},
		{"America/Santiago", "2024-04-06 12:00", [4][2]string{
			{"2024-04-06T00:00:00-03:00", "2024-04-07T00:00:00-04:00"},
			{"2024-04-01T00:00:00-03:00", "2024-04-08T00:00:00-04:00"},
			{"2024-04-01T00:00:00-03:00", "2024-05-01T00:00:00-04:00"},
			{"2024-01-01T00:00:00-03:00", "2025-01-01T00:00:00-03:00"},
		}},
	} {
		t.Run(tc.zone+tc.now, func(t *testing.T) {
			c := testConfig()
			c.Timezone = tc.zone
			r := Build(nil, local(c, tc.now), c, Config{})
			for i, p := range r.Periods {
				if p.From.Format(time.RFC3339) != tc.bounds[i][0] || p.To.Format(time.RFC3339) != tc.bounds[i][1] {
					t.Fatalf("%s: %v to %v", p.ID, p.From, p.To)
				}
				if p.FromDate != p.From.Format(time.DateOnly) || p.ThroughDate != p.To.Add(-time.Nanosecond).Format(time.DateOnly) {
					t.Fatal(p)
				}
			}
		})
	}
}

func TestBoundariesDoNotCarryMissingMidnightHour(t *testing.T) {
	for _, tc := range []struct {
		zone, now string
		period    int
		from, to  string
	}{
		{"Asia/Tehran", "2021-03-22 12:00", 1, "2021-03-22T01:00:00+04:30", "2021-03-29T00:00:00+04:30"},
		{"Africa/Cairo", "2014-08-01 12:00", 2, "2014-08-01T01:00:00+03:00", "2014-09-01T00:00:00+03:00"},
		{"Asia/Kathmandu", "1986-01-01 12:00", 3, "1986-01-01T00:15:00+05:45", "1987-01-01T00:00:00+05:45"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			c := testConfig()
			c.Timezone = tc.zone
			p := Build(nil, local(c, tc.now), c, Config{}).Periods[tc.period]
			if p.From.Format(time.RFC3339) != tc.from || p.To.Format(time.RFC3339) != tc.to {
				t.Fatal(p)
			}
		})
	}
}

func TestPeriodEndsAtMissingMidnight(t *testing.T) {
	for _, tc := range []struct {
		zone, now   string
		period      int
		to, through string
	}{
		{"America/Santiago", "2024-09-07 12:00", 0, "2024-09-08T01:00:00-03:00", "2024-09-07"},
		{"Africa/Cairo", "2024-04-25 12:00", 0, "2024-04-26T01:00:00+03:00", "2024-04-25"},
		{"Asia/Tehran", "2021-03-21 12:00", 1, "2021-03-22T01:00:00+04:30", "2021-03-21"},
		{"Africa/Cairo", "2014-07-31 12:00", 2, "2014-08-01T01:00:00+03:00", "2014-07-31"},
		{"Asia/Kathmandu", "1985-12-31 12:00", 3, "1986-01-01T00:15:00+05:45", "1985-12-31"},
	} {
		t.Run(tc.zone+tc.now, func(t *testing.T) {
			c := testConfig()
			c.Timezone = tc.zone
			p := Build(nil, local(c, tc.now), c, Config{}).Periods[tc.period]
			if p.To.Format(time.RFC3339) != tc.to || p.ThroughDate != tc.through {
				t.Fatal(p)
			}
		})
	}
}

func TestGoalsFutureYearBoundary(t *testing.T) {
	c := testConfig() // America/New_York, beyond explicit timezone transitions.
	for _, date := range []string{"2040-12-31", "2041-01-01"} {
		t.Run(date, func(t *testing.T) {
			r := Build(nil, local(c, date+" 12:00"), c, Config{})
			p := r.Periods[0]
			if p.FromDate != date || p.ThroughDate != date || p.From.Hour() != 0 || p.To.Sub(p.From) != 24*time.Hour {
				t.Fatal(p)
			}
			year := r.Periods[3]
			want := "2041-01-01T00:00:00-05:00"
			if date == "2041-01-01" {
				want = "2042-01-01T00:00:00-05:00"
			}
			if year.To.Format(time.RFC3339) != want {
				t.Fatal(year)
			}
		})
	}
}

func TestGoalsAcrossMissingMidnight(t *testing.T) {
	for _, tc := range []struct{ zone, start, stop string }{
		{"America/Santiago", "2024-09-07 23:30", "2024-09-08 01:30"},
		{"Africa/Cairo", "2024-04-25 23:30", "2024-04-26 01:30"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			c := testConfig()
			c.Timezone = tc.zone
			events := []event.Event{ev(c, tc.start, event.Start, 0), ev(c, tc.stop, event.Stop, 0)}
			target := Targets{Time: time.Hour}
			r := Build(events, local(c, tc.stop), c, Config{Daily: target, Weekly: target, Monthly: target, Yearly: target})
			for i, p := range r.Periods {
				want := 3600.
				if i == 0 {
					want = 1800
				}
				closeTo(t, p.Metrics[0].Current, want)
			}
		})
	}
}

func TestLocalDateLabelsJSON(t *testing.T) {
	old := time.Local
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = loc
	defer func() { time.Local = old }()
	c := testConfig()
	c.Timezone = "Local"
	now, _ := time.Parse(time.RFC3339, "2025-03-05T16:00:00Z")
	r := Build(nil, now, c, Config{})
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Periods []struct {
			FromDate    string `json:"from_date"`
			ThroughDate string `json:"through_date"`
		}
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	p := decoded.Periods[0]
	if p.FromDate != "2025-03-06" || p.ThroughDate != "2025-03-06" {
		t.Fatalf("%s", data)
	}
}
