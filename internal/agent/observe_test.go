package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
	"hatrack/internal/sensors"
)

// chanBackend is a sensor backend that reports what the test sends.
type chanBackend struct {
	name   string
	ok     bool
	reason string
	in     chan chanMsg
}

type chanMsg struct {
	at    time.Time
	attrs core.Attrs
	ack   chan struct{}
}

func newChan(name string) *chanBackend {
	return &chanBackend{name: name, ok: true, in: make(chan chanMsg)}
}

func (b *chanBackend) Name() string { return b.name }
func (b *chanBackend) Detect(context.Context) (bool, string) {
	return b.ok, b.reason
}
func (b *chanBackend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-b.in:
			emit(m.at, m.attrs)
			close(m.ack)
		}
	}
}

// send reports a state and returns once the recorder has it.
func (b *chanBackend) send(t *testing.T, at time.Time, attrs core.Attrs) {
	t.Helper()
	m := chanMsg{at, attrs, make(chan struct{})}
	select {
	case b.in <- m:
		<-m.ack
	case <-time.After(5 * time.Second):
		t.Fatal("sensor backend is not running")
	}
}

func sensorRegistry(idle, window sensors.Backend) *sensors.Registry {
	r := &sensors.Registry{}
	r.Register(sensors.Sensor{ID: "idle", Backends: []sensors.Backend{idle}})
	r.Register(sensors.Sensor{ID: "window", Backends: []sensors.Backend{window}})
	return r
}

const observeConfig = `version: 1
observe:
  devices:
    laptop: {window: true}
hats:
  venom:
    name: VENOM
    activities: [development]
  private:
    name: Private
    observe: false
    activities: []
`

// obsAgent makes an agent with sensors; name is the device name the agent
// believes in (the server knows the rig's device as "laptop").
func (r *rig) obsAgent(name string, reg *sensors.Registry) *Agent {
	a, _ := r.agent(name)
	a.SetSensors(reg)
	return a
}

func (a *Agent) running(id string) bool {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	r := a.runners[id]
	return r != nil && r.known
}

func (r *rig) stored() []core.Observation {
	r.t.Helper()
	now := time.Now()
	got, err := r.st.ObservationsBetween(context.Background(), now.Add(-48*time.Hour), now.Add(time.Hour), "", "")
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *rig) storedOf(sensor string) []core.Observation {
	var out []core.Observation
	for _, o := range r.stored() {
		if o.Sensor == sensor {
			out = append(out, o)
		}
	}
	return out
}

func waitConnected(t *testing.T, a *Agent) {
	t.Helper()
	eventually(t, "connection", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.connected })
}

func near(a, b time.Time) bool { return a.Sub(b).Abs() < time.Second }

func obsOf(id string, start, end time.Time) core.Observation {
	return core.Observation{ID: id, Sensor: "idle", Start: start, End: end, Attrs: core.Attrs{"state": "idle"}}
}

// Test 12: spans pile up while the server is away and arrive later; an open
// span is queued once, with its latest end.
func TestObservationsQueueWhileServerDown(t *testing.T) {
	r := newRig(t)
	idle, window := newChan("chan"), newChan("chan")
	a := r.obsAgent("laptop", sensorRegistry(idle, window))
	r.down.Store(true)
	r.start(a)
	eventually(t, "idle sensor", func() bool { return a.running("idle") })

	now := time.Now()
	idle.send(t, now.Add(-10*time.Minute), core.Attrs{"state": "idle"})
	a.rec.Checkpoint(now.Add(-3 * time.Minute))
	a.rec.Checkpoint(now.Add(-2 * time.Minute))
	if n := a.db.ObsQueueLen(); n != 1 {
		t.Fatalf("queue holds %d rows, want 1 (upsert)", n)
	}
	q, _ := a.db.ObsQueue(10)
	if !near(q[0].End, now.Add(-2*time.Minute)) {
		t.Fatalf("queued end %v, want the latest checkpoint", q[0].End)
	}
	a.kickObs()
	time.Sleep(100 * time.Millisecond)
	if len(r.stored()) != 0 {
		t.Fatal("server has observations while down")
	}

	r.down.Store(false)
	a.kickObs()
	eventually(t, "observations at the server", func() bool { return len(r.storedOf("idle")) == 1 && a.db.ObsQueueLen() == 0 })
	got := r.storedOf("idle")[0]
	if !near(got.End, now.Add(-2*time.Minute)) || !near(got.Start, now.Add(-10*time.Minute)) {
		t.Fatalf("stored span %v - %v", got.Start, got.End)
	}
}

// Test 13: the two queues do not hold each other up.
func TestObservationAndEventQueuesAreIndependent(t *testing.T) {
	r := newRig(t)
	r.importYAML(observeConfig)
	idle, window := newChan("chan"), newChan("chan")
	a := r.obsAgent("laptop", sensorRegistry(idle, window))
	r.start(a)
	waitConnected(t, a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 2 })
	eventually(t, "idle sensor", func() bool { return a.running("idle") })

	// the observation endpoint refuses everything: a switch still arrives
	r.obsStatus.Store(400)
	now := time.Now()
	idle.send(t, now.Add(-10*time.Minute), core.Attrs{"state": "idle"})
	a.rec.Checkpoint(now.Add(-2 * time.Minute))
	a.kickObs()
	if resp := call(t, a, Request{Cmd: "on", Hat: "venom"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	eventually(t, "switch at the server", func() bool { st, _ := r.st.CurrentState(context.Background()); return st.Hat == "venom" })
	eventually(t, "refused observations dropped", func() bool { return a.db.ObsQueueLen() == 0 })
	if len(r.stored()) != 0 {
		t.Fatal("refused observation was stored")
	}

	// events stuck: observations still go out
	r.obsStatus.Store(0)
	r.evStatus.Store(503)
	if resp := call(t, a, Request{Cmd: "off"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	idle.send(t, now.Add(-1*time.Minute), core.Attrs{"state": "idle", "n": "2"})
	a.rec.Checkpoint(now)
	a.kickObs()
	eventually(t, "observations despite stuck events", func() bool { return len(r.storedOf("idle")) > 0 && a.db.ObsQueueLen() == 0 })
	if a.db.QueueLen() == 0 {
		t.Fatal("event queue should still hold the switch")
	}
	r.evStatus.Store(0)
	a.kickFlush()
	eventually(t, "events delivered", func() bool { return a.db.QueueLen() == 0 })
}

// Test 14: the hello carries the sensors.
func TestHelloHasSensorCapabilities(t *testing.T) {
	r := newRig(t)
	idle, window := newChan("chan"), newChan("x11")
	window.ok, window.reason = false, "no display"
	a := r.obsAgent("laptop", sensorRegistry(idle, window))
	r.start(a)
	waitConnected(t, a)
	var caps map[string]modules.Capability
	eventually(t, "capabilities", func() bool {
		devs, _ := r.st.Devices(context.Background())
		for _, d := range devs {
			if len(d.Capabilities) > 0 && json.Unmarshal(d.Capabilities, &caps) == nil {
				_, ok := caps["sensor:window"]
				return ok
			}
		}
		return false
	})
	if c := caps["sensor:idle"]; !c.Available || c.Backend != "chan" {
		t.Errorf("sensor:idle = %+v", c)
	}
	c := caps["sensor:window"]
	if c.Available || c.Reason != "x11: no display" {
		t.Errorf("sensor:window = %+v", c)
	}
	if _, ok := caps["look"]; !ok {
		t.Error("module capabilities are gone")
	}
}

// Test 15 (run with -race): switches and sensor reports at the same time.
func TestSwitchesAndSensorsConcurrently(t *testing.T) {
	r := newRig(t)
	r.importYAML(observeConfig)
	idle, window := newChan("chan"), newChan("chan")
	a := r.obsAgent("laptop", sensorRegistry(idle, window))
	r.start(a)
	waitConnected(t, a)
	eventually(t, "sensors", func() bool { return a.running("idle") && a.running("window") })

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			hat := []string{"venom", "private"}[i%2]
			if resp := call(t, a, Request{Cmd: "on", Hat: hat}); !resp.OK {
				t.Error(resp.Error)
				return
			}
		}
	}()
	for _, b := range []*chanBackend{idle, window} {
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				b.send(t, time.Now(), core.Attrs{"app": fmt.Sprint("a", i%3)})
				a.rec.Checkpoint(time.Now())
			}
		}()
	}
	wg.Wait()
	a.kickObs()
	eventually(t, "queue empty", func() bool { return a.db.ObsQueueLen() == 0 })
}

// Test 16: only the version that was sent leaves the queue.
func TestDequeueObsKeepsNewerVersion(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	id := core.NewID()
	v1 := obsOf(id, now.Add(-time.Hour), now.Add(-30*time.Minute))
	v2 := obsOf(id, now.Add(-time.Hour), now.Add(-20*time.Minute))
	if err := db.EnqueueObs(v1); err != nil {
		t.Fatal(err)
	}
	sent, _ := db.ObsQueue(10)
	if err := db.EnqueueObs(v2); err != nil {
		t.Fatal(err)
	}
	if err := db.DequeueObs(sent); err != nil {
		t.Fatal(err)
	}
	q, _ := db.ObsQueue(10)
	if len(q) != 1 || !q[0].End.Equal(v2.End) || db.ObsQueueLen() != 1 {
		t.Fatalf("queue after outdated dequeue: %+v", q)
	}
	if err := db.DequeueObs(q); err != nil {
		t.Fatal(err)
	}
	if db.ObsQueueLen() != 0 {
		t.Fatal("current version should be gone")
	}
}

// Test 17: a hat with observe: false stops window spans, not idle.
func TestHatWithoutObserving(t *testing.T) {
	r := newRig(t)
	r.importYAML(observeConfig)
	idle, window := newChan("chan"), newChan("chan")
	a := r.obsAgent("laptop", sensorRegistry(idle, window))
	r.start(a)
	waitConnected(t, a)
	eventually(t, "sensors", func() bool { return a.running("idle") && a.running("window") })

	now := time.Now()
	idle.send(t, now.Add(-time.Hour), core.Attrs{"state": "idle"})
	window.send(t, now.Add(-time.Hour), core.Attrs{"app": "a"})
	if resp := call(t, a, Request{Cmd: "on", Hat: "private"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	window.send(t, time.Now(), core.Attrs{"app": "b"}) // while the hat forbids it
	a.rec.Checkpoint(time.Now().Add(5 * time.Second))
	a.kickObs()
	eventually(t, "flushed", func() bool { return a.db.ObsQueueLen() == 0 && len(r.storedOf("idle")) == 1 })
	for _, o := range r.storedOf("window") {
		if o.Attrs["app"] != "a" {
			t.Fatalf("window span recorded under the private hat: %+v", o)
		}
	}
	if idles := r.storedOf("idle"); !near(idles[0].End, time.Now().Add(5*time.Second)) {
		t.Fatalf("idle should continue, ends %v", idles[0].End)
	}

	if resp := call(t, a, Request{Cmd: "on", Hat: "venom"}); !resp.OK {
		t.Fatal(resp.Error)
	}
	a.rec.Checkpoint(time.Now().Add(10 * time.Second))
	a.kickObs()
	eventually(t, "window span resumes", func() bool {
		for _, o := range r.storedOf("window") {
			if o.Attrs["app"] == "b" {
				return true
			}
		}
		return false
	})
}

// Test 18: the name from the hello decides, not the local one.
func TestServerDeviceNameDecidesSensors(t *testing.T) {
	r := newRig(t)
	r.importYAML(observeConfig)
	idle, window := newChan("chan"), newChan("chan")
	a := r.obsAgent("somename", sensorRegistry(idle, window))
	r.start(a)
	eventually(t, "idle sensor", func() bool { return a.running("idle") })
	eventually(t, "window sensor after the hello", func() bool { return a.running("window") })
	if got := a.deviceName(); got != "laptop" {
		t.Fatalf("device name %q", got)
	}
	var cached string
	if ok, _ := a.db.Get("server_device", &cached); !ok || cached != "laptop" {
		t.Fatalf("cached name %q", cached)
	}

	now := time.Now()
	window.send(t, now.Add(-10*time.Minute), core.Attrs{"app": "code"})
	a.rec.Checkpoint(now.Add(-time.Minute))
	a.kickObs()
	eventually(t, "window span at the server", func() bool { return len(r.storedOf("window")) == 1 })
}

// Test 19: the queue has a bound.
func TestObsQueueBound(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 19998 rows in one go (a commit each would take minutes), then 7 through
	// EnqueueObs: 20005 in all
	const bulk = MaxObsQueue - 2
	if _, err := db.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
	  INSERT INTO obs_queue (id, json) SELECT 'bulk-' || i, '{"id":"bulk-' || i || '"}' FROM n`, bulk); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 7; i++ {
		if err := db.EnqueueObs(obsOf(core.NewID(), now.Add(-time.Hour), now.Add(-30*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if n := db.ObsQueueLen(); n != MaxObsQueue {
		t.Fatalf("queue length %d, want %d", n, MaxObsQueue)
	}
	q, err := db.ObsQueue(1)
	if err != nil || len(q) != 1 || q[0].ID != "bulk-6" {
		t.Fatalf("oldest left: %+v (%v), want bulk-6", q, err)
	}
	var rows int
	db.db.QueryRow(`SELECT COUNT(*) FROM obs_queue`).Scan(&rows)
	if rows != MaxObsQueue {
		t.Fatalf("%d rows in the table", rows)
	}
}
