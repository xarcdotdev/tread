package event

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestOfflineDeliveryThenRetry(t *testing.T) {
	online := false
	delivered := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online {
			http.Error(w, "unavailable", 503)
			return
		}
		var events []Event
		if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
			t.Error(err)
		}
		for _, e := range events {
			delivered[e.ID] = true
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(t.TempDir(), "nested", "queue.jsonl"))
	e := New("desk", Start, 0)
	if err := client.Report(e); err == nil {
		t.Fatal("failed delivery should be reported")
	}
	queued, err := client.readQueue()
	if err != nil || len(queued) != 1 || queued[0].ID != e.ID {
		t.Fatalf("event lost while offline: %v %v", queued, err)
	}
	online = true
	if err := client.Flush(); err != nil {
		t.Fatal(err)
	}
	if !delivered[e.ID] {
		t.Fatal("queued event never delivered")
	}
	queued, err = client.readQueue()
	if err != nil || len(queued) != 0 {
		t.Fatal("queue not cleared after success")
	}
}
