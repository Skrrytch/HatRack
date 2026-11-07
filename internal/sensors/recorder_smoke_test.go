package sensors

import (
	"testing"
	"time"

	"hatrack/internal/core"
)

type collector struct{ got map[string]core.Observation }

func newRec() (*Recorder, *collector) {
	c := &collector{got: map[string]core.Observation{}}
	r := NewRecorder(time.Minute, func(o core.Observation) { c.got[o.ID] = o })
	r.SetPolicy(core.Observe{Devices: map[string]map[string]bool{"laptop": {"window": true}}}, "laptop", t0)
	return r, c
}

var t0 = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

func win(app string) core.Attrs { return core.Attrs{"app": app} }

func TestSmokeBoundaries(t *testing.T) {
	r, c := newRec()
	r.Emit("window", t0, win("a"))
	r.Emit("window", t0.Add(10*time.Second), win("b"))
	if len(c.got) != 1 {
		t.Fatalf("got %d", len(c.got))
	}
	for _, o := range c.got {
		if !o.Start.Equal(t0) || !o.End.Equal(t0.Add(10*time.Second)) || o.Attrs["app"] != "a" {
			t.Fatalf("%+v", o)
		}
	}
}

func TestSmokeDebounceReopen(t *testing.T) {
	r, c := newRec()
	r.Emit("window", t0, win("A"))
	r.Emit("window", t0.Add(10*time.Second), win("B"))
	r.Emit("window", t0.Add(10400*time.Millisecond), win("A"))
	r.Close(t0.Add(20 * time.Second))
	if len(c.got) != 1 {
		t.Fatalf("got %+v", c.got)
	}
	for _, o := range c.got {
		if o.Attrs["app"] != "A" || !o.End.Equal(t0.Add(20*time.Second)) {
			t.Fatalf("%+v", o)
		}
	}
}

func TestSmokeGap(t *testing.T) {
	r, c := newRec()
	r.Emit("window", t0, win("A"))
	for i := 1; i <= 3; i++ {
		r.Checkpoint(t0.Add(time.Duration(i) * time.Minute))
	}
	r.Checkpoint(t0.Add(8 * time.Minute))
	if len(c.got) != 1 {
		t.Fatalf("got %+v", c.got)
	}
	for _, o := range c.got {
		if !o.End.Equal(t0.Add(3 * time.Minute)) {
			t.Fatalf("%+v", o)
		}
	}
	r.Close(t0.Add(9 * time.Minute))
	if len(c.got) != 2 {
		t.Fatalf("got %+v", c.got)
	}
}
