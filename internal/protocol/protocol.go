// Package protocol encodes commands for the Sportstech DFT200.
package protocol

import "fmt"

// BLE characteristics of the DFT200.
const (
	NotifyCharUUID = "0000fff1-0000-1000-8000-00805f9b34fb"
	WriteCharUUID  = "0000fff2-0000-1000-8000-00805f9b34fb"
)

type Command uint8

const (
	CmdStatus   Command = 0x00
	CmdStart    Command = 0x01
	CmdStop     Command = 0x02
	CmdSetSpeed Command = 0x03
	CmdPause    Command = 0x04
)

const (
	MinLevel = 1
	MaxLevel = 8
)

var header = []byte{0xf0, 0xc3, 0x03}

// Encode builds a packet: f0 c3 03 <cmd> <value> 00 <checksum>.
func Encode(cmd Command, value uint8) []byte {
	p := append([]byte{}, header...)
	p = append(p, byte(cmd), value, 0x00)
	return append(p, checksum(p))
}

// Speed encodes a speed change to level 1–8 (sent as 10–80).
func Speed(level int) ([]byte, error) {
	if level < MinLevel || level > MaxLevel {
		return nil, fmt.Errorf("speed level %d out of range %d-%d", level, MinLevel, MaxLevel)
	}
	return Encode(CmdSetSpeed, uint8(level*10)), nil
}

func checksum(b []byte) uint8 {
	var sum uint8
	for _, n := range b {
		sum += n
	}
	return sum
}
