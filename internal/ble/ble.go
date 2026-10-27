// Package ble sends packets to the treadmill over Bluetooth LE.
package ble

import (
	"fmt"

	"github.com/xarcdotdev/oss-treadmill/internal/protocol"
	"tinygo.org/x/bluetooth"
)

// Send connects to the device, writes one packet and disconnects.
// On Linux, BlueZ must already know the device (pair or scan it once with bluetoothctl).
func Send(mac string, packet []byte) error {
	addr, err := bluetooth.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("parse MAC: %w", err)
	}
	writeUUID, err := bluetooth.ParseUUID(protocol.WriteCharUUID)
	if err != nil {
		return err
	}

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("enable adapter: %w", err)
	}
	dev, err := adapter.Connect(bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: addr}}, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("connect %s: %w", mac, err)
	}
	defer dev.Disconnect()

	services, err := dev.DiscoverServices(nil)
	if err != nil {
		return fmt.Errorf("discover services: %w", err)
	}
	for _, s := range services {
		chars, err := s.DiscoverCharacteristics([]bluetooth.UUID{writeUUID})
		if err != nil || len(chars) == 0 {
			continue
		}
		if _, err := chars[0].WriteWithoutResponse(packet); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	}
	return fmt.Errorf("characteristic %s not found", protocol.WriteCharUUID)
}
