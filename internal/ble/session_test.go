package ble

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeBackend struct{ c *fakeConnection }

func (b fakeBackend) Enable() error                      { return nil }
func (b fakeBackend) Connect(string) (Connection, error) { return b.c, nil }

type fakeConnection struct {
	mu                         sync.Mutex
	callbacks                  map[string]func([]byte)
	events                     []string
	failSubscribe, failCleanup bool
	failWrite                  bool
	block, entered             chan struct{}
	active, max                atomic.Int32
	disconnected               atomic.Bool
}

func (f *fakeConnection) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, s)
}
func (f *fakeConnection) Discover([]string) error { f.record("discover"); return nil }
func (f *fakeConnection) Subscribe(u string, cb func([]byte)) error {
	f.mu.Lock()
	if f.callbacks == nil {
		f.callbacks = make(map[string]func([]byte))
	}
	f.callbacks[u] = cb
	f.mu.Unlock()
	f.record("subscribe")
	if f.failSubscribe {
		return errors.New("subscribe failure")
	}
	return nil
}
func (f *fakeConnection) Unsubscribe(string) error {
	f.record("unsubscribe")
	if f.failCleanup {
		return errors.New("cleanup failure")
	}
	return nil
}
func (f *fakeConnection) Disconnect() error        { f.record("disconnect"); return nil }
func (f *fakeConnection) Connected() (bool, error) { return !f.disconnected.Load(), nil }
func (f *fakeConnection) Write(string, []byte) error {
	if f.failWrite {
		return errors.New("write failed")
	}
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for old := f.max.Load(); n > old; old = f.max.Load() {
		if f.max.CompareAndSwap(old, n) {
			break
		}
	}
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	return nil
}
func (f *fakeConnection) emit(data []byte) {
	f.mu.Lock()
	cb := f.callbacks["notify"]
	f.mu.Unlock()
	cb(data)
}
func openFake(t *testing.T, f *fakeConnection, capacity int) *Session {
	t.Helper()
	s, err := Open(context.Background(), fakeBackend{f}, Options{WriteUUIDs: []string{"write"}, NotifyUUIDs: []string{"notify"}, NotificationBuffer: capacity})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func awaitDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session did not terminate")
	}
}
func TestSubscriptionsCopiesAndLateCallback(t *testing.T) {
	f := &fakeConnection{}
	s := openFake(t, f, 2)
	f.mu.Lock()
	events := append([]string{}, f.events...)
	f.mu.Unlock()
	if len(events) != 2 || events[0] != "discover" || events[1] != "subscribe" {
		t.Fatal(events)
	}
	data := []byte{1}
	f.emit(data)
	data[0] = 9
	if got := <-s.Notifications(); got.UUID != "notify" || got.Data[0] != 1 {
		t.Fatal(got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f.emit([]byte{2})
	if _, ok := <-s.Notifications(); ok {
		t.Fatal("channel still open")
	}
}
func TestSerializedWrites(t *testing.T) {
	f := &fakeConnection{}
	s := openFake(t, f, 2)
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if err := s.Write(context.Background(), "write", []byte{1}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if f.max.Load() != 1 {
		t.Fatal("overlapping writes")
	}
}
func TestBlockedWriteKeepsCloseAndCancellationWaiting(t *testing.T) {
	f := &fakeConnection{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	s := openFake(t, f, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Write(ctx, "write", nil) }()
	<-f.entered
	cancel()
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-result:
		t.Fatal("write returned while backend running")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-closed:
		t.Fatal("Close returned while backend running")
	default:
	}
	select {
	case <-s.Done():
		t.Fatal("Done closed early")
	default:
	}
	close(f.block)
	<-result
	<-closed
	if err := s.Write(context.Background(), "write", nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestCancelWaitingWrite(t *testing.T) {
	f := &fakeConnection{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	s := openFake(t, f, 2)
	done := make(chan error, 1)
	go func() { done <- s.Write(context.Background(), "write", nil) }()
	<-f.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Write(ctx, "write", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(f.block)
	<-done
	if len(f.entered) != 0 {
		t.Fatal("cancelled write reached backend")
	}
}
func TestOverflowTerminates(t *testing.T) {
	f := &fakeConnection{}
	s := openFake(t, f, 1)
	f.emit([]byte{1})
	f.emit([]byte{2})
	awaitDone(t, s)
	if !errors.Is(s.Err(), ErrOverflow) {
		t.Fatal(s.Err())
	}
	if _, ok := <-s.Notifications(); ok {
		t.Fatal("fault retained fragments")
	}
	f.emit(nil)
}
func TestDisconnect(t *testing.T) {
	f := &fakeConnection{}
	s := openFake(t, f, 1)
	f.disconnected.Store(true)
	awaitDone(t, s)
	if !errors.Is(s.Err(), ErrDisconnected) {
		t.Fatal(s.Err())
	}
}
func TestSetupFailureAndCleanupErrors(t *testing.T) {
	f := &fakeConnection{failSubscribe: true, failCleanup: true}
	s, err := Open(context.Background(), fakeBackend{f}, Options{NotifyUUIDs: []string{"notify"}})
	if s != nil || err == nil {
		t.Fatal(s, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) != 4 || f.events[2] != "unsubscribe" || f.events[3] != "disconnect" {
		t.Fatal(f.events)
	}
}
func TestOversizedNotification(t *testing.T) {
	f := &fakeConnection{}
	s := openFake(t, f, 1)
	f.emit(make([]byte, 513))
	awaitDone(t, s)
	if !errors.Is(s.Err(), ErrOverflow) {
		t.Fatal(s.Err())
	}
}

func TestWriteFailureTerminates(t *testing.T) {
	f := &fakeConnection{failWrite: true}
	s := openFake(t, f, 1)
	if err := s.Write(context.Background(), "write", nil); err == nil {
		t.Fatal("missing write error")
	}
	awaitDone(t, s)
	if s.Err() == nil {
		t.Fatal("missing fault")
	}
	if err := s.Write(context.Background(), "write", nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

type blockedConnect struct {
	f                *fakeConnection
	entered, release chan struct{}
}

func (b blockedConnect) Enable() error { return nil }
func (b blockedConnect) Connect(string) (Connection, error) {
	close(b.entered)
	<-b.release
	return b.f, nil
}
func TestSetupCancellationWaitsAndCleansUp(t *testing.T) {
	f := &fakeConnection{}
	b := blockedConnect{f, make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := Open(ctx, b, Options{}); result <- err }()
	<-b.entered
	cancel()
	select {
	case <-result:
		t.Fatal("setup returned with connection work pending")
	case <-time.After(20 * time.Millisecond):
	}
	close(b.release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) != 1 || f.events[0] != "disconnect" {
		t.Fatal(f.events)
	}
}

func TestCloseCleanupError(t *testing.T) {
	f := &fakeConnection{failCleanup: true}
	s := openFake(t, f, 1)
	if err := s.Close(); err == nil {
		t.Fatal("missing cleanup error")
	}
	if err := s.Close(); err == nil {
		t.Fatal("idempotent Close lost error")
	}
}
