package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"hatrack/internal/core"
)

// presenceEnv adds a second device "desktop" that has said hello.
func presenceEnv(t *testing.T, hello string) (*env, string) {
	t.Helper()
	e := setup(t)
	e.now = time.Now() // devices are "seen" by the real clock
	_, tok, err := e.st.AddDevice(context.Background(), "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if hello != "" {
		saved := e.token
		e.token = tok
		e.do("POST", "/api/devices/hello", hello)
		e.token = saved
	}
	return e, tok
}

func (e *env) presence() map[string]PresenceDevice {
	e.t.Helper()
	r, out := e.do("GET", "/api/presence", "")
	if r.StatusCode != 200 {
		e.t.Fatalf("presence: %d %s", r.StatusCode, out)
	}
	var p PresenceResponse
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		e.t.Fatal(err)
	}
	m := map[string]PresenceDevice{}
	for _, d := range p.Devices {
		m[d.Device] = d
	}
	return m
}

func (e *env) idleSpan(device string, from, to time.Duration, kind string) {
	e.t.Helper()
	o := core.Observation{ID: core.NewID(), Sensor: "idle", Start: e.now.Add(from), End: e.now.Add(to), Attrs: core.Attrs{"kind": kind}}
	if _, _, err := e.st.PutObservations(context.Background(), device, []core.Observation{o}); err != nil {
		e.t.Fatal(err)
	}
}

func TestPresenceNeedsToken(t *testing.T) {
	e, _ := presenceEnv(t, "")
	e.token = ""
	if r, _ := e.do("GET", "/api/presence", ""); r.StatusCode != 401 {
		t.Fatalf("without token: %d", r.StatusCode)
	}
}

func TestPresenceLeavesOutCaller(t *testing.T) {
	e, _ := presenceEnv(t, "")
	p := e.presence()
	if _, ok := p["laptop"]; ok || len(p) != 1 {
		t.Fatalf("devices: %+v", p)
	}
}

func TestPresenceCombinations(t *testing.T) {
	// never said hello: offline, unknown
	e, _ := presenceEnv(t, "")
	if d := e.presence()["desktop"]; d.Online || d.Known || d.Away {
		t.Fatalf("silent device: %+v", d)
	}

	// online, reports the idle sensor, no span: known, not away
	e, _ = presenceEnv(t, sensorCaps)
	if d := e.presence()["desktop"]; !d.Online || !d.Known || d.Away {
		t.Fatalf("active device: %+v", d)
	}

	// span that ends at the last checkpoint: away since its start
	e.idleSpan("desktop", -30*time.Minute, -time.Minute, "locked")
	d := e.presence()["desktop"]
	if !d.Away || !near(d.AwaySince, e.now.Add(-30*time.Minute)) {
		t.Fatalf("away device: %+v", d)
	}

	// idle sensor switched off for it: unknown, so never away
	cfg, _ := core.ParseConfig([]byte("version: 1\nobserve:\n  devices:\n    desktop: {idle: false}\n"))
	e.st.PutConfig(context.Background(), cfg)
	if d := e.presence()["desktop"]; d.Known || d.Away {
		t.Fatalf("idle off: %+v", d)
	}
}

func TestPresenceSpanTooOldIsNotAway(t *testing.T) {
	e, _ := presenceEnv(t, sensorCaps)
	e.idleSpan("desktop", -40*time.Minute, -3*time.Minute, "inactive") // ended 180 s ago
	if d := e.presence()["desktop"]; d.Away {
		t.Fatalf("stale span counts as away: %+v", d)
	}
	e.idleSpan("desktop", -3*time.Minute, -2*time.Minute, "locked") // 120 s ago: still fresh
	d := e.presence()["desktop"]
	if !d.Away || !near(d.AwaySince, e.now.Add(-40*time.Minute)) {
		t.Fatalf("joined spans: %+v", d)
	}
}

func TestPresenceIdleSensorUnavailable(t *testing.T) {
	e, _ := presenceEnv(t, `{"capabilities":{"sensor:idle":{"available":false,"reason":"no backend"}}}`)
	e.idleSpan("desktop", -10*time.Minute, -time.Second, "locked")
	if d := e.presence()["desktop"]; d.Known || d.Away {
		t.Fatalf("unavailable sensor: %+v", d)
	}
}

func TestPresenceOfflineDevice(t *testing.T) {
	e, _ := presenceEnv(t, sensorCaps)
	e.now = e.now.Add(2 * time.Minute) // 120 s since the hello
	if d := e.presence()["desktop"]; d.Online {
		t.Fatalf("device last seen 120 s ago is online: %+v", d)
	}
	e.now = e.now.Add(-time.Minute) // 60 s
	if d := e.presence()["desktop"]; !d.Online {
		t.Fatalf("device last seen 60 s ago is offline: %+v", d)
	}
}

func near(a, b time.Time) bool { return a.Sub(b).Abs() < time.Second }
