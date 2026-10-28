package demo

import (
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/stats"
)

func TestDemoBirthdateProfile(t *testing.T) {
	cfg := Config()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, cfg.Location())
	age, ok := cfg.Personal.AgeAt(now)
	if !ok || age != 32 || cfg.Personal.Birthdate != "1994-01-01" {
		t.Fatalf("invalid demo birthdate profile: %+v, age %d, valid %v", cfg.Personal, age, ok)
	}
	from, err := stats.PeriodStart("1w", now, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	r := stats.Build(Events(now, cfg.Location()), now, from, "1w", cfg)
	if !r.CaloriesAvailable || r.Totals.Calories <= 0 {
		t.Fatalf("demo calories unavailable: %+v", r.Totals)
	}
}
