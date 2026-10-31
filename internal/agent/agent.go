// Package agent owns one local BLE link and a private command socket.
package agent

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/drivers"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/ownership"
)

type Options struct {
	RuntimeDir    string
	Backend       ble.Backend
	RetryInterval time.Duration
	// ConnectWait bounds how long a command waits for the link, e.g. right after
	// the agent started. The command is still sent at most once.
	ConnectWait time.Duration
}

type host struct {
	ctx            context.Context
	mu             sync.Mutex
	stopping, busy bool
	active         *ble.Session
	connectWait    time.Duration
	idleTimeout    time.Duration
	suspended      bool
	wake           chan struct{}
	work           sync.WaitGroup
	store          *observed.Store
	cfg            config.Config
	driver         device.Driver
}

// Run retains host ownership through all admitted transactions and backend
// cleanup. Cancellation can block indefinitely on synchronous TinyGo calls.
// RuntimeDir must already exist, owned by this user, with mode 0700.
func Run(ctx context.Context, cfg config.Config, opts Options) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	mac, err := net.ParseMAC(cfg.Treadmill.MAC)
	if err != nil || len(mac) != 6 {
		return errors.New("invalid treadmill MAC")
	}
	driver, err := drivers.Lookup(cfg.Treadmill.Driver)
	if err != nil {
		return err
	}
	if opts.RuntimeDir == "" {
		opts.RuntimeDir = ownership.RuntimeDir()
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = time.Second
	}
	if opts.RetryInterval < 50*time.Millisecond {
		opts.RetryInterval = 50 * time.Millisecond
	}
	if opts.ConnectWait <= 0 {
		opts.ConnectWait = 10 * time.Second
	}
	lock, err := ownership.Acquire(opts.RuntimeDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	listener, cleanup, err := listen(opts.RuntimeDir)
	if err != nil {
		return err
	}
	defer cleanup()
	idleTimeout, _ := cfg.Treadmill.IdleDisconnectDuration() // validated above; retained for runtime
	h := &host{
		ctx:         ctx,
		cfg:         cfg,
		driver:      driver,
		connectWait: opts.ConnectWait,
		idleTimeout: idleTimeout,
		wake:        make(chan struct{}, 1),
		store:       observed.NewStore(cfg.Device, device.CapabilitiesFor(driver), observed.StoreOptions{}),
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler:           http.HandlerFunc(h.handle),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    4096,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	linked := make(chan struct{})
	go func() { defer close(linked); h.connections(runCtx, driver, opts) }()
	serveFinished := false
	select {
	case <-ctx.Done():
		err = nil
	case err = <-served:
		serveFinished = true
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	h.mu.Lock()
	h.stopping = true
	h.active = nil
	h.mu.Unlock()
	cancel()
	server.Close()
	if !serveFinished {
		<-served
	}
	h.work.Wait()
	<-linked
	return err
}

func listen(dir string) (*net.UnixListener, func(), error) {
	path := filepath.Join(dir, ownership.SocketName)
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("refusing to remove non-socket agent path")
		}
		if err = os.Remove(path); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	l.SetUnlinkOnClose(false)
	own, err := os.Lstat(path)
	cleanup := func() {
		l.Close()
		if st, e := os.Lstat(path); e == nil && own != nil && os.SameFile(own, st) {
			os.Remove(path)
		}
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	return l, cleanup, nil
}

func (h *host) connections(ctx context.Context, driver device.Driver, opts Options) {
	for ctx.Err() == nil {
		h.mu.Lock()
		suspended := h.suspended
		h.mu.Unlock()
		if suspended {
			select {
			case <-ctx.Done():
				return
			case <-h.wake:
				continue
			}
		}
		generation := h.store.BeginConnection()
		var telemetry device.TelemetrySession
		var err error
		if provider, ok := driver.(device.TelemetryProvider); ok {
			telemetry, err = provider.NewTelemetry(device.TelemetryOptions{ExperimentalVAN: h.cfg.Treadmill.ExperimentalVANTelemetry})
			if errors.Is(err, device.ErrTelemetryDisabled) {
				h.store.SetTelemetry(generation, observed.TelemetryDisabled)
				err = nil
			} else if err == nil {
				h.store.SetTelemetry(generation, observed.TelemetryLinking)
			}
		}
		options := ble.Options{Address: h.cfg.Treadmill.MAC, WriteUUIDs: []string{driver.WriteUUID()}}
		if telemetry != nil {
			options.NotifyUUIDs = telemetry.NotifyUUIDs()
		}
		var session *ble.Session
		if err == nil {
			session, err = ble.Open(ctx, opts.Backend, options)
		}
		if err == nil {
			err = h.connection(ctx, session, telemetry, generation)
		} else {
			h.store.SetConnection(generation, observed.ConnectionFault)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("agent connection: %v", err)
		}
		timer := time.NewTimer(opts.RetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// connection runs one link until it faults, idles out, or the agent stops.
//
// Lifecycle: the session becomes h.active once ready (immediately without
// telemetry, otherwise after the first capability or reading update). The wait
// loop then watches for cancellation, protocol exit, session fault and idle
// expiry. On any exit the session is detached from h.active and closed, and a
// still-running telemetry goroutine is awaited before returning.
//
// closing and ready are guarded by h.mu, including inside telemetry callbacks
// that may arrive during cleanup.
func (h *host) connection(ctx context.Context, session *ble.Session, telemetry device.TelemetrySession, generation uint64) error {
	protocolCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	var idle idleReadings
	closing := false
	ready := func() { // caller holds h.mu
		if closing || h.stopping || protocolCtx.Err() != nil || session.Err() != nil {
			return
		}
		if h.store.SetConnection(generation, observed.ConnectionConnected) {
			h.active = session
		}
	}
	if telemetry == nil {
		h.mu.Lock()
		ready()
		h.mu.Unlock()
	} else {
		go func() {
			finished <- telemetry.Run(protocolCtx, session, session.Notifications(), func(update observed.Update) {
				// Capabilities are emitted after the driver's handshake. Readings also
				// establish readiness for providers without a capability handshake.
				h.mu.Lock()
				defer h.mu.Unlock()
				if closing || h.stopping || session.Err() != nil || protocolCtx.Err() != nil {
					return
				}
				if h.store.Apply(generation, update) {
					idle.update(time.Now(), update)
					if update.Capabilities != nil || update.State != nil || len(update.Measurements) > 0 {
						ready()
					}
					if update.State != nil || len(update.Measurements) > 0 {
						h.store.SetTelemetry(generation, observed.TelemetryActive)
					}
				}
			})
		}()
	}
	// Done follows cleanup. Err becomes visible at fault detection, so invalidate
	// readings even while an uninterruptible Disconnect is still outstanding.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var err error
	protocolFinished := false
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case err = <-finished:
			protocolFinished = true
			break wait
		case <-session.Done():
			err = session.Err()
			break wait
		case <-ticker.C:
			if err = session.Err(); err != nil {
				break wait
			}
			h.mu.Lock()
			if idle.expired(time.Now(), h.idleTimeout) && !h.busy && !h.stopping && h.active == session {
				closing = true
				h.active = nil
				h.suspended = true
				h.store.SetConnection(generation, observed.ConnectionDisconnected)
			}
			idleClose := closing
			h.mu.Unlock()
			if idleClose {
				break wait
			}
		}
	}
	h.mu.Lock()
	closing = true
	if h.active == session {
		h.active = nil
	}
	h.store.SetConnection(generation, observed.ConnectionDisconnected)
	h.mu.Unlock()
	cancel()
	closeErr := session.Close()
	if telemetry != nil && !protocolFinished {
		<-finished
	}
	return errors.Join(err, closeErr)
}

// waitActive returns the live session, waiting up to connectWait for one.
func (h *host) waitActive(ctx context.Context) *ble.Session {
	timer := time.NewTimer(h.connectWait)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.Lock()
		session, stopping := h.active, h.stopping
		h.mu.Unlock()
		if stopping || h.ctx.Err() != nil || ctx.Err() != nil {
			return nil
		}
		if session != nil {
			return session
		}
		select {
		case <-h.ctx.Done():
			return nil
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return nil
		case <-ticker.C:
		}
	}
}

// service builds a controller whose transport writes through session. A nil
// session refuses every write, which lets sync run without a device.
func (h *host) service(session *ble.Session) *control.Service {
	send := func(_, uuid string, packet []byte) error {
		if session == nil {
			return errors.New("device unavailable; no command sent")
		}
		return session.Write(context.Background(), uuid, packet)
	}
	return &control.Service{Controller: &control.Controller{
		Config: h.cfg,
		Queue:  event.NewClient(h.cfg.ServerURL, h.cfg.QueuePath()),
		Send:   send,
	}}
}
