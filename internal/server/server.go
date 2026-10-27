// Package server serves the event API and the web UI.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
	"github.com/xarcdotdev/oss-treadmill/web"
)

type Server struct {
	store *store.Store
	opt   stats.Options
}

func New(s *store.Store) *Server {
	return &Server{store: s, opt: stats.DefaultOptions()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/events", s.postEvents)
	mux.HandleFunc("GET /api/stats", s.getStats)
	mux.Handle("GET /", http.FileServerFS(web.FS))
	return mux
}

// postEvents accepts a JSON array of events.
func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	var events []event.Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&events); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.AddEvents(events); err != nil {
		log.Printf("add events: %v", err)
		http.Error(w, "store failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type statsResponse struct {
	Sessions []stats.Session `json:"sessions"`
	Days     []stats.Day     `json:"days"`
}

// getStats returns sessions and daily totals for the last ?days=N days (default 7).
func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days <= 0 {
		days = 7
	}
	now := time.Now()
	y, m, d := now.Date()
	from := time.Date(y, m, d-days+1, 0, 0, 0, 0, time.Local)

	events, err := s.store.Events(from, now.Add(time.Minute))
	if err != nil {
		log.Printf("load events: %v", err)
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	sessions := stats.Sessions(events, now, s.opt)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statsResponse{Sessions: sessions, Days: stats.Daily(sessions, time.Local)})
}
