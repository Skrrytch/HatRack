package sensors

import (
	"time"

	"hatrack/internal/core"
)

// IdleReportAfter is the idle time from which the user counts as inactive.
// Shorter pauses are not reported at all; the agent decides on its own
// thresholds how long an absence must be to matter.
const IdleReportAfter = 60 * time.Second

// idleRestartSlack is how far the start of an idle span may move between two
// readings before it counts as a new span (input in between).
const idleRestartSlack = 5 * time.Second

// Attribute values of the idle sensor.
const (
	idleInactive = "inactive"
	idleLocked   = "locked"
)

// idleLogic turns raw readings (idle time, lock state) into sensor states.
// Locked wins over inactive; it emits only when the state changes. It is
// not safe for concurrent use; each backend run owns one.
type idleLogic struct {
	inactive bool      // idle time at or above IdleReportAfter
	idleAt   time.Time // start of the current inactivity
	locked   bool
	lockAt   time.Time

	kind string    // last emitted kind: "", inactive, locked
	at   time.Time // its at
}

// Idle takes a reading of the time since the last input. The returned values
// are meaningful only if changed is true: the state to report with the
// moment it began (now for "active").
func (l *idleLogic) Idle(now time.Time, idleFor time.Duration) (time.Time, core.Attrs, bool) {
	if idleFor < 0 {
		idleFor = 0
	}
	if idleFor >= IdleReportAfter {
		start := now.Add(-idleFor)
		// A start that moved on means there was input and a new idle span.
		if !l.inactive || start.After(l.idleAt.Add(idleRestartSlack)) {
			l.idleAt = start
		}
		l.inactive = true
	} else {
		l.inactive = false
	}
	return l.result(now)
}

// Lock takes a change of the lock state. Unlocking reports by the idle
// time of the last Idle reading, so feed a fresh one first.
func (l *idleLogic) Lock(now time.Time, locked bool) (time.Time, core.Attrs, bool) {
	if locked && !l.locked {
		l.lockAt = now
	}
	l.locked = locked
	return l.result(now)
}

func (l *idleLogic) result(now time.Time) (time.Time, core.Attrs, bool) {
	kind, at := "", now
	switch {
	case l.locked:
		kind, at = idleLocked, l.lockAt
	case l.inactive:
		kind, at = idleInactive, l.idleAt
	}
	if kind == l.kind && (kind == "" || at.Equal(l.at)) {
		return time.Time{}, nil, false
	}
	l.kind, l.at = kind, at
	if kind == "" {
		return at, nil, true
	}
	return at, core.Attrs{"kind": kind}, true
}
