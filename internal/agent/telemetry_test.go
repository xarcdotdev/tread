package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

type testTelemetry struct {
	emit    chan func(observed.Update)
	stopped chan struct{}
}

func (*testTelemetry) NotifyUUIDs() []string { return []string{"notify"} }
func (p *testTelemetry) Run(ctx context.Context, _ device.Link, _ <-chan device.Notification, emit func(observed.Update)) error {
	defer close(p.stopped)
	p.emit <- emit
	<-ctx.Done()
	return ctx.Err()
}

type idleTestDriver struct {
	device.DFT200
	emit chan func(observed.Update)
}

func (d idleTestDriver) NewTelemetry(device.TelemetryOptions) (device.TelemetrySession, error) {
	return &testTelemetry{emit: d.emit, stopped: make(chan struct{})}, nil
}

func TestIdleDisconnectCommandWaitsForCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testConfig(t)
	cfg.Treadmill.IdleDisconnectTimeout = "60ms"
	timeout, _ := cfg.Treadmill.IdleDisconnectDuration()
	h := &host{ctx: ctx, cfg: cfg, store: observed.NewStore("test", observed.Capabilities{}, observed.StoreOptions{}), idleTimeout: timeout, connectWait: time.Second, wake: make(chan struct{}, 1)}
	backend := &fakeBackend{}
	driver := idleTestDriver{emit: make(chan func(observed.Update), 2)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.connections(ctx, driver, Options{Backend: backend, RetryInterval: 10 * time.Millisecond})
	}()
	defer func() { cancel(); <-done }()
	emit := <-driver.emit
	first := backend.latest()
	entered, release := make(chan struct{}), make(chan struct{})
	first.mu.Lock()
	first.disconnectEntered, first.disconnectRelease = entered, release
	first.mu.Unlock()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	zero := 0.0
	paused := observed.Update{State: &observed.State{Value: observed.StatePaused}, Measurements: map[observed.Field]observed.Measurement{observed.FieldSpeed: {Value: &zero}}}
	emit(paused)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("idle link not closed")
	}
	// A delayed callback cannot republish the closing session.
	emit(paused)
	if s := h.store.Snapshot(); s.Connection != observed.ConnectionDisconnected || s.Measurements[observed.FieldSpeed].Value != nil {
		t.Fatalf("closing session republished: %+v", s)
	}
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/v1/observed", ""},
		{"POST", "/v1/sync", ""},
		{"POST", "/v1/command", `{"action":"reset-state"}`},
	} {
		h.handle(httptest.NewRecorder(), httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		h.mu.Lock()
		suspended := h.suspended
		h.mu.Unlock()
		if !suspended {
			t.Fatal("non-BLE request woke link")
		}
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.handle(w, httptest.NewRequest("POST", "/v1/command", strings.NewReader(`{"action":"status"}`)))
		response <- w
	}()
	wait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.busy && !h.suspended })
	select {
	case <-response:
		t.Fatal("command completed before cleanup")
	case <-time.After(80 * time.Millisecond):
	}
	if backend.latest() != first {
		t.Fatal("reconnected before cleanup")
	}
	close(release)
	released = true
	emit = <-driver.emit
	emit(observed.Update{Capabilities: &observed.Capabilities{}})
	select {
	case w := <-response:
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"error":""`) {
			t.Fatalf("status: %s", w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command did not wake link")
	}
	if w, _ := first.counts(); w != 0 {
		t.Fatalf("old link writes=%d", w)
	}
	second := backend.latest()
	if w, _ := second.counts(); w != 1 {
		t.Fatalf("new link writes=%d", w)
	}
	emit(paused)
	wait(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.suspended })
	time.Sleep(80 * time.Millisecond)
	if backend.latest() != second {
		t.Fatal("suspended link retried without a command")
	}
}

// Packet receipt checks must catch activity and freshness gaps even if the
// periodic idle check never runs between the packets.
func TestIdleReadingInterruptions(t *testing.T) {
	zero := 0.0
	paused := observed.Update{State: &observed.State{Value: observed.StatePaused}, Measurements: map[observed.Field]observed.Measurement{observed.FieldSpeed: {Value: &zero}}}
	start := time.Now()
	for _, running := range []bool{false, true} {
		var idle idleReadings
		idle.update(start, paused)
		next := start.Add(observed.DefaultFreshness)
		if running {
			next = start.Add(time.Second)
			idle.update(next, observed.Update{State: &observed.State{Value: observed.StateRunning}})
		}
		idle.update(next, paused)
		if idle.expired(next, time.Second) {
			t.Fatalf("interruption lost, running=%v", running)
		}
		if !idle.expired(next.Add(time.Second), time.Second) {
			t.Fatal("new idle interval not counted")
		}
		if idle.expired(next.Add(observed.DefaultFreshness), time.Second) {
			t.Fatal("stale readings counted")
		}
	}
}

func TestTelemetryReadinessAndLossInvalidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeBackend{}
	session, err := ble.Open(ctx, backend, ble.Options{WriteUUIDs: []string{"write"}, NotifyUUIDs: []string{"notify"}})
	if err != nil {
		t.Fatal(err)
	}
	store := observed.NewStore("test", observed.Capabilities{}, observed.StoreOptions{})
	generation := store.BeginConnection()
	store.SetTelemetry(generation, observed.TelemetryLinking)
	h := &host{ctx: ctx, store: store}
	telemetry := &testTelemetry{emit: make(chan func(observed.Update), 1), stopped: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- h.connection(ctx, session, telemetry, generation) }()
	emit := <-telemetry.emit
	h.mu.Lock()
	active := h.active
	h.mu.Unlock()
	if active != nil {
		t.Fatal("command link ready before handshake")
	}
	emit(observed.Update{Capabilities: &observed.Capabilities{ObservedFields: []observed.Field{observed.FieldSpeed}}})
	if s := store.Snapshot(); s.Connection != observed.ConnectionConnected || s.Telemetry != observed.TelemetryLinking {
		t.Fatalf("capabilities counted as readings: %+v", s)
	}
	value := 2.5
	emit(observed.Update{Measurements: map[observed.Field]observed.Measurement{observed.FieldSpeed: {Value: &value}}})
	if s := store.Snapshot(); s.Telemetry != observed.TelemetryActive {
		t.Fatalf("reading not active: %+v", s)
	}
	conn := backend.latest()
	conn.mu.Lock()
	conn.connected = false
	conn.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loss not detected")
	}
	<-telemetry.stopped
	emit(observed.Update{Measurements: map[observed.Field]observed.Measurement{observed.FieldSpeed: {Value: &value}}})
	s := store.Snapshot()
	if s.Measurements[observed.FieldSpeed].Value != nil || s.Generation == generation {
		t.Fatalf("old reading retained: %+v", s)
	}
	h.mu.Lock()
	active = h.active
	h.mu.Unlock()
	if active != nil {
		t.Fatal("lost session still admitted")
	}
}
