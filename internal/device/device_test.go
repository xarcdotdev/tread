package device

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// A command-only driver remains valid without either optional interface.
type commandOnly struct{}

func (commandOnly) Packet(string, int) ([]byte, error) { return []byte{1}, nil }
func (commandOnly) WriteUUID() string                  { return "write" }

var _ Driver = commandOnly{}

func TestConservativeCapabilities(t *testing.T) {
	for _, driver := range []Driver{nil, commandOnly{}} {
		got := CapabilitiesFor(driver)
		if len(got.Actions) != 0 || got.SpeedLevels != nil || len(got.ObservedFields) != 0 {
			t.Fatalf("assumed capabilities for command-only driver: %+v", got)
		}
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
