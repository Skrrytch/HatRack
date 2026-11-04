package core

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Errors for commands that do not apply in the current state.
var (
	ErrNoHatToResume = errors.New("no hat to resume")
	ErrNoHat         = errors.New("no hat is on")
	ErrPaused        = errors.New("the hat is paused")
	ErrFuture        = errors.New("the time is in the future")
)

// Ask answers for resume_activity: ask.
const (
	AnswerNone = ""
	AnswerYes  = "yes"
	AnswerNo   = "no"
)

// Input of a command, with the context needed to turn it into an event.
type Input struct {
	Device string
	Now    time.Time
	At     time.Time // zero: now
}

func (in Input) ts() (time.Time, error) {
	if in.At.IsZero() {
		return in.Now, nil
	}
	if in.At.After(in.Now.Add(time.Minute)) {
		return time.Time{}, ErrFuture
	}
	return in.At, nil
}

func hatItems(hats []Hat) []Named {
	out := make([]Named, len(hats))
	for i, h := range hats {
		out[i] = Named{Key: h.Key, Name: h.DisplayName()}
	}
	return out
}

func activityItems(h Hat) []Named {
	out := make([]Named, len(h.Activities))
	for i, a := range h.Activities {
		out[i] = Named{Key: a, Name: a}
	}
	return out
}

// ResolveHat finds an active hat by name or prefix.
func ResolveHat(cfg Config, name string) (Hat, error) {
	n, err := Resolve(name, hatItems(cfg.ActiveHats()))
	if err != nil {
		return Hat{}, withKind(err, "hat")
	}
	h, _ := cfg.Hat(n.Key)
	return h, nil
}

// ResolveActivity finds an activity within one hat.
func ResolveActivity(h Hat, name string) (string, error) {
	n, err := Resolve(name, activityItems(h))
	if err != nil {
		return "", withKind(err, "activity")
	}
	return n.Key, nil
}

// OnArgs are the arguments of "hat on".
type OnArgs struct {
	Hat         string // empty: resume the paused hat
	Activity    string // from "do"; empty if not given
	HasActivity bool
	Answer      string // for resume_activity: ask
	Detail      string // optional free text below the activity
}

// ResumeInfo tells the CLI whether it has to ask before putting on a hat.
type ResumeInfo struct {
	Hat  Hat
	Mode string
	Last string // last activity used in this hat, still valid
}

// InfoOn resolves the hat of "hat on" and reports its resume setting.
func InfoOn(cfg Config, last map[string]string, name string) (ResumeInfo, error) {
	h, err := ResolveHat(cfg, name)
	if err != nil {
		return ResumeInfo{}, err
	}
	ri := ResumeInfo{Hat: h, Mode: cfg.ResumeMode(h)}
	if l := last[h.Key]; l != "" {
		if a, err := ResolveActivity(h, l); err == nil {
			ri.Last = a
		}
	}
	return ri, nil
}

// PlanOn turns "hat on" into an event. A nil event means nothing changes.
func PlanOn(cfg Config, st State, last map[string]string, a OnArgs, in Input) (*Event, error) {
	ts, err := in.ts()
	if err != nil {
		return nil, err
	}
	if a.Hat == "" {
		if a.HasActivity {
			// "hat on do x" without a hat: treat like "hat do x" after resuming.
			if st.Mode != ModePaused {
				return nil, ErrNoHatToResume
			}
		}
		switch st.Mode {
		case ModeNone:
			return nil, ErrNoHatToResume
		case ModeWearing:
			return nil, nil
		}
		// resuming keeps the detail, unless a new activity is chosen
		act, detail := st.Activity, st.Detail
		if a.HasActivity {
			h, _ := cfg.Hat(st.Hat)
			if act, err = ResolveActivity(h, a.Activity); err != nil {
				return nil, err
			}
			detail = a.Detail
		}
		ev := NewEvent(in.Device, EvSwitch, ts)
		ev.Hat, ev.Activity, ev.Detail = st.Hat, act, detail
		return &ev, nil
	}

	ri, err := InfoOn(cfg, last, a.Hat)
	if err != nil {
		return nil, err
	}
	h := ri.Hat
	if st.Mode == ModeWearing && st.Hat == h.Key && !a.HasActivity {
		return nil, nil
	}
	act := ""
	if a.HasActivity {
		if a.Activity != "-" {
			if act, err = ResolveActivity(h, a.Activity); err != nil {
				return nil, err
			}
		}
	} else {
		switch ri.Mode {
		case ResumeLast:
			act = ri.Last
		case ResumeAsk:
			if a.Answer == AnswerYes {
				act = ri.Last
			}
		}
	}
	detail := strings.TrimSpace(a.Detail)
	if st.Mode == ModeWearing && st.Hat == h.Key && st.Activity == act && st.Detail == detail {
		return nil, nil
	}
	ev := NewEvent(in.Device, EvSwitch, ts)
	ev.Hat, ev.Activity, ev.Detail = h.Key, act, detail
	return &ev, nil
}

// PlanDo turns "hat do" into an event. activity "-" clears it. A new
// activity starts without a detail unless one is given.
func PlanDo(cfg Config, st State, activity, detail string, in Input) (*Event, error) {
	ts, err := in.ts()
	if err != nil {
		return nil, err
	}
	switch st.Mode {
	case ModeNone:
		return nil, ErrNoHat
	case ModePaused:
		return nil, ErrPaused
	}
	act := ""
	if strings.TrimSpace(activity) != "-" {
		h, _ := cfg.Hat(st.Hat)
		if act, err = ResolveActivity(h, activity); err != nil {
			return nil, err
		}
	}
	detail = strings.TrimSpace(detail)
	if act == st.Activity && detail == st.Detail {
		return nil, nil
	}
	if act == st.Activity && st.Detail == "" {
		// the first detail of the running entry names it from its start
		return PlanDetail(st, detail, in)
	}
	ev := NewEvent(in.Device, EvActivity, ts)
	ev.Hat, ev.Activity, ev.Detail = st.Hat, act, detail
	return &ev, nil
}

// PlanDetail sets the detail of the current activity. The first detail of a
// running entry applies to all of it (a detail event); a changed detail
// starts a new entry (an activity event). "-" clears it.
func PlanDetail(st State, detail string, in Input) (*Event, error) {
	ts, err := in.ts()
	if err != nil {
		return nil, err
	}
	switch st.Mode {
	case ModeNone:
		return nil, ErrNoHat
	case ModePaused:
		return nil, ErrPaused
	}
	detail = strings.TrimSpace(detail)
	if detail == "-" {
		detail = ""
	}
	if detail == st.Detail {
		return nil, nil
	}
	typ := EvActivity
	if st.Detail == "" {
		typ = EvDetail
	}
	ev := NewEvent(in.Device, typ, ts)
	ev.Hat, ev.Activity, ev.Detail = st.Hat, st.Activity, detail
	return &ev, nil
}

// PlanOff turns "hat off" into an event. A nil event means nothing changes.
func PlanOff(st State, neutral bool, in Input) (*Event, error) {
	ts, err := in.ts()
	if err != nil {
		return nil, err
	}
	switch st.Mode {
	case ModeNone:
		return nil, ErrNoHat
	case ModePaused:
		if !neutral || st.Neutral {
			return nil, nil
		}
	}
	ev := NewEvent(in.Device, EvOff, ts)
	ev.Hat, ev.Neutral = st.Hat, neutral
	return &ev, nil
}

func withKind(err error, kind string) error {
	var nf *NotFoundError
	var am *AmbiguousError
	switch {
	case errors.As(err, &nf):
		nf.Kind = kind
	case errors.As(err, &am):
		am.Kind = kind
	}
	return err
}
