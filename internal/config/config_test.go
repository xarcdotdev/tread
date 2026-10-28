package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExperimentalVANTelemetry(t *testing.T) {
	if Default().Treadmill.ExperimentalVANTelemetry {
		t.Fatal("experimental telemetry must default off")
	}
	for _, tc := range []struct {
		name, body string
		want       bool
		valid      bool
	}{
		{"missing", "", false, true},
		{"disabled", "experimental_van_telemetry = false", false, true},
		{"enabled", "experimental_van_telemetry = true", true, true},
		{"not a boolean", "experimental_van_telemetry = \"true\"", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[treadmill]\n"+tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFile(path)
			if (err == nil) != tc.valid {
				t.Fatalf("LoadFile = %v, want valid %v", err, tc.valid)
			}
			if tc.valid && cfg.Treadmill.ExperimentalVANTelemetry != tc.want {
				t.Fatalf("experimental telemetry = %v, want %v", cfg.Treadmill.ExperimentalVANTelemetry, tc.want)
			}
			if tc.valid {
				data, err := json.Marshal(cfg.Treadmill)
				if err != nil {
					t.Fatal(err)
				}
				var decoded struct {
					Enabled *bool `json:"experimental_van_telemetry"`
				}
				if err := json.Unmarshal(data, &decoded); err != nil || decoded.Enabled == nil || *decoded.Enabled != tc.want {
					t.Fatalf("telemetry JSON = %s, %v", data, err)
				}
			}
		})
	}
}

func TestDefaultAddresses(t *testing.T) {
	cfg := Default()
	if cfg.Listen != "127.0.0.1:18743" {
		t.Fatalf("Listen = %q, want 127.0.0.1:18743", cfg.Listen)
	}
	if cfg.ServerURL != "http://"+cfg.Listen {
		t.Fatalf("ServerURL = %q, want http://%s", cfg.ServerURL, cfg.Listen)
	}

	cfg, err := LoadFile(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:18743" || cfg.ServerURL != "http://127.0.0.1:18743" {
		t.Fatalf("missing config addresses = %q, %q", cfg.Listen, cfg.ServerURL)
	}
}

func TestLoadAddresses(t *testing.T) {
	for _, tc := range []struct {
		name, body, listen, serverURL string
	}{
		{"defaults", "", "127.0.0.1:18743", "http://127.0.0.1:18743"},
		{"old addresses", "listen = \"127.0.0.1:8080\"\nserver_url = \"http://127.0.0.1:8080\"", "127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"custom addresses", "listen = \"192.168.1.10:9090\"\nserver_url = \"https://tread.example:9443\"", "192.168.1.10:9090", "https://tread.example:9443"},
		{"listen only", "listen = \"127.0.0.1:8080\"", "127.0.0.1:8080", "http://127.0.0.1:18743"},
		{"server URL only", "server_url = \"http://127.0.0.1:8080\"", "127.0.0.1:18743", "http://127.0.0.1:8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Listen != tc.listen || cfg.ServerURL != tc.serverURL {
				t.Fatalf("addresses = %q, %q; want %q, %q", cfg.Listen, cfg.ServerURL, tc.listen, tc.serverURL)
			}
		})
	}
}

func TestBirthdateValidation(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, birthdate, zone string
		valid                 bool
	}{
		{"missing", "", "UTC", true},
		{"valid", "1990-06-15", "UTC", true},
		{"today", "2026-06-15", "UTC", true},
		{"tomorrow", "2026-06-16", "UTC", false},
		{"future locally", "2026-06-15", "America/Los_Angeles", false},
		{"valid locally", "2026-06-14", "America/Los_Angeles", true},
		{"120th birthday", "1906-06-15", "UTC", true},
		{"before 121st birthday", "1905-06-16", "UTC", true},
		{"121st birthday", "1905-06-15", "UTC", false},
		{"leap day", "2000-02-29", "UTC", true},
		{"non-leap day", "1900-02-29", "UTC", false},
		{"bad day", "2000-04-31", "UTC", false},
		{"bad month", "2000-13-01", "UTC", false},
		{"zero year", "0000-01-01", "UTC", false},
		{"zero date", "0000-00-00", "UTC", false},
		{"single month", "1990-6-15", "UTC", false},
		{"single day", "1990-06-5", "UTC", false},
		{"short year", "90-06-15", "UTC", false},
		{"timestamp", "1990-06-15T00:00:00Z", "UTC", false},
		{"whitespace", " 1990-06-15", "UTC", false},
		{"malformed", "not a date", "UTC", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Timezone = tc.zone
			cfg.Personal.Birthdate = tc.birthdate
			err := cfg.validate(now)
			if (err == nil) != tc.valid {
				t.Fatalf("validate = %v, want valid %v", err, tc.valid)
			}
			if err != nil && !strings.Contains(err.Error(), "personal.birthdate") {
				t.Fatalf("missing field context: %v", err)
			}
		})
	}
}

func TestAgeAt(t *testing.T) {
	for _, tc := range []struct {
		birthdate, date string
		age             int
	}{
		{"1990-06-15", "2026-06-14", 35},
		{"1990-06-15", "2026-06-15", 36},
		{"1990-06-15", "2026-06-16", 36},
		{"1990-12-31", "2026-01-01", 35},
		{"2000-02-29", "2023-02-28", 22},
		{"2000-02-29", "2023-03-01", 23},
		{"2000-02-29", "2024-02-28", 23},
		{"2000-02-29", "2024-02-29", 24},
		{"2000-02-29", "2024-03-01", 24},
		{"2026-06-15", "2026-06-15", 0},
	} {
		t.Run(tc.birthdate+"/"+tc.date, func(t *testing.T) {
			now, err := time.Parse(time.DateOnly, tc.date)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := (Personal{Birthdate: tc.birthdate}).AgeAt(now)
			if !ok || got != tc.age {
				t.Fatalf("AgeAt = %d, %v; want %d, true", got, ok, tc.age)
			}
		})
	}
}

func TestLoadBirthdate(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		valid            bool
	}{
		{"missing", "", "", true},
		{"empty", "birthdate = \"\"", "", true},
		{"valid", "birthdate = \"1990-06-15\"", "1990-06-15", true},
		{"legacy ignored", "age = 30", "", true},
		{"no legacy fallback", "age = 30\nbirthdate = \"invalid\"", "", false},
		{"invalid date", "birthdate = \"2001-02-29\"", "", false},
		{"future", "birthdate = \"9999-01-01\"", "", false},
		{"number", "birthdate = 30", "", false},
		{"unquoted date", "birthdate = 1990-06-15", "", false},
		{"malformed TOML", "birthdate = \"", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[personal]\n"+tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFile(path)
			if (err == nil) != tc.valid {
				t.Fatalf("LoadFile = %v, want valid %v", err, tc.valid)
			}
			if tc.valid && cfg.Personal.Birthdate != tc.want {
				t.Fatalf("birthdate = %q, want %q", cfg.Personal.Birthdate, tc.want)
			}
		})
	}
}

func TestPersonalJSONReplacesAge(t *testing.T) {
	data, err := json.Marshal(Personal{Birthdate: "1990-06-15"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"age"`) || !strings.Contains(string(data), `"birthdate":"1990-06-15"`) {
		t.Fatalf("unexpected profile JSON: %s", data)
	}
}
