package protocol

import (
	"bytes"
	"testing"
)

func TestEncode(t *testing.T) {
	tests := []struct {
		name  string
		cmd   Command
		value uint8
		want  []byte
	}{
		{"start", CmdStart, 0, []byte{0xf0, 0xc3, 0x03, 0x01, 0x00, 0x00, 0xb7}},
		{"stop", CmdStop, 0, []byte{0xf0, 0xc3, 0x03, 0x02, 0x00, 0x00, 0xb8}},
		{"speed 30", CmdSetSpeed, 30, []byte{0xf0, 0xc3, 0x03, 0x03, 0x1e, 0x00, 0xd7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Encode(tt.cmd, tt.value); !bytes.Equal(got, tt.want) {
				t.Errorf("Encode() = % x, want % x", got, tt.want)
			}
		})
	}
}

func TestSpeedRange(t *testing.T) {
	for _, level := range []int{0, 9} {
		if _, err := Speed(level); err == nil {
			t.Errorf("Speed(%d) should fail", level)
		}
	}
	if _, err := Speed(3); err != nil {
		t.Errorf("Speed(3) = %v", err)
	}
}
