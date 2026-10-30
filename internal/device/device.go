// Package device is the contract between treadmill drivers and the controller,
// agent and Bluetooth host. Drivers live under internal/drivers.
package device

type Driver interface {
	Packet(action string, level int) ([]byte, error)
	WriteUUID() string
}
