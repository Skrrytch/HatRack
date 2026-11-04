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
	EvCorrection  EventType = "correction"
)

// Status of a correction.
const (
	StatusApplied  = "applied"
	StatusProposed = "proposed" // waits for confirmation (corrections by an AI)
	StatusRejected = "rejected"
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

	// Corrections only: the events that no longer count and the events that
	// count instead. Nothing is ever deleted from the log.
	Supersedes  []string `json:"supersedes,omitempty"`
	Replacement []Event  `json:"replacement,omitempty"`
	Source      string   `json:"source,omitempty"` // user | ai
	Status      string   `json:"status,omitempty"` // applied | proposed | rejected
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
	case EvCorrection:
		if len(e.Supersedes) == 0 && len(e.Replacement) == 0 {
			return fmt.Errorf("correction %s changes nothing", e.ID)
		}
		switch e.Status {
		case "", StatusApplied, StatusProposed, StatusRejected:
		default:
			return fmt.Errorf("correction %s has unknown status %q", e.ID, e.Status)
		}
		for _, r := range e.Replacement {
			if r.Type == EvCorrection || r.Type == EvModuleError {
				return fmt.Errorf("correction %s: replacement %s must be a switch, activity or off", e.ID, r.ID)
			}
			if err := r.Validate(); err != nil {
				return fmt.Errorf("correction %s: %w", e.ID, err)
			}
		}
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
	if ev.Type == EvModuleError || ev.Type == EvCorrection || ev.TS.Before(s.LastTS) {
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

// Applied reports whether a correction counts.
func (e Event) Applied() bool {
	return e.Type == EvCorrection && (e.Status == "" || e.Status == StatusApplied)
}

// Effective returns the events that count: applied corrections drop the
// events they supersede and add their replacements. The result has no
// corrections in it and is sorted by time.
func Effective(events []Event) []Event {
	gone := map[string]bool{}
	var added []Event
	for _, e := range events {
		if e.Applied() {
			for _, id := range e.Supersedes {
				gone[id] = true
			}
			added = append(added, e.Replacement...)
		}
	}
	out := make([]Event, 0, len(events)+len(added))
	for _, list := range [][]Event{events, added} {
		for _, e := range list {
			if e.Type != EvCorrection && !gone[e.ID] {
				out = append(out, e)
			}
		}
	}
	SortEvents(out)
	return out
}

// Fold derives the state from a list of events in any order. Corrections are
// taken into account.
func Fold(events []Event) State {
	ev := Effective(events)
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
	StartID  string    `json:"start_id,omitempty"` // the event that opened it
	EndID    string    `json:"end_id,omitempty"`   // the event that closed it
}

// Duration of the interval.
func (i Interval) Duration() time.Duration { return i.End.Sub(i.Start) }

// Intervals derives the intervals from events, corrections included. An
// interval that is still open ends at now.
func Intervals(events []Event, now time.Time) []Interval {
	ev := Effective(events)
	var out []Interval
	var cur *Interval
	endID := ""
	closeAt := func(t time.Time) {
		if cur == nil {
			return
		}
		cur.End, cur.EndID = t, endID
		if cur.End.After(cur.Start) {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, e := range ev {
		endID = e.ID
		switch e.Type {
		case EvSwitch:
			closeAt(e.TS)
			cur = &Interval{Hat: e.Hat, Activity: e.Activity, Start: e.TS, StartID: e.ID}
		case EvActivity:
			if cur != nil {
				hat := cur.Hat
				closeAt(e.TS)
				cur = &Interval{Hat: hat, Activity: e.Activity, Start: e.TS, StartID: e.ID}
			}
		case EvOff:
			closeAt(e.TS)
		}
	}
	endID = ""
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
