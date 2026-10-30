package device

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/protocol"
)

// These transport-only tests use synthetic replies. They make no BLE connection.
type vanWrite struct {
	uuid string
	data []byte
}

type vanTestLink struct {
	writes chan vanWrite
	hook   func(context.Context, []byte) error
}

func (l *vanTestLink) Write(ctx context.Context, uuid string, data []byte) error {
	l.writes <- vanWrite{uuid, bytes.Clone(data)}
	if l.hook != nil {
		return l.hook(ctx, data)
	}
	return nil
}

type vanHarness struct {
	t             *testing.T
	session       *vanSession
	link          *vanTestLink
	notifications chan Notification
	updates       chan observed.Update
	done          chan error
	cancel        context.CancelFunc
}

func newVANHarness(t *testing.T, configure func(*vanSession, *vanTestLink)) *vanHarness {
	t.Helper()
	raw, err := (DFT200{}).NewTelemetry(TelemetryOptions{ExperimentalVAN: true})
	if err != nil {
		t.Fatal(err)
	}
	session := raw.(*vanSession)
	// Long defaults prevent unintended test races; individual timing tests use
	// short private durations without exposing timing knobs in the public API.
	session.pollInterval, session.handshakeTimeout, session.responseTimeout = time.Hour, time.Second, time.Second
	link := &vanTestLink{writes: make(chan vanWrite, 128)}
	if configure != nil {
		configure(session, link)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &vanHarness{t: t, session: session, link: link, notifications: make(chan Notification, 64), updates: make(chan observed.Update, 64), done: make(chan error, 1), cancel: cancel}
	go func() {
		h.done <- session.Run(ctx, link, h.notifications, func(u observed.Update) { h.updates <- u })
		close(h.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			t.Error("VAN Run leaked after cancellation")
		}
		for {
			select {
			case write := <-link.writes:
				h.checkSafeWrite(write)
			default:
				return
			}
		}
	})
	return h
}

func (h *vanHarness) checkSafeWrite(write vanWrite) {
	h.t.Helper()
	if write.uuid != protocol.WriteCharUUID || (!bytes.Equal(write.data, []byte{0xf0, 0xc0, 0, 0xb0}) && !bytes.Equal(write.data, vanPacket(0xc1, 0, h.session.unit)) && !bytes.Equal(write.data, []byte{0xf0, 0xc3, 3, 0, 0, 0, 0xb6})) {
		h.t.Errorf("unsafe telemetry write %s %x", write.uuid, write.data)
	}
}

func (h *vanHarness) write(want []byte) {
	h.t.Helper()
	select {
	case got := <-h.link.writes:
		h.checkSafeWrite(got)
		if !bytes.Equal(got.data, want) {
			h.t.Fatalf("write = %x; want %x", got.data, want)
		}
	case <-time.After(2 * time.Second):
		h.t.Fatalf("missing write %x", want)
	}
}

func (h *vanHarness) send(data []byte) {
	h.notifications <- Notification{UUID: protocol.NotifyCharUUID, Data: data}
}

func (h *vanHarness) update() observed.Update {
	h.t.Helper()
	select {
	case u := <-h.updates:
		return u
	case <-time.After(2 * time.Second):
		h.t.Fatal("missing observed update")
		return observed.Update{}
	}
}

func (h *vanHarness) result() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(2 * time.Second):
		h.t.Fatal("Run did not return")
		return nil
	}
}

func (h *vanHarness) handshake() {
	h.t.Helper()
	h.write([]byte{0xf0, 0xc0, 0, 0xb0})
	h.send(vanPacket(0xd0))
	h.write(vanPacket(0xc1, 0, h.session.unit))
	h.send(vanPacket(0xd1, 1, 200, 0, 20, 1, 0, 1))
	if update := h.update(); update.Capabilities == nil || len(update.Measurements) != 0 || update.State != nil {
		h.t.Fatalf("D1 did not publish only capabilities: %+v", update)
	}
	h.write([]byte{0xf0, 0xc3, 3, 0, 0, 0, 0xb6})
}

func TestVANSyntheticHandshakeOrderingAndUnits(t *testing.T) {
	h := newVANHarness(t, func(s *vanSession, _ *vanTestLink) { s.unit = 7 })
	h.write([]byte{0xf0, 0xc0, 0, 0xb0})
	if !reflect.DeepEqual(h.session.NotifyUUIDs(), []string{protocol.NotifyCharUUID}) {
		t.Fatal("wrong notify characteristic")
	}
	uuidCopy := h.session.NotifyUUIDs()
	uuidCopy[0] = "mutated"
	if h.session.NotifyUUIDs()[0] != protocol.NotifyCharUUID {
		t.Fatal("notification UUIDs share mutable storage")
	}
	// Wrong UUIDs cannot poison reassembly or advance the handshake.
	h.notifications <- Notification{UUID: protocol.WriteCharUUID, Data: vanPacket(0xd0)}
	h.notifications <- Notification{UUID: "fff1", Data: []byte{0xf0, 0xd0, 64}}
	h.send(vanPacket(0xd1, make([]byte, 7)...))
	h.send(vanPacket(0xd3, syntheticVANStatus()...))
	h.notifications <- Notification{UUID: strings.ToUpper(protocol.NotifyCharUUID), Data: vanPacket(0xd0)}
	h.write([]byte{0xf0, 0xc1, 2, 0, 7, 0xba})
	select {
	case update := <-h.updates:
		t.Fatalf("premature observed claim: %+v", update)
	default:
	}
	// Duplicate/out-of-order replies and short D1 must not activate telemetry.
	for _, frame := range [][]byte{vanPacket(0xd0), vanPacket(0xd1, make([]byte, 6)...), vanPacket(0xd3, syntheticVANStatus()...), vanPacket(0x99), vanPacket(0xdd)} {
		h.send(frame)
	}
	all := append(vanPacket(0xd1, 1, 200, 0, 20, 1, 0, 1), vanPacket(0xd3, syntheticVANStatus()...)...)
	for _, b := range all { // Every byte is a separate notification.
		h.send([]byte{b})
	}
	capUpdate := h.update()
	if capUpdate.Capabilities == nil || !reflect.DeepEqual(*capUpdate.Capabilities, vanObservedCapabilities()) || capUpdate.Capabilities.SpeedLevels != nil || len(capUpdate.Capabilities.Actions) != 0 {
		t.Fatalf("unexpected D1 claims: %+v", capUpdate)
	}
	h.write([]byte{0xf0, 0xc3, 3, 0, 0, 0, 0xb6})
	if update := h.update(); update.State == nil || update.State.Value != observed.StateRunning || len(update.Measurements) != 6 {
		t.Fatalf("D3 update = %+v", update)
	}
	if len(h.updates) != 0 || len(h.link.writes) != 0 {
		t.Fatal("unexpected updates or handshake writes")
	}
	// Discovery contains field support only; static command limits remain intact.
	store := observed.NewStore("dft200", CapabilitiesFor(DFT200{}), observed.StoreOptions{})
	generation := store.BeginConnection()
	store.Apply(generation, capUpdate)
	got := store.Snapshot().Capabilities
	if got.SpeedLevels == nil || got.SpeedLevels.Min != 1 || got.SpeedLevels.Max != 8 || len(got.ObservedFields) != 7 {
		t.Fatalf("discovery changed command limits: %+v", got)
	}
}

func TestVANSyntheticPollingAndResponseLoss(t *testing.T) {
	h := newVANHarness(t, func(s *vanSession, _ *vanTestLink) {
		s.pollInterval, s.responseTimeout = 5*time.Millisecond, 60*time.Millisecond
	})
	h.handshake()
	h.write([]byte{0xf0, 0xc3, 3, 0, 0, 0, 0xb6})
	// Valid unknown commands, duplicate D1 and malformed D3 do not count as
	// fresh status responses and cannot indefinitely keep a dead session alive.
	h.send(vanPacket(0x99, 1))
	h.send(vanPacket(0xd1, make([]byte, 7)...))
	h.send(vanPacket(0xd3, make([]byte, 15)...))
	h.send(vanPacket(0xd3, make([]byte, 17)...))
	bad := vanPacket(0xd3, syntheticVANStatus()...)
	bad[len(bad)-1]++
	h.send(bad)
	if err := h.result(); err == nil || !strings.Contains(err.Error(), "response timeout awaiting D3") {
		t.Fatalf("response loss = %v", err)
	}
	if len(h.updates) != 0 {
		t.Fatal("invalid status or duplicate D1 published")
	}
}

func TestVANSyntheticResponseRefresh(t *testing.T) {
	h := newVANHarness(t, func(s *vanSession, _ *vanTestLink) { s.responseTimeout = 150 * time.Millisecond })
	h.handshake()
	for range 3 {
		time.Sleep(60 * time.Millisecond)
		h.send(vanPacket(0xd3, make([]byte, 16)...))
		if update := h.update(); update.Measurements[observed.FieldSpeed].Value == nil || *update.Measurements[observed.FieldSpeed].Value != 0 {
			t.Fatal("actual zero missing")
		}
	}
	h.cancel()
	if err := h.result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("valid responses failed to refresh session: %v", err)
	}
}

func TestVANSyntheticHandshakeTimeouts(t *testing.T) {
	for _, stage := range []string{"D0", "D1"} {
		t.Run(stage, func(t *testing.T) {
			h := newVANHarness(t, func(s *vanSession, _ *vanTestLink) { s.handshakeTimeout = 40 * time.Millisecond })
			h.write([]byte{0xf0, 0xc0, 0, 0xb0})
			if stage == "D1" {
				h.send(vanPacket(0xd0))
				h.write(vanPacket(0xc1, 0, 0))
				h.send(vanPacket(0xd1, make([]byte, 6)...))
			}
			if err := h.result(); err == nil || !strings.Contains(err.Error(), "handshake timeout awaiting "+stage) {
				t.Fatalf("timeout = %v", err)
			}
			if len(h.updates) != 0 {
				t.Fatal("incomplete handshake claimed capabilities")
			}
		})
	}
}

func TestVANSyntheticDeviceErrors(t *testing.T) {
	for _, linked := range []bool{false, true} {
		h := newVANHarness(t, nil)
		if linked {
			h.handshake()
		} else {
			h.write([]byte{0xf0, 0xc0, 0, 0xb0})
		}
		h.send(vanPacket(0xdd, 0x42))
		if err := h.result(); err == nil || !strings.Contains(err.Error(), "device error 0x42") {
			t.Fatalf("DD error = %v", err)
		}
	}
}

func TestVANWriteFailures(t *testing.T) {
	failure := errors.New("transport failure")
	for _, command := range []byte{0xc0, 0xc1, 0xc3} {
		h := newVANHarness(t, func(_ *vanSession, link *vanTestLink) {
			link.hook = func(_ context.Context, data []byte) error {
				if data[1] == command {
					return failure
				}
				return nil
			}
		})
		h.write(vanPacket(0xc0))
		if command != 0xc0 {
			h.send(vanPacket(0xd0))
			h.write(vanPacket(0xc1, 0, 0))
		}
		if command == 0xc3 {
			h.send(vanPacket(0xd1, make([]byte, 7)...))
			h.update()
			h.write(vanPacket(0xc3, 0, 0, 0))
		}
		if err := h.result(); !errors.Is(err, failure) {
			t.Fatalf("write failure = %v", err)
		}
	}
}

func TestVANCancellationAndNotificationClose(t *testing.T) {
	for _, stage := range []string{"before Run", "D0", "D1", "linked", "closed"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "before Run" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				session, _ := (DFT200{}).NewTelemetry(TelemetryOptions{ExperimentalVAN: true})
				// Nil dependencies ensure no write or emit can be admitted.
				if err := session.Run(ctx, nil, nil, nil); !errors.Is(err, context.Canceled) {
					t.Fatalf("pre-cancelled Run = %v", err)
				}
				return
			}
			h := newVANHarness(t, nil)
			if stage == "linked" {
				h.handshake()
			} else {
				h.write(vanPacket(0xc0))
				if stage == "D1" {
					h.send(vanPacket(0xd0))
					h.write(vanPacket(0xc1, 0, 0))
				}
			}
			if stage == "closed" {
				close(h.notifications)
				if err := h.result(); err != nil {
					t.Fatal(err)
				}
			} else {
				h.cancel()
				h.send(vanPacket(0xd0))
				h.send(vanPacket(0xd1, make([]byte, 7)...))
				h.send(vanPacket(0xd3, syntheticVANStatus()...))
				if err := h.result(); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel = %v", err)
				}
				if len(h.updates) != 0 || len(h.link.writes) != 0 {
					t.Fatal("admitted work after cancellation")
				}
			}
		})
	}
}

func TestVANCancellationWaitsForSynchronousWrite(t *testing.T) {
	release := make(chan struct{})
	h := newVANHarness(t, func(_ *vanSession, link *vanTestLink) {
		link.hook = func(context.Context, []byte) error { <-release; return nil }
	})
	// Release even if this test fails, before the harness cleanup waits for Run.
	defer close(release)
	h.write(vanPacket(0xc0))
	h.cancel()
	select {
	case <-h.done:
		t.Fatal("Run returned before synchronous write completed")
	default:
	}
	// Can't close twice through defer; release the blocking call with one send.
	release <- struct{}{}
	if err := h.result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel after synchronous write = %v", err)
	}
	if len(h.link.writes) != 0 || len(h.updates) != 0 {
		t.Fatal("work admitted after canceled synchronous write")
	}
}

func TestVANFreshSessionsAndDefaults(t *testing.T) {
	first, err := (DFT200{}).NewTelemetry(TelemetryOptions{ExperimentalVAN: true, ExperimentalVANUnit: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := (DFT200{}).NewTelemetry(TelemetryOptions{ExperimentalVAN: true})
	if first == second || first.(*vanSession).unit != 1 || second.(*vanSession).unit != 0 {
		t.Fatal("session options leaked between connections")
	}
	defaults := second.(*vanSession)
	if defaults.pollInterval != time.Second || defaults.handshakeTimeout != 5*time.Second || defaults.responseTimeout != 5*time.Second || observed.DefaultFreshness != 5*time.Second {
		t.Fatalf("unexpected app timing defaults: %+v", defaults)
	}
	// Disconnect in the middle of a frame. A new session must start with C0
	// and cannot complete an old frame or inherit the old handshake.
	h := newVANHarness(t, nil)
	h.handshake()
	frame := vanPacket(0xd3, syntheticVANStatus()...)
	h.send(frame[:10])
	close(h.notifications)
	if err := h.result(); err != nil {
		t.Fatal(err)
	}
	fresh := newVANHarness(t, nil)
	fresh.write(vanPacket(0xc0))
	fresh.send(frame[10:])
	fresh.send(vanPacket(0xd3, syntheticVANStatus()...))
	fresh.send(vanPacket(0xd0))
	fresh.write(vanPacket(0xc1, 0, 0))
	if len(fresh.updates) != 0 {
		t.Fatal("parser or handshake state leaked across connections")
	}
}
