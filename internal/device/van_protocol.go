package device

import "github.com/xarcdotdev/oss-treadmill/internal/observed"

// This synthetic contract follows static VAN FIT analysis, not DFT200 captures.
// In particular the exact D3 payload length of 16 is still experimental.
const vanMaxPayload = 64

type vanFrame struct {
	command byte
	payload []byte
}

func vanChecksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return sum
}

func vanPacket(command byte, payload ...byte) []byte {
	packet := append([]byte{0xf0, command, byte(len(payload))}, payload...)
	return append(packet, vanChecksum(packet))
}

func parseVANFrame(data []byte) (vanFrame, bool) {
	if len(data) < 4 || data[0] != 0xf0 || int(data[2]) > vanMaxPayload || len(data) != int(data[2])+4 || vanChecksum(data[:len(data)-1]) != data[len(data)-1] {
		return vanFrame{}, false
	}
	return vanFrame{command: data[1], payload: data[3 : len(data)-1]}, true
}

// vanStream retains at most one bounded frame, regardless of notification size.
// A malformed candidate loses only its marker so later frames can resynchronize.
// Payloads passed to accept alias the buffer and are valid only during the call.
type vanStream struct {
	buffer [vanMaxPayload + 4]byte
	n      int
}

func (s *vanStream) feed(data []byte, accept func(vanFrame) error) error {
	for _, b := range data {
		s.buffer[s.n] = b
		s.n++
		for s.n > 0 {
			if s.buffer[0] != 0xf0 {
				s.drop(1)
				continue
			}
			if s.n < 3 {
				break
			}
			length := int(s.buffer[2]) + 4
			if length > len(s.buffer) {
				s.drop(1)
				continue
			}
			if s.n < length {
				break
			}
			frame, ok := parseVANFrame(s.buffer[:length])
			if !ok {
				s.drop(1)
				continue
			}
			err := accept(frame)
			s.drop(length)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *vanStream) drop(n int) {
	copy(s.buffer[:], s.buffer[n:s.n])
	s.n -= n
}

func vanObservedCapabilities() observed.Capabilities {
	// D1 min/max values are physical readings with unresolved units, not
	// command level limits. Do not replace the static 1..8 command range.
	return observed.Capabilities{ObservedFields: []observed.Field{
		observed.FieldState, observed.FieldSpeed, observed.FieldDistance,
		observed.FieldElapsed, observed.FieldIncline, observed.FieldCalories, observed.FieldPulse,
	}}
}

func decodeVANStatus(payload []byte, unit byte) (observed.Update, bool) {
	if len(payload) != 16 {
		return observed.Update{}, false
	}
	state := observed.StateUnknown
	switch payload[11] {
	case 3:
		state = observed.StateRunning
	case 4:
		state = observed.StatePaused
	}
	// Raw 2 is running-related but may be countdown. No other code has a
	// verified normalized meaning; retain it rather than guessing stopped.
	raw := uint16(payload[11])
	// Seconds/minutes arithmetic establishes the elapsed unit, not hardware
	// validity. Every measurement still has experimental data confidence.
	// Pace, sport mode, count_number, paceFrequency and hard_key are omitted.
	// Their physical meaning is unresolved; none is a verified step counter.
	speedUnit, distanceUnit, confidence := observed.UnitUnknown, observed.UnitUnknown, observed.UnitUnknownConfidence
	if unit == 0 {
		// Inferred on a DFT200 (no display to verify): speed reads 1.0 and 2.0 at
		// levels 1 and 2, the same ×10 encoding as the level command, and distance
		// accrues at that speed read as km/h. Mph would put level 8 at 12.9 km/h
		// against a measured 7.1. Nominal speed, not calibrated belt speed.
		speedUnit, distanceUnit, confidence = observed.UnitKMH, observed.UnitKilometres, observed.UnitInferred
	}
	return observed.Update{
		State: &observed.State{Value: state, RawCode: &raw},
		Measurements: map[observed.Field]observed.Measurement{
			observed.FieldElapsed:  vanMeasurement(float64(payload[0])+60*float64(payload[1]), observed.UnitSeconds, observed.UnitVerified),
			observed.FieldDistance: vanMeasurement(float64(100*int(payload[2])+int(payload[3]))*0.01, distanceUnit, confidence),
			observed.FieldCalories: vanMeasurement(float64(100*int(payload[4])+int(payload[5])), observed.UnitKCal, observed.UnitInferred),
			observed.FieldPulse:    vanMeasurement(float64(payload[6]), observed.UnitBPM, observed.UnitInferred),
			observed.FieldSpeed:    vanMeasurement(float64(payload[7])*0.1, speedUnit, confidence),
			observed.FieldIncline:  vanMeasurement(float64(payload[8]), observed.UnitUnknown, observed.UnitUnknownConfidence),
		},
	}, true
}

func vanMeasurement(value float64, unit observed.Unit, confidence observed.UnitConfidence) observed.Measurement {
	return observed.Measurement{Value: &value, Unit: unit, UnitConfidence: confidence, DataConfidence: observed.DataExperimental}
}
