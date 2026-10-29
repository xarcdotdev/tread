// Package server serves the event API and the web UI.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/goals"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
	"github.com/xarcdotdev/oss-treadmill/web"
)

type Server struct {
	store *store.Store
	cfg   config.Config
	demo  bool
	// Live reads the local agent's snapshot. The server never touches Bluetooth.
	Live func(context.Context) (observed.Snapshot, error)
}

func New(s *store.Store, cfg config.Config, demo bool) *Server {
	return &Server{store: s, cfg: cfg, demo: demo}
}
func (s *Server) settings() (config.Config, error) {
	if s.demo {
		return s.cfg, nil
	}
	return config.LoadFile(s.cfg.Path)
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/events", s.postEvents)
	mux.HandleFunc("GET /api/stats", s.getStats)
	mux.HandleFunc("GET /api/goals", s.getGoals)
	mux.HandleFunc("GET /api/live", s.getLive)
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.settings()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, configResponse(c, s.demo, time.Now()))
	})
	mux.Handle("GET /", http.FileServerFS(web.FS))
	return mux
}
func configResponse(c config.Config, demo bool, now time.Time) map[string]any {
	var age *int
	if years, ok := c.Personal.AgeAt(now.In(c.Location())); ok {
		age = &years
	}
	return map[string]any{"config": c, "demo": demo, "read_only": true, "age": age}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

type liveResponse struct {
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Snapshot  *observed.Snapshot `json:"snapshot,omitempty"`
}

// getLive reports the agent snapshot, or why none is available. It never fails:
// a missing agent is a normal state for the dashboard.
func (s *Server) getLive(w http.ResponseWriter, r *http.Request) {
	if s.demo || s.Live == nil {
		writeJSON(w, liveResponse{Reason: "live readings unavailable in demo mode"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	snapshot, err := s.Live(ctx)
	if err != nil {
		writeJSON(w, liveResponse{Reason: err.Error()})
		return
	}
	writeJSON(w, liveResponse{Available: true, Snapshot: &snapshot})
}

// postEvents accepts a JSON array of events.
func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	if s.demo {
		http.Error(w, "demo is read-only", http.StatusForbidden)
		return
	}
	var events []event.Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&events); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, e := range events {
		if err := e.Validate(time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.store.AddEvents(events); err != nil {
		log.Printf("add events: %v", err)
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type goalsResponse struct {
	goals.Report
	Path     string `json:"path"`
	Timezone string `json:"timezone"`
	Device   string `json:"device"`
	Demo     bool   `json:"demo"`
	ReadOnly bool   `json:"read_only"`
}

func (s *Server) getGoals(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := s.settings()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := goals.Path(cfg)
	var targets goals.Config
	if s.demo {
		// In-memory examples only. Never open the user's goals file in demo mode.
		path = "Built-in sample targets (no file)"
		targets = goals.Config{
			Daily:   goals.Targets{Steps: 1000, Calories: 250, Time: 30 * time.Minute, AverageSpeedKMH: 1.5},
			Weekly:  goals.Targets{Time: 5 * time.Hour},
			Monthly: goals.Targets{Steps: 150000},
			Yearly:  goals.Targets{Time: 250 * time.Hour},
		}
	} else {
		targets, err = goals.LoadFile(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	now := time.Now()
	// Include preceding events so sessions crossing calendar boundaries survive.
	events, err := s.store.Events(time.Unix(0, 0), now.Add(time.Millisecond))
	if err != nil {
		log.Printf("load goals events: %v", err)
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, goalsResponse{
		Report: goals.Build(events, now, cfg, targets),
		Path:   path, Timezone: cfg.Timezone, Device: cfg.Treadmill.Name,
		Demo: s.demo, ReadOnly: true,
	})
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.settings()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now()
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "1w"
	}
	from, err := stats.PeriodStart(period, now, cfg.Location())
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, e := strconv.Atoi(raw)
		if e != nil || days < 1 || days > 36500 {
			http.Error(w, "days must be 1–36500", 400)
			return
		}
		n := now.In(cfg.Location())
		from = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, cfg.Location()).AddDate(0, 0, -days+1)
		err = nil
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Load preceding events too: a session may cross the period boundary.
	events, err := s.store.Events(time.Unix(0, 0), now.Add(time.Millisecond))
	if err != nil {
		log.Printf("load events: %v", err)
		http.Error(w, "load failed", 500)
		return
	}
	writeJSON(w, stats.Build(events, now, from, period, cfg))
}
