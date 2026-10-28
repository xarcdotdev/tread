// Package ble provides a persistent, protocol-independent Bluetooth LE session.
package ble

import (
	"context"
	"errors"
)

// Send connects, writes one packet, and disconnects without retries. The caller
// must already hold host ownership and retain it until Send returns. Send does
// not acquire ownership itself. On Linux, BlueZ must already know the device.
func Send(mac, characteristic string, packet []byte) error {
	s, err := Open(context.Background(), nil, Options{Address: mac, WriteUUIDs: []string{characteristic}})
	if err != nil {
		return err
	}
	err = s.Write(context.Background(), characteristic, packet)
	return errors.Join(err, s.Close())
}
