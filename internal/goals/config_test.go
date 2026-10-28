package goals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
)

func TestPath(t *testing.T) {
	if got := Path(config.Config{Path: "/tmp/custom/settings.toml"}); got != "/tmp/custom/goals.yaml" {
		t.Fatal(got)
	}
	if got := Path(config.Config{}); got != filepath.Join(config.Dir(), "goals.yaml") {
		t.Fatal(got)
	}
	if got := Path(config.Config{Path: "config.toml"}); got != "goals.yaml" {
		t.Fatal(got)
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "goals.yaml")
	if got, err := LoadFile(path); err != nil || got != (Config{}) {
		t.Fatalf("missing: %+v, %v", got, err)
	}
	for _, text := range []string{"", "# optional\n", "{}", "daily: {}"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := LoadFile(path); err != nil || got != (Config{}) {
			t.Fatalf("%q: %+v, %v", text, got, err)
		}
	}
	text := "daily:\n  steps: 1000\n  calories: 150.5\n  time: 30m\n  average_speed_kmh: 3\nweekly:\n  time: 5h\nmonthly:\n  steps: 20000\nyearly:\n  calories: 2000\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	want := Config{Daily: Targets{Steps: 1000, Calories: 150.5, Time: 30 * time.Minute, AverageSpeedKMH: 3}, Weekly: Targets{Time: 5 * time.Hour}, Monthly: Targets{Steps: 20000}, Yearly: Targets{Calories: 2000}}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct{ text, message string }{
		{"daily: [", "line"}, {"daily: {}\n---\nweekly: {}", "multiple"}, {"{}\n---", "multiple"},
		{"null", "mapping"}, {"[]", "mapping"}, {"daily: null", "daily"}, {"daily: 5", "daily"},
		{"daliy: {}", "unknown period"}, {"daily: {step: 2}", "daily.step"},
		{"daily: {}\ndaily: {}", "duplicate"}, {"daily: {steps: 1, steps: 2}", "duplicate"},
		{"daily: {steps: 1.5}", "daily.steps"}, {"daily: {steps: 1.0}", "daily.steps"},
		{"daily: {steps: '100'}", "daily.steps"}, {"daily: {steps: 9223372036854775808}", "daily.steps"},
		{"daily: {steps: 0}", "daily.steps"}, {"daily: {steps: -2}", "daily.steps"},
		{"daily: {steps: true}", "daily.steps"}, {"daily: {steps: null}", "daily.steps"},
		{"daily: {calories: .nan}", "daily.calories"}, {"daily: {calories: .inf}", "daily.calories"},
		{"daily: {calories: -.inf}", "daily.calories"}, {"daily: {calories: 0}", "daily.calories"},
		{"daily: {calories: -1}", "daily.calories"}, {"daily: {calories: '10'}", "daily.calories"},
		{"daily: {average_speed_kmh: false}", "daily.average_speed_kmh"},
		{"daily: {average_speed_kmh: .nan}", "daily.average_speed_kmh"},
		{"daily: {average_speed_kmh: -1}", "daily.average_speed_kmh"},
		{"daily: {time: 30}", "daily.time"}, {"daily: {time: 1d}", "daily.time"},
		{"daily: {time: 0s}", "daily.time"}, {"daily: {time: -1h}", "daily.time"},
		{"daily: {time: 999999999999999h}", "daily.time"}, {"daily: {time: null}", "daily.time"},
		{"daily: {time: [30m]}", "daily.time"}, {"daily: &d {}\nweekly: *d", "weekly"},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "goals.yaml")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadFile(path)
			if err == nil || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), path) {
				t.Fatalf("got %+v, %v; want %q", got, err, tc.message)
			}
			if got != (Config{}) {
				t.Fatalf("partial config leaked: %+v", got)
			}
		})
	}
	if _, err := LoadFile(t.TempDir()); err == nil {
		t.Fatal("expected read error")
	}
}
