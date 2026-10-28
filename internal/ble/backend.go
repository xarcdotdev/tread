package ble

import (
	"fmt"
	"io"

	"tinygo.org/x/bluetooth"
)

type tinyGoBackend struct{ adapter *bluetooth.Adapter }

func newTinyGoBackend() Backend        { return &tinyGoBackend{adapter: bluetooth.DefaultAdapter} }
func (b *tinyGoBackend) Enable() error { return b.adapter.Enable() }
func (b *tinyGoBackend) Connect(address string) (Connection, error) {
	mac, err := bluetooth.ParseMAC(address)
	if err != nil {
		return nil, err
	}
	dev, err := b.adapter.Connect(bluetooth.Address{MACAddress: bluetooth.MACAddress{MAC: mac}}, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, err
	}
	return &tinyGoConnection{dev: dev, chars: make(map[string]*bluetooth.DeviceCharacteristic)}, nil
}

type tinyGoConnection struct {
	dev   bluetooth.Device
	chars map[string]*bluetooth.DeviceCharacteristic
}

func (c *tinyGoConnection) Discover(uuids []string) error {
	wanted := make(map[string]bluetooth.UUID)
	for _, u := range uuids {
		parsed, err := bluetooth.ParseUUID(u)
		if err != nil {
			return err
		}
		wanted[u] = parsed
	}
	services, err := c.dev.DiscoverServices(nil)
	if err != nil {
		return err
	}
	for _, service := range services {
		chars, err := service.DiscoverCharacteristics(nil)
		if err != nil {
			return err
		}
		for i := range chars {
			for name, uuid := range wanted {
				if chars[i].UUID() == uuid {
					c.chars[name] = &chars[i]
				}
			}
		}
	}
	for _, u := range uuids {
		if c.chars[u] == nil {
			return fmt.Errorf("characteristic %s not found", u)
		}
	}
	return nil
}
func (c *tinyGoConnection) Subscribe(u string, callback func([]byte)) error {
	return c.chars[u].EnableNotifications(callback)
}
func (c *tinyGoConnection) Unsubscribe(u string) error { return c.chars[u].EnableNotifications(nil) }
func (c *tinyGoConnection) Write(u string, packet []byte) error {
	n, err := c.chars[u].Write(packet)
	if err == nil && n != len(packet) {
		return io.ErrShortWrite
	}
	return err
}

// TinyGo's Linux connect handler does not watch remote disconnections unless
// advertising. Poll Connected instead, through the same serialized worker.
func (c *tinyGoConnection) Connected() (bool, error) { return c.dev.Connected() }
func (c *tinyGoConnection) Disconnect() error        { return c.dev.Disconnect() }
