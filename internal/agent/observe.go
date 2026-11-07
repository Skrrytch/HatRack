package agent

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
	"hatrack/internal/sensors"
)

// Timing of the observation machinery; tests shorten some of them.
var (
	sensorRetry   = 30 * time.Second // wait before a failed sensor tries again
	obsFlushEvery = 60 * time.Second
	obsHold404    = 10 * time.Minute // back-off against a server without observations
	helloGap      = 10 * time.Second // least time between two hellos for sensor changes
)

const (
	obsKickAt     = 100 // queued observations that wake the flusher early
	detectTimeout = 5 * time.Second
)

// Locks, in the order they may be taken:
//
//	Agent.mu -> runMu -> runner.emitMu -> Recorder.mu
//	Agent.mu -> awayMu
//
// Sensor runners, the observation flusher and the recorder's sink never take
// Agent.mu. The device name has its own lock (devMu), and so has the hello
// state (helloMu); neither is held while taking another lock. The idle runner
// hands its states to awayLoop through a channel (idleCh), never blocking.

// runner is one running sensor.
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}

	// guarded by Agent.runMu
	backend string // name of the backend in use, "" if none works
	reason  string // why no backend works
	known   bool   // detection has run

	emitMu  sync.Mutex // serialises emits with the final "nothing" at stop
	stopped bool
}

// SetSensors sets the sensors of this device. Without it the agent records
// nothing. Call it before Run.
func (a *Agent) SetSensors(r *sensors.Registry) { a.sensors = r }

// deviceName is the name the server knows this device by (from the hello),
// else the cached one, else the locally configured name.
func (a *Agent) deviceName() string {
	a.devMu.Lock()
	defer a.devMu.Unlock()
	if a.serverDevice != "" {
		return a.serverDevice
	}
	return a.cfg.Device
}

// setServerDevice remembers the name from the hello and reports a change.
func (a *Agent) setServerDevice(name string) bool {
	if name == "" {
		return false
	}
	a.devMu.Lock()
	changed := a.serverDevice != name
	a.serverDevice = name
	a.devMu.Unlock()
	if changed {
		if err := a.db.Put("server_device", name); err != nil {
			log.Printf("cache server_device: %v", err)
		}
	}
	return changed
}

// observeNow reports whether the worn hat allows observing. Caller holds a.mu.
func (a *Agent) observeNow() bool {
	if a.state.Mode == core.ModeNone {
		return true
	}
	h, ok := a.config.Hat(a.state.Hat)
	return !ok || h.Observes()
}

// applyObserve hands the observe settings to the recorder and starts or stops
// sensors to match. Caller holds a.mu.
func (a *Agent) applyObserve(ctx context.Context) {
	a.rec.SetPolicy(a.config.Observe, a.deviceName(), a.Now())
	a.syncSensors(ctx)
}

// enqueueObs is the recorder's sink. It runs with the recorder's lock held
// and must not take Agent.mu.
func (a *Agent) enqueueObs(o core.Observation) {
	if err := a.db.EnqueueObs(o); err != nil {
		log.Printf("queue observation: %v", err)
		return
	}
	if a.db.ObsQueueLen() >= obsKickAt {
		a.kickObs()
	}
}

func (a *Agent) kickObs() {
	select {
	case a.obsKick <- struct{}{}:
	default:
	}
}

// syncSensors starts the sensors that are switched on and stops the ones
// that are not. Caller holds a.mu (it reads the configuration).
func (a *Agent) syncSensors(ctx context.Context) {
	if a.sensors == nil || ctx.Err() != nil {
		return
	}
	dev, obs := a.deviceName(), a.config.Observe
	changed := false
	a.runMu.Lock()
	if a.runners == nil {
		a.runners = map[string]*runner{}
	}
	for _, s := range a.sensors.All() {
		on := obs.Enabled(dev, s.ID)
		r := a.runners[s.ID]
		switch {
		case on && r == nil:
			rctx, cancel := context.WithCancel(ctx)
			r = &runner{cancel: cancel, done: make(chan struct{})}
			a.runners[s.ID] = r
			go a.runSensor(rctx, s, r)
			changed = true
		case !on && r != nil:
			delete(a.runners, s.ID)
			a.stopRunner(s.ID, r)
			changed = true
		}
	}
	a.runMu.Unlock()
	if changed {
		a.requestHello(ctx)
	}
}

// stopRunner ends a runner and closes its open span.
func (a *Agent) stopRunner(id string, r *runner) {
	r.cancel()
	r.emitMu.Lock()
	r.stopped = true
	a.rec.Emit(id, a.Now(), nil)
	r.emitMu.Unlock()
}

// stopSensors stops every runner and waits a moment for them to finish.
func (a *Agent) stopSensors() {
	a.runMu.Lock()
	rs := a.runners
	a.runners = nil
	for id, r := range rs {
		a.stopRunner(id, r)
	}
	a.runMu.Unlock()
	deadline := time.After(2 * time.Second)
	for _, r := range rs {
		select {
		case <-r.done:
		case <-deadline:
			return
		}
	}
}

// detect picks the first backend that works here. Without one it returns the
// reasons, joined.
func detect(ctx context.Context, s sensors.Sensor) (sensors.Backend, string) {
	var reasons []string
	for _, b := range s.Backends {
		dctx, cancel := context.WithTimeout(ctx, detectTimeout)
		ok, why := b.Detect(dctx)
		cancel()
		if ok {
			return b, ""
		}
		if why == "" {
			why = "not available"
		}
		reasons = append(reasons, b.Name()+": "+why)
	}
	if len(reasons) == 0 {
		return nil, "no backend"
	}
	return nil, strings.Join(reasons, "; ")
}

// setStatus records which backend a runner uses and tells the server when
// that is news.
func (a *Agent) setStatus(ctx context.Context, r *runner, backend, reason string) {
	a.runMu.Lock()
	changed := !r.known || r.backend != backend || r.reason != reason
	r.backend, r.reason, r.known = backend, reason, true
	a.runMu.Unlock()
	if changed {
		a.requestHello(ctx)
	}
}

// wait sleeps for d; false if ctx ended first.
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// runSensor runs one sensor until ctx ends.
func (a *Agent) runSensor(ctx context.Context, s sensors.Sensor, r *runner) {
	defer close(r.done)
	emitRec := func(at time.Time, attrs core.Attrs) {
		r.emitMu.Lock()
		defer r.emitMu.Unlock()
		if !r.stopped {
			a.rec.Emit(s.ID, at, attrs)
		}
	}
	emit := emitRec
	if s.ID == sensors.IdleSensor {
		emit = func(at time.Time, attrs core.Attrs) {
			r.emitMu.Lock()
			defer r.emitMu.Unlock()
			if !r.stopped {
				a.rec.Emit(s.ID, at, attrs)
				a.pushIdle(idleMsg{at, attrs})
			}
		}
	}
	for ctx.Err() == nil {
		b, reason := detect(ctx, s)
		if b == nil {
			a.setStatus(ctx, r, "", reason)
			if !wait(ctx, sensorRetry) {
				return
			}
			continue
		}
		a.setStatus(ctx, r, b.Name(), "")
		err := b.Run(ctx, emit)
		if ctx.Err() != nil {
			return
		}
		log.Printf("sensor %s (%s): %v", s.ID, b.Name(), err)
		emitRec(a.Now(), nil) // no news from a dead sensor: close its span
		if !wait(ctx, sensorRetry) {
			return
		}
	}
}

// sensorCaps reports every registered sensor as "sensor:<id>".
func (a *Agent) sensorCaps(ctx context.Context, caps map[string]modules.Capability) {
	if a.sensors == nil {
		return
	}
	for _, s := range a.sensors.All() {
		var c modules.Capability
		a.runMu.Lock()
		r := a.runners[s.ID]
		var backend, reason string
		known := r != nil && r.known
		if known {
			backend, reason = r.backend, r.reason
		}
		a.runMu.Unlock()
		if !known {
			// off, or not detected yet: ask the backends directly
			var b sensors.Backend
			b, reason = detect(ctx, s)
			if b != nil {
				backend = b.Name()
			}
		}
		if backend != "" {
			c = modules.Capability{Backend: backend, Available: true}
		} else {
			c = modules.Capability{Reason: reason}
		}
		if st := a.rec.Status(s.ID); st != "" {
			if c.Reason != "" {
				c.Reason += "; "
			}
			c.Reason += st
		}
		caps["sensor:"+s.ID] = c
	}
}

// helloCaps merges the module capabilities with the current sensor ones.
func (a *Agent) helloCaps(ctx context.Context, base map[string]modules.Capability) map[string]modules.Capability {
	out := make(map[string]modules.Capability, len(base)+2)
	for k, v := range base {
		out[k] = v
	}
	a.sensorCaps(ctx, out)
	return out
}

// setBaseCaps remembers the module capabilities for later hellos.
func (a *Agent) setBaseCaps(caps map[string]modules.Capability) {
	cp := make(map[string]modules.Capability, len(caps))
	for k, v := range caps {
		cp[k] = v
	}
	a.helloMu.Lock()
	a.baseCaps = cp
	a.helloMu.Unlock()
}

// requestHello sends a new hello soon, because the sensors changed: at most
// one is pending, and two are at least helloGap apart.
func (a *Agent) requestHello(ctx context.Context) {
	if a.sensors == nil || !a.helloPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		a.helloMu.Lock()
		gap := helloGap - time.Since(a.lastHello)
		a.helloMu.Unlock()
		if gap > 0 && !wait(ctx, gap) {
			return
		}
		a.helloPending.Store(false)
		a.helloMu.Lock()
		base := a.baseCaps
		a.helloMu.Unlock()
		a.mu.Lock()
		up := a.connected
		a.mu.Unlock()
		if base == nil || !up || ctx.Err() != nil {
			return // the next connect says it all
		}
		a.sendHello(ctx, a.helloCaps(ctx, base))
	}()
}

// sendHello posts the capabilities and notes the server's answer.
func (a *Agent) sendHello(ctx context.Context, caps map[string]modules.Capability) bool {
	a.helloMu.Lock()
	a.lastHello = time.Now()
	a.helloMu.Unlock()
	hr, err := a.client.Hello(ctx, caps)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("hello: %v", err)
		}
		return false
	}
	a.noteServer(hr)
	if a.setServerDevice(hr.Device) {
		// the name decides which sensors are on
		a.mu.Lock()
		a.applyObserve(ctx)
		a.mu.Unlock()
	}
	return true
}

// checkpointLoop lets the recorder write out its open spans every minute.
func (a *Agent) checkpointLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.rec.Checkpoint(a.Now())
		}
	}
}

var errObsUnsupported = errors.New("server does not accept observations yet")

// obsFlushLoop sends queued observations; it is independent of the event
// queue so a switch never waits behind them.
func (a *Agent) obsFlushLoop(ctx context.Context) {
	fails := 0
	var hold time.Time // after an error, kicks wait until then
	timer := time.NewTimer(obsFlushEvery)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.obsKick:
			if time.Now().Before(hold) {
				continue
			}
		case <-timer.C:
		}
		next := obsFlushEvery
		switch err := a.flushObs(ctx); {
		case err == nil:
			fails, hold = 0, time.Time{}
		case errors.Is(err, errObsUnsupported):
			if !a.obs404Logged.Swap(true) {
				log.Printf("%v", err)
			}
			next = obsHold404
			hold = time.Now().Add(next)
		default:
			next = backoff(fails)
			fails++
			hold = time.Now().Add(next)
			if ctx.Err() == nil && fails == 1 {
				log.Printf("sending observations failed, they stay queued: %v", err)
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(next)
	}
}

func isNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == 404
}

// flushObs sends the observation queue until it is empty.
func (a *Agent) flushObs(ctx context.Context) error {
	for {
		batch, err := a.db.ObsQueue(core.MaxObservationBatch)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		// what the server would refuse anyway is not worth sending
		now := a.Now()
		var ok, bad []core.Observation
		for _, o := range batch {
			if o.Validate(now) != nil {
				bad = append(bad, o)
			} else {
				ok = append(ok, o)
			}
		}
		if len(bad) > 0 {
			log.Printf("dropping %d invalid observations", len(bad))
			if err := a.db.DequeueObs(bad); err != nil {
				return err
			}
		}
		if len(ok) == 0 {
			continue
		}
		_, _, err = a.client.PostObservations(ctx, ok)
		switch {
		case err == nil:
		case isNotFound(err):
			return errObsUnsupported
		case rejected(err):
			// one bad span must not hold up the rest: one by one
			dropped := 0
			var sent []core.Observation
			for _, o := range ok {
				_, _, err := a.client.PostObservations(ctx, []core.Observation{o})
				switch {
				case err == nil:
					sent = append(sent, o)
				case isNotFound(err):
					return errObsUnsupported
				case rejected(err):
					dropped++
					sent = append(sent, o) // refused for good: out of the queue
				default:
					a.db.DequeueObs(sent)
					return err
				}
			}
			if dropped > 0 {
				log.Printf("the server refused %d observations, dropped", dropped)
			}
			ok = sent
		default:
			return err
		}
		if err := a.db.DequeueObs(ok); err != nil {
			return err
		}
	}
}

// finishObserving stops the sensors, closes the recorder and sends what is
// left, best effort.
func (a *Agent) finishObserving() {
	a.stopSensors()
	a.rec.Close(a.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.flushObs(ctx); err != nil {
		log.Printf("last observations stay queued: %v", err)
	}
}
