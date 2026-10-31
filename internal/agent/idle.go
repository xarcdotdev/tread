package agent

import (
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

// One connection's accepted readings, guarded by host.mu. Receipt times retain
// their monotonic component; snapshot timestamps intentionally do not.
type idleReadings struct {
	stateAt, speedAt, since time.Time
	paused, zero            bool
}

func (i *idleReadings) freshStopped(now time.Time) bool {
	return i.paused && i.zero && !i.stateAt.IsZero() && !i.speedAt.IsZero() &&
		now.Sub(i.stateAt) < observed.DefaultFreshness && now.Sub(i.speedAt) < observed.DefaultFreshness
}

func (i *idleReadings) update(now time.Time, u observed.Update) {
	// Check before replacing receipt times: a gap between packets must not be
	// hidden by a fresh packet arriving just before the next ticker read.
	if !i.freshStopped(now) {
		i.since = time.Time{}
	}
	if u.State != nil {
		i.stateAt = now
		i.paused = u.State.Value == observed.StatePaused
	}
	if speed, ok := u.Measurements[observed.FieldSpeed]; ok {
		i.speedAt = now
		i.zero = speed.Value != nil && *speed.Value == 0
	}
	if !i.freshStopped(now) {
		i.since = time.Time{}
	} else if i.since.IsZero() {
		i.since = now
	}
}

func (i *idleReadings) expired(now time.Time, timeout time.Duration) bool {
	if !i.freshStopped(now) {
		i.since = time.Time{}
	}
	return timeout > 0 && !i.since.IsZero() && now.Sub(i.since) >= timeout
}
