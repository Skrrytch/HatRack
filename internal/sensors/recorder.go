package sensors

import (
	"maps"
	"strings"
	"sync"
	"time"

	"hatrack/internal/core"
)

// Recorder limits.
const (
	MinSpan        = time.Second // shorter spans are flicker and get merged
	MaxSpansPerDay = 5000        // per sensor and local day
	MaxValueRunes  = 512         // per attribute value
	IdleSensor     = "idle"
)

const statusLimited = "too many changes today"

// span is a stretch of constant attributes. end is only set once closed.
type span struct {
	id, sensor string
	start, end time.Time
	attrs      core.Attrs
	emitted    bool // sent to the sink at least once; never discarded then
}

// state is what the recorder knows about one sensor.
type state struct {
	raw     core.Attrs // last state as reported, may be nil
	open    *span      // current span, nil if none
	prev    *span      // last closed span, for the debounce merge
	day     [3]int     // local day of count
	count   int        // spans started on day
	limited bool
	status  string
}

// Recorder turns the states sensors report into spans. It has no goroutines
// and no clock: every time is an argument. The sink is called with the lock
// held and must not call back into the recorder.
type Recorder struct {
	mu       sync.Mutex
	interval time.Duration
	sink     func(core.Observation)
	sensors  map[string]*state
	policy   *core.Observe // nil until SetPolicy: nothing is recorded
	device   string
	observe  bool
	lastCP   time.Time
	closed   bool
}

// NewRecorder returns a recorder; interval is the checkpoint interval used
// to detect gaps (suspend).
func NewRecorder(interval time.Duration, sink func(core.Observation)) *Recorder {
	return &Recorder{interval: interval, sink: sink, sensors: map[string]*state{}, observe: true}
}

func (r *Recorder) st(sensor string) *state {
	s := r.sensors[sensor]
	if s == nil {
		s = &state{}
		r.sensors[sensor] = s
	}
	return s
}

// Emit reports the state of a sensor at the given time.
func (r *Recorder) Emit(sensor string, at time.Time, a core.Attrs) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	s := r.st(sensor)
	s.raw = copyAttrs(a)
	r.gapCheck(at)
	r.apply(sensor, s, r.effective(sensor, s), at)
}

// SetObserve switches observing on or off (idle is always observed).
func (r *Recorder) SetObserve(on bool, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.observe = on
	r.reapply(at)
}

// SetPolicy sets the observe settings and the device name they apply to.
func (r *Recorder) SetPolicy(p core.Observe, device string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.policy, r.device = &p, device
	r.reapply(at)
}

// Checkpoint emits the open spans with End = now, so a crash loses at most
// one interval.
func (r *Recorder) Checkpoint(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.gapCheck(now)
	for _, s := range r.sensors {
		// Younger spans may still be discarded as flicker; never emit them.
		if s.open != nil && now.Sub(s.open.start) >= MinSpan {
			r.emit(s.open, now)
		}
	}
	r.lastCP = now
}

// Close ends all open spans at the given time; the recorder is dead after.
func (r *Recorder) Close(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for _, s := range r.sensors {
		r.closeSpan(s, at)
	}
	r.closed = true
}

// Status returns "" or the reason a sensor is not recording.
func (r *Recorder) Status(sensor string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sensors[sensor]; s != nil {
		return s.status
	}
	return ""
}

// reapply recomputes every sensor from its raw state after a settings change.
func (r *Recorder) reapply(at time.Time) {
	r.gapCheck(at)
	for id, s := range r.sensors {
		r.apply(id, s, r.effective(id, s), at)
	}
}

// effective filters the raw state; nil means no span.
func (r *Recorder) effective(sensor string, s *state) core.Attrs {
	if r.policy == nil || !r.policy.Enabled(r.device, sensor) {
		return nil
	}
	if !r.observe && sensor != IdleSensor {
		return nil
	}
	if s.raw == nil {
		return nil
	}
	a := copyAttrs(s.raw)
	app := a["app"]
	if r.policy.Ignored(app) {
		return core.Attrs{core.AttrPrivate: "1"}
	}
	if r.policy.Redacted(app) {
		delete(a, "title")
	}
	// a reader started in a race with a revoke sends nothing ungranted
	a, keep := r.policy.FilterGranted(r.device, sensor, a, nil)
	if !keep {
		return nil
	}
	if t, ok := a["title"]; ok {
		if t = core.NormalizeTitle(t); t == "" {
			delete(a, "title")
		} else {
			a["title"] = t
		}
	}
	for k, v := range a {
		a[k] = cutRunes(v, MaxValueRunes)
	}
	return a
}

// apply moves a sensor to the effective state e at time t.
func (r *Recorder) apply(id string, s *state, e core.Attrs, t time.Time) {
	if d := dayOf(t); d != s.day {
		s.day, s.count = d, 0
		s.limited, s.status = false, ""
	}
	if s.limited {
		return // only the raw state is remembered
	}
	o := s.open
	if o != nil && t.Before(o.start) {
		t = o.start // clock stepped back
	}
	switch {
	case o != nil && equalAttrs(e, o.attrs):
		return
	case e == nil:
		r.closeSpan(s, t)
	case o == nil:
		r.openSpan(id, s, e, t)
	case t.Sub(o.start) < MinSpan && !o.emitted:
		// Debounce: the flicker O is dropped (it was never emitted). If the
		// span before it carried E, that one continues; otherwise E takes
		// over from O's start, so the flicker's time is not lost.
		s.open = nil
		if p := s.prev; p != nil && p.end.Equal(o.start) && equalAttrs(p.attrs, e) {
			s.open, s.prev = p, nil
			return
		}
		r.openSpan(id, s, e, o.start)
	default:
		r.closeSpan(s, t)
		r.openSpan(id, s, e, t)
	}
}

// openSpan starts a span unless the daily limit is used up; then the sensor
// pauses until the next local day.
func (r *Recorder) openSpan(id string, s *state, e core.Attrs, t time.Time) {
	if s.count >= MaxSpansPerDay {
		s.limited, s.status = true, statusLimited
		return
	}
	s.open = &span{id: core.NewID(), sensor: id, start: t, attrs: e}
	s.count++
}

// closeSpan ends the open span at t, emits it and remembers it as previous.
func (r *Recorder) closeSpan(s *state, t time.Time) {
	o := s.open
	if o == nil {
		return
	}
	if t.Before(o.start) {
		t = o.start
	}
	o.end = t
	r.emit(o, t)
	s.prev, s.open = o, nil
}

func (r *Recorder) emit(sp *span, end time.Time) {
	sp.emitted = true
	r.sink(core.Observation{ID: sp.id, Sensor: sp.sensor, Start: sp.start, End: end, Attrs: copyAttrs(sp.attrs)})
}

// gapCheck detects that the agent did not run since the last checkpoint
// (suspend): open spans end at that checkpoint and restart at t with the
// same attributes. Nothing may be merged across the gap.
func (r *Recorder) gapCheck(t time.Time) {
	if r.lastCP.IsZero() || t.Sub(r.lastCP) <= 3*r.interval {
		return
	}
	for id, s := range r.sensors {
		s.prev = nil
		if s.open == nil {
			continue
		}
		a := s.open.attrs
		r.closeSpan(s, r.lastCP)
		s.prev = nil
		r.openSpan(id, s, a, t)
	}
	r.lastCP = t
}

func dayOf(t time.Time) [3]int {
	y, m, d := t.Local().Date()
	return [3]int{y, int(m), d}
}

func equalAttrs(a, b core.Attrs) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return maps.Equal(a, b)
}

func copyAttrs(a core.Attrs) core.Attrs {
	if a == nil {
		return nil
	}
	return maps.Clone(a)
}

// cutRunes shortens v to at most n runes and makes sure it is valid UTF-8.
func cutRunes(v string, n int) string {
	v = strings.ToValidUTF8(v, "�")
	i := 0
	for pos := range v {
		if i == n {
			return v[:pos]
		}
		i++
	}
	return v
}
