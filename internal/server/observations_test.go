package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"hatrack/internal/core"
)

func (e *env) obs(sensor string, endMin int, attrs core.Attrs) core.Observation {
	end := e.now.Add(time.Duration(endMin) * time.Minute)
	return core.Observation{ID: core.NewID(), Sensor: sensor, Start: end.Add(-5 * time.Minute), End: end, Attrs: attrs}
}

func (e *env) postObs(obs ...core.Observation) (int, map[string]int) {
	e.t.Helper()
	b, _ := json.Marshal(obs)
	r, out := e.do("POST", "/api/observations", string(b))
	var got map[string]int
	json.Unmarshal([]byte(out), &got)
	return r.StatusCode, got
}

func (e *env) stored() []core.Observation {
	e.t.Helper()
	got, err := e.st.ObservationsBetween(context.Background(), e.now.Add(-100*24*time.Hour), e.now.Add(time.Hour), "", "")
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func (e *env) eventCount() int {
	e.t.Helper()
	evs, err := e.st.EventsBetween(context.Background(), e.now.Add(-100*24*time.Hour), e.now.Add(24*time.Hour))
	if err != nil {
		e.t.Fatal(err)
	}
	return len(evs)
}

func TestObservationsValidation(t *testing.T) {
	e := setup(t)
	rev, _ := e.st.EventRev(context.Background())
	n := e.eventCount()

	bad := e.obs("idle", -1, nil)
	bad.Sensor = "Idle"
	if code, _ := e.postObs(e.obs("idle", -2, nil), bad); code != 400 {
		t.Fatalf("invalid entry: %d", code)
	}
	if len(e.stored()) != 0 {
		t.Fatal("batch with an invalid entry was stored")
	}

	// the device in the body is ignored
	o := e.obs("idle", -1, core.Attrs{"state": "idle"})
	o.Device = "someone-else"
	if code, got := e.postObs(o); code != 200 || got["stored"] != 1 {
		t.Fatalf("post: %d %v", code, got)
	}
	if s := e.stored(); len(s) != 1 || s[0].Device != "laptop" {
		t.Fatalf("stored: %+v", s)
	}

	many := make([]core.Observation, core.MaxObservationBatch+1)
	for i := range many {
		many[i] = e.obs("idle", -1, nil)
	}
	if code, _ := e.postObs(many...); code != 400 {
		t.Fatalf("501 entries: %d", code)
	}
	if code, _ := e.postObs(many[:core.MaxObservationBatch]...); code != 200 {
		t.Fatalf("500 entries: %d", code)
	}

	if r, _ := e.do("POST", "/api/observations", "{"); r.StatusCode != 400 {
		t.Fatalf("bad json: %d", r.StatusCode)
	}

	e.token = ""
	if code, _ := e.postObs(e.obs("idle", -1, nil)); code != 401 {
		t.Fatalf("no token: %d", code)
	}

	if rev2, _ := e.st.EventRev(context.Background()); rev2 != rev || e.eventCount() != n {
		t.Fatal("observations changed the event log")
	}
}

func TestObservationsConfigRetentionUpsert(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	n := e.eventCount()

	// window is off by default and skipped, idle needs no config
	code, got := e.postObs(e.obs("window", -1, core.Attrs{"app": "x"}), e.obs("idle", -1, nil))
	if code != 200 || got["stored"] != 1 || got["skipped"] != 1 {
		t.Fatalf("default config: %d %v", code, got)
	}
	if s := e.stored(); len(s) != 1 || s[0].Sensor != "idle" {
		t.Fatalf("stored: %+v", s)
	}

	cfg, _, _ := e.st.GetConfig(ctx)
	cfg.Observe.Devices = map[string]map[string]bool{"laptop": {"window": true}}
	if _, err := e.st.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if code, got := e.postObs(e.obs("window", -1, core.Attrs{"app": "x"})); code != 200 || got["stored"] != 1 || got["skipped"] != 0 {
		t.Fatalf("enabled: %d %v", code, got)
	}

	// older than retention (28 days by default)
	old := e.obs("idle", -29*24*60, nil)
	if code, got := e.postObs(old); code != 200 || got["stored"] != 0 || got["skipped"] != 1 {
		t.Fatalf("retention: %d %v", code, got)
	}

	// upsert: same id, later end
	o := e.obs("idle", -30, nil)
	e.postObs(o)
	o.End = o.End.Add(3 * time.Minute)
	if code, got := e.postObs(o); code != 200 || got["stored"] != 1 {
		t.Fatalf("upsert: %d %v", code, got)
	}
	found := false
	for _, s := range e.stored() {
		if s.ID == o.ID {
			found = true
			if !s.End.Equal(o.End) {
				t.Fatalf("end did not grow: %v", s.End)
			}
		}
	}
	if !found {
		t.Fatal("upserted span missing")
	}
	if e.eventCount() != n {
		t.Fatal("observations changed the event log")
	}
}
