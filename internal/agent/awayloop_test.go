package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
)

// awayConfig has thresholds of one minute, which HATRACK_AWAY_SCALE=60
// turns into one second.
const awayConfig = `version: 1
observe:
  idle: {after: 1, ask_after: 1, stop_after: 1}
hats:
  venom:
    name: VENOM
    activities: [development, review]
  tag:
    name: TAG
    activities: [support]
`

// fakeNotify is a notify module that records messages and answers with a
// prepared button.
type fakeNotify struct {
	mu     sync.Mutex
	choose string
	shown  []string
	plain  int
	acts   []modules.Action
}

func (f *fakeNotify) ID() string                                                   { return "notify" }
func (f *fakeNotify) RunsIn() modules.RunsIn                                       { return modules.RunsInAgent }
func (f *fakeNotify) ConfigSchema() []byte                                         { return nil }
func (f *fakeNotify) Backends() []modules.Backend                                  { return nil }
func (f *fakeNotify) Enter(context.Context, modules.Switch, json.RawMessage) error { return nil }
func (f *fakeNotify) Leave(context.Context, modules.Switch, json.RawMessage) error { return nil }
func (f *fakeNotify) Notify(_ context.Context, summary, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plain++
	return nil
}
func (f *fakeNotify) NotifyActions(_ context.Context, summary, body string, acts []modules.Action) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shown = append(f.shown, summary+"|"+body)
	f.acts = acts
	return f.choose, nil
}
func (f *fakeNotify) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.shown) }

// awayRig starts an agent with the idle sensor driven by the test.
func awayRig(t *testing.T) (*rig, *Agent, *chanBackend, *fakeNotify) {
	t.Helper()
	t.Setenv(AwayScaleEnv, "60")
	r := newRig(t)
	r.importYAML(awayConfig)
	idle, window := newChan("chan"), newChan("chan")
	a := r.obsAgent("laptop", sensorRegistry(idle, window))
	fn := &fakeNotify{}
	a.host.Reg.Register(fn)
	r.start(a)
	waitConnected(t, a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 2 })
	eventually(t, "idle sensor", func() bool { return a.running("idle") })
	return r, a, idle, fn
}

func (r *rig) serverEvents() []core.Event {
	r.t.Helper()
	evs, err := r.st.EventsBetween(context.Background(), time.Now().Add(-time.Hour), time.Time{})
	if err != nil {
		r.t.Fatal(err)
	}
	return evs
}

func awayOf(t *testing.T, a *Agent) *AwayQuestion {
	t.Helper()
	return statusOf(t, call(t, a, Request{Cmd: "status"})).Away
}

// goAway wears venom, locks the screen, waits for the clock to stop and
// returns after the user came back. It returns the time of the lock.
func goAway(t *testing.T, r *rig, a *Agent, idle *chanBackend) (lockedAt time.Time) {
	t.Helper()
	if resp := call(t, a, Request{Cmd: "on", Hat: "venom", HasActivity: true, Activity: "development", Detail: "VEN-1"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	time.Sleep(50 * time.Millisecond)
	lockedAt = time.Now()
	idle.send(t, lockedAt, core.Attrs{"kind": "locked"})
	eventually(t, "clock stopped", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.state.Mode == core.ModePaused
	})
	return lockedAt
}

func TestAwayStopsAndAsksThroughTheAgent(t *testing.T) {
	r, a, idle, _ := awayRig(t)
	lockedAt := goAway(t, r, a, idle)
	if awayOf(t, a) != nil {
		t.Fatal("question while away")
	}
	idle.send(t, time.Now(), nil)
	eventually(t, "resumed", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.state.Mode == core.ModeWearing
	})
	q := awayOf(t, a)
	if q == nil || q.Booked != AwayPause || q.Hat != "venom" || q.Detail != "VEN-1" || !near(q.From, lockedAt) {
		t.Fatalf("question: %+v", q)
	}
	eventually(t, "events at the server", func() bool { return len(r.serverEvents()) == 3 })
	evs := r.serverEvents()
	if evs[0].Type != core.EvSwitch || evs[1].Type != core.EvOff || evs[2].Type != core.EvSwitch ||
		!near(evs[1].TS, lockedAt) || evs[2].Hat != "venom" || evs[2].Activity != "development" || evs[2].Detail != "VEN-1" {
		t.Fatalf("events: %+v", evs)
	}

	// the answer "work": a switch right after the lock; no new question
	resp := call(t, a, Request{Cmd: "away", Away: q.ID, Answer: AwayWork})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if d := statusOf(t, resp); d.Away != nil || d.State.Mode != core.ModeWearing {
		t.Fatalf("status after the answer: %+v", d)
	}
	eventually(t, "answer at the server", func() bool { return len(r.serverEvents()) == 4 })
	evs = r.serverEvents()
	last := evs[1] // sorted by time: switch, off, then the answer 1 ms later
	if evs[2].Type != core.EvSwitch || evs[2].TS.Sub(evs[1].TS) != time.Millisecond || last.Type != core.EvOff {
		t.Fatalf("events: %+v", evs)
	}
	// nothing left to answer
	if resp := call(t, a, Request{Cmd: "away", Answer: AwayKeep}); resp.OK || resp.Error == "" {
		t.Fatalf("second answer: %+v", resp)
	}
}

func TestAwayAnswerOtherThroughSocket(t *testing.T) {
	r, a, idle, _ := awayRig(t)
	goAway(t, r, a, idle)
	idle.send(t, time.Now(), nil)
	eventually(t, "question", func() bool { return awayOf(t, a) != nil })
	if resp := call(t, a, Request{Cmd: "away", Answer: AwayOther, Hat: "nope"}); resp.OK {
		t.Fatal("unknown hat accepted")
	}
	if awayOf(t, a) == nil {
		t.Fatal("question gone after an error")
	}
	resp := call(t, a, Request{Cmd: "away", Answer: AwayOther, Hat: "ta", Activity: "supp", Detail: "call"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	eventually(t, "events", func() bool { return len(r.serverEvents()) == 4 })
	var got core.Event
	for _, e := range r.serverEvents() {
		if e.Hat == "tag" {
			got = e
		}
	}
	if got.Type != core.EvSwitch || got.Activity != "support" || got.Detail != "call" {
		t.Fatalf("answer event: %+v", got)
	}
	// the live state still shows venom
	if d := statusOf(t, call(t, a, Request{Cmd: "status"})); d.State.Hat != "venom" {
		t.Fatalf("live state: %+v", d.State)
	}
}

// Without a tray the question comes as a notification; its button answers.
func TestAwayNotificationFallback(t *testing.T) {
	r, a, idle, fn := awayRig(t)
	fn.choose = "alt"
	goAway(t, r, a, idle)
	idle.send(t, time.Now(), nil)
	eventually(t, "notification", func() bool { return fn.count() == 1 })
	fn.mu.Lock()
	if len(fn.acts) != 2 || fn.acts[0].Key != "alt" || fn.acts[1].Key != "view" {
		t.Fatalf("actions: %+v", fn.acts)
	}
	fn.mu.Unlock()
	// "alt" of a pause question is "work": the question is answered
	eventually(t, "answered by the button", func() bool { return awayOf(t, a) == nil && len(r.serverEvents()) == 4 })
}

// With a watcher (the tray) there is no notification.
func TestAwayNoNotificationWhenWatched(t *testing.T) {
	r, a, idle, fn := awayRig(t)
	ch := a.subscribe()
	defer a.unsubscribe(ch)
	goAway(t, r, a, idle)
	idle.send(t, time.Now(), nil)
	eventually(t, "question", func() bool { return awayOf(t, a) != nil })
	time.Sleep(300 * time.Millisecond)
	if fn.count() != 0 || fn.plain != 0 {
		t.Fatal("notified although the tray watches")
	}
	select {
	case <-ch:
	default:
		t.Fatal("watcher was not woken")
	}
}

// The state survives a restart of the agent.
func TestAwayStateIsPersisted(t *testing.T) {
	r, a, idle, _ := awayRig(t)
	goAway(t, r, a, idle)
	idle.send(t, time.Now(), nil)
	eventually(t, "question", func() bool { return awayOf(t, a) != nil })
	q := awayOf(t, a)

	b, _ := r.agent("laptop")
	b.db = a.db
	if err := b.Load(); err != nil {
		t.Fatal(err)
	}
	if got := b.away.Question(); got == nil || got.ID != q.ID {
		t.Fatalf("loaded question: %+v", got)
	}
}

// Without a working idle sensor nothing is booked (rule 7).
func TestAwayNeedsIdleSensor(t *testing.T) {
	t.Setenv(AwayScaleEnv, "60")
	r := newRig(t)
	r.importYAML(awayConfig + "")
	a, _ := r.agent("laptop")
	a.SetSensors(sensorRegistry(&chanBackend{name: "x", ok: false, reason: "none", in: make(chan chanMsg)}, newChan("chan")))
	r.start(a)
	waitConnected(t, a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 2 })
	if resp := call(t, a, Request{Cmd: "on", Hat: "venom"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	time.Sleep(1500 * time.Millisecond) // ticks of 250 ms, thresholds of 1 s
	a.mu.Lock()
	mode := a.state.Mode
	a.mu.Unlock()
	if mode != core.ModeWearing {
		t.Fatalf("mode %s: the clock was stopped without an idle sensor", mode)
	}
}

// Test 13 at agent level (run with -race): idle states and ticks run
// while the user switches hats.
func TestAwayConcurrentWithSwitches(t *testing.T) {
	r, a, idle, _ := awayRig(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			attrs := core.Attrs{"kind": "inactive"}
			if i%2 == 1 {
				attrs = nil
			}
			idle.send(t, time.Now().Add(-2*time.Second), attrs)
			time.Sleep(30 * time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			hat := []string{"venom", "tag"}[i%2]
			call(t, a, Request{Cmd: "on", Hat: hat})
			call(t, a, Request{Cmd: "away", Answer: AwayKeep})
			time.Sleep(30 * time.Millisecond)
		}
	}()
	wg.Wait()
	_ = r
}
