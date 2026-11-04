package core

import (
	"errors"
	"fmt"
	"time"
)

// Edits of the time log. Each edit works on the timeline as it counts now
// (corrections applied) and is turned into exactly one correction event: the
// events that the edit removes are superseded, the events it adds are the
// replacement. Nothing is ever deleted.

// Errors of edits.
var (
	ErrEmptyRange = errors.New("the end must be after the start")
	ErrNoInterval = errors.New("this entry does not exist (any more)")
	ErrNoChange   = errors.New("nothing to change")
)

// Edit describes what an interval should be: from Start to End doing Hat
// and Activity. A zero End means "until now, and it goes on".
type Edit struct {
	Start, End    time.Time
	Hat, Activity string
}

// timeline returns the effective switch/activity/off events, sorted.
func timeline(events []Event) []Event {
	var out []Event
	for _, e := range Effective(events) {
		if e.Type == EvSwitch || e.Type == EvActivity || e.Type == EvOff {
			out = append(out, e)
		}
	}
	return out
}

func without(list []Event, id string) []Event {
	out := make([]Event, 0, len(list))
	for _, e := range list {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return out
}

func find(list []Event, id string) (Event, bool) {
	for _, e := range list {
		if e.ID == id {
			return e, true
		}
	}
	return Event{}, false
}

// stateAt is the state after all events up to and including t.
func stateAt(list []Event, t time.Time) State {
	s := NewState()
	for _, e := range list {
		if e.TS.After(t) {
			break
		}
		s = s.Apply(e)
	}
	return s
}

// planner builds the new timeline and remembers the device and time.
type planner struct {
	in Input
}

func (p planner) event(typ EventType, ts time.Time) Event {
	return NewEvent(p.in.Device, typ, ts)
}

// paint makes [s, e) one stretch of hat/activity (hat "" is a gap). What ran
// at e before goes on after it. With open (zero e), the stretch goes on and
// every later event is dropped.
func (p planner) paint(list []Event, s, e time.Time, hat, act string) []Event {
	open := e.IsZero()
	var after State
	hasAtE := false
	if !open {
		after = stateAt(list, e)
		for _, ev := range list {
			if ev.TS.Equal(e) {
				hasAtE = true
			}
		}
	}
	var out []Event
	for _, ev := range list {
		if ev.TS.Before(s) || (!open && !ev.TS.Before(e)) {
			out = append(out, ev)
		}
	}
	if hat != "" {
		sw := p.event(EvSwitch, s)
		sw.Hat, sw.Activity = hat, act
		out = append(out, sw)
	} else if st := stateAt(out, s); st.Mode == ModeWearing {
		off := p.event(EvOff, s)
		off.Hat = st.Hat
		out = append(out, off)
	}
	if !open && !hasAtE {
		switch after.Mode {
		case ModeWearing:
			sw := p.event(EvSwitch, e)
			sw.Hat, sw.Activity = after.Hat, after.Activity
			out = append(out, sw)
		default:
			if hat != "" {
				off := p.event(EvOff, e)
				off.Hat, off.Neutral = hat, after.Neutral
				out = append(out, off)
			}
		}
	}
	SortEvents(out)
	return out
}

func (p planner) check(ed Edit) error {
	if ed.Start.After(p.in.Now) || ed.End.After(p.in.Now.Add(time.Minute)) {
		return ErrFuture
	}
	if !ed.End.IsZero() && !ed.End.After(ed.Start) {
		return ErrEmptyRange
	}
	return nil
}

// correction compares two timelines and returns the correction that turns
// the first into the second, or nil if they are the same.
// normalize turns an activity change into a full switch where the hat it
// belongs to is no longer on before it (because an edit removed or changed
// the entry before it); otherwise it would not count.
func (p planner) normalize(list []Event) []Event {
	out := make([]Event, 0, len(list))
	st := NewState()
	for _, e := range list {
		if e.Type == EvActivity && e.Hat != "" && (st.Mode != ModeWearing || st.Hat != e.Hat) {
			sw := e
			sw.ID, sw.Type, sw.Device = NewID(), EvSwitch, p.in.Device
			e = sw
		}
		st = st.Apply(e)
		out = append(out, e)
	}
	return out
}

func (p planner) correction(before, after []Event, reason string) *Event {
	after = p.normalize(after)
	had := map[string]bool{}
	for _, e := range before {
		had[e.ID] = true
	}
	// an added event that equals a removed one keeps the old one, so a
	// correction only holds what really changed
	inAfter := map[string]bool{}
	for _, e := range after {
		inAfter[e.ID] = true
	}
	removed := map[string][]Event{}
	for _, e := range before {
		if !inAfter[e.ID] {
			removed[sameKey(e)] = append(removed[sameKey(e)], e)
		}
	}
	after = append([]Event(nil), after...)
	for i, e := range after {
		if had[e.ID] {
			continue
		}
		if olds := removed[sameKey(e)]; len(olds) > 0 {
			after[i] = olds[0]
			removed[sameKey(e)] = olds[1:]
		}
	}
	has := map[string]bool{}
	var repl []Event
	for _, e := range after {
		has[e.ID] = true
		if !had[e.ID] {
			repl = append(repl, e)
		}
	}
	var sup []string
	for _, e := range before {
		if !has[e.ID] {
			sup = append(sup, e.ID)
		}
	}
	if len(sup) == 0 && len(repl) == 0 {
		return nil
	}
	c := p.event(EvCorrection, p.in.Now)
	c.Supersedes, c.Replacement = sup, repl
	c.Source, c.Status, c.Note = "user", StatusApplied, reason
	return &c
}

func sameKey(e Event) string {
	return fmt.Sprintf("%d|%s|%s|%s|%t", e.TS.UnixNano(), e.Type, e.Hat, e.Activity, e.Neutral)
}

// PlanChange edits the interval that starts with event startID: its start,
// end, hat and activity. Moving a boundary moves the neighbour's boundary
// with it.
func PlanChange(events []Event, startID string, ed Edit, in Input) (*Event, error) {
	p := planner{in}
	if err := p.check(ed); err != nil {
		return nil, err
	}
	list := timeline(events)
	var iv *Interval
	for _, i := range Intervals(events, in.Now) {
		if i.StartID == startID {
			iv = &i
			break
		}
	}
	if iv == nil {
		return nil, ErrNoInterval
	}
	next := without(list, iv.StartID)
	end := ed.End
	if !iv.Open {
		if end.IsZero() {
			end = iv.End
		}
		if iv.EndID != "" && end.Before(iv.End) {
			// shorter: the next entry (or the gap) starts earlier, where this
			// one ends now. Longer is handled by paint: it covers what was
			// there, and what ran at the new end goes on after it.
			ev, _ := find(next, iv.EndID)
			next = without(next, iv.EndID)
			moved := ev
			moved.ID, moved.TS, moved.Device = NewID(), end, in.Device
			next = append(next, moved)
			SortEvents(next)
		}
	} else if !end.IsZero() {
		// an open entry gets an end: from then on nothing runs
		off := p.event(EvOff, end)
		off.Hat = ed.Hat
		next = append(next, off)
		SortEvents(next)
	}
	next = p.paint(next, ed.Start, end, ed.Hat, ed.Activity)
	c := p.correction(list, next, "change")
	if c == nil {
		return nil, ErrNoChange
	}
	return c, nil
}

// PlanInsert adds an entry, for example to fill a gap or for a forgotten
// switch. Whatever was in that time is replaced. A zero End means "since
// Start, and it goes on".
func PlanInsert(events []Event, ed Edit, in Input) (*Event, error) {
	p := planner{in}
	if err := p.check(ed); err != nil {
		return nil, err
	}
	list := timeline(events)
	c := p.correction(list, p.paint(list, ed.Start, ed.End, ed.Hat, ed.Activity), "insert")
	if c == nil {
		return nil, ErrNoChange
	}
	return c, nil
}

// PlanDelete removes an entry; its time becomes a gap.
func PlanDelete(events []Event, startID string, in Input) (*Event, error) {
	p := planner{in}
	list := timeline(events)
	for _, i := range Intervals(events, in.Now) {
		if i.StartID != startID {
			continue
		}
		end := i.End
		if i.Open {
			end = time.Time{}
		}
		next := without(list, i.StartID)
		next = p.paint(next, i.Start, end, "", "")
		if i.Open {
			// nothing runs from the start of the deleted entry on
			if st := stateAt(next, i.Start); st.Mode == ModeWearing {
				off := p.event(EvOff, i.Start)
				off.Hat = st.Hat
				next = append(next, off)
				SortEvents(next)
			}
		}
		c := p.correction(list, next, "delete")
		if c == nil {
			return nil, ErrNoChange
		}
		return c, nil
	}
	return nil, ErrNoInterval
}

// PlanUndo reverses a correction: what it added no longer counts, what it
// superseded counts again (as copies, since superseded events stay
// superseded).
func PlanUndo(events []Event, correctionID string, in Input) (*Event, error) {
	p := planner{in}
	var c *Event
	byID := map[string]Event{}
	for i, e := range events {
		byID[e.ID] = e
		if e.ID == correctionID && e.Applied() {
			c = &events[i]
		}
	}
	if c == nil {
		return nil, ErrNoInterval
	}
	list := timeline(events)
	next := list
	for _, r := range c.Replacement {
		next = without(next, r.ID)
	}
	for _, id := range c.Supersedes {
		if old, ok := byID[id]; ok {
			cp := old
			cp.ID = NewID()
			next = append(next, cp)
		} else {
			// the superseded event was itself a replacement of an older correction
			for _, e := range events {
				for _, r := range e.Replacement {
					if r.ID == id {
						cp := r
						cp.ID = NewID()
						next = append(next, cp)
					}
				}
			}
		}
	}
	SortEvents(next)
	u := p.correction(list, next, "undo")
	if u == nil {
		return nil, ErrNoChange
	}
	return u, nil
}
