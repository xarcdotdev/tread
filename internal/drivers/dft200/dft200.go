// Package dft200 drives the Sportstech DFT200: its command protocol and the
// experimental VAN telemetry, which share one frame format.
package dft200

import (
	"fmt"

	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// BLE characteristics of the DFT200.
const (
	notifyUUID = "0000fff1-0000-1000-8000-00805f9b34fb"
	writeUUID  = "0000fff2-0000-1000-8000-00805f9b34fb"
)

// Commands are C3 frames: f0 c3 03 <command> <value> 00 <checksum>. The VAN
// status poll is the same frame as cmdStatus.
const (
	cmdStatus   byte = 0x00
	cmdStart    byte = 0x01
	cmdStop     byte = 0x02
	cmdSetSpeed byte = 0x03
	cmdPause    byte = 0x04
)

// Speed levels 1–8 are sent as 10–80.
const (
	minLevel = 1
	maxLevel = 8
)

func command(cmd, value byte) []byte { return vanPacket(0xc3, cmd, value, 0) }

type Driver struct{}

var _ device.CapabilityProvider = Driver{}

// Capabilities describes only the implemented command protocol. Experimental
// telemetry fields are discovered per connection after the VAN handshake.
func (Driver) Capabilities() observed.Capabilities {
	return observed.Capabilities{
		Actions:     []string{"speed", "start", "resume", "stop", "pause", "status"},
		SpeedLevels: &observed.LevelRange{Min: minLevel, Max: maxLevel, Step: 1},
	}
}

func (Driver) WriteUUID() string { return writeUUID }

func (Driver) Packet(action string, level int) ([]byte, error) {
	switch action {
	case "speed":
		if level < minLevel || level > maxLevel {
			return nil, fmt.Errorf("speed level %d out of range %d-%d", level, minLevel, maxLevel)
		}
		return command(cmdSetSpeed, byte(level*10)), nil
	case "start", "resume":
		return command(cmdStart, 0), nil
	case "stop":
		return command(cmdStop, 0), nil
	case "pause":
		return command(cmdPause, 0), nil
	case "status":
		return command(cmdStatus, 0), nil
	}
	return nil, fmt.Errorf("unknown command %q", action)
}
