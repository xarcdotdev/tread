package event

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotAcknowledgementPreservesNewEvents(t *testing.T) {
	c := NewClient("", filepath.Join(t.TempDir(), "queue.jsonl"))
	a, b := New("desk", Start, 0), New("desk", Pause, 0)
	if err := c.Enqueue(a); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Enqueue(b); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].ID != a.ID {
		t.Fatal(snapshot)
	}
	for i := 0; i < 2; i++ {
		if err := c.Acknowledge(snapshot); err != nil {
			t.Fatal(err)
		}
		remaining, err := c.Snapshot()
		if err != nil || len(remaining) != 1 || remaining[0].ID != b.ID {
			t.Fatalf("%v %v", remaining, err)
		}
	}
}

func TestQueueOnlyRecovery(t *testing.T) {
	c := NewClient("", filepath.Join(t.TempDir(), "queue.jsonl"))
	e := New("old-device", Pause, 0)
	if err := c.Enqueue(e, e); err != nil {
		t.Fatal(err)
	}
	if err := c.Enqueue(e); err != nil {
		t.Fatal(err)
	}
	if err := c.Preflight(); err != nil {
		t.Fatal(err)
	}
	es, err := c.readQueue()
	if err != nil || len(es) != 1 || es[0].ID != e.ID {
		t.Fatalf("replay: %v %v", es, err)
	}
	bad := []byte("{broken\n")
	if err := os.WriteFile(c.QueuePath, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Preflight(); err == nil {
		t.Fatal("corrupt queue accepted")
	}
	if err := c.Enqueue(e); err == nil {
		t.Fatal("corrupt queue overwritten")
	}
	got, err := os.ReadFile(c.QueuePath)
	if err != nil || string(got) != string(bad) {
		t.Fatalf("corrupt data lost: %q %v", got, err)
	}
}

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
