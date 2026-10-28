package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/goals"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
)

func goalsServer(t *testing.T, demo bool) (http.Handler, *store.Store, config.Config) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	return New(st, cfg, demo).Handler(), st, cfg
}

func writeGoalsFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requestGoals(t *testing.T, handler http.Handler) goalsResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/goals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("goals: %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("goals response headers: %v", w.Header())
	}
	var report goalsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestGoalsMissingFile(t *testing.T) {
	handler, _, cfg := goalsServer(t, false)
	r := requestGoals(t, handler)
	if r.Path != goals.Path(cfg) || r.Timezone != "UTC" || r.Demo || !r.ReadOnly || r.Device != cfg.Treadmill.Name {
		t.Fatalf("metadata: %+v", r)
	}
	if len(r.Periods) != 4 {
		t.Fatalf("periods: %+v", r.Periods)
	}
	for i, id := range []string{"daily", "weekly", "monthly", "yearly"} {
		p := r.Periods[i]
		if p.ID != id || p.Metrics == nil || len(p.Metrics) != 0 || !p.To.After(p.From) {
			t.Errorf("missing goals period: %+v", p)
		}
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Fatalf("request must not create goals.yaml: %v", err)
	}
}

func TestGoalsReloadTargetsAndSettingsWithPriorHistory(t *testing.T) {
	handler, st, cfg := goalsServer(t, false)
	writeGoalsFixture(t, cfg.Path, `timezone = "America/New_York"
[treadmill]
name = "First treadmill"
[treadmill.speed_kmh]
"3" = 4.0
[personal]
step_length_m = 0.5
weight_kg = 70
`)
	writeGoalsFixture(t, goals.Path(cfg), `daily:
  steps: 6000
weekly:
  time: 5h
monthly:
  average_speed_kmh: 3.5
yearly:
  steps: 100
  calories: 10
  time: 20m
  average_speed_kmh: 3
`)
	now := time.Now().UTC()
	year := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	duration := 30 * time.Minute
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Tokyo"} {
		local := now.In(mustGoalsLocation(t, zone))
		start := time.Date(local.Year(), 1, 1, 0, 0, 0, 0, local.Location())
		duration = min(duration, local.Sub(start)/4)
	}
	duration = duration.Truncate(time.Millisecond)
	// The speed setting precedes every current calendar period. Omitting prior
	// history would incorrectly use the uncalibrated initial level instead.
	events := []event.Event{
		{ID: "old-speed", Device: "desk", Action: event.Speed, Value: 3, TS: year.AddDate(-1, 0, 0)},
		{ID: "start", Device: "desk", Action: event.Start, TS: now.Add(-2 * duration)},
		{ID: "stop", Device: "desk", Action: event.Stop, TS: now.Add(-duration)},
	}
	if err := st.AddEvents(events); err != nil {
		t.Fatal(err)
	}
	r := requestGoals(t, handler)
	if r.Timezone != "America/New_York" || r.Device != "First treadmill" || len(r.Periods) != 4 {
		t.Fatalf("initial metadata: %+v", r)
	}
	weekly := r.Periods[1]
	if weekly.From.In(mustGoalsLocation(t, r.Timezone)).Weekday() != time.Monday {
		t.Fatalf("week did not start Monday: %+v", weekly)
	}
	initial := r.Periods[3].Metrics
	if len(initial) != 4 || initial[0].Target != 100 || !initial[0].Available || initial[0].Current <= 0 || initial[3].Current != 4 || !initial[3].Available {
		t.Fatalf("prior calibration/history not used: %+v", initial)
	}
	if initial[2].Current != duration.Seconds() || initial[2].Unit != "seconds" {
		t.Fatalf("walking duration: %+v", initial[2])
	}

	writeGoalsFixture(t, goals.Path(cfg), "yearly:\n  steps: 200\n  calories: 20\n  time: 40m\n  average_speed_kmh: 4\n")
	writeGoalsFixture(t, cfg.Path, `timezone = "Asia/Tokyo"
[treadmill]
name = "Reloaded treadmill"
[treadmill.speed_kmh]
"3" = 6.0
[personal]
step_length_m = 0.25
weight_kg = 80
`)
	r = requestGoals(t, handler)
	updated := r.Periods[3].Metrics
	if r.Timezone != "Asia/Tokyo" || r.Device != "Reloaded treadmill" || len(r.Periods[0].Metrics) != 0 {
		t.Fatalf("settings/targets not reloaded: %+v", r)
	}
	if updated[0].Target != 200 || updated[2].Target != 2400 || updated[3].Target != 4 || updated[3].Current != 6 {
		t.Fatalf("new targets/calibration: %+v", updated)
	}
	if math.Abs(updated[0].Current-3*initial[0].Current) > 2 || updated[1].Current <= initial[1].Current {
		t.Fatalf("profile/calibration not reapplied: before %+v, after %+v", initial, updated)
	}
	if _, offset := r.Periods[0].From.Zone(); offset != 9*3600 {
		t.Fatalf("new timezone boundary: %s", r.Periods[0].From)
	}
}

func mustGoalsLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestGoalsUnavailableMetricsHaveReasons(t *testing.T) {
	handler, _, cfg := goalsServer(t, false)
	writeGoalsFixture(t, goals.Path(cfg), "daily:\n  steps: 6000\n  calories: 250\n  time: 30m\n  average_speed_kmh: 3.5\n")
	r := requestGoals(t, handler)
	metrics := r.Periods[0].Metrics
	if len(metrics) != 4 {
		t.Fatalf("configured metrics: %+v", metrics)
	}
	for _, i := range []int{0, 1, 3} {
		if metrics[i].Available || metrics[i].Progress != 0 || metrics[i].Reason == "" {
			t.Errorf("unavailable metric must explain missing data: %+v", metrics[i])
		}
	}
	if !metrics[2].Available || metrics[2].Current != 0 || metrics[2].Progress != 0 {
		t.Fatalf("walking time is available without profile/calibration: %+v", metrics[2])
	}
}

func TestGoalsInvalidFilesAreErrors(t *testing.T) {
	handler, _, cfg := goalsServer(t, false)
	for _, content := range []string{"daily: [", "daily:\n  steps: -1\n", "daily:\n  typo: 1\n"} {
		writeGoalsFixture(t, goals.Path(cfg), content)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/goals", nil))
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), goals.Path(cfg)) || strings.Contains(w.Body.String(), `"periods"`) {
			t.Fatalf("invalid YAML returned fake progress: %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("error response must not be cached")
		}
	}
	writeGoalsFixture(t, goals.Path(cfg), "daily:\n  steps: 100\n")
	writeGoalsFixture(t, cfg.Path, `timezone = "Not/AZone"`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/goals", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "timezone") {
		t.Fatalf("invalid settings ignored: %d %s", w.Code, w.Body.String())
	}
}

func TestGoalsDemoNeverLoadsDiskTargetsOrSettings(t *testing.T) {
	handler, _, cfg := goalsServer(t, true)
	writeGoalsFixture(t, cfg.Path, "invalid TOML [")
	for _, content := range []string{"daily: [", "daily:\n  steps: 999999\n"} {
		writeGoalsFixture(t, goals.Path(cfg), content)
		r := requestGoals(t, handler)
		if !r.Demo || !r.ReadOnly || r.Path == goals.Path(cfg) || !strings.Contains(r.Path, "sample") || r.Timezone != cfg.Timezone {
			t.Fatalf("demo metadata: %+v", r)
		}
		if len(r.Periods) != 4 || len(r.Periods[0].Metrics) != 4 || r.Periods[0].Metrics[0].Target != 6000 || r.Periods[1].Metrics[0].Target != 18000 {
			t.Fatalf("demo did not use illustrative targets: %+v", r.Periods)
		}
		data, err := os.ReadFile(goals.Path(cfg))
		if err != nil || string(data) != content {
			t.Fatalf("demo modified real goals: %q, %v", data, err)
		}
	}
}

func TestGoalsAssetsServed(t *testing.T) {
	handler, _, _ := goalsServer(t, false)
	for _, path := range []string{"/goals.html", "/goals.js", "/style.css", "/icons.svg"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("%s: %d, %d bytes", path, w.Code, w.Body.Len())
		}
	}
}
