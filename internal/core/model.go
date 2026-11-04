// Package core holds the HatRack data model: events, state, intervals,
// name resolution and configuration. It does no desktop or network I/O.
package core

import (
	"crypto/rand"
	"fmt"
	"sort"
	"time"
)

// EventType is the kind of an entry in the event log.
type EventType string

const (
	EvSwitch      EventType = "switch"
	EvActivity    EventType = "activity"
	EvOff         EventType = "off"
	EvModuleError EventType = "module_error"
)

// Event is one append-only entry of the log. The ID and the device timestamp
// make events safe to queue offline and to send more than once.
type Event struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Device   string    `json:"device,omitempty"`
	Type     EventType `json:"type"`
	Hat      string    `json:"hat,omitempty"`      // hat key
	Activity string    `json:"activity,omitempty"` // activity name
	Neutral  bool      `json:"neutral,omitempty"`  // off: restore the neutral look
	Note     string    `json:"note,omitempty"`
}

// NewID returns a random UUID (version 4).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewEvent creates an event with a fresh ID.
func NewEvent(device string, typ EventType, ts time.Time) Event {
	return Event{ID: NewID(), TS: ts, Device: device, Type: typ}
}

// Validate checks that an event is well formed.
func (e Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("event without id")
	}
	if e.TS.IsZero() {
		return fmt.Errorf("event %s without timestamp", e.ID)
	}
	switch e.Type {
	case EvSwitch:
		if e.Hat == "" {
			return fmt.Errorf("switch event %s without hat", e.ID)
		}
	case EvActivity, EvOff, EvModuleError:
	default:
		return fmt.Errorf("event %s has unknown type %q", e.ID, e.Type)
	}
	return nil
}

// SortEvents orders events by timestamp. The sort is stable, so events with
// the same timestamp keep their arrival order.
func SortEvents(ev []Event) {
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].TS.Before(ev[j].TS) })
}

// Mode is the coarse state of the rack.
type Mode string

const (
	ModeNone    Mode = "none"
	ModeWearing Mode = "wearing"
	ModePaused  Mode = "paused"
)

// State is the single state per user, shared by all devices.
type State struct {
	Mode     Mode      `json:"mode"`
	Hat      string    `json:"hat,omitempty"`
	Activity string    `json:"activity,omitempty"`
	Since    time.Time `json:"since,omitzero"`
	// Neutral is set while paused when the neutral look was requested.
	Neutral bool `json:"neutral,omitempty"`
	// EventID is the event this state comes from.
	EventID string `json:"event_id,omitempty"`
	// LastTS is the timestamp of the newest event that shaped the state.
	LastTS time.Time `json:"last_ts,omitzero"`
}

// NewState returns the state before any hat was worn.
func NewState() State { return State{Mode: ModeNone} }

// Apply folds one event into the state. An event older than the newest one
// that shaped the state is part of the log but does not change the state.
func (s State) Apply(ev Event) State {
	if s.Mode == "" {
		s.Mode = ModeNone
	}
	if ev.Type == EvModuleError || ev.TS.Before(s.LastTS) {
		return s
	}
	switch ev.Type {
	case EvSwitch:
		return State{Mode: ModeWearing, Hat: ev.Hat, Activity: ev.Activity,
			Since: ev.TS, EventID: ev.ID, LastTS: ev.TS}
	case EvActivity:
		if s.Mode != ModeWearing {
			return s
		}
		s.Activity, s.Since, s.EventID, s.LastTS = ev.Activity, ev.TS, ev.ID, ev.TS
	case EvOff:
		switch s.Mode {
		case ModeWearing:
			s.Mode, s.Neutral = ModePaused, ev.Neutral
			s.Since, s.EventID, s.LastTS = ev.TS, ev.ID, ev.TS
		case ModePaused:
			if ev.Neutral {
				s.Neutral, s.EventID, s.LastTS = true, ev.ID, ev.TS
			}
		}
	}
	return s
}

// Fold derives the state from a list of events in any order.
func Fold(events []Event) State {
	ev := append([]Event(nil), events...)
	SortEvents(ev)
	s := NewState()
	for _, e := range ev {
		s = s.Apply(e)
	}
	return s
}

// Look returns the key of the hat whose look the desktop should show, or ""
// for the neutral look.
func (s State) Look() string {
	switch s.Mode {
	case ModeWearing:
		return s.Hat
	case ModePaused:
		if !s.Neutral {
			return s.Hat
		}
	}
	return ""
}

// Interval is a stretch of time spent wearing one hat and activity.
type Interval struct {
	Hat      string    `json:"hat"`
	Activity string    `json:"activity,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Open     bool      `json:"open,omitempty"`
}

// Duration of the interval.
func (i Interval) Duration() time.Duration { return i.End.Sub(i.Start) }

// Intervals derives the intervals from events. An interval that is still
// open ends at now.
func Intervals(events []Event, now time.Time) []Interval {
	ev := append([]Event(nil), events...)
	SortEvents(ev)
	var out []Interval
	var cur *Interval
	closeAt := func(t time.Time) {
		if cur == nil {
			return
		}
		cur.End = t
		if cur.End.After(cur.Start) {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, e := range ev {
		switch e.Type {
		case EvSwitch:
			closeAt(e.TS)
			cur = &Interval{Hat: e.Hat, Activity: e.Activity, Start: e.TS}
		case EvActivity:
			if cur != nil {
				hat := cur.Hat
				closeAt(e.TS)
				cur = &Interval{Hat: hat, Activity: e.Activity, Start: e.TS}
			}
		case EvOff:
			closeAt(e.TS)
		}
	}
	if cur != nil {
		cur.Open = true
		if now.After(cur.Start) {
			closeAt(now)
			out[len(out)-1].Open = true
		}
	}
	return out
}

// Clip cuts intervals to [from, to) and drops those outside.
func Clip(in []Interval, from, to time.Time) []Interval {
	var out []Interval
	for _, i := range in {
		if !i.End.After(from) || !i.Start.Before(to) {
			continue
		}
		if i.Start.Before(from) {
			i.Start = from
		}
		if i.End.After(to) {
			i.End = to
			i.Open = false
		}
		out = append(out, i)
	}
	return out
}

// ActivityTotal is the time spent on one activity of a hat.
type ActivityTotal struct {
	Activity string        `json:"activity"` // "" means no activity
	Duration time.Duration `json:"duration"`
}

// HatTotal is the time spent in one hat, split by activity.
type HatTotal struct {
	Hat        string          `json:"hat"`
	Duration   time.Duration   `json:"duration"`
	Activities []ActivityTotal `json:"activities"`
}

// Totals sums intervals per hat and per hat/activity, largest first.
func Totals(in []Interval) []HatTotal {
	byHat := map[string]*HatTotal{}
	byAct := map[string]map[string]time.Duration{}
	var order []string
	for _, i := range in {
		t := byHat[i.Hat]
		if t == nil {
			t = &HatTotal{Hat: i.Hat}
			byHat[i.Hat] = t
			byAct[i.Hat] = map[string]time.Duration{}
			order = append(order, i.Hat)
		}
		t.Duration += i.Duration()
		byAct[i.Hat][i.Activity] += i.Duration()
	}
	out := make([]HatTotal, 0, len(order))
	for _, h := range order {
		t := byHat[h]
		for a, d := range byAct[h] {
			t.Activities = append(t.Activities, ActivityTotal{a, d})
		}
		sort.Slice(t.Activities, func(i, j int) bool {
			if t.Activities[i].Duration != t.Activities[j].Duration {
				return t.Activities[i].Duration > t.Activities[j].Duration
			}
			return t.Activities[i].Activity < t.Activities[j].Activity
		})
		out = append(out, *t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Duration > out[j].Duration })
	return out
}
