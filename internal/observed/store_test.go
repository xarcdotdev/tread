package observed

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"
)

func pointer[T any](value T) *T { return &value }

func TestMissingAndZeroJSON(t *testing.T) {
	s := NewStore("desk", Capabilities{}, StoreOptions{})
	assertJSON := func(wantSpeed, wantRaw string) {
		t.Helper()
		data, err := json.Marshal(s.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Device       string                               `json:"device"`
			State        map[string]json.RawMessage           `json:"state"`
			Measurements map[Field]map[string]json.RawMessage `json:"measurements"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Device != "desk" || len(got.Measurements) != 6 {
			t.Fatalf("unexpected snapshot: %s", data)
		}
		for field, measurement := range got.Measurements {
			want := "null"
			if field == FieldSpeed {
				want = wantSpeed
			}
			if string(measurement["value"]) != want {
				t.Errorf("%s value = %s, want %s", field, measurement["value"], want)
			}
		}
		if string(got.State["raw_code"]) != wantRaw {
			t.Errorf("raw_code = %s, want %s", got.State["raw_code"], wantRaw)
		}
	}
	assertJSON("null", "null")
	generation := s.BeginConnection()
	s.Apply(generation, Update{
		State:        &State{Value: StatePaused, RawCode: pointer(uint16(0))},
		Measurements: map[Field]Measurement{FieldSpeed: {Value: pointer(0.0)}},
	})
	assertJSON("0", "0")
	s.Apply(generation, Update{Measurements: map[Field]Measurement{FieldSpeed: {}}})
	assertJSON("null", "0")
}

func TestPartialUpdatesAndIndependentFreshness(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.FixedZone("test", 7200))
	s := NewStore("desk", Capabilities{}, StoreOptions{Now: func() time.Time { return now }})
	generation := s.BeginConnection()
	s.SetConnection(generation, ConnectionConnected)
	s.SetTelemetry(generation, TelemetryActive)
	s.Apply(generation, Update{
		State: &State{Value: StateRunning, RawCode: pointer(uint16(3))},
		Measurements: map[Field]Measurement{
			FieldSpeed:    {Value: pointer(3.2)},
			FieldDistance: {Value: pointer(10.0), Unit: UnitMetres, UnitConfidence: UnitVerified, DataConfidence: DataVerified},
		},
	})
	initial := s.Snapshot()
	if initial.State.Timestamp.Location() != time.UTC || initial.Measurements[FieldSpeed].Timestamp.Location() != time.UTC {
		t.Fatal("timestamps must be UTC")
	}
	if initial.State.Stale || initial.Measurements[FieldSpeed].Stale || initial.Telemetry != TelemetryActive {
		t.Fatal("new readings are stale")
	}
	// Unknown speed units must remain unknown, not calibrated or normalized.
	speed := initial.Measurements[FieldSpeed]
	if *speed.Value != 3.2 || speed.Unit != UnitUnknown || speed.UnitConfidence != UnitUnknownConfidence || speed.DataConfidence != DataExperimental {
		t.Fatalf("speed changed or gained confidence: %+v", speed)
	}
	now = now.Add(4 * time.Second)
	s.Apply(generation, Update{Measurements: map[Field]Measurement{
		FieldSpeed: {Value: pointer(4.1), Unit: UnitKMH, UnitConfidence: UnitInferred},
	}})
	now = now.Add(time.Second)
	got := s.Snapshot()
	if !got.State.Stale || !got.Measurements[FieldDistance].Stale || got.Measurements[FieldSpeed].Stale {
		t.Fatalf("fields did not age independently: %+v", got)
	}
	if !got.State.Timestamp.Equal(*initial.State.Timestamp) || !got.Measurements[FieldDistance].Timestamp.Equal(*initial.Measurements[FieldDistance].Timestamp) {
		t.Fatal("omitted fields received new timestamps")
	}
	if *got.Measurements[FieldDistance].Value != 10 || got.State.Value != StateRunning {
		t.Fatal("omitted fields were erased")
	}
	if got.Telemetry != TelemetryActive {
		t.Fatal("fresh speed should keep telemetry active")
	}
	now = now.Add(4 * time.Second)
	got = s.Snapshot()
	if !got.Measurements[FieldSpeed].Stale || got.Telemetry != TelemetryStale {
		t.Fatal("idle notification stream did not become stale")
	}
	s.Apply(generation, Update{Measurements: map[Field]Measurement{FieldSpeed: {Value: pointer(0.0)}}})
	if s.Snapshot().Telemetry != TelemetryActive {
		t.Fatal("fresh zero reading should restore active telemetry")
	}
}

func TestFreshnessOption(t *testing.T) {
	now := time.Now()
	s := NewStore("desk", Capabilities{}, StoreOptions{Freshness: time.Second, Now: func() time.Time { return now }})
	generation := s.BeginConnection()
	s.Apply(generation, Update{State: &State{Value: StatePaused}})
	now = now.Add(time.Second)
	if !s.Snapshot().State.Stale {
		t.Fatal("configured freshness threshold ignored")
	}
}

func TestSnapshotsAndInputsAreDetached(t *testing.T) {
	static := Capabilities{Actions: []string{"speed"}, SpeedLevels: &LevelRange{Min: 1, Max: 8, Step: 1}, ObservedFields: []Field{FieldSpeed}}
	s := NewStore("desk", static, StoreOptions{})
	static.Actions[0] = "mutated"
	static.SpeedLevels.Max = 99
	static.ObservedFields[0] = FieldPulse
	generation := s.BeginConnection()
	update := Update{
		State:        &State{Value: StateRunning, RawCode: pointer(uint16(3))},
		Measurements: map[Field]Measurement{FieldSpeed: {Value: pointer(3.2)}},
		Capabilities: &Capabilities{Actions: []string{"pause"}, ObservedFields: []Field{FieldDistance}},
	}
	s.Apply(generation, update)
	*update.State.RawCode = 99
	*update.Measurements[FieldSpeed].Value = 99
	update.Capabilities.Actions[0] = "mutated"
	update.Capabilities.ObservedFields[0] = FieldPulse
	first := s.Snapshot()
	if first.Capabilities.Actions[0] != "speed" || first.Capabilities.Actions[1] != "pause" || first.Capabilities.SpeedLevels.Max != 8 || first.Capabilities.ObservedFields[0] != FieldSpeed || first.Capabilities.ObservedFields[1] != FieldDistance || *first.State.RawCode != 3 || *first.Measurements[FieldSpeed].Value != 3.2 {
		t.Fatalf("store aliases its inputs: %+v", first)
	}
	want := s.Snapshot()
	first.Capabilities.Actions[0] = "mutated"
	first.Capabilities.SpeedLevels.Min = 99
	first.Capabilities.ObservedFields[0] = FieldPulse
	*first.State.RawCode = 99
	*first.State.Timestamp = time.Time{}
	*first.Measurements[FieldSpeed].Value = 99
	*first.Measurements[FieldSpeed].Timestamp = time.Time{}
	delete(first.Measurements, FieldDistance)
	if got := s.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mutation changed store: %+v", got)
	}
}

func TestConnectionGenerationsInvalidateLiveData(t *testing.T) {
	for _, terminal := range []ConnectionStatus{ConnectionDisconnected, ConnectionDisabled, ConnectionFault} {
		t.Run(string(terminal), func(t *testing.T) {
			static := Capabilities{Actions: []string{"speed"}, SpeedLevels: &LevelRange{Min: 1, Max: 8, Step: 1}, ObservedFields: []Field{FieldSpeed}}
			s := NewStore("desk", static, StoreOptions{})
			generation := s.BeginConnection()
			s.SetConnection(generation, ConnectionConnected)
			s.SetTelemetry(generation, TelemetryActive)
			update := Update{
				State:        &State{Value: StateRunning},
				Measurements: map[Field]Measurement{FieldSpeed: {Value: pointer(3.2)}},
				Capabilities: &Capabilities{Actions: []string{"pause"}, SpeedLevels: &LevelRange{Min: 0, Max: 100, Step: 10}, ObservedFields: []Field{FieldPulse}},
			}
			s.Apply(generation, update)
			if s.Snapshot().Capabilities.SpeedLevels.Max != 8 {
				t.Fatal("discovery overwrote static command limits")
			}
			if !s.SetConnection(generation, terminal) {
				t.Fatal("terminal connection update rejected")
			}
			got := s.Snapshot()
			if got.Generation <= generation || got.Connection != terminal || got.State.Value != StateUnknown || got.State.Timestamp != nil || got.Measurements[FieldSpeed].Value != nil || got.Measurements[FieldSpeed].Timestamp != nil || !reflect.DeepEqual(got.Capabilities, static) {
				t.Fatalf("terminal connection retained live data: %+v", got)
			}
			if s.Apply(generation, update) || s.SetConnection(generation, ConnectionConnected) || s.SetTelemetry(generation, TelemetryActive) || s.Apply(got.Generation, update) {
				t.Fatal("late or disconnected callback accepted")
			}
			next := s.BeginConnection()
			if next <= got.Generation || s.Snapshot().Connection != ConnectionConnecting || s.Apply(generation, update) || !s.Apply(next, update) {
				t.Fatal("reconnected generation did not isolate callbacks")
			}
			// A new attempt without an explicit disconnect also invalidates data.
			s.BeginConnection()
			if s.Snapshot().Measurements[FieldSpeed].Value != nil || !reflect.DeepEqual(s.Snapshot().Capabilities, static) {
				t.Fatal("reconnect retained prior discovery/readings")
			}
		})
	}
}

func TestCapabilityPatchReplacementAndUnknownFields(t *testing.T) {
	s := NewStore("desk", Capabilities{Actions: []string{"start"}}, StoreOptions{})
	generation := s.BeginConnection()
	s.Apply(generation, Update{Capabilities: &Capabilities{ObservedFields: []Field{FieldSpeed, FieldSpeed}}})
	s.Apply(generation, Update{})
	if len(s.Snapshot().Capabilities.ObservedFields) != 1 {
		t.Fatal("empty patch changed capabilities or discovery was not deduplicated")
	}
	s.Apply(generation, Update{Capabilities: &Capabilities{}, Measurements: map[Field]Measurement{
		Field("vendor"): {Value: pointer(3.0)},
	}})
	got := s.Snapshot()
	if len(got.Capabilities.ObservedFields) != 0 || len(got.Capabilities.Actions) != 1 || len(got.Measurements) != 6 {
		t.Fatalf("discovered metadata was not replaced or unknown field accepted: %+v", got)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := NewStore("desk", Capabilities{}, StoreOptions{})
	generation := s.BeginConnection()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Apply(generation, Update{Measurements: map[Field]Measurement{FieldSpeed: {Value: pointer(float64(j))}}})
				s.Snapshot()
			}
		}()
	}
	wg.Wait()
}
