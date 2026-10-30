package dft200

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

func TestCommandFrames(t *testing.T) {
	tests := []struct {
		action string
		level  int
		want   []byte
	}{
		{"start", 0, []byte{0xf0, 0xc3, 0x03, 0x01, 0x00, 0x00, 0xb7}},
		{"resume", 0, []byte{0xf0, 0xc3, 0x03, 0x01, 0x00, 0x00, 0xb7}},
		{"pause", 0, []byte{0xf0, 0xc3, 0x03, 0x04, 0x00, 0x00, 0xba}},
		{"status", 0, []byte{0xf0, 0xc3, 0x03, 0x00, 0x00, 0x00, 0xb6}},
		{"stop", 0, []byte{0xf0, 0xc3, 0x03, 0x02, 0x00, 0x00, 0xb8}},
		{"speed", 3, []byte{0xf0, 0xc3, 0x03, 0x03, 0x1e, 0x00, 0xd7}},
	}
	for _, tt := range tests {
		if got, err := (Driver{}).Packet(tt.action, tt.level); err != nil || !bytes.Equal(got, tt.want) {
			t.Errorf("Packet(%q, %d) = % x, %v; want % x", tt.action, tt.level, got, err, tt.want)
		}
	}
}

func TestCommandContract(t *testing.T) {
	driver := Driver{}
	if driver.WriteUUID() != writeUUID {
		t.Fatal("write UUID changed")
	}
	for level := 1; level <= 8; level++ {
		got, err := driver.Packet("speed", level)
		if err != nil || !bytes.Equal(got, command(cmdSetSpeed, byte(level*10))) {
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

func TestCapabilities(t *testing.T) {
	driver := Driver{}
	got := device.CapabilitiesFor(driver)
	if !reflect.DeepEqual(got.Actions, []string{"speed", "start", "resume", "stop", "pause", "status"}) || got.SpeedLevels == nil || *got.SpeedLevels != (observed.LevelRange{Min: 1, Max: 8, Step: 1}) || len(got.ObservedFields) != 0 {
		t.Fatalf("command capabilities = %+v", got)
	}
	if session, err := driver.NewTelemetry(device.TelemetryOptions{}); session != nil || !errors.Is(err, device.ErrTelemetryDisabled) {
		t.Fatalf("default telemetry = %v, %v", session, err)
	}
	if session, err := driver.NewTelemetry(device.TelemetryOptions{ExperimentalVAN: true}); session == nil || err != nil {
		t.Fatalf("opt-in telemetry = %v, %v", session, err)
	}
	if len(device.CapabilitiesFor(driver).ObservedFields) != 0 {
		t.Fatal("opting in must not claim observed fields before a valid handshake")
	}
	got.Actions[0] = "mutated"
	got.SpeedLevels.Max = 99
	if fresh := device.CapabilitiesFor(driver); fresh.Actions[0] != "speed" || fresh.SpeedLevels.Max != 8 {
		t.Fatal("capabilities share mutable state")
	}
}
