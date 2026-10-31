package event

import (
	"testing"
	"time"
)

func TestSpeedRequiresPositiveLevel(t *testing.T) {
	for _, level := range []int{-1, 0, 1, 8, 9, 12} {
		e := New("desk", Speed, level)
		if err := e.Validate(time.Now()); (err == nil) != (level > 0) {
			t.Errorf("level %d: %v", level, err)
		}
	}
}

func TestOtherActionsRequireZeroValue(t *testing.T) {
	for _, action := range []Action{Start, Stop, Pause} {
		for _, value := range []int{-1, 0, 1, 12} {
			e := New("desk", action, value)
			if err := e.Validate(time.Now()); (err == nil) != (value == 0) {
				t.Errorf("%s value %d: %v", action, value, err)
			}
		}
	}
}
