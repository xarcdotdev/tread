package agent

import (
	"context"
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
