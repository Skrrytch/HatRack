package sensors

import (
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"hatrack/internal/core"
)

// recBase is a fixed local time, so the "local day" logic is predictable.
var recBase = time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)

// recSink collects emitted observations. latest keeps the newest version of
// each ID, order the IDs in order of first emission, emissions counts every
// call (upserts included).
type recSink struct {
	latest    map[string]core.Observation
	order     []string
	emissions int
}

func (s *recSink) add(o core.Observation) {
	if _, ok := s.latest[o.ID]; !ok {
		s.order = append(s.order, o.ID)
	}
	s.latest[o.ID] = o
	s.emissions++
}

// spans returns the latest version of every observation, in order of first
// emission.
func (s *recSink) spans() []core.Observation {
	out := make([]core.Observation, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.latest[id])
	}
	return out
}

// ofSensor returns the spans of one sensor, in order of first emission.
func (s *recSink) ofSensor(sensor string) []core.Observation {
	var out []core.Observation
	for _, o := range s.spans() {
		if o.Sensor == sensor {
			out = append(out, o)
		}
	}
	return out
}

// laptopPolicy enables window and idle (idle is on by default) for "laptop".
func laptopPolicy() core.Observe {
	return core.Observe{Devices: map[string]map[string]bool{"laptop": {"window": true}}}
}

// newRecWith returns a recorder with the given policy for device "laptop",
// set at recBase, and the sink it writes to.
func newRecWith(p core.Observe) (*Recorder, *recSink) {
	s := &recSink{latest: map[string]core.Observation{}}
	r := NewRecorder(time.Minute, s.add)
	r.SetPolicy(p, "laptop", recBase)
	return r, s
}

// winAttrs returns window attributes for an app and title.
func winAttrs(app, title string) core.Attrs {
	return core.Attrs{"app": app, "title": title}
}

func checkTime(t *testing.T, what string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func checkAttrs(t *testing.T, what string, got, want core.Attrs) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// Case 1: two different states 10 s apart give exact span boundaries.
func TestRecorderCase01TwoStatesBoundaries(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "code"})
	r.Emit("window", recBase.Add(10*time.Second), core.Attrs{"app": "firefox"})
	if n := len(s.spans()); n != 1 {
		t.Fatalf("after second state: got %d spans, want 1", n)
	}
	r.Close(recBase.Add(20 * time.Second))

	got := s.spans()
	if len(got) != 2 {
		t.Fatalf("got %d spans, want 2", len(got))
	}
	checkTime(t, "first start", got[0].Start, recBase)
	checkTime(t, "first end", got[0].End, recBase.Add(10*time.Second))
	checkAttrs(t, "first attrs", got[0].Attrs, core.Attrs{"app": "code"})
	checkTime(t, "second start", got[1].Start, recBase.Add(10*time.Second))
	checkTime(t, "second end", got[1].End, recBase.Add(20*time.Second))
	checkAttrs(t, "second attrs", got[1].Attrs, core.Attrs{"app": "firefox"})
}

// Case 2: the same state twice gives one span; nothing is emitted until a
// checkpoint or close.
func TestRecorderCase02SameStateOneSpan(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "code"})
	r.Emit("window", recBase.Add(5*time.Second), core.Attrs{"app": "code"})
	if s.emissions != 0 {
		t.Fatalf("emitted %d times before checkpoint, want 0", s.emissions)
	}

	r.Checkpoint(recBase.Add(30 * time.Second))
	if len(s.spans()) != 1 {
		t.Fatalf("after checkpoint: got %d spans, want 1", len(s.spans()))
	}
	checkTime(t, "checkpoint end", s.spans()[0].End, recBase.Add(30*time.Second))

	r.Close(recBase.Add(60 * time.Second))
	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("after close: got %d spans, want 1", len(got))
	}
	checkTime(t, "close end", got[0].End, recBase.Add(60*time.Second))
	checkTime(t, "start", got[0].Start, recBase)
}

// Case 3: flicker shorter than MinSpan is merged; the span keeps the last
// state and starts at the first one.
func TestRecorderCase03FlickerMerged(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "A"})
	r.Emit("window", recBase.Add(300*time.Millisecond), core.Attrs{"app": "B"})
	r.Emit("window", recBase.Add(600*time.Millisecond), core.Attrs{"app": "C"})
	r.Checkpoint(recBase.Add(5 * time.Second))

	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1", len(got))
	}
	checkAttrs(t, "attrs", got[0].Attrs, core.Attrs{"app": "C"})
	checkTime(t, "start", got[0].Start, recBase)
}

// Case 3b: B is held only briefly between two A states. B is discarded, the
// earlier A span is reopened with the same ID and grows.
func TestRecorderCase03bDebounceReopensPrevious(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "A"})
	r.Emit("window", recBase.Add(10*time.Second), core.Attrs{"app": "B"})
	r.Emit("window", recBase.Add(10400*time.Millisecond), core.Attrs{"app": "A"})
	r.Close(recBase.Add(20 * time.Second))

	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1 (B must not exist)", len(got))
	}
	for _, o := range got {
		if o.Attrs["app"] == "B" {
			t.Fatalf("span with B exists: %+v", o)
		}
	}
	checkAttrs(t, "attrs", got[0].Attrs, core.Attrs{"app": "A"})
	checkTime(t, "start", got[0].Start, recBase)
	checkTime(t, "end", got[0].End, recBase.Add(20*time.Second))
	if s.emissions != 2 {
		// once when B started, once at close: same ID both times
		t.Errorf("emissions: got %d, want 2", s.emissions)
	}
}

// Case 4: ignored apps are recorded as a private span without details; a
// second ignored app does not start a new span.
func TestRecorderCase04IgnoredAppsPrivate(t *testing.T) {
	p := laptopPolicy()
	p.Ignore = []string{"keepassxc", "signal"}
	r, s := newRecWith(p)
	r.Emit("window", recBase, core.Attrs{"app": "KeePassXC", "title": "secret"})
	r.Emit("window", recBase.Add(5*time.Second), core.Attrs{"app": "Signal", "title": "chat"})
	r.Close(recBase.Add(10 * time.Second))

	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1", len(got))
	}
	checkAttrs(t, "attrs", got[0].Attrs, core.Attrs{core.AttrPrivate: "1"})
	checkTime(t, "end", got[0].End, recBase.Add(10*time.Second))
}

// Case 5: a redacted app keeps its span while the title changes; the title
// is never stored.
func TestRecorderCase05RedactedTitleDropped(t *testing.T) {
	p := laptopPolicy()
	p.Redact = []string{"thunderbird"}
	r, s := newRecWith(p)
	r.Emit("window", recBase, core.Attrs{"app": "thunderbird", "title": "Inbox"})
	r.Emit("window", recBase.Add(10*time.Second), core.Attrs{"app": "thunderbird", "title": "Inbox (2)"})
	r.Emit("window", recBase.Add(20*time.Second), core.Attrs{"app": "thunderbird", "title": "Draft"})
	r.Emit("window", recBase.Add(30*time.Second), core.Attrs{"app": "thunderbird", "title": "Inbox"})
	if s.emissions != 0 {
		t.Fatalf("emitted %d times during one span, want 0", s.emissions)
	}
	r.Close(recBase.Add(40 * time.Second))

	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1", len(got))
	}
	checkAttrs(t, "attrs", got[0].Attrs, core.Attrs{"app": "thunderbird"})
	if _, ok := got[0].Attrs["title"]; ok {
		t.Errorf("title stored: %v", got[0].Attrs)
	}
}

// Case 6: a title longer than 512 runes is cut to 512 runes, still valid
// UTF-8, and is a prefix of the original.
func TestRecorderCase06LongTitleCut(t *testing.T) {
	orig := strings.Repeat("ä€😀", 200) // 600 runes, multibyte
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "code", "title": orig})
	r.Close(recBase.Add(5 * time.Second))

	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1", len(got))
	}
	title := got[0].Attrs["title"]
	if n := utf8.RuneCountInString(title); n != MaxValueRunes {
		t.Errorf("title has %d runes, want %d", n, MaxValueRunes)
	}
	if !utf8.ValidString(title) {
		t.Errorf("title is not valid UTF-8")
	}
	if !strings.HasPrefix(orig, title) {
		t.Errorf("title is not a prefix of the original")
	}
}

// Case 7: switching observing off closes the window span but not the idle
// span; switching it on opens a window span with the last window state
// without a new Emit.
func TestRecorderCase07SetObserveOffOn(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("idle", recBase, core.Attrs{"state": "active"})
	r.Emit("window", recBase, core.Attrs{"app": "code"})

	t1 := recBase.Add(1 * time.Minute)
	r.SetObserve(false, t1)
	t2 := recBase.Add(3 * time.Minute)
	r.SetObserve(true, t2)
	r.Checkpoint(recBase.Add(4 * time.Minute))

	window := s.ofSensor("window")
	if len(window) != 2 {
		t.Fatalf("got %d window spans, want 2", len(window))
	}
	checkTime(t, "first window start", window[0].Start, recBase)
	checkTime(t, "first window end", window[0].End, t1)
	checkTime(t, "second window start", window[1].Start, t2)
	checkTime(t, "second window end", window[1].End, recBase.Add(4*time.Minute))
	checkAttrs(t, "second window attrs", window[1].Attrs, core.Attrs{"app": "code"})
	if window[0].ID == window[1].ID {
		t.Errorf("window spans share an ID")
	}

	idle := s.ofSensor("idle")
	if len(idle) != 1 {
		t.Fatalf("got %d idle spans, want 1", len(idle))
	}
	checkTime(t, "idle start (span stayed open)", idle[0].Start, recBase)
}

// Case 8: a new ignore list that contains the open span's app closes the span
// and opens a private span at the same moment.
func TestRecorderCase08PolicyChangeClosesSpan(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "code", "title": "x"})

	t1 := recBase.Add(1 * time.Minute)
	p := laptopPolicy()
	p.Ignore = []string{"code"}
	r.SetPolicy(p, "laptop", t1)
	r.Close(recBase.Add(2 * time.Minute))

	got := s.spans()
	if len(got) != 2 {
		t.Fatalf("got %d spans, want 2", len(got))
	}
	checkAttrs(t, "first attrs", got[0].Attrs, core.Attrs{"app": "code", "title": "x"})
	checkTime(t, "first end", got[0].End, t1)
	checkAttrs(t, "second attrs", got[1].Attrs, core.Attrs{core.AttrPrivate: "1"})
	checkTime(t, "second start", got[1].Start, t1)
	checkTime(t, "second end", got[1].End, recBase.Add(2*time.Minute))
}

// Case 9: a checkpoint after a gap of more than 3 intervals ends the open
// span at the last checkpoint and starts a new one at the new time.
func TestRecorderCase09GapSplitsSpan(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "code"})
	r.Checkpoint(recBase.Add(1 * time.Minute))
	r.Checkpoint(recBase.Add(2 * time.Minute))
	r.Checkpoint(recBase.Add(7 * time.Minute))

	window := s.ofSensor("window")
	if len(window) != 1 {
		t.Fatalf("before close: got %d window spans, want 1", len(window))
	}
	checkTime(t, "span end at last checkpoint", window[0].End, recBase.Add(2*time.Minute))

	r.Close(recBase.Add(8 * time.Minute))
	window = s.ofSensor("window")
	if len(window) != 2 {
		t.Fatalf("after close: got %d window spans, want 2", len(window))
	}
	if window[0].ID == window[1].ID {
		t.Errorf("new span reuses the old ID")
	}
	checkTime(t, "first end", window[0].End, recBase.Add(2*time.Minute))
	checkTime(t, "second start", window[1].Start, recBase.Add(7*time.Minute))
	checkTime(t, "second end", window[1].End, recBase.Add(8*time.Minute))
	checkAttrs(t, "second attrs", window[1].Attrs, core.Attrs{"app": "code"})
}

// Case 10: at most MaxSpansPerDay spans open per sensor and local day; the
// next change is refused until the next local day.
func TestRecorderCase10DailyLimit(t *testing.T) {
	const n = MaxSpansPerDay
	r, s := newRecWith(laptopPolicy())

	// n openings, two states alternating 2 s apart (no debounce).
	for i := 0; i < n; i++ {
		app := "a"
		if i%2 == 1 {
			app = "b"
		}
		r.Emit("window", recBase.Add(time.Duration(2*i)*time.Second), core.Attrs{"app": app})
	}
	if st := r.Status("window"); st != "" {
		t.Fatalf("after %d openings: status %q, want empty", n, st)
	}
	if got := len(s.spans()); got != n-1 {
		t.Fatalf("after %d openings: %d spans emitted, want %d (last still open)", n, got, n-1)
	}

	// The next different state closes the n-th span and is refused.
	limitAt := recBase.Add(time.Duration(2*n) * time.Second)
	r.Emit("window", limitAt, core.Attrs{"app": "c"})
	if st := r.Status("window"); st != statusLimited {
		t.Fatalf("status %q, want %q", st, statusLimited)
	}
	if got := len(s.spans()); got != n {
		t.Fatalf("after limit: %d spans, want %d", got, n)
	}

	// Further states on the same day open nothing.
	r.Emit("window", limitAt.Add(2*time.Second), core.Attrs{"app": "d"})
	if got := len(s.spans()); got != n {
		t.Fatalf("after refused change: %d spans, want %d (no new ID)", got, n)
	}
	if st := r.Status("window"); st != statusLimited {
		t.Fatalf("status %q, want %q", st, statusLimited)
	}

	// On the next local day the limit is lifted and a span opens again.
	nextDay := recBase.Add(24 * time.Hour)
	r.Emit("window", nextDay, core.Attrs{"app": "e"})
	if st := r.Status("window"); st != "" {
		t.Fatalf("next day: status %q, want empty", st)
	}
	r.Close(nextDay.Add(time.Minute))
	got := s.spans()
	if len(got) != n+1 {
		t.Fatalf("next day: %d spans, want %d", len(got), n+1)
	}
	last := got[len(got)-1]
	checkTime(t, "next day start", last.Start, nextDay)
	checkAttrs(t, "next day attrs", last.Attrs, core.Attrs{"app": "e"})
}

// Case 11: a sensor disabled for the device records nothing; idle, without an
// explicit setting, records.
func TestRecorderCase11DisabledSensorRecordsNothing(t *testing.T) {
	p := core.Observe{Devices: map[string]map[string]bool{"laptop": {"window": false}}}
	r, s := newRecWith(p)
	r.Emit("window", recBase, core.Attrs{"app": "code"})
	r.Emit("window", recBase.Add(5*time.Second), core.Attrs{"app": "firefox"})
	r.Emit("idle", recBase, core.Attrs{"state": "active"})
	r.Close(recBase.Add(10 * time.Second))

	if n := len(s.ofSensor("window")); n != 0 {
		t.Errorf("window: got %d spans, want 0", n)
	}
	idle := s.ofSensor("idle")
	if len(idle) != 1 {
		t.Fatalf("idle: got %d spans, want 1", len(idle))
	}
	checkTime(t, "idle start", idle[0].Start, recBase)
	checkTime(t, "idle end", idle[0].End, recBase.Add(10*time.Second))
}

// Case 12: before the first SetPolicy nothing is recorded.
func TestRecorderCase12NoPolicyNoRecording(t *testing.T) {
	s := &recSink{latest: map[string]core.Observation{}}
	r := NewRecorder(time.Minute, s.add)
	r.Emit("window", recBase, core.Attrs{"app": "code"})
	r.Emit("idle", recBase, core.Attrs{"state": "active"})
	r.Checkpoint(recBase.Add(1 * time.Minute))
	r.Close(recBase.Add(2 * time.Minute))

	if s.emissions != 0 || len(s.spans()) != 0 {
		t.Fatalf("recorded without policy: %d emissions, %d spans", s.emissions, len(s.spans()))
	}
}

// Case 13: a checkpoint emits only spans that are at least MinSpan old; close
// emits everything.
func TestRecorderCase13CheckpointMinAge(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, core.Attrs{"app": "code"})
	r.Checkpoint(recBase.Add(500 * time.Millisecond))
	if s.emissions != 0 {
		t.Fatalf("checkpoint emitted a 0.5 s span (%d emissions)", s.emissions)
	}

	r.Close(recBase.Add(500 * time.Millisecond))
	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("after close: got %d spans, want 1", len(got))
	}
	checkTime(t, "end", got[0].End, recBase.Add(500*time.Millisecond))
}

// Case 14: changing the caller's attribute map after Emit does not change
// the recorded span, and the recorder's raw state is not aliased either.
func TestRecorderCase14InputMapCopied(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	attrs := core.Attrs{"app": "code", "title": "x"}
	r.Emit("window", recBase, attrs)

	attrs["app"] = "other"
	attrs["title"] = "changed"
	attrs["extra"] = "1"

	// Reapplying the settings uses the remembered raw state; if it were
	// aliased to attrs, this would split the span.
	r.SetObserve(true, recBase.Add(5*time.Second))
	r.Close(recBase.Add(10 * time.Second))

	got := s.spans()
	if len(got) != 1 {
		t.Fatalf("got %d spans, want 1", len(got))
	}
	checkAttrs(t, "attrs", got[0].Attrs, core.Attrs{"app": "code", "title": "x"})
	checkTime(t, "end", got[0].End, recBase.Add(10*time.Second))
}

// Case 15: concurrent Emit, SetObserve, Checkpoint and Status calls. Only
// meant to run under -race; no assertions on the content.
func TestRecorderCase15ConcurrentCalls(t *testing.T) {
	r, _ := newRecWith(laptopPolicy())
	var wg sync.WaitGroup

	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				at := recBase.Add(time.Duration(i*50+g) * time.Millisecond)
				r.Emit("window", at, core.Attrs{"app": fmt.Sprintf("app%d", (i+g)%3)})
				r.Emit("idle", at, core.Attrs{"state": "active"})
			}
		}(g)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.SetObserve(i%2 == 0, recBase.Add(time.Duration(i)*time.Second))
			_ = r.Status("window")
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.Checkpoint(recBase.Add(time.Duration(i) * time.Second))
		}
	}()

	wg.Wait()
	r.Close(recBase.Add(time.Hour))
}

// Case 16: a title that only changes its status symbol or unread counter
// stays one span, stored without the noise; a title of noise only is dropped.
func TestRecorderCase16TitleNoise(t *testing.T) {
	r, s := newRecWith(laptopPolicy())
	r.Emit("window", recBase, winAttrs("Gnome-terminal", "◑ Task"))
	r.Emit("window", recBase.Add(10*time.Second), winAttrs("Gnome-terminal", "✳ Task"))
	r.Emit("window", recBase.Add(20*time.Second), winAttrs("Gnome-terminal", "⠂  Task "))
	r.Emit("window", recBase.Add(30*time.Second), winAttrs("Firefox", "(3) Mail"))
	r.Emit("window", recBase.Add(40*time.Second), winAttrs("Firefox", "(4) Mail"))
	r.Emit("window", recBase.Add(50*time.Second), winAttrs("Firefox", "◐"))
	r.Close(recBase.Add(60 * time.Second))

	got := s.ofSensor("window")
	if len(got) != 3 {
		t.Fatalf("got %d spans, want 3: %v", len(got), got)
	}
	checkAttrs(t, "terminal", got[0].Attrs, winAttrs("Gnome-terminal", "Task"))
	checkTime(t, "terminal end", got[0].End, recBase.Add(30*time.Second))
	checkAttrs(t, "mail", got[1].Attrs, winAttrs("Firefox", "Mail"))
	checkAttrs(t, "noise only", got[2].Attrs, core.Attrs{"app": "Firefox"})
}
