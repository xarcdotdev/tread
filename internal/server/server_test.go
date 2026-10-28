package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
)

func TestEventIngestionAndDashboard(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Path = t.TempDir() + "/config.toml"
	handler := New(st, cfg, false).Handler()
	start := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	stop := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	body := `[{"id":"start","device":"desk","action":"start","ts":"` + start + `"},{"id":"stop","device":"desk","action":"stop","ts":"` + stop + `"}]`
	for n := 0; n < 2; n++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/events", strings.NewReader(body)))
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/stats?period=all", nil))
	var r stats.Report
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 || r.Totals.Hours != .5 {
		t.Fatalf("duplicate ingestion or lost history: %+v", r)
	}
	// Server reads calibration from TOML on each request, without restart.
	if err := os.WriteFile(cfg.Path, []byte("[treadmill.speed_kmh]\n\"1\" = 2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/stats?period=all", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.DistanceAvailable || r.Totals.DistanceKM != 1 {
		t.Fatalf("live calibration not applied: %+v", r)
	}
	for _, path := range []string{"/", "/settings.html", "/style.css", "/app.js", "/api/config"} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, body := range []string{`[{"id":"bad","device":"desk","action":"speed","value":9,"ts":"` + start + `"}]`, `[{"id":"bad","device":"desk","action":"fly","ts":"` + start + `"}]`} {
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/events", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid event accepted: %d", w.Code)
		}
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/stats?period=garbage", nil))
	if w.Code != 400 {
		t.Fatal("invalid period accepted")
	}
}
