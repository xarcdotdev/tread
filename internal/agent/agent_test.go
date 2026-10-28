package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/ownership"
)

type fakeBackend struct {
	mu          sync.Mutex
	connections []*fakeConnection
}

func (*fakeBackend) Enable() error { return nil }
func (b *fakeBackend) Connect(string) (ble.Connection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := &fakeConnection{connected: true}
	b.connections = append(b.connections, c)
	return c, nil
}
func (b *fakeBackend) latest() *fakeConnection {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.connections) == 0 {
		return nil
	}
	return b.connections[len(b.connections)-1]
}

type fakeConnection struct {
	mu                    sync.Mutex
	connected             bool
	writes, subscriptions int
	entered, release      chan struct{}
}

func (*fakeConnection) Discover([]string) error { return nil }
func (c *fakeConnection) Subscribe(string, func([]byte)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subscriptions++
	return nil
}
func (*fakeConnection) Unsubscribe(string) error { return nil }
func (c *fakeConnection) Write(string, []byte) error {
	c.mu.Lock()
	c.writes++
	entered, release := c.entered, c.release
	c.mu.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	return nil
}
func (c *fakeConnection) Connected() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected, nil
}
func (c *fakeConnection) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	return nil
}
func (c *fakeConnection) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes, c.subscriptions
}
func wait(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	cfg.ServerURL = ""
	cfg.Treadmill.MAC = "01:02:03:04:05:06"
	return cfg
}
func start(t *testing.T) (string, config.Config, *fakeBackend, context.CancelFunc, chan error) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	cfg := testConfig(t)
	backend := &fakeBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, Options{RuntimeDir: dir, Backend: backend, RetryInterval: 50 * time.Millisecond})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	})
	client := NewClient(dir)
	wait(t, func() bool {
		s, e := client.Snapshot(context.Background())
		return e == nil && s.Connection == observed.ConnectionConnected
	})
	return dir, cfg, backend, cancel, done
}

func TestDisabledTelemetryReconnectAndOwnership(t *testing.T) {
	dir, _, backend, _, _ := start(t)
	client := NewClient(dir)
	st, err := os.Stat(filepath.Join(dir, ownership.SocketName))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("socket: %v %v", st, err)
	}
	cfg := testConfig(t)
	if err := Run(context.Background(), cfg, Options{RuntimeDir: dir, Backend: backend}); !errors.Is(err, ownership.ErrOwned) {
		t.Fatalf("independent config acquired owner: %v", err)
	}
	first := backend.latest()
	if w, s := first.counts(); w != 0 || s != 0 {
		t.Fatalf("telemetry sent: %d %d", w, s)
	}
	snap, err := client.Snapshot(context.Background())
	if err != nil || snap.Telemetry != observed.TelemetryDisabled {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	out, err := client.Execute(context.Background(), CommandRequest{Action: "start"})
	if err != nil || !out.Result.Motion || !out.DeliveryPending {
		t.Fatalf("command: %+v %v", out, err)
	}
	first.mu.Lock()
	first.connected = false
	first.mu.Unlock()
	wait(t, func() bool { return backend.latest() != first })
	wait(t, func() bool {
		s, e := client.Snapshot(context.Background())
		return e == nil && s.Generation > snap.Generation && s.Connection == observed.ConnectionConnected
	})
	if w, _ := backend.latest().counts(); w != 0 {
		t.Fatalf("replayed %d writes", w)
	}
}

func TestLostResponseCommitsAndShutdownWaits(t *testing.T) {
	dir, cfg, backend, cancel, done := start(t)
	conn := backend.latest()
	conn.mu.Lock()
	conn.entered = make(chan struct{})
	conn.release = make(chan struct{})
	entered, release := conn.entered, conn.release
	conn.mu.Unlock()
	ctx, disconnect := context.WithCancel(context.Background())
	response := make(chan CommandResponse, 1)
	go func() { out, _ := NewClient(dir).Execute(ctx, CommandRequest{Action: "start"}); response <- out }()
	<-entered
	disconnect()
	out := <-response
	if !out.OutcomeUnknown || !strings.Contains(out.Error, "do not retry") {
		t.Fatalf("lost response: %+v", out)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("released while blocked: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if lock, err := ownership.Acquire(dir); !errors.Is(err, ownership.ErrOwned) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("ownership released early: %v", err)
	}
	close(release)
	// Cleanup consumes done. Wait for socket removal, which follows journal work.
	wait(t, func() bool {
		_, e := os.Stat(filepath.Join(dir, ownership.SocketName))
		return errors.Is(e, os.ErrNotExist)
	})
	if w, _ := conn.counts(); w != 1 {
		t.Fatalf("writes=%d", w)
	}
	batch, err := event.NewClient("", cfg.QueuePath()).Snapshot()
	if err != nil || len(batch) != 1 {
		t.Fatalf("journal did not commit after disconnect: %v %v", batch, err)
	}
}

func TestStrictRequests(t *testing.T) {
	dir, _, backend, _, _ := start(t)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, ownership.SocketName))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, body := range []string{`{"action":"start","mac":"anything"}`, `{"action":"start","level":0}`, `{"action":"start","level":null}`, `{"action":"speed"}`, `{"action":"speed","level":9}`, `{"action":"raw"}`, `{"action":"start"} {}`, strings.Repeat("x", maxBody+1)} {
		resp, err := client.Post("http://agent/v1/command", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("%q: %d", body[:min(len(body), 80)], resp.StatusCode)
		}
	}
	if w, _ := backend.latest().counts(); w != 0 {
		t.Fatalf("invalid requests wrote %d", w)
	}
}

func TestUnsafeSocketPaths(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			os.Chmod(dir, 0700)
			path := filepath.Join(dir, ownership.SocketName)
			switch kind {
			case "file":
				os.WriteFile(path, []byte("preserve"), 0600)
			case "symlink":
				os.Symlink("missing", path)
			case "directory":
				os.Mkdir(path, 0700)
			}
			backend := &fakeBackend{}
			if err := Run(context.Background(), testConfig(t), Options{RuntimeDir: dir, Backend: backend}); err == nil {
				t.Fatal("accepted unsafe socket")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("removed unsafe path")
			}
			if backend.latest() != nil {
				t.Fatal("touched adapter")
			}
		})
	}
}

func TestSocketCleanupPreservesReplacement(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	l, cleanup, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	path := filepath.Join(dir, ownership.SocketName)
	os.Remove(path)
	os.WriteFile(path, []byte("replacement"), 0600)
	cleanup()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "replacement" {
		t.Fatalf("replacement removed: %s %v", b, err)
	}
}

func TestGenerationInvalidatesOldReadings(t *testing.T) {
	store := observed.NewStore("device", observed.Capabilities{}, observed.StoreOptions{})
	generation := store.BeginConnection()
	value := 3.0
	store.Apply(generation, observed.Update{Measurements: map[observed.Field]observed.Measurement{observed.FieldSpeed: {Value: &value}}})
	store.SetConnection(generation, observed.ConnectionDisconnected)
	if store.Apply(generation, observed.Update{State: &observed.State{Value: observed.StateUnknown}}) {
		t.Fatal("old generation accepted")
	}
	b, _ := json.Marshal(store.Snapshot())
	if store.Snapshot().Measurements[observed.FieldSpeed].Value != nil {
		t.Fatalf("old value retained: %s", b)
	}
}
