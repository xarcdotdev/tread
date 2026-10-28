package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func fixture(t *testing.T) (*Controller, *[][]byte) {
	t.Helper()
	cfg := config.Default()
	cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Treadmill.MAC = "AA:BB:CC:DD:EE:FF"
	packets := new([][]byte)
	c := &Controller{Config: cfg, Queue: event.NewClient("", cfg.QueuePath()), BootID: func() (string, error) { return "boot1", nil }}
	c.Send = func(mac, uuid string, packet []byte) error {
		if mac != "aa:bb:cc:dd:ee:ff" || uuid != (device.DFT200{}).WriteUUID() {
			t.Fatalf("transport identity: %s %s", mac, uuid)
		}
		*packets = append(*packets, append([]byte(nil), packet...))
		return nil
	}
	return c, packets
}
func execute(t *testing.T, c *Controller, a string, n int) string {
	t.Helper()
	msg, err := c.Execute(a, n)
	if err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	return msg
}
func events(t *testing.T, c *Controller) []event.Event {
	t.Helper()
	b, err := os.ReadFile(c.Config.QueuePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var result []event.Event
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var e event.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		result = append(result, e)
	}
	return result
}
func TestKnowledgeAndPackets(t *testing.T) {
	c, packets := fixture(t)
	if _, err := c.Execute("toggle", 0); err == nil {
		t.Fatal("unknown toggle allowed")
	}
	execute(t, c, "speed", 3)
	if _, err := c.Execute("speed-up", 0); err == nil {
		t.Fatal("speed invented mode")
	}
	if _, err := c.Execute("toggle", 0); err == nil {
		t.Fatal("speed invented mode")
	}
	for _, a := range []string{"start", "speed-up", "toggle", "toggle", "stop", "toggle", "resume"} {
		execute(t, c, a, 0)
	}
	wants := []struct {
		a string
		n int
	}{{"speed", 3}, {"start", 0}, {"speed", 4}, {"pause", 0}, {"start", 0}, {"stop", 0}, {"start", 0}, {"resume", 0}}
	es := events(t, c)
	if len(es) != len(wants) || len(*packets) != len(wants) {
		t.Fatalf("counts %d %d", len(es), len(*packets))
	}
	for i, w := range wants {
		p, _ := (device.DFT200{}).Packet(w.a, w.n)
		if !bytes.Equal((*packets)[i], p) {
			t.Fatalf("packet %d", i)
		}
		a := w.a
		if a == "resume" {
			a = "start"
		}
		if string(es[i].Action) != a || es[i].Value != w.n {
			t.Fatalf("event %d: %+v", i, es[i])
		}
	}
	s, _ := c.load()
	if s.Mode != "running" || s.Speed != 4 {
		t.Fatalf("state %+v", s)
	}
}
func TestNoInventedSpeedAndBounds(t *testing.T) {
	c, p := fixture(t)
	execute(t, c, "start", 0)
	if _, err := c.Execute("speed-up", 0); err == nil {
		t.Fatal("initial_level inferred")
	}
	for _, tc := range []struct {
		level  int
		action string
	}{{1, "speed-down"}, {8, "speed-up"}} {
		execute(t, c, "speed", tc.level)
		count := len(*p)
		before := len(events(t, c))
		msg := execute(t, c, tc.action, 0)
		if len(*p) != count || len(events(t, c)) != before || !strings.Contains(msg, "no command sent") {
			t.Fatal("bound sent")
		}
	}
	execute(t, c, "speed-down", 0)
	s, _ := c.load()
	if s.Speed != 7 {
		t.Fatal(s)
	}
	execute(t, c, "pause", 0)
	if _, err := c.Execute("speed-up", 0); err == nil {
		t.Fatal("paused speed allowed")
	}
}
func TestInvalidationsPreservePending(t *testing.T) {
	for _, change := range []string{"boot", "device", "driver", "mac", "reset", "inflight"} {
		t.Run(change, func(t *testing.T) {
			c, p := fixture(t)
			execute(t, c, "start", 0)
			execute(t, c, "speed", 3)
			s, _ := c.load()
			old := event.New("original-device", event.Pause, 0)
			s.Pending = []event.Event{old}
			if err := c.save(s); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "boot":
				c.BootID = func() (string, error) { return "boot2", nil }
			case "device":
				c.Config.Device = "other"
			case "driver":
				c.Config.Treadmill.Driver = "other"
			case "mac":
				c.Config.Treadmill.MAC = "11:22:33:44:55:66"
			case "reset":
				c.Config.Treadmill.MAC = ""
				execute(t, c, "reset-state", 0)
			case "inflight":
				if err := writeState(StatePath(c.Config)+".in-flight", s); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.Execute("toggle", 0); err == nil {
				t.Fatal("stale toggle allowed")
			}
			if len(*p) != 2 {
				t.Fatal("sent")
			}
			es := events(t, c)
			if len(es) != 3 || es[2].ID != old.ID || es[2].Device != old.Device {
				t.Fatal(es)
			}
		})
	}
}
func TestPreflightRefusals(t *testing.T) {
	for _, fault := range []string{"state", "queue", "boot", "emptyboot", "mac", "missingmac", "write"} {
		t.Run(fault, func(t *testing.T) {
			c, p := fixture(t)
			switch fault {
			case "state":
				os.WriteFile(StatePath(c.Config), []byte("{bad"), 0600)
			case "queue":
				os.WriteFile(c.Config.QueuePath(), []byte("{}\n"), 0600)
			case "boot":
				c.BootID = func() (string, error) { return "", errors.New("unavailable") }
			case "emptyboot":
				c.BootID = func() (string, error) { return "", nil }
			case "mac":
				c.Config.Treadmill.MAC = "bad"
			case "missingmac":
				c.Config.Treadmill.MAC = ""
			case "write":
				c.write = func(string, state) error { return errors.New("disk") }
			}
			if _, err := c.Execute("start", 0); err == nil {
				t.Fatal("allowed")
			}
			if len(*p) != 0 {
				t.Fatal("sent")
			}
			if fault == "state" {
				if _, err := c.Execute("reset-state", 0); err == nil || !strings.Contains(err.Error(), "preserve") {
					t.Fatal(err)
				}
				b, _ := os.ReadFile(StatePath(c.Config))
				if string(b) != "{bad" {
					t.Fatal("corruption discarded")
				}
			}
		})
	}
}
func TestFailureAndInterruption(t *testing.T) {
	for _, fault := range []string{"transport", "commit", "commit-after-rename", "interruption", "queue", "clear-journal"} {
		t.Run(fault, func(t *testing.T) {
			c, _ := fixture(t)
			execute(t, c, "start", 0)
			execute(t, c, "speed", 3)
			count := 0
			c.write = func(path string, s state) error {
				count++
				if (fault == "commit" || fault == "commit-after-rename") && count == 2 {
					if fault == "commit-after-rename" {
						if err := writeState(path, s); err != nil {
							t.Fatal(err)
						}
					}
					return errors.New("disk")
				}
				if fault == "clear-journal" && count == 3 {
					return errors.New("disk")
				}
				return writeState(path, s)
			}
			c.Send = func(string, string, []byte) error {
				if fault == "transport" {
					return errors.New("ambiguous")
				}
				if fault == "interruption" {
					panic("crash")
				}
				if fault == "queue" {
					return os.Mkdir(c.Config.QueuePath()+".blocked", 0700)
				}
				return nil
			}
			if fault == "queue" {
				c.Send = func(string, string, []byte) error {
					if err := os.Remove(c.Config.QueuePath()); err != nil {
						return err
					}
					return os.Mkdir(c.Config.QueuePath(), 0700)
				}
			}
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil && fault != "interruption" {
						panic(r)
					}
				}()
				_, err = c.Execute("speed-up", 0)
			}()
			if fault != "interruption" && err == nil {
				t.Fatal("expected failure")
			}
			if strings.HasPrefix(fault, "commit") && !strings.Contains(err.Error(), "command sent; local recording failed") {
				t.Fatal(err)
			}
			c.write = nil
			c.Send = func(string, string, []byte) error { t.Fatal("unexpected retry"); return nil }
			if fault == "queue" {
				os.Remove(c.Config.QueuePath())
			}
			if fault == "queue" || fault == "clear-journal" {
				if _, err := c.recover(); err != nil {
					t.Fatal(err)
				}
				es := events(t, c)
				if es[len(es)-1].Value != 4 {
					t.Fatal(es)
				}
				return
			}
			if _, err := c.Execute("speed-up", 0); err == nil {
				t.Fatal("stale knowledge")
			}
			es := events(t, c)
			want := 2
			if fault == "commit-after-rename" {
				want = 3
			}
			if len(es) != want {
				t.Fatal(es)
			}
		})
	}
}
func TestSyncOfflineAndDuplicateReplay(t *testing.T) {
	c, _ := fixture(t)
	received := map[string]int{}
	offline := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var es []event.Event
		if err := json.NewDecoder(r.Body).Decode(&es); err != nil {
			t.Error(err)
		}
		for _, e := range es {
			received[e.ID]++
		}
		if offline {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	c.Queue.ServerURL = srv.URL
	execute(t, c, "start", 0)
	sync := func() error {
		batch, err := c.Sync()
		if err != nil {
			return err
		}
		if err := c.Queue.Post(batch); err != nil {
			return err
		}
		return c.Queue.Acknowledge(batch)
	}
	if err := sync(); err == nil {
		t.Fatal("expected offline")
	}
	execute(t, c, "speed", 2)
	s, _ := c.load()
	es := events(t, c)
	s.Pending = []event.Event{es[0]}
	c.save(s)
	offline = false
	if err := sync(); err != nil {
		t.Fatal(err)
	}
	if len(events(t, c)) != 0 {
		t.Fatal("queue not empty")
	}
	if received[es[0].ID] != 2 || received[es[1].ID] != 1 {
		t.Fatal(received)
	}
}
func TestStatusDoesNotObserve(t *testing.T) {
	c, p := fixture(t)
	execute(t, c, "status", 0)
	if len(*p) != 1 || len(events(t, c)) != 0 {
		t.Fatal("status reporting")
	}
	if _, err := c.Execute("toggle", 0); err == nil {
		t.Fatal("status observed mode")
	}
}
