package agent

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/modules"
	"hatrack/internal/sensors"
)

// idleChCap is how many idle states wait for awayLoop; the oldest is dropped
// when it is full.
const idleChCap = 64

// notifyTimeout is how long the notification fallback waits for a click.
const notifyTimeout = 10 * time.Minute

// idleMsg is one state of the idle sensor, as the runner reported it.
type idleMsg struct {
	at    time.Time
	attrs core.Attrs
}

// pushIdle hands a state to awayLoop without ever blocking: when the channel
// is full the oldest state goes. Called by the idle runner, which must not
// take Agent.mu.
func (a *Agent) pushIdle(m idleMsg) {
	for {
		select {
		case a.idleCh <- m:
			return
		default:
		}
		select {
		case <-a.idleCh:
		default:
		}
	}
}

// awayEnv describes the world to the tracker. Caller holds a.mu.
func (a *Agent) awayEnv() awayEnv {
	return awayEnv{Now: a.Now(), St: a.state, Config: a.config, Dev: a.cfg.Device}
}

// idleWorks reports whether the idle sensor is switched on for this device
// and has a working backend; without it nothing is booked. Caller holds a.mu.
func (a *Agent) idleWorks() bool {
	if !a.config.Observe.Enabled(a.deviceName(), sensors.IdleSensor) {
		return false
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	r := a.runners[sensors.IdleSensor]
	return r != nil && r.known && r.backend != ""
}

// awayLoop is the only caller of the tracker's transitions. It takes
// Agent.mu to read the state and to submit events, never for the network.
func (a *Agent) awayLoop(ctx context.Context) {
	tick := time.NewTicker(a.away.tickEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-a.idleCh:
			a.awayInput(ctx, m)
		case <-tick.C:
			a.awayTick(ctx)
		}
	}
}

// awayInput feeds one idle state to the tracker.
func (a *Agent) awayInput(ctx context.Context, m idleMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.idleWorks() {
		return
	}
	a.applyAway(ctx, a.away.Feed(a.awayEnv(), m.at, m.attrs))
}

// awayTick runs the tracker's tick; when the clock should stop it asks the
// server whether another computer is in use (without Agent.mu) and then stops.
func (a *Agent) awayTick(ctx context.Context) {
	a.mu.Lock()
	if !a.idleWorks() {
		a.away.Reset(a.Now())
		a.mu.Unlock()
		return
	}
	res := a.away.Tick(a.awayEnv())
	a.applyAway(ctx, res)
	a.mu.Unlock()
	if res.WantStop {
		others, err := a.client.Presence(ctx)
		if ctx.Err() != nil {
			return
		}
		// somebody else at work: no stop now, try again on the next tick
		if stopAt, ok := awayStopTime(res.Since, others, err); ok {
			a.mu.Lock()
			a.applyAway(ctx, a.away.Stop(a.awayEnv(), stopAt))
			a.mu.Unlock()
		}
	}
	// back from a suspend: the return follows the stop decision
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyAway(ctx, a.away.FinishReturn(a.awayEnv()))
}

// applyAway submits what the tracker decided. Caller holds a.mu.
func (a *Agent) applyAway(ctx context.Context, r awayResult) {
	if len(r.Events) > 0 {
		if err := a.submitAll(ctx, r.Events...); err != nil {
			log.Printf("absence: %v", err)
		}
	} else if r.Changed {
		a.changed()
	}
	if r.Created != nil {
		a.notifyAway(ctx, *r.Created)
	}
}

// answerAway is the socket command "away".
func (a *Agent) answerAway(ctx context.Context, req Request) Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	res, err := a.away.Answer(a.awayEnv(), req.Away, AwayAnswer{Kind: req.Answer, Hat: req.Hat, Activity: req.Activity, Detail: req.Detail})
	if err != nil {
		if res.Changed {
			a.changed()
		}
		return fail(errText(err))
	}
	if len(res.Events) > 0 {
		if err := a.submitAll(ctx, res.Events...); err != nil {
			return fail(err.Error())
		}
	} else {
		a.changed()
	}
	return a.status(true)
}

// notifyAway shows the question as a notification when nobody watches the
// agent (no tray): the other answer and a look at the day. Caller holds a.mu.
func (a *Agent) notifyAway(ctx context.Context, q AwayQuestion) {
	a.subMu.Lock()
	watched := len(a.subs) > 0
	a.subMu.Unlock()
	if watched {
		return
	}
	title := i18n.T("away.title", clock(q.From), clock(q.To))
	what := q.HatName
	for _, p := range []string{q.Activity, core.ShortDetail(q.Detail)} {
		if p != "" {
			what += " · " + p
		}
	}
	body, alt, altAnswer := i18n.T("away.booked_pause", what), i18n.T("away.act.work"), AwayWork
	if q.Booked == AwayWork {
		body, alt, altAnswer = i18n.T("away.booked_work", what), i18n.T("away.act.pause"), AwayPause
	}
	server := a.cfg.Server
	go func() {
		actions := []modules.Action{{Key: "alt", Label: alt}, {Key: "view", Label: i18n.T("away.act.view")}}
		nctx, cancel := context.WithTimeout(ctx, notifyTimeout)
		defer cancel()
		chosen, err := a.host.NotifyActions(nctx, title, body, actions)
		if err == modules.ErrNoBackend {
			return
		}
		if err != nil {
			log.Printf("absence notification: %v", err)
			a.host.Notify(ctx, title, body) // no actions, but the message
			return
		}
		switch chosen {
		case "alt":
			a.Handle(ctx, Request{Cmd: "away", Away: q.ID, Answer: altAnswer})
		case "view":
			if server != "" {
				exec.Command("xdg-open", strings.TrimRight(server, "/")+"/day").Start()
			}
		}
	}()
}
