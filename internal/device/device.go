// Package device keeps treadmill-specific encoding out of the CLI and statistics.
// Add a driver here when a second device needs a different protocol.
package device

import (
	"fmt"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/protocol"
)

type Driver interface {
	Packet(action string, level int) ([]byte, error)
	WriteUUID() string
}
type DFT200 struct{}

// Capabilities describes only the implemented command protocol. Experimental
// telemetry fields are discovered per connection after the VAN handshake.
func (DFT200) Capabilities() observed.Capabilities {
	return observed.Capabilities{
		Actions:     []string{"speed", "start", "resume", "stop", "pause", "status"},
		SpeedLevels: &observed.LevelRange{Min: 1, Max: 8, Step: 1},
	}
}

func (DFT200) WriteUUID() string { return protocol.WriteCharUUID }
func (DFT200) Packet(action string, level int) ([]byte, error) {
	switch action {
	case "speed":
		return protocol.Speed(level)
	case "start", "resume":
		return protocol.Encode(protocol.CmdStart, 0), nil
	case "stop":
		return protocol.Encode(protocol.CmdStop, 0), nil
	case "pause":
		return protocol.Encode(protocol.CmdPause, 0), nil
	case "status":
		return protocol.Encode(protocol.CmdStatus, 0), nil
	}
	return nil, fmt.Errorf("unknown command %q", action)
}
func Lookup(name string) (Driver, error) {
	if name == "dft200" {
		return DFT200{}, nil
	}
	return nil, fmt.Errorf("unknown driver %q", name)
}
