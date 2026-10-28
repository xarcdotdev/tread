package control

import (
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// A command-only driver is still valid, without capability or telemetry methods.
type commandOnlyDriver struct{}

func (commandOnlyDriver) Packet(string, int) ([]byte, error) { return []byte{1}, nil }
func (commandOnlyDriver) WriteUUID() string                  { return "command" }

type rangedDriver struct {
	commandOnlyDriver
	caps observed.Capabilities
}

func (d rangedDriver) Capabilities() observed.Capabilities { return d.caps }

func TestRelativeLevelUsesOptionalDriverMetadata(t *testing.T) {
	caps := observed.Capabilities{Actions: []string{"speed"}, SpeedLevels: &observed.LevelRange{Min: 2, Max: 6, Step: 2}}
	driver := rangedDriver{caps: caps}
	for _, tc := range []struct {
		speed   int
		action  string
		level   int
		bounded bool
		bad     bool
	}{
		{2, "speed-up", 4, false, false},
		{6, "speed-down", 4, false, false},
		{2, "speed-down", 2, true, false},
		{6, "speed-up", 6, true, false},
		{3, "speed-up", 0, false, true},
		{1, "speed-up", 0, false, true},
	} {
		level, bounded, err := relativeLevel(driver, tc.speed, tc.action)
		if level != tc.level || bounded != tc.bounded || (err != nil) != tc.bad {
			t.Errorf("%d %s: level=%d bounded=%v err=%v", tc.speed, tc.action, level, bounded, err)
		}
	}
	for _, d := range []device.Driver{
		commandOnlyDriver{},
		rangedDriver{},
		rangedDriver{caps: observed.Capabilities{SpeedLevels: caps.SpeedLevels}},
		rangedDriver{caps: observed.Capabilities{Actions: caps.Actions, SpeedLevels: &observed.LevelRange{Min: 1, Max: 8}}},
	} {
		if _, _, err := relativeLevel(d, 2, "speed-up"); err == nil {
			t.Errorf("relative speed allowed without valid metadata: %#v", d)
		}
	}
}
