package device

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/protocol"
)

// A command-only driver remains valid without either optional interface.
type commandOnly struct{}

func (commandOnly) Packet(string, int) ([]byte, error) { return []byte{1}, nil }
func (commandOnly) WriteUUID() string                  { return "write" }

var _ Driver = commandOnly{}
var _ CapabilityProvider = DFT200{}

func TestConservativeCapabilities(t *testing.T) {
	for _, driver := range []Driver{nil, commandOnly{}} {
		got := CapabilitiesFor(driver)
		if len(got.Actions) != 0 || got.SpeedLevels != nil || len(got.ObservedFields) != 0 {
			t.Fatalf("assumed capabilities for command-only driver: %+v", got)
		}
	}
	driver, err := Lookup("dft200")
	if err != nil {
		t.Fatal(err)
	}
	got := CapabilitiesFor(driver)
	if !reflect.DeepEqual(got.Actions, []string{"speed", "start", "resume", "stop", "pause", "status"}) || got.SpeedLevels == nil || *got.SpeedLevels != (observed.LevelRange{Min: 1, Max: 8, Step: 1}) || len(got.ObservedFields) != 0 {
		t.Fatalf("DFT200 command capabilities = %+v", got)
	}
	provider, ok := driver.(TelemetryProvider)
	if !ok {
		t.Fatal("DFT200 must provide opt-in telemetry")
	}
	if session, err := provider.NewTelemetry(TelemetryOptions{}); session != nil || !errors.Is(err, ErrTelemetryDisabled) {
		t.Fatalf("default telemetry = %v, %v", session, err)
	}
	if session, err := provider.NewTelemetry(TelemetryOptions{ExperimentalVAN: true}); session == nil || err != nil {
		t.Fatalf("opt-in telemetry = %v, %v", session, err)
	}
	if len(CapabilitiesFor(driver).ObservedFields) != 0 {
		t.Fatal("opting in must not claim observed fields before a valid handshake")
	}
	got.Actions[0] = "mutated"
	got.SpeedLevels.Max = 99
	if fresh := CapabilitiesFor(driver); fresh.Actions[0] != "speed" || fresh.SpeedLevels.Max != 8 {
		t.Fatal("capabilities share mutable state")
	}
}

func TestDFT200CommandContract(t *testing.T) {
	driver := DFT200{}
	if driver.WriteUUID() != protocol.WriteCharUUID {
		t.Fatal("write UUID changed")
	}
	for _, tc := range []struct {
		action string
		cmd    protocol.Command
	}{
		{"start", protocol.CmdStart}, {"resume", protocol.CmdStart},
		{"stop", protocol.CmdStop}, {"pause", protocol.CmdPause}, {"status", protocol.CmdStatus},
	} {
		got, err := driver.Packet(tc.action, 0)
		if err != nil || !bytes.Equal(got, protocol.Encode(tc.cmd, 0)) {
			t.Fatalf("Packet(%q) = %x, %v", tc.action, got, err)
		}
	}
	for level := 1; level <= 8; level++ {
		got, err := driver.Packet("speed", level)
		want, _ := protocol.Speed(level)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("speed level %d = %x, %v", level, got, err)
		}
	}
	for _, level := range []int{0, 9} {
		if _, err := driver.Packet("speed", level); err == nil {
			t.Fatalf("speed level %d accepted", level)
		}
	}
	if _, err := driver.Packet("unknown", 0); err == nil {
		t.Fatal("unknown action accepted")
	}
}

// Test implementations exercise the transport-independent session contract.
type optionalDriver struct{ commandOnly }

func (optionalDriver) NewTelemetry(options TelemetryOptions) (TelemetrySession, error) {
	return &testSession{experimental: options.ExperimentalVAN}, nil
}

type testSession struct{ experimental bool }

func (*testSession) NotifyUUIDs() []string { return []string{"notify"} }
func (*testSession) Run(ctx context.Context, link Link, notifications <-chan Notification, emit func(observed.Update)) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case notification, ok := <-notifications:
		if !ok {
			return nil
		}
		if err := link.Write(ctx, notification.UUID, notification.Data); err != nil {
			return err
		}
		emit(observed.Update{State: &observed.State{Value: observed.StateRunning}})
		return nil
	}
}

type testLink struct {
	uuid string
	data []byte
}

func (l *testLink) Write(_ context.Context, uuid string, data []byte) error {
	l.uuid, l.data = uuid, append([]byte(nil), data...)
	return nil
}

func TestOptionalTelemetryContract(t *testing.T) {
	var driver Driver = optionalDriver{}
	provider, ok := driver.(TelemetryProvider)
	if !ok {
		t.Fatal("optional provider not detected")
	}
	session, err := provider.NewTelemetry(TelemetryOptions{ExperimentalVAN: true})
	if err != nil || !session.(*testSession).experimental || !reflect.DeepEqual(session.NotifyUUIDs(), []string{"notify"}) {
		t.Fatalf("NewTelemetry = %#v, %v", session, err)
	}
	another, _ := provider.NewTelemetry(TelemetryOptions{})
	if another == session || another.(*testSession).experimental {
		t.Fatal("sessions/options leak across connection attempts")
	}
	link := &testLink{}
	notifications := make(chan Notification, 1)
	notifications <- Notification{UUID: "notify", Data: []byte{42}}
	var update observed.Update
	if err := session.Run(context.Background(), link, notifications, func(u observed.Update) { update = u }); err != nil {
		t.Fatal(err)
	}
	if link.uuid != "notify" || !bytes.Equal(link.data, []byte{42}) || update.State == nil || update.State.Value != observed.StateRunning {
		t.Fatal("session failed to consume transport-independent link, notification and emit callback")
	}
}
