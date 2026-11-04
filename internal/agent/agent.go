package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/modules"
	"hatrack/internal/server"
)

// Agent applies hats on this device and keeps the log in sync.
type Agent struct {
	cfg    Config
	db     *DB
	host   *modules.Host
	client *Client
	Now    func() time.Time

	mu        sync.Mutex // guards everything below and serialises switches
	state     core.State
	last      map[string]string
	recent    map[string]map[string][]string // hat -> activity -> recent details
	config    core.Config
	applied   string // hat key whose look is applied, "" for neutral
	connected bool
	authFail  bool
	neutral   map[string]json.RawMessage // module id -> neutral snapshot
	// minRev is the server's event revision after our last sent events; a
	// state with a lower revision was sent before they arrived and is stale.
	minRev int64

	kick chan struct{}

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

func (a *Agent) subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	a.subMu.Lock()
	if a.subs == nil {
		a.subs = map[chan struct{}]struct{}{}
	}
	a.subs[ch] = struct{}{}
	a.subMu.Unlock()
	return ch
}

func (a *Agent) unsubscribe(ch chan struct{}) {
	a.subMu.Lock()
	delete(a.subs, ch)
	a.subMu.Unlock()
}

// changed wakes everything that watches the agent.
func (a *Agent) changed() {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// New creates an agent. The registry holds the modules available on this
// device.
func New(cfg Config, db *DB, reg *modules.Registry) *Agent {
	return &Agent{
		cfg: cfg, db: db, host: modules.NewHost(reg), Now: time.Now,
		client: NewClient(cfg.Server, cfg.Token),
		state:  core.NewState(), last: map[string]string{}, config: core.NewConfig(),
		neutral: map[string]json.RawMessage{}, kick: make(chan struct{}, 1),
	}
}

// Load reads the cached state, configuration and applied look.
func (a *Agent) Load() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, dst := range map[string]any{
		"state": &a.state, "last": &a.last, "config": &a.config,
		"applied": &a.applied, "neutral": &a.neutral,
	} {
		if _, err := a.db.Get(key, dst); err != nil {
			return fmt.Errorf("cache %s: %w", key, err)
		}
	}
	if a.last == nil {
		a.last = map[string]string{}
	}
	if a.neutral == nil {
		a.neutral = map[string]json.RawMessage{}
	}
	return nil
}

func (a *Agent) save() {
	for key, v := range map[string]any{
		"state": a.state, "last": a.last, "config": a.config,
		"applied": a.applied, "neutral": a.neutral,
	} {
		if err := a.db.Put(key, v); err != nil {
			log.Printf("cache %s: %v", key, err)
		}
	}
	a.changed()
}

// Run starts the connection to the server and the flusher; it returns when
// ctx is cancelled. The socket is served by Serve.
func (a *Agent) Run(ctx context.Context) {
	a.mu.Lock()
	a.reconcile(ctx)
	a.save()
	a.mu.Unlock()
	go a.flushLoop(ctx)
	a.streamLoop(ctx)
}

func backoff(n int) time.Duration {
	d := time.Second << min(n, 6)
	return min(d, time.Minute)
}

func (a *Agent) setConnected(v bool) {
	a.mu.Lock()
	a.connected = v
	a.mu.Unlock()
	a.changed()
}

func (a *Agent) streamLoop(ctx context.Context) {
	fails := 0
	for ctx.Err() == nil {
		err := a.client.Stream(ctx, func() {
			fails = 0
			a.setConnected(true)
			a.onConnect(ctx)
		}, func(event, data string) { a.onMessage(ctx, event, data) })
		a.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrUnauthorized) {
			a.mu.Lock()
			if !a.authFail {
				log.Printf("%s", i18n.T("err.unauthorized"))
			}
			a.authFail = true
			a.mu.Unlock()
		} else if fails == 0 {
			log.Printf("server connection lost: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(fails)):
		}
		fails++
	}
}

func (a *Agent) onConnect(ctx context.Context) {
	a.mu.Lock()
	a.authFail = false
	a.mu.Unlock()
	caps := a.host.Capabilities(ctx)
	if err := a.client.Hello(ctx, caps); err != nil {
		log.Printf("hello: %v", err)
	}
	a.refreshConfig(ctx)
	a.kickFlush()
	// What is installed (themes, wallpapers with previews) takes longer to
	// collect; it follows in a second hello, for the pick lists in Setup.
	go func() {
		ch := a.host.Choices(ctx)
		if len(ch) == 0 || ctx.Err() != nil {
			return
		}
		for id, c := range ch {
			cp := caps[id]
			cp.Choices = c
			caps[id] = cp
		}
		if err := a.client.Hello(ctx, caps); err != nil && ctx.Err() == nil {
			log.Printf("hello with choices: %v", err)
		}
	}()
}

func (a *Agent) refreshConfig(ctx context.Context) {
	cfg, _, err := a.client.Config(ctx)
	if err != nil {
		log.Printf("config: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config = cfg
	a.save()
}

func (a *Agent) onMessage(ctx context.Context, event, data string) {
	switch event {
	case "state":
		var resp server.StateResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			log.Printf("bad state message: %v", err)
			return
		}
		a.adoptState(ctx, resp)
	case "config":
		a.refreshConfig(ctx)
	}
}

// adoptState takes the server's state as the truth, unless local events are
// still waiting: then the server has not seen them and its state is stale.
func (a *Agent) adoptState(ctx context.Context, resp server.StateResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db.QueueLen() > 0 || resp.EventRev < a.minRev {
		return
	}
	a.state = resp.State
	if resp.RecentDetails != nil {
		a.recent = resp.RecentDetails
	}
	if resp.LastActivities != nil {
		a.last = resp.LastActivities
	}
	a.reconcile(ctx)
	a.save()
}

func (a *Agent) kickFlush() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *Agent) flushLoop(ctx context.Context) {
	fails := 0
	wait := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
		case <-time.After(wait):
		}
		if err := a.flush(ctx); err != nil {
			wait = backoff(fails)
			fails++
			if ctx.Err() == nil && fails == 1 {
				log.Printf("sending events failed, they stay queued: %v", err)
			}
			continue
		}
		fails, wait = 0, 30*time.Second
	}
}

// flush sends the queue in order. When it is empty, the server's state is
// adopted, so a replay of old events ends in the right look.
func (a *Agent) flush(ctx context.Context) error {
	sent := false
	for {
		batch, err := a.db.Queue(100)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		rev, err := a.client.PostEvents(ctx, batch)
		if err != nil {
			return err
		}
		// raise minRev before the queue empties, so no stale state slips in
		a.mu.Lock()
		a.minRev = max(a.minRev, rev)
		a.mu.Unlock()
		if err := a.db.Dequeue(batch); err != nil {
			return err
		}
		sent = true
	}
	if sent {
		resp, err := a.client.State(ctx)
		if err != nil {
			return err
		}
		a.adoptState(ctx, resp)
	}
	return nil
}

// submit records an event: it is queued, folded into the local state and
// applied right away, with or without a server.
func (a *Agent) submit(ctx context.Context, ev core.Event) error {
	if err := a.db.Enqueue(ev); err != nil {
		return err
	}
	if (ev.Type == core.EvSwitch || ev.Type == core.EvActivity) && ev.Activity != "" && ev.Hat != "" {
		if !ev.TS.Before(a.state.LastTS) {
			a.last[ev.Hat] = ev.Activity
		}
	}
	a.state = a.state.Apply(ev)
	a.reconcile(ctx)
	a.save()
	a.kickFlush()
	return nil
}

func (a *Agent) switchFor(h core.Hat, activity string) modules.Switch {
	return modules.Switch{Hat: h.Key, HatName: h.DisplayName(), Color: h.Color, Activity: activity}
}

// moduleConfigs returns the expanded settings of every module for a hat.
func (a *Agent) moduleConfigs(h core.Hat) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, id := range a.host.Reg.IDs() {
		m, ok := a.config.ModuleConfig(h, id)
		if !ok {
			continue
		}
		b, err := json.Marshal(core.ExpandEnv(m))
		if err == nil {
			out[id] = b
		}
	}
	return out
}

// reconcile makes the desktop show what the state asks for. It compares the
// look that is applied with the look that is wanted; only a change of look
// runs hooks. Own events, replays and pushes from other devices all end up
// here, so each of them is handled the same way. Caller holds a.mu.
func (a *Agent) reconcile(ctx context.Context) {
	want := a.state.Look()
	if want == a.applied {
		return
	}
	var fails []modules.Failure
	oldHat, hadOld := a.config.Hat(a.applied)
	newHat, hasNew := a.config.Hat(want)
	if want != "" && !hasNew {
		return // unknown hat: nothing to apply until the configuration arrives
	}

	var oldCfg, newCfg map[string]json.RawMessage
	if a.applied != "" && hadOld {
		oldCfg = a.moduleConfigs(oldHat)
	}
	if want != "" {
		newCfg = a.moduleConfigs(newHat)
	}
	a.ensureNeutral(ctx)

	prev := modules.Switch{}
	if hadOld {
		prev = a.switchFor(oldHat, "")
	}
	if len(oldCfg) > 0 {
		fails = append(fails, a.host.Run(ctx, "leave", prev, oldCfg)...)
	}
	// Looks the old hat set but the new one does not are given back.
	back := map[string]json.RawMessage{}
	for id := range oldCfg {
		if _, set := newCfg[id]; !set {
			if snap, ok := a.neutral[id]; ok {
				back[id] = snap
			}
		}
	}
	if len(back) > 0 {
		fails = append(fails, a.host.Restore(ctx, back)...)
	}
	if want != "" {
		sw := a.switchFor(newHat, a.state.Activity)
		sw.Prev, sw.PrevName = prev.Hat, prev.HatName
		fails = append(fails, a.host.Run(ctx, "enter", sw, newCfg)...)
	}
	a.applied = want
	a.report(ctx, fails)
}

// ensureNeutral takes the neutral snapshot of every module that has none,
// while no hat look is applied.
func (a *Agent) ensureNeutral(ctx context.Context) {
	if a.applied != "" {
		return
	}
	snaps, fails := a.host.Snapshot(ctx)
	for id, s := range snaps {
		if _, ok := a.neutral[id]; !ok {
			a.neutral[id] = s
		}
	}
	a.report(ctx, fails)
}

// report logs module failures, tells the user and records module_error
// events. The other modules have already run.
func (a *Agent) report(ctx context.Context, fails []modules.Failure) {
	for _, f := range fails {
		log.Printf("module error: %v", f)
		ev := core.NewEvent(a.cfg.Device, core.EvModuleError, a.Now())
		ev.Note = f.Error()
		ev.Hat = a.state.Hat
		a.db.Enqueue(ev)
		if f.Module != "notify" {
			a.host.Notify(ctx, i18n.T("notify.module_error", f.Module), f.Err.Error())
		}
	}
	if len(fails) > 0 {
		a.kickFlush()
	}
}

// RestoreAll gives the neutral look back to every module, for example when
// the agent is removed. The shared state is not touched.
func (a *Agent) RestoreAll(ctx context.Context) []modules.Failure {
	a.mu.Lock()
	defer a.mu.Unlock()
	fails := a.host.Restore(ctx, a.neutral)
	a.applied = ""
	a.save()
	return fails
}
