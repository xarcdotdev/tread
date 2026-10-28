// Package server serves the event API and the web UI.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
	"github.com/xarcdotdev/oss-treadmill/web"
)

type Server struct {
	store *store.Store
	cfg   config.Config
	demo  bool
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
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.settings()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"config": c, "demo": s.demo, "read_only": true})
	})
	mux.Handle("GET /", http.FileServerFS(web.FS))
	return mux
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
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
