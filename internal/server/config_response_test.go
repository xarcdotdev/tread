package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
)

func TestConfigAPIAge(t *testing.T) {
	for _, timezone := range []string{"UTC", "", "Local"} {
		t.Run("timezone="+timezone, func(t *testing.T) {
			cfg := config.Default()
			cfg.Timezone = timezone
			now := time.Now().In(cfg.Location())
			for _, tc := range []struct {
				name      string
				birthdate string
				age       any
			}{
				{"normal", now.AddDate(-30, 0, -1).Format(time.DateOnly), float64(30)},
				{"missing", "", nil},
				{"invalid", "bad", nil},
				{"zero", now.AddDate(0, 0, -1).Format(time.DateOnly), float64(0)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg.Personal.Birthdate = tc.birthdate
					w := httptest.NewRecorder()
					// Demo settings also exercise invalid profiles without file validation.
					New(nil, cfg, true).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))
					if w.Code != http.StatusOK {
						t.Fatalf("config: %d: %s", w.Code, w.Body.String())
					}
					var response map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if age, ok := response["age"]; !ok || age != tc.age {
						t.Fatalf("age = %v (present=%v), want %v", age, ok, tc.age)
					}
					if response["demo"] != true || response["read_only"] != true {
						t.Fatalf("metadata: %v", response)
					}
					personal := response["config"].(map[string]any)["personal"].(map[string]any)
					if personal["birthdate"] != tc.birthdate {
						t.Fatalf("birthdate changed: %v", personal)
					}
					if _, ok := personal["age"]; ok {
						t.Fatal("derived age must not become a personal config field")
					}
				})
			}
		})
	}
}

func TestConfigResponseAgeUsesServerCalendar(t *testing.T) {
	original := time.Local
	time.Local = time.FixedZone("server-local", -8*60*60)
	t.Cleanup(func() { time.Local = original })
	now := time.Date(2026, 6, 15, 0, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		timezone string
		age      int
	}{
		{"UTC", 36},
		{"", 36}, // Go treats the empty timezone as UTC, not server local.
		{"Local", 35},
	} {
		t.Run("timezone="+tc.timezone, func(t *testing.T) {
			cfg := config.Default()
			cfg.Timezone = tc.timezone
			cfg.Personal.Birthdate = "1990-06-15"
			age := configResponse(cfg, false, now)["age"].(*int)
			if age == nil || *age != tc.age {
				t.Fatalf("age = %v, want %d", age, tc.age)
			}
		})
	}
}

func TestConfigAPIReloadsAge(t *testing.T) {
	cfg := config.Default()
	cfg.Path = t.TempDir() + "/config.toml"
	handler := New(nil, cfg, false).Handler()
	for _, birthdate := range []string{"1990-01-01", ""} {
		if err := os.WriteFile(cfg.Path, []byte("[personal]\nbirthdate = \""+birthdate+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("config: %d: %s", w.Code, w.Body.String())
		}
		var response struct {
			Age *int `json:"age"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if birthdate == "" {
			if response.Age != nil {
				t.Fatalf("missing birthdate returned age %d", *response.Age)
			}
		} else if response.Age == nil || *response.Age != time.Now().UTC().Year()-1990 {
			t.Fatalf("birthdate did not supply current age: %s", w.Body.String())
		}
	}
}
