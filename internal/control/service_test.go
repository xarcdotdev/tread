package control

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func serviceForTest(t *testing.T) *Service {
	t.Helper()
	cfg := config.Default()
	cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Treadmill.MAC = "01:02:03:04:05:06"
	cfg.ServerURL = ""
	return &Service{Controller: &Controller{Config: cfg, Queue: event.NewClient("", cfg.QueuePath()), Send: func(string, string, []byte) error { return nil }, BootID: func() (string, error) { return "test", nil }}}
}
func TestServiceDeliveryFailureIsNotMotorFailure(t *testing.T) {
	s := serviceForTest(t)
	writes := 0
	s.Controller.Send = func(string, string, []byte) error { writes++; return nil }
	out, err := s.Execute("start", 0)
	if err != nil || !out.Result.Motion || !out.DeliveryPending || out.DeliveryMessage == "" || writes != 1 {
		t.Fatalf("result=%+v err=%v writes=%d", out, err, writes)
	}
	batch, err := s.Controller.Queue.Snapshot()
	if err != nil || len(batch) != 1 {
		t.Fatalf("queue=%v err=%v", batch, err)
	}
}
func TestServiceDeliveryReleasesLockAndPreservesConcurrentEvents(t *testing.T) {
	s := serviceForTest(t)
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(200) }))
	defer server.Close()
	s.Controller.Queue.ServerURL = server.URL
	done := make(chan error, 1)
	go func() { _, err := s.Execute("start", 0); done <- err }()
	<-entered
	appended := make(chan error, 1)
	go func() {
		appended <- WithLock(s.Controller.Config, func() error { return s.Controller.Queue.Enqueue(event.New(s.Controller.Config.Device, event.Stop, 0)) })
	}()
	select {
	case err := <-appended:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("queue lock held during HTTP delivery")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	batch, err := s.Controller.Queue.Snapshot()
	if err != nil || len(batch) != 1 || batch[0].Action != event.Stop {
		t.Fatalf("concurrent append lost: %v %v", batch, err)
	}
}
func TestServiceStatusDoesNotDeliver(t *testing.T) {
	s := serviceForTest(t)
	if err := s.Controller.Queue.Enqueue(event.New(s.Controller.Config.Device, event.Stop, 0)); err != nil {
		t.Fatal(err)
	}
	out, err := s.Execute("status", 0)
	if err != nil || out.Result.Motion || out.DeliveryPending {
		t.Fatalf("status delivered queue: %+v %v", out, err)
	}
	if err := s.Sync(); err == nil {
		t.Fatal("sync should report unavailable delivery")
	}
}
