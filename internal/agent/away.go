package agent

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/server"
)

// The absence tracker turns what the idle sensor reports into bookings.
//
// It is a pure state machine: no goroutines and no clock of its own; every
// call gets the time, the agent's state and the configuration. awayMu is its
// own lock, taken after Agent.mu (Agent.mu -> awayMu); the tracker never calls
// back into the agent. It returns the events to submit, the agent submits
// them. Only the agent's awayLoop drives the transitions.
//
// What an absence books (all with plain events, never corrections):
//
//	clock stopped, user back   off at the stop time, switch(Prev) at the return
//	short absence, user back   nothing booked, "work" question
//
// Events may be backdated; the log sorts them, the live state ignores events
// older than its LastTS.

// Kinds of absence.
const (
	awayInactive = "inactive"
	awayLocked   = "locked"
	awaySuspend  = "suspend"
)

// Answers to a question.
const (
	AwayKeep  = "keep"
	AwayPause = "pause"
	AwayWork  = "work"
	AwayOther = "other"
)

// Fixed timing of the tracker; HATRACK_AWAY_SCALE divides all of it (and the
// configured minutes), so tests can run in seconds.
const (
	awayTickEvery   = 15 * time.Second
	awaySuspendGap  = 2 * time.Minute
	awayQuestionTTL = 12 * time.Hour
	// AwayScaleEnv is a test hook: "60" makes minutes seconds.
	AwayScaleEnv = "HATRACK_AWAY_SCALE"
)

var (
	errNoQuestion = errors.New("nothing to answer")
	errAwayGone   = errors.New("the answer no longer applies")
)

type prevEntry struct {
	Hat      string `json:"hat,omitempty"`
	Activity string `json:"activity,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// awayState is what is persisted (kv key "away") after every change.
type awayState struct {
	Idle     core.Attrs    `json:"idle,omitempty"` // last idle sensor state, nil = active
	IdleAt   time.Time     `json:"idle_at,omitzero"`
	Away     bool          `json:"away,omitempty"`
	Since    time.Time     `json:"since,omitzero"`
	Kind     string        `json:"kind,omitempty"`
	Stopped  bool          `json:"stopped,omitempty"` // this absence stopped the clock
	StopID   string        `json:"stop_id,omitempty"` // id of our off event
	StopAt   time.Time     `json:"stop_at,omitzero"`
	Prev     prevEntry     `json:"prev"`
	Question *AwayQuestion `json:"question,omitempty"`
	// ResumeID is the id of our switch that ended the absence, if any.
	ResumeID string    `json:"resume_id,omitempty"`
	LastTick time.Time `json:"last_tick,omitzero"`
	// PendingReturn: back from a suspend, but the stop decision (presence)
	// comes first; FinishReturn completes the return.
	PendingReturn bool `json:"pending_return,omitempty"`
}

// AwayQuestion is shown after a return; it is part of the status.
type AwayQuestion struct {
	ID       string    `json:"id"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Booked   string    `json:"booked"` // "pause" or "work": what the default booked
	Hat      string    `json:"hat"`
	HatName  string    `json:"hat_name"`
	Color    string    `json:"color,omitempty"`
	Activity string    `json:"activity,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// AwayAnswer is an answer to the question: keep, pause, work, or other with
// a choice.
type AwayAnswer struct {
	Kind     string
	Hat      string
	Activity string
	Detail   string
}

// awayEnv is what a call knows about the world.
type awayEnv struct {
	Now    time.Time
	St     core.State
	Config core.Config
	Dev    string
}

func (e awayEnv) idle() core.Idle { return e.Config.Observe.Idle }

// awayResult is what a call did.
type awayResult struct {
	Events   []core.Event
	Changed  bool          // the persisted state or the question changed
	Created  *AwayQuestion // a new question, for the notification fallback
	WantStop bool          // the clock should stop: check presence, then call Stop
	Since    time.Time     // start of the absence, with WantStop
}

type awayTracker struct {
	scale   float64
	persist func(awayState) // called with awayMu held; nil in tests

	mu sync.Mutex // awayMu
	s  awayState
}

func newAwayTracker(scale float64) *awayTracker {
	if scale <= 0 {
		scale = 1
	}
	return &awayTracker{scale: scale}
}

// awayScale reads the test hook.
func awayScale() float64 {
	f, err := strconv.ParseFloat(os.Getenv(AwayScaleEnv), 64)
	if err != nil || f <= 0 {
		return 1
	}
	return f
}

func (t *awayTracker) d(x time.Duration) time.Duration { return time.Duration(float64(x) / t.scale) }

// tickEvery is the period of the agent's ticks.
func (t *awayTracker) tickEvery() time.Duration { return max(t.d(awayTickEvery), 20*time.Millisecond) }

// restore loads the persisted state.
func (t *awayTracker) restore(s awayState) {
	t.mu.Lock()
	t.s = s
	t.mu.Unlock()
}

// Question returns a copy of the open question, or nil.
func (t *awayTracker) Question() *AwayQuestion {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.s.Question == nil {
		return nil
	}
	q := *t.s.Question
	return &q
}

func (t *awayTracker) save() {
	if t.persist != nil {
		t.persist(t.s)
	}
}

// worn reports whether st wears a hat and nothing happened since the time.
func worn(st core.State, since time.Time) bool {
	return st.Mode == core.ModeWearing && st.Hat != "" && !st.LastTS.After(since)
}

func entryOf(st core.State) prevEntry {
	return prevEntry{Hat: st.Hat, Activity: st.Activity, Detail: st.Detail}
}

func (p prevEntry) event(dev string, ts time.Time) core.Event {
	ev := core.NewEvent(dev, core.EvSwitch, ts)
	ev.Hat, ev.Activity, ev.Detail = p.Hat, p.Activity, p.Detail
	return ev
}

// Feed takes a state of the idle sensor: attrs is nil while the user is
// active, else kind inactive or locked; at is the moment it began.
func (t *awayTracker) Feed(env awayEnv, at time.Time, attrs core.Attrs) awayResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.s
	var r awayResult
	if len(attrs) == 0 {
		wasIdle := s.Idle != nil
		s.Idle, s.IdleAt = nil, time.Time{}
		if s.Away {
			t.returnAt(env, at, &r)
			r.Changed = true
		} else {
			r.Changed = wasIdle
		}
	} else {
		if at.After(env.Now) {
			at = env.Now
		}
		if s.Idle != nil && s.Idle["kind"] == attrs["kind"] && s.IdleAt.Equal(at) {
			return r
		}
		s.Idle, s.IdleAt = attrs, at
		t.checkAway(env, &r)
		r.Changed = true
	}
	if r.Changed {
		t.save()
	}
	return r
}

// checkAway starts an absence when the sensor state says so (rule 1).
func (t *awayTracker) checkAway(env awayEnv, r *awayResult) {
	s := &t.s
	if s.Away || s.Idle == nil {
		return
	}
	switch kind := s.Idle["kind"]; {
	case kind == awayLocked:
		t.begin(env, s.IdleAt, awayLocked)
		r.Changed = true
	case env.Now.Sub(s.IdleAt) >= t.d(env.idle().AfterD()):
		t.begin(env, s.IdleAt, awayInactive)
		r.Changed = true
	}
}

// begin starts an absence; it clears an old question.
func (t *awayTracker) begin(env awayEnv, since time.Time, kind string) {
	s := &t.s
	s.Away, s.Since, s.Kind = true, since, kind
	s.Stopped, s.StopID, s.StopAt, s.ResumeID = false, "", time.Time{}, ""
	s.Question = nil
	s.Prev = prevEntry{}
	if worn(env.St, since) {
		s.Prev = entryOf(env.St)
	}
}

// Tick is called every few seconds. It detects a suspend, starts absences
// whose inactivity has become long enough, expires the question and says
// whether the clock should stop now.
func (t *awayTracker) Tick(env awayEnv) awayResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.s
	var r awayResult
	cfg := env.idle()
	// the agent did not run: suspend
	if !s.LastTick.IsZero() && env.Now.Sub(s.LastTick) > t.d(awaySuspendGap) && !s.Away {
		t.begin(env, s.LastTick, awaySuspend)
		r.Changed = true
		if s.Idle == nil { // the sensor will not report a change: the user is back
			s.PendingReturn = true
		}
	}
	t.checkAway(env, &r)
	if s.Away && !s.Stopped && env.Now.Sub(s.Since) >= t.d(cfg.StopAfterD()) && worn(env.St, s.Since) {
		r.WantStop, r.Since = true, s.Since
	}
	if s.PendingReturn && !r.WantStop {
		t.returnAt(env, env.Now, &r) // no stop to decide first
	}
	if q := s.Question; q != nil && t.gone(env, q) {
		s.Question = nil
		r.Changed = true
	}
	s.LastTick = env.Now
	t.save() // the tick time is part of the state
	return r
}

// gone reports a question that cannot be answered any more (rule 5).
func (t *awayTracker) gone(env awayEnv, q *AwayQuestion) bool {
	if env.Now.Sub(q.To) > t.d(awayQuestionTTL) {
		return true
	}
	return env.St.LastTS.After(q.To) && env.St.EventID != t.s.ResumeID
}

// Stop stops the clock of the current absence at at (never before its
// start), after the agent has checked that no other computer is in use. It
// checks the conditions again, so a change since Tick is not overridden.
func (t *awayTracker) Stop(env awayEnv, at time.Time) awayResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.s
	var r awayResult
	if !s.Away || s.Stopped || env.Now.Sub(s.Since) < t.d(env.idle().StopAfterD()) || !worn(env.St, s.Since) {
		return r
	}
	if at.Before(s.Since) {
		at = s.Since
	}
	if at.After(env.Now) {
		at = env.Now
	}
	ev := core.NewEvent(env.Dev, core.EvOff, at)
	ev.Hat = env.St.Hat
	s.Prev = entryOf(env.St)
	s.Stopped, s.StopID, s.StopAt = true, ev.ID, at
	r.Events, r.Changed = []core.Event{ev}, true
	t.save()
	return r
}

// FinishReturn completes a return from a suspend once the stop decision is
// made: the clock was stopped (after the presence check) or it was not
// because another computer is in use. Without a pending return it does nothing.
func (t *awayTracker) FinishReturn(env awayEnv) awayResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	var r awayResult
	if !t.s.PendingReturn {
		return r
	}
	t.returnAt(env, env.Now, &r)
	r.Changed = true
	t.save()
	return r
}

// returnAt ends the absence at t (rule 4). Caller holds awayMu.
func (t *awayTracker) returnAt(env awayEnv, at time.Time, r *awayResult) {
	s := &t.s
	s.PendingReturn = false
	cfg := env.idle()
	if at.Before(s.Since) {
		at = s.Since
	}
	if at.After(env.Now) {
		at = env.Now
	}
	d := at.Sub(s.Since)
	asks := cfg.Asks()
	booked := ""
	switch {
	case s.Stopped && env.St.EventID == s.StopID:
		// still our pause: the clock runs again from the return
		if min := env.St.LastTS.Add(time.Millisecond); at.Before(min) {
			at = min
		}
		ev := s.Prev.event(env.Dev, at)
		r.Events = append(r.Events, ev)
		s.ResumeID = ev.ID
		booked = AwayPause
	case !s.Stopped && s.Prev.Hat != "" && env.St.Hat == s.Prev.Hat && worn(env.St, s.Since) && d >= t.d(cfg.AskAfterD()):
		booked = AwayWork
	}
	if booked != "" && asks {
		h, _ := env.Config.Hat(s.Prev.Hat)
		name := h.DisplayName()
		if h.Key == "" {
			name = s.Prev.Hat
		}
		q := &AwayQuestion{ID: core.NewID(), From: s.Since, To: at, Booked: booked, Hat: s.Prev.Hat,
			HatName: name, Color: h.Color, Activity: s.Prev.Activity, Detail: s.Prev.Detail}
		s.Question = q
		c := *q
		r.Created = &c
	}
	s.Away, s.Stopped, s.StopID = false, false, ""
}

// Answer applies an answer to the open question (rule 6) and returns the
// events for it. An unknown hat or activity is an error and the question
// stays; a question that cannot apply any more is dropped.
func (t *awayTracker) Answer(env awayEnv, id string, a AwayAnswer) (awayResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.s
	var r awayResult
	q := s.Question
	if q == nil || (id != "" && id != q.ID) {
		return r, errNoQuestion
	}
	if t.gone(env, q) {
		s.Question = nil
		t.save()
		r.Changed = true
		return r, errAwayGone
	}
	// the start of the booked time: after our own off, if it came later
	start := q.From
	if q.Booked == AwayPause && s.StopAt.After(start) {
		start = s.StopAt
	}
	at := start.Add(time.Millisecond)
	var evs []core.Event
	switch a.Kind {
	case AwayKeep, "":
	case AwayPause:
		if q.Booked == AwayWork {
			off := core.NewEvent(env.Dev, core.EvOff, q.From)
			off.Hat = q.Hat
			evs = append(evs, off, s.Prev.event(env.Dev, q.To))
		}
	case AwayWork:
		if q.Booked == AwayPause {
			evs = append(evs, s.Prev.event(env.Dev, at))
		}
	case AwayOther:
		choice, err := resolveChoice(env.Config, a)
		if err != nil {
			return r, err
		}
		evs = append(evs, choice.event(env.Dev, at))
		if q.Booked == AwayWork {
			evs = append(evs, s.Prev.event(env.Dev, q.To))
		}
	default:
		return r, errors.New("unknown answer " + a.Kind)
	}
	s.Question = nil
	r.Events, r.Changed = evs, true
	t.save()
	return r, nil
}

func resolveChoice(cfg core.Config, a AwayAnswer) (prevEntry, error) {
	if a.Hat == "" {
		return prevEntry{}, core.ErrNoHat
	}
	h, err := core.ResolveHat(cfg, a.Hat)
	if err != nil {
		return prevEntry{}, err
	}
	p := prevEntry{Hat: h.Key, Detail: a.Detail}
	if a.Activity != "" {
		if p.Activity, err = core.ResolveActivity(h, a.Activity); err != nil {
			return prevEntry{}, err
		}
	}
	return p, nil
}

// Reset forgets an absence that is under way, for when idle detection is off
// or has no working backend: nothing is reported, so nothing is booked.
func (t *awayTracker) Reset(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.s
	s.LastTick = now
	if s.Away || s.Idle != nil {
		s.Away, s.Idle, s.IdleAt = false, nil, time.Time{}
		t.save()
	}
}

// awayStopTime decides from the presence of the other devices whether the
// clock may stop for an absence since since, and from when. Every online
// device must be known and away; then it stops at the latest of since and
// their away_since. An unreachable server (err) allows it.
func awayStopTime(since time.Time, others []server.PresenceDevice, err error) (time.Time, bool) {
	if err != nil {
		return since, true
	}
	at := since
	for _, d := range others {
		if !d.Online {
			continue
		}
		if !d.Known || !d.Away {
			return time.Time{}, false
		}
		if d.AwaySince.After(at) {
			at = d.AwaySince
		}
	}
	return at, true
}
