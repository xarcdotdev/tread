package device

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// All frames in this file are synthetic fixtures, not hardware captures.
func syntheticVANStatus() []byte {
	return []byte{59, 2, 12, 34, 5, 67, 88, 23, 9, 91, 92, 3, 13, 14, 15, 16}
}

func TestVANSyntheticStatusFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		values  []float64
	}{
		{"populated", syntheticVANStatus(), []float64{179, 12.34, 567, 88, 2.3, 9}},
		{"actual zero", make([]byte, 16), []float64{0, 0, 0, 0, 0, 0}},
		{"base100 without binary or byte overflow", bytes.Repeat([]byte{255}, 16), []float64{15555, 257.55, 25755, 255, 25.5, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeVANStatus(tc.payload, 0)
			// Units are inferred only for the default unit byte.
			if other, _ := decodeVANStatus(tc.payload, 1); other.Measurements[observed.FieldSpeed].Unit != observed.UnitUnknown || other.Measurements[observed.FieldDistance].Unit != observed.UnitUnknown {
				t.Fatalf("unit byte 1 inferred units: %+v", other.Measurements)
			}
			if !ok || len(got.Measurements) != 6 || got.Capabilities != nil || got.State == nil || got.State.RawCode == nil || *got.State.RawCode != uint16(tc.payload[11]) {
				t.Fatalf("decoded update = %+v, %v", got, ok)
			}
			for i, field := range []observed.Field{observed.FieldElapsed, observed.FieldDistance, observed.FieldCalories, observed.FieldPulse, observed.FieldSpeed, observed.FieldIncline} {
				m := got.Measurements[field]
				if m.Value == nil || math.Abs(*m.Value-tc.values[i]) > 1e-9 || m.DataConfidence != observed.DataExperimental || m.Timestamp != nil {
					t.Errorf("%s = %+v; want %g experimental", field, m, tc.values[i])
				}
				unit, confidence := observed.UnitUnknown, observed.UnitUnknownConfidence
				switch field {
				case observed.FieldElapsed:
					unit, confidence = observed.UnitSeconds, observed.UnitVerified
				case observed.FieldCalories:
					unit, confidence = observed.UnitKCal, observed.UnitInferred
				case observed.FieldPulse:
					unit, confidence = observed.UnitBPM, observed.UnitInferred
				case observed.FieldSpeed:
					unit, confidence = observed.UnitKMH, observed.UnitInferred
				case observed.FieldDistance:
					unit, confidence = observed.UnitKilometres, observed.UnitInferred
				}
				if m.Unit != unit || m.UnitConfidence != confidence {
					t.Errorf("%s units = %s/%s; want %s/%s", field, m.Unit, m.UnitConfidence, unit, confidence)
				}
			}
		})
	}
}

func TestVANSyntheticStates(t *testing.T) {
	for raw := range 256 {
		payload := syntheticVANStatus()
		payload[11] = byte(raw)
		got, _ := decodeVANStatus(payload, 0)
		want := observed.StateUnknown
		if raw == 3 {
			want = observed.StateRunning
		} else if raw == 4 {
			want = observed.StatePaused
		}
		if got.State.Value != want || *got.State.RawCode != uint16(raw) {
			t.Fatalf("raw %d = %+v; want %s", raw, got.State, want)
		}
	}
}

func TestVANStrictFrameAndStatusLengths(t *testing.T) {
	good := vanPacket(0xd3, syntheticVANStatus()...)
	if _, ok := parseVANFrame(good); !ok {
		t.Fatal("valid synthetic frame rejected")
	}
	badChecksum := bytes.Clone(good)
	badChecksum[len(badChecksum)-1]++
	badMarker := bytes.Clone(good)
	badMarker[0] = 0xf1
	for _, invalid := range [][]byte{nil, good[:3], good[:len(good)-1], append(bytes.Clone(good), 0), badChecksum, badMarker, vanPacket(0xd3, make([]byte, vanMaxPayload+1)...)} {
		if _, ok := parseVANFrame(invalid); ok {
			t.Fatalf("invalid frame accepted: %x", invalid)
		}
	}
	for n := 0; n <= vanMaxPayload; n++ {
		_, ok := decodeVANStatus(make([]byte, n), 0)
		if ok != (n == 16) {
			t.Fatalf("D3 length %d accepted = %v", n, ok)
		}
	}
}

func collectVANFrames(t *testing.T, chunks ...[]byte) [][]byte {
	t.Helper()
	var stream vanStream
	var frames [][]byte
	for _, chunk := range chunks {
		if err := stream.feed(chunk, func(frame vanFrame) error {
			frames = append(frames, vanPacket(frame.command, frame.payload...))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if stream.n >= len(stream.buffer) {
			t.Fatalf("unbounded retained buffer: %d", stream.n)
		}
	}
	return frames
}

func TestVANStreamSyntheticSplitsAndCoalescing(t *testing.T) {
	frames := [][]byte{vanPacket(0xd0), vanPacket(0xd1, 1, 80, 0, 10, 0, 0, 1), vanPacket(0xd3, syntheticVANStatus()...)}
	all := bytes.Join(frames, nil)
	for split := 0; split <= len(all); split++ {
		if got := collectVANFrames(t, all[:split], all[split:]); !reflect.DeepEqual(got, frames) {
			t.Fatalf("split %d = %x; want %x", split, got, frames)
		}
	}
	for size := 1; size <= len(all); size++ {
		var chunks [][]byte
		for i := 0; i < len(all); i += size {
			chunks = append(chunks, all[i:min(i+size, len(all))])
		}
		if got := collectVANFrames(t, chunks...); !reflect.DeepEqual(got, frames) {
			t.Fatalf("chunk size %d = %x", size, got)
		}
	}
}

func TestVANStreamNoiseAndResynchronization(t *testing.T) {
	good := vanPacket(0xd3, syntheticVANStatus()...)
	badChecksum := bytes.Clone(good)
	badChecksum[len(badChecksum)-1]++
	for _, noise := range [][]byte{
		bytes.Repeat([]byte{0x7f}, 10000),
		{0xf0, 0xd3, 255},
		badChecksum,
		{0xf0, 0xd3, 0, 1},
		append([]byte{0xf0, 0xd3, vanMaxPayload}, bytes.Repeat([]byte{0}, vanMaxPayload+1)...),
	} {
		if got := collectVANFrames(t, append(bytes.Clone(noise), good...)); !reflect.DeepEqual(got, [][]byte{good}) {
			t.Fatalf("resync failed after %x: %x", noise[:min(len(noise), 25)], got)
		}
	}
}

func TestVANStreamMaximumPayloadAndCallbackError(t *testing.T) {
	// Frame markers inside a valid payload must not cause premature resync.
	maximum := vanPacket(0x99, bytes.Repeat([]byte{0xf0}, vanMaxPayload)...)
	if got := collectVANFrames(t, maximum); !reflect.DeepEqual(got, [][]byte{maximum}) {
		t.Fatalf("maximum payload = %x", got)
	}
	var stream vanStream
	failure := errors.New("stop parsing")
	if err := stream.feed(maximum, func(vanFrame) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("callback error = %v", err)
	}
	if stream.n != 0 {
		t.Fatal("callback failure retained a full buffer")
	}
	if err := stream.feed(vanPacket(0xd0), func(vanFrame) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func FuzzVANParser(f *testing.F) {
	f.Add(vanPacket(0xd3, syntheticVANStatus()...), uint8(1))
	f.Add([]byte{0xf0, 0xd3, 255, 0xf0, 0xd0, 0, 0xc0}, uint8(5))
	f.Add(bytes.Repeat([]byte{0xf0}, 100), uint8(13))
	f.Fuzz(func(t *testing.T, data []byte, chunkSize uint8) {
		if frame, ok := parseVANFrame(data); ok {
			if len(frame.payload) > vanMaxPayload || !bytes.Equal(vanPacket(frame.command, frame.payload...), data) {
				t.Fatal("frame violated strict contract")
			}
			if frame.command == 0xd3 {
				if _, decoded := decodeVANStatus(frame.payload, 0); decoded != (len(frame.payload) == 16) {
					t.Fatal("D3 violated exact payload length")
				}
			}
		}
		// Arbitrary chunking must not affect decoding or retained memory bounds.
		var chunks [][]byte
		size := int(chunkSize) + 1
		for i := 0; i < len(data); i += size {
			chunks = append(chunks, data[i:min(i+size, len(data))])
		}
		if !reflect.DeepEqual(collectVANFrames(t, data), collectVANFrames(t, chunks...)) {
			t.Fatal("notification chunking changed frames")
		}
	})
}
