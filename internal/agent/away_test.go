package agent

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/server"
)

const awayCfg = `version: 1
hats:
  venom:
    name: VENOM
    color: "#336699"
    activities: [development, review]
  office:
    name: Office
    activities: [mails, meeting]
`

func at(h, m int) time.Time { return time.Date(2026, 11, 4, h, m, 0, 0, time.UTC) }

var (
	locked   = core.Attrs{"kind": "locked"}
	inactive = core.Attrs{"kind": "inactive"}
)

// world is a fake agent around the tracker: it keeps the state, applies the
// events the tracker returns and knows what the presence check says.
type world struct {
	t      *testing.T
	tr     *awayTracker
	st     core.State
	cfg    core.Config
	now    time.Time
	events []core.Event // everything submitted
	others []server.PresenceDevice
	perr   error
	asked  int // presence checks
}

func newWorld(t *testing.T) *world {
	t.Helper()
	cfg, err := core.ParseConfig([]byte(awayCfg))
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, tr: newAwayTracker(1), st: core.NewState(), cfg: cfg, now: at(8, 0)}
	w.event(core.EvSwitch, at(8, 0), "venom", "development", "VEN-142")
	return w
}

func (w *world) env() awayEnv {
	return awayEnv{Now: w.now, St: w.st, Config: w.cfg, Dev: "laptop"}
}

func (w *world) event(typ core.EventType, ts time.Time, hat, act, detail string) core.Event {
	ev := core.NewEvent("other", typ, ts)
	ev.Hat, ev.Activity, ev.Detail = hat, act, detail
	w.st = w.st.Apply(ev)
	return ev
}

func (w *world) apply(r awayResult) awayResult {
	for _, ev := range r.Events {
		w.events = append(w.events, ev)
		w.st = w.st.Apply(ev)
	}
	return r
}

// feed delivers a state of the sensor at now (ticks have run until then).
func (w *world) feed(now, when time.Time, a core.Attrs) awayResult {
	w.ticksUntil(now)
	w.now = now
	return w.apply(w.tr.Feed(w.env(), when, a))
}

// tick runs one tick the way awayTick does: the clock stops after the
// presence check.
func (w *world) tick(now time.Time) {
	w.t.Helper()
	w.now = now
	r := w.apply(w.tr.Tick(w.env()))
	if r.WantStop {
		w.asked++
		if stopAt, ok := awayStopTime(r.Since, w.others, w.perr); ok {
			w.apply(w.tr.Stop(w.env(), stopAt))
		}
	}
	w.apply(w.tr.FinishReturn(w.env())) // as awayTick does
}

// ticksUntil ticks every 15 s from the current time up to and including to.
func (w *world) ticksUntil(to time.Time) {
	for t := w.now.Add(15 * time.Second); !t.After(to); t = t.Add(15 * time.Second) {
		w.tick(t)
	}
}

func (w *world) answer(a AwayAnswer) error {
	w.t.Helper()
	r, err := w.tr.Answer(w.env(), "", a)
	w.apply(r)
	return err
}

type wantEv struct {
	typ              core.EventType
	ts               time.Time
	hat, act, detail string
}

func (w *world) expect(want ...wantEv) {
	w.t.Helper()
	if len(w.events) != len(want) {
		w.t.Fatalf("events: %d, want %d: %s", len(w.events), len(want), dump(w.events))
	}
	for i, e := range w.events {
		x := want[i]
		if e.Type != x.typ || !e.TS.Equal(x.ts) || e.Hat != x.hat || e.Activity != x.act || e.Detail != x.detail || e.Device != "laptop" {
			w.t.Fatalf("event %d: %s, want %+v", i, dump(w.events[i:i+1]), x)
		}
	}
}

func dump(evs []core.Event) string {
	s := ""
	for _, e := range evs {
		s += fmt.Sprintf("[%s %s %s/%s/%s dev=%s] ", e.Type, e.TS.Format("15:04:05.000"), e.Hat, e.Activity, e.Detail, e.Device)
	}
	return s
}

func (w *world) question() *AwayQuestion { return w.tr.Question() }

func offEv(ts time.Time) wantEv { return wantEv{core.EvOff, ts, "venom", "", ""} }

var venom = func(typ core.EventType, ts time.Time) wantEv {
	return wantEv{typ, ts, "venom", "development", "VEN-142"}
}

// Test 1: locked 14:02, back 14:47: off at 14:02 at the first tick from
// 14:17, switch at 14:47, question booked as pause.
func TestAwayLockedStopsAndResumes(t *testing.T) {
	w := newWorld(t)
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.ticksUntil(at(14, 16))
	if len(w.events) != 0 {
		t.Fatalf("stopped too early: %s", dump(w.events))
	}
	w.ticksUntil(at(14, 17))
	w.expect(wantEv{core.EvOff, at(14, 2), "venom", "", ""})
	if w.st.Mode != core.ModePaused {
		t.Fatalf("state: %+v", w.st)
	}
	w.ticksUntil(at(14, 46))
	r := w.feed(at(14, 47), at(14, 47), nil)
	w.expect(offEv(at(14, 2)), venom(core.EvSwitch, at(14, 47)))
	q := w.question()
	if q == nil || q.Booked != AwayPause || !q.From.Equal(at(14, 2)) || !q.To.Equal(at(14, 47)) ||
		q.Hat != "venom" || q.HatName != "VENOM" || q.Color != "#336699" || q.Activity != "development" || q.Detail != "VEN-142" {
		t.Fatalf("question: %+v", q)
	}
	if r.Created == nil || r.Created.ID != q.ID {
		t.Fatal("no created question reported")
	}
	if w.st.Mode != core.ModeWearing || w.st.Hat != "venom" || !w.st.Since.Equal(at(14, 47)) {
		t.Fatalf("state after return: %+v", w.st)
	}
}

// Test 2: 12 minutes inactive: no stop, a "work" question.
func TestAwayShortAbsenceAsksWork(t *testing.T) {
	w := newWorld(t)
	w.tick(at(11, 19))
	w.feed(at(11, 21), at(11, 20), inactive) // reported a minute later, backdated
	w.ticksUntil(at(11, 31))
	w.feed(at(11, 32), at(11, 32), nil)
	if len(w.events) != 0 {
		t.Fatalf("events: %s", dump(w.events))
	}
	q := w.question()
	if q == nil || q.Booked != AwayWork || !q.From.Equal(at(11, 20)) || !q.To.Equal(at(11, 32)) {
		t.Fatalf("question: %+v", q)
	}
}

// Test 3: seven minutes of inactivity: nothing.
func TestAwayShortInactivityDoesNothing(t *testing.T) {
	w := newWorld(t)
	w.tick(at(11, 19))
	w.feed(at(11, 21), at(11, 20), inactive)
	w.ticksUntil(at(11, 26))
	w.feed(at(11, 27), at(11, 27), nil)
	if len(w.events) != 0 || w.question() != nil {
		t.Fatalf("events %s, question %+v", dump(w.events), w.question())
	}
	// nine minutes: away, but below the question threshold
	w.ticksUntil(at(12, 0))
	w.feed(at(12, 1), at(12, 0), inactive)
	w.ticksUntil(at(12, 8))
	w.feed(at(12, 9), at(12, 9), nil)
	if len(w.events) != 0 || w.question() != nil {
		t.Fatalf("nine minutes: events %s, question %+v", dump(w.events), w.question())
	}
}

// Test 4: another device switched at 14:10: no stop, no question.
func TestAwayForeignEventBlocks(t *testing.T) {
	w := newWorld(t)
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.ticksUntil(at(14, 10))
	w.event(core.EvActivity, at(14, 10), "venom", "review", "")
	w.ticksUntil(at(14, 40))
	w.feed(at(14, 41), at(14, 41), nil)
	if len(w.events) != 0 || w.question() != nil {
		t.Fatalf("events %s, question %+v", dump(w.events), w.question())
	}
}

// Test 5: another computer is in use: no stop; when it goes away the stop is
// booked at its away_since.
func TestAwayPresenceOtherInUse(t *testing.T) {
	w := newWorld(t)
	w.others = []server.PresenceDevice{{Device: "desktop", Online: true, Known: true, Away: false}}
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.ticksUntil(at(14, 30))
	if len(w.events) != 0 || w.asked == 0 {
		t.Fatalf("events %s, asked %d", dump(w.events), w.asked)
	}
	w.others = []server.PresenceDevice{{Device: "desktop", Online: true, Known: true, Away: true, AwaySince: at(14, 25)}}
	w.ticksUntil(at(14, 30).Add(15 * time.Second))
	w.expect(offEv(at(14, 25)))
}

func TestAwayStopTime(t *testing.T) {
	since := at(14, 2)
	dev := func(online, known, away bool, s time.Time) server.PresenceDevice {
		return server.PresenceDevice{Device: "x", Online: online, Known: known, Away: away, AwaySince: s}
	}
	tests := []struct {
		name   string
		others []server.PresenceDevice
		err    error
		want   time.Time
		ok     bool
	}{
		{"nobody else", nil, nil, since, true},
		{"offline", []server.PresenceDevice{dev(false, true, false, time.Time{})}, nil, since, true},
		{"online unknown", []server.PresenceDevice{dev(true, false, false, time.Time{})}, nil, time.Time{}, false},
		{"online known active", []server.PresenceDevice{dev(true, true, false, time.Time{})}, nil, time.Time{}, false},
		{"away before", []server.PresenceDevice{dev(true, true, true, at(13, 0))}, nil, since, true},
		{"away later", []server.PresenceDevice{dev(true, true, true, at(14, 9)), dev(true, true, true, at(14, 5))}, nil, at(14, 9), true},
		{"one active", []server.PresenceDevice{dev(true, true, true, at(14, 9)), dev(true, true, false, time.Time{})}, nil, time.Time{}, false},
		{"error", nil, fmt.Errorf("down"), since, true},
	}
	for _, tc := range tests {
		got, ok := awayStopTime(since, tc.others, tc.err)
		if ok != tc.ok || (ok && !got.Equal(tc.want)) {
			t.Errorf("%s: %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// Test 6: the server cannot be reached: the clock stops.
func TestAwayPresenceUnreachableStops(t *testing.T) {
	w := newWorld(t)
	w.perr = fmt.Errorf("unreachable")
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.ticksUntil(at(14, 17))
	w.expect(offEv(at(14, 2)))
}

// Test 7: every row of the answer table.
func TestAwayAnswers(t *testing.T) {
	off := func(ts time.Time) wantEv { return wantEv{core.EvOff, ts, "venom", "", ""} }
	sw := func(ts time.Time, hat, act, det string) wantEv { return wantEv{core.EvSwitch, ts, hat, act, det} }
	ms := time.Millisecond
	prevAt := func(ts time.Time) wantEv { return sw(ts, "venom", "development", "VEN-142") }
	// the events of the default that is already booked
	pauseDefault := []wantEv{off(at(14, 2)), prevAt(at(14, 47))}
	tests := []struct {
		name   string
		booked string
		ans    AwayAnswer
		extra  []wantEv // added to the default events
	}{
		{"pause/keep", AwayPause, AwayAnswer{Kind: AwayKeep}, nil},
		{"pause/pause", AwayPause, AwayAnswer{Kind: AwayPause}, nil},
		{"pause/work", AwayPause, AwayAnswer{Kind: AwayWork}, []wantEv{prevAt(at(14, 2).Add(ms))}},
		{"pause/other", AwayPause, AwayAnswer{Kind: AwayOther, Hat: "off", Activity: "meet", Detail: "Daily"},
			[]wantEv{sw(at(14, 2).Add(ms), "office", "meeting", "Daily")}},
		{"pause/other hat only", AwayPause, AwayAnswer{Kind: AwayOther, Hat: "office"},
			[]wantEv{sw(at(14, 2).Add(ms), "office", "", "")}},
		{"work/keep", AwayWork, AwayAnswer{Kind: AwayKeep}, nil},
		{"work/work", AwayWork, AwayAnswer{Kind: AwayWork}, nil},
		{"work/pause", AwayWork, AwayAnswer{Kind: AwayPause}, []wantEv{off(at(11, 20)), prevAt(at(11, 32))}},
		{"work/other", AwayWork, AwayAnswer{Kind: AwayOther, Hat: "office", Activity: "mails"},
			[]wantEv{sw(at(11, 20).Add(ms), "office", "mails", ""), prevAt(at(11, 32))}},
	}
	for _, tc := range tests {
		w := newWorld(t)
		var base []wantEv
		if tc.booked == AwayPause {
			w.tick(at(14, 0))
			w.feed(at(14, 2), at(14, 2), locked)
			w.ticksUntil(at(14, 17))
			w.feed(at(14, 47), at(14, 47), nil)
			base = pauseDefault
		} else {
			w.tick(at(11, 19))
			w.feed(at(11, 21), at(11, 20), inactive)
			w.ticksUntil(at(11, 31))
			w.feed(at(11, 32), at(11, 32), nil)
		}
		if q := w.question(); q == nil || q.Booked != tc.booked {
			t.Fatalf("%s: question %+v", tc.name, q)
		}
		n := len(w.events)
		if err := w.answer(tc.ans); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if w.question() != nil {
			t.Errorf("%s: question stays after the answer", tc.name)
		}
		got := w.events[n:]
		if len(got) != len(tc.extra) {
			t.Fatalf("%s: %d new events, want %d: %s", tc.name, len(got), len(tc.extra), dump(got))
		}
		for i, e := range got {
			x := tc.extra[i]
			if e.Type != x.typ || !e.TS.Equal(x.ts) || e.Hat != x.hat || e.Activity != x.act || e.Detail != x.detail {
				t.Errorf("%s: event %d: %s, want %+v", tc.name, i, dump(got[i:i+1]), x)
			}
		}
		if len(w.events[:n]) != len(base) {
			t.Errorf("%s: default events %s", tc.name, dump(w.events[:n]))
		}
		// the live state is untouched by backdated answers
		if w.st.Mode != core.ModeWearing || w.st.Hat != "venom" {
			t.Errorf("%s: state %+v", tc.name, w.st)
		}
	}
}

func TestAwayAnswerUnknownChoiceKeepsQuestion(t *testing.T) {
	w := newWorld(t)
	w.tick(at(11, 19))
	w.feed(at(11, 21), at(11, 20), inactive)
	w.ticksUntil(at(11, 31))
	w.feed(at(11, 32), at(11, 32), nil)
	for _, a := range []AwayAnswer{{Kind: AwayOther, Hat: "nope"}, {Kind: AwayOther, Hat: "venom", Activity: "zzz"}, {Kind: AwayOther}, {Kind: "bogus"}} {
		if err := w.answer(a); err == nil {
			t.Fatalf("%+v accepted", a)
		}
		if w.question() == nil {
			t.Fatalf("%+v dropped the question", a)
		}
	}
	if len(w.events) != 0 {
		t.Fatal(dump(w.events))
	}
	// an answer for another question id is refused
	if _, err := w.tr.Answer(w.env(), "other-id", AwayAnswer{Kind: AwayKeep}); err != errNoQuestion {
		t.Fatalf("wrong id: %v", err)
	}
	if err := w.answer(AwayAnswer{Kind: AwayKeep}); err != nil {
		t.Fatal(err)
	}
	if err := w.answer(AwayAnswer{Kind: AwayKeep}); err != errNoQuestion {
		t.Fatalf("second answer: %v", err)
	}
}

// Test 8: an answer after somebody else switched is refused, the question
// dropped.
func TestAwayAnswerAfterForeignEvent(t *testing.T) {
	for _, booked := range []string{AwayPause, AwayWork} {
		w := newWorld(t)
		if booked == AwayPause {
			w.tick(at(14, 0))
			w.feed(at(14, 2), at(14, 2), locked)
			w.ticksUntil(at(14, 17))
			w.feed(at(14, 47), at(14, 47), nil)
		} else {
			w.tick(at(11, 19))
			w.feed(at(11, 21), at(11, 20), inactive)
			w.ticksUntil(at(11, 31))
			w.feed(at(11, 32), at(11, 32), nil)
		}
		n := len(w.events)
		w.now = w.now.Add(2 * time.Minute)
		w.event(core.EvSwitch, w.now, "office", "mails", "")
		if err := w.answer(AwayAnswer{Kind: AwayWork}); err != errAwayGone {
			t.Fatalf("%s: err = %v", booked, err)
		}
		if w.question() != nil || len(w.events) != n {
			t.Fatalf("%s: question %+v, events %d (was %d)", booked, w.question(), len(w.events), n)
		}
	}
}

// A tick drops a question that can no longer apply, and one that is old.
func TestAwayQuestionLifetime(t *testing.T) {
	w := newWorld(t)
	w.tick(at(11, 19))
	w.feed(at(11, 21), at(11, 20), inactive)
	w.ticksUntil(at(11, 31))
	w.feed(at(11, 32), at(11, 32), nil)
	w.ticksUntil(at(11, 40))
	if w.question() == nil {
		t.Fatal("question dropped early")
	}
	w.ticksUntil(at(11, 32).Add(12*time.Hour - time.Minute))
	if w.question() == nil {
		t.Fatal("dropped before 12 h")
	}
	w.ticksUntil(at(11, 32).Add(12*time.Hour + time.Minute))
	if w.question() != nil {
		t.Fatal("kept after 12 h")
	}

	// foreign event
	w = newWorld(t)
	w.tick(at(11, 19))
	w.feed(at(11, 21), at(11, 20), inactive)
	w.ticksUntil(at(11, 31))
	w.feed(at(11, 32), at(11, 32), nil)
	w.event(core.EvSwitch, at(11, 40), "office", "mails", "")
	w.tick(at(11, 41))
	if w.question() != nil {
		t.Fatal("question kept after a foreign switch")
	}

	// a new absence clears it
	w = newWorld(t)
	w.tick(at(11, 19))
	w.feed(at(11, 21), at(11, 20), inactive)
	w.ticksUntil(at(11, 31))
	w.feed(at(11, 32), at(11, 32), nil)
	w.feed(at(11, 40), at(11, 40), locked)
	if w.question() != nil {
		t.Fatal("question kept after a new absence began")
	}
}

// Test 9: ticks at 10:00, then 10:40 with the user active: away since 10:00,
// off at 10:00, switch at 10:40, question pause.
func TestAwaySuspend(t *testing.T) {
	w := newWorld(t)
	w.tick(at(10, 0))
	w.tick(at(10, 40))
	w.expect(offEv(at(10, 0)), venom(core.EvSwitch, at(10, 40)))
	if q := w.question(); q == nil || q.Booked != AwayPause || !q.From.Equal(at(10, 0)) || !q.To.Equal(at(10, 40)) {
		t.Fatalf("question: %+v", q)
	}
}

func TestAwaySuspendShorterThanStop(t *testing.T) {
	w := newWorld(t)
	w.tick(at(10, 0))
	w.tick(at(10, 3)) // 3 minutes: a suspend, but below ask and stop
	if len(w.events) != 0 || w.question() != nil {
		t.Fatalf("events %s question %+v", dump(w.events), w.question())
	}
	w.tick(at(10, 20)) // 17 minutes
	w.expect(offEv(at(10, 3)), venom(core.EvSwitch, at(10, 20)))
	w2 := newWorld(t)
	w2.tr.s.LastTick = at(10, 0)
	w2.tr.s.Idle = inactive // the sensor still says inactive: wait for it
	w2.tr.s.IdleAt = at(9, 58)
	w2.tick(at(10, 40))
	w2.expect(offEv(at(10, 0)))
	if w2.question() != nil {
		t.Fatal("returned although the sensor says inactive")
	}
	if !w2.tr.s.Away || w2.tr.s.Kind != awaySuspend {
		t.Fatalf("state: %+v", w2.tr.s)
	}
	w2.feed(at(10, 41), at(10, 41), nil)
	w2.expect(offEv(at(10, 0)), venom(core.EvSwitch, at(10, 41)))
	if q := w2.question(); q == nil || q.Booked != AwayPause {
		t.Fatalf("question: %+v", q)
	}
}

// A suspend while another computer is in use: the laptop must not book that
// time as a pause; it asks whether it was work instead.
func TestAwaySuspendOtherComputerInUse(t *testing.T) {
	w := newWorld(t)
	w.others = []server.PresenceDevice{{Device: "office", Online: true, Known: true, Away: false}}
	w.tick(at(10, 0))
	w.tick(at(10, 40))
	if len(w.events) != 0 {
		t.Fatalf("booked although another computer is in use: %s", dump(w.events))
	}
	if q := w.question(); q == nil || q.Booked != AwayWork {
		t.Fatalf("question: %+v", q)
	}
}

// Test 10: a restart while away and stopped: the persisted state is
// loaded, the user is back: resume and question.
func TestAwayRestart(t *testing.T) {
	var saved awayState
	w := newWorld(t)
	w.tr.persist = func(s awayState) { saved = s }
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.ticksUntil(at(14, 17))
	if len(w.events) != 1 {
		t.Fatal(dump(w.events))
	}
	// the agent restarts: a new tracker with the saved state
	w.tr = newAwayTracker(1)
	w.tr.restore(saved)
	w.feed(at(14, 50), at(14, 50), nil) // the new sensor says: active
	w.expect(offEv(at(14, 2)), venom(core.EvSwitch, at(14, 50)))
	if q := w.question(); q == nil || q.Booked != AwayPause {
		t.Fatalf("question: %+v", q)
	}
}

// Test 11: paused, or no hat, at the start: nothing.
func TestAwayNothingToStop(t *testing.T) {
	for _, name := range []string{"paused", "no hat", "off before"} {
		w := newWorld(t)
		switch name {
		case "paused":
			w.event(core.EvOff, at(13, 0), "venom", "", "")
		case "no hat":
			w.st = core.NewState()
		case "off before":
			w.event(core.EvOff, at(13, 59), "venom", "", "")
		}
		w.tick(at(14, 0))
		w.feed(at(14, 2), at(14, 2), locked)
		w.ticksUntil(at(14, 40))
		w.feed(at(14, 41), at(14, 41), nil)
		if len(w.events) != 0 || w.question() != nil {
			t.Errorf("%s: events %s question %+v", name, dump(w.events), w.question())
		}
	}
}

// Test 12: ask=false: the defaults apply, no question.
func TestAwayAskOff(t *testing.T) {
	no := false
	w := newWorld(t)
	w.cfg.Observe.Idle.Ask = &no
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.ticksUntil(at(14, 17))
	w.feed(at(14, 47), at(14, 47), nil)
	w.expect(offEv(at(14, 2)), venom(core.EvSwitch, at(14, 47)))
	if w.question() != nil {
		t.Fatal("question although ask is off")
	}
	// short absence: nothing at all
	w.feed(at(15, 0), at(15, 0), nil)
	w.feed(at(15, 1), at(15, 0), inactive)
	w.ticksUntil(at(15, 12))
	w.feed(at(15, 13), at(15, 13), nil)
	if len(w.events) != 2 || w.question() != nil {
		t.Fatalf("events %s", dump(w.events))
	}
}

// The configured thresholds are used, not the defaults.
func TestAwayCustomThresholds(t *testing.T) {
	w := newWorld(t)
	w.cfg.Observe.Idle = core.Idle{After: 1, AskAfter: 2, StopAfter: 3}
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 0), inactive) // inactive for 2 minutes already
	w.ticksUntil(at(14, 2))
	w.ticksUntil(at(14, 3))
	w.expect(offEv(at(14, 0)))
}

// The scale makes minutes seconds.
func TestAwayScale(t *testing.T) {
	tr := newAwayTracker(60)
	if tr.tickEvery() != 250*time.Millisecond {
		t.Fatalf("tick %v", tr.tickEvery())
	}
	w := newWorld(t)
	w.tr = tr
	w.tick(at(14, 0))
	w.feed(at(14, 0).Add(10*time.Second), at(14, 0), locked)
	w.tick(at(14, 0).Add(14 * time.Second))
	if len(w.events) != 0 {
		t.Fatal("stopped before 15 s")
	}
	w.tick(at(14, 0).Add(16 * time.Second))
	if len(w.events) != 1 {
		t.Fatalf("not stopped at 16 s: %s", dump(w.events))
	}
}

// Idle states repeated by the sensor change nothing.
func TestAwayFeedIdempotent(t *testing.T) {
	w := newWorld(t)
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	since := w.tr.s.Since
	r := w.feed(at(14, 5), at(14, 2), locked)
	if r.Changed || !w.tr.s.Since.Equal(since) {
		t.Fatal("repeated state changed the absence")
	}
	// locked while inactive: the absence keeps its start
	w2 := newWorld(t)
	w2.tick(at(14, 0))
	w2.feed(at(14, 7), at(14, 0), inactive)
	w2.tick(at(14, 7).Add(time.Second))
	w2.feed(at(14, 8), at(14, 8), locked)
	if !w2.tr.s.Since.Equal(at(14, 0)) {
		t.Fatalf("since moved to %v", w2.tr.s.Since)
	}
}

func TestAwayReset(t *testing.T) {
	w := newWorld(t)
	w.tick(at(14, 0))
	w.feed(at(14, 2), at(14, 2), locked)
	w.tr.Reset(at(14, 10))
	if w.tr.s.Away || w.tr.s.Idle != nil {
		t.Fatal("not reset")
	}
	w.tick(at(14, 11)) // no suspend after the reset
	if w.tr.s.Away {
		t.Fatal("away after reset")
	}
}

// Test 13: idle states, ticks and switches run concurrently (-race).
func TestAwayConcurrent(t *testing.T) {
	cfg, _ := core.ParseConfig([]byte(awayCfg))
	tr := newAwayTracker(1)
	tr.persist = func(awayState) {}
	var mu sync.Mutex // the agent's mutex: the tracker is used under it
	st := core.NewState()
	now := at(9, 0)
	var wg sync.WaitGroup
	run := func(f func(i int, env awayEnv)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				mu.Lock()
				now = now.Add(20 * time.Second)
				f(i, awayEnv{Now: now, St: st, Config: cfg, Dev: "d"})
				mu.Unlock()
			}
		}()
	}
	run(func(i int, env awayEnv) {
		var a core.Attrs
		if i%3 != 0 {
			a = core.Attrs{"kind": []string{"inactive", "locked"}[i%2]}
		}
		r := tr.Feed(env, env.Now.Add(-time.Minute), a)
		for _, ev := range r.Events {
			st = st.Apply(ev)
		}
	})
	run(func(i int, env awayEnv) {
		r := tr.Tick(env)
		if r.WantStop {
			r = tr.Stop(env, r.Since)
		}
		for _, ev := range r.Events {
			st = st.Apply(ev)
		}
	})
	run(func(i int, env awayEnv) {
		ev := core.NewEvent("d", core.EvSwitch, env.Now)
		ev.Hat = "venom"
		st = st.Apply(ev)
		r, _ := tr.Answer(env, "", AwayAnswer{Kind: AwayWork})
		for _, ev := range r.Events {
			st = st.Apply(ev)
		}
		tr.Question()
	})
	wg.Wait()
}
