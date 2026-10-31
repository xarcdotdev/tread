package control

import (
	"testing"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

func TestRelativeLevelStepsWithinDriverRange(t *testing.T) {
	r := observed.LevelRange{Min: 2, Max: 6, Step: 2}
	for _, tc := range []struct {
		speed   int
		action  string
		level   int
		bounded bool
		bad     bool
	}{
		{2, "speed-up", 4, false, false},
		{6, "speed-down", 4, false, false},
		{2, "speed-down", 2, true, false},
		{6, "speed-up", 6, true, false},
		{3, "speed-up", 0, false, true},
		{1, "speed-up", 0, false, true},
	} {
		level, bounded, err := relativeLevel(r, tc.speed, tc.action)
		if level != tc.level || bounded != tc.bounded || (err != nil) != tc.bad {
			t.Errorf("%d %s: level=%d bounded=%v err=%v", tc.speed, tc.action, level, bounded, err)
		}
	}
}
