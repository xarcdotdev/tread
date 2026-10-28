package ble

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/device"
)

var (
	ErrClosed       = errors.New("BLE session closed")
	ErrOverflow     = errors.New("BLE notification buffer overflow")
	ErrDisconnected = errors.New("BLE disconnected")
)

// Backend operations are synchronous. Connect must return any partially opened
// connection even on error, so Open can disconnect it before returning.
// Callers must hold host ownership before Open and until Done closes.
type Backend interface {
	Enable() error
	Connect(address string) (Connection, error)
}

// Connection is used by one worker only. Subscribe callbacks may be concurrent
// and may arrive after Unsubscribe. Discover runs once and validates all UUIDs.
// All methods must finish their backend work before returning, even on error.
type Connection interface {
	Discover(uuids []string) error
	Subscribe(uuid string, callback func([]byte)) error
	Unsubscribe(uuid string) error
	Write(uuid string, packet []byte) error
	Connected() (bool, error)
	Disconnect() error
}

type Options struct {
	Address     string
	WriteUUIDs  []string
	NotifyUUIDs []string
	// NotificationBuffer defaults to 64. Overflow terminates the connection.
	// Payloads larger than the BLE attribute limit of 512 bytes also fault.
	NotificationBuffer int
}

type writeRequest struct {
	ctx    context.Context
	uuid   string
	packet []byte
	result chan error
}

// Session is one connection generation. It never reconnects or retries writes.
// Contexts cancel admission and checks between synchronous backend calls, not
// calls already in progress. Close and Done wait for those calls and cleanup.
type Session struct {
	conn          Connection
	writes        map[string]bool
	requests      chan writeRequest
	notifications chan device.Notification
	stop          chan struct{}
	done          chan struct{}
	mu            sync.Mutex
	stopping      bool
	err           error
	subscribed    []string
}

var _ device.Link = (*Session)(nil)

// Open enables, connects, discovers once, and subscribes before returning.
// A nil backend selects TinyGo. Failure returns only after synchronous cleanup.
// ctx governs setup only; use Close to terminate a successfully opened session.
func Open(ctx context.Context, backend Backend, opts Options) (*Session, error) {
	if opts.NotificationBuffer < 0 {
		return nil, errors.New("negative notification buffer")
	}
	if opts.NotificationBuffer == 0 {
		opts.NotificationBuffer = 64
	}
	if backend == nil {
		backend = newTinyGoBackend()
	}
	s := &Session{writes: make(map[string]bool), requests: make(chan writeRequest), notifications: make(chan device.Notification, opts.NotificationBuffer), stop: make(chan struct{}), done: make(chan struct{})}
	fail := func(err error) (*Session, error) { s.halt(err); s.cleanup(); return nil, s.Err() }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := backend.Enable(); err != nil {
		return fail(fmt.Errorf("enable adapter: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	var err error
	s.conn, err = backend.Connect(opts.Address)
	if err != nil {
		return fail(fmt.Errorf("connect: %w", err))
	}
	if s.conn == nil {
		return fail(errors.New("backend returned nil connection"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	all := make([]string, 0, len(opts.WriteUUIDs)+len(opts.NotifyUUIDs))
	seen := make(map[string]bool)
	for _, u := range append(append([]string{}, opts.WriteUUIDs...), opts.NotifyUUIDs...) {
		if !seen[u] {
			all = append(all, u)
			seen[u] = true
		}
	}
	for _, u := range opts.WriteUUIDs {
		s.writes[u] = true
	}
	if err := s.conn.Discover(all); err != nil {
		return fail(fmt.Errorf("discover: %w", err))
	}
	seen = make(map[string]bool)
	for _, u := range opts.NotifyUUIDs {
		if seen[u] {
			continue
		}
		seen[u] = true
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		// Include failed subscriptions: the backend may have partially enabled them.
		s.subscribed = append(s.subscribed, u)
		if err := s.conn.Subscribe(u, func(data []byte) { s.notify(u, data) }); err != nil {
			return fail(fmt.Errorf("subscribe %s: %w", u, err))
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	select {
	case <-s.stop:
		return fail(nil)
	default:
	}
	go s.run()
	return s, nil
}

// Notifications contains owned copies. On fault buffered notifications are
// discarded; consumers must discard parser state when this channel closes.
func (s *Session) Notifications() <-chan device.Notification { return s.notifications }

// Done closes only after backend work and cleanup finish. Hold ownership until then.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns the fault and any cleanup errors; its final value is available after Done.
func (s *Session) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Session) halt(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = errors.Join(s.err, err)
	if !s.stopping {
		s.stopping = true
		close(s.stop)
	}
}
func (s *Session) notify(uuid string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return
	}
	if len(data) > 512 {
		s.err = errors.Join(s.err, ErrOverflow)
		s.stopping = true
		close(s.stop)
		return
	}
	select {
	case s.notifications <- device.Notification{UUID: uuid, Data: append([]byte(nil), data...)}:
	default:
		s.err = errors.Join(s.err, ErrOverflow)
		s.stopping = true
		close(s.stop)
	}
}

// Write serializes writes without a persistent command queue. Once admitted it
// waits for the synchronous call, even if ctx expires. Never retry an uncertain
// motor command. Only UUIDs declared in WriteUUIDs may be written.
func (s *Session) Write(ctx context.Context, uuid string, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.writes[uuid] {
		return fmt.Errorf("write UUID %s not declared", uuid)
	}
	req := writeRequest{ctx: ctx, uuid: uuid, packet: append([]byte(nil), packet...), result: make(chan error, 1)}
	select {
	case <-s.stop:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.requests <- req:
	}
	return <-req.result
}

// Close is idempotent and waits without a timeout. Releasing host ownership
// before it returns could allow overlapping backend operations.
func (s *Session) Close() error { s.halt(nil); <-s.done; return s.Err() }
func (s *Session) run() {
	defer s.cleanup()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case r := <-s.requests:
			select {
			case <-s.stop:
				r.result <- ErrClosed
				continue
			default:
			}
			if err := r.ctx.Err(); err != nil {
				r.result <- err
				continue
			}
			err := s.conn.Write(r.uuid, r.packet)
			if err != nil {
				s.halt(fmt.Errorf("write: %w", err))
			}
			r.result <- err
		case <-ticker.C:
			select {
			case <-s.stop:
				return
			default:
			}
			connected, err := s.conn.Connected()
			if err != nil {
				s.halt(fmt.Errorf("connection status: %w", err))
			} else if !connected {
				s.halt(ErrDisconnected)
			}
		}
	}
}
func (s *Session) cleanup() {
	s.halt(nil)
	if s.conn != nil {
		for _, u := range s.subscribed {
			if err := s.conn.Unsubscribe(u); err != nil {
				s.halt(fmt.Errorf("unsubscribe %s: %w", u, err))
			}
		}
		if err := s.conn.Disconnect(); err != nil {
			s.halt(fmt.Errorf("disconnect: %w", err))
		}
	}
	s.mu.Lock()
	if s.err != nil {
	drain:
		for {
			select {
			case <-s.notifications:
			default:
				break drain
			}
		}
	}
	close(s.notifications)
	s.mu.Unlock()
	close(s.done)
}
