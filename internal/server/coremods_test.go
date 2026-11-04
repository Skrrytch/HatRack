package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
	"hatrack/internal/store"
)

// fakePublisher is a core module that records what the server hands it.
type fakePublisher struct {
	modules.MQTTModule
	mu     sync.Mutex
	cfgs   []string
	states []modules.MQTTState
	closed bool
}

func (f *fakePublisher) Configure(raw json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfgs = append(f.cfgs, string(raw))
	return nil
}

func (f *fakePublisher) Publish(st modules.MQTTState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, st)
}

func (f *fakePublisher) Close() { f.closed = true }

func (f *fakePublisher) lastState() modules.MQTTState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) == 0 {
		return modules.MQTTState{}
	}
	return f.states[len(f.states)-1]
}

func TestCoreModules(t *testing.T) {
	t.Setenv("HATRACK_TEST_MQTT_PW", "geheim")
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := &fakePublisher{}
	reg := modules.Builtin(nil)
	reg.Register(fake)
	srv := New(st, reg)
	srv.Loc = time.UTC
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	_, token, _ := st.AddDevice(context.Background(), "laptop")
	e := &env{t: t, ts: ts, st: st, token: token, now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	srv.Start(context.Background())
	if fake.lastState().Mode != "none" {
		t.Fatalf("start publishes the state: %+v", fake.states)
	}

	// settings come from the defaults, with ${env:…} expanded
	e.do("PUT", "/api/config", `version: 1
modules:
  mqtt:
    broker: mqtt://broker
    password: ${env:HATRACK_TEST_MQTT_PW}
    discovery: true
hats:
  tickets:
    name: Tickets
    color: "#8c4ea3"
    activities: [Testen]
`)
	if c := fake.cfgs[len(fake.cfgs)-1]; !strings.Contains(c, `"password":"geheim"`) || !strings.Contains(c, `"broker":"mqtt://broker"`) {
		t.Fatalf("configure: %s", c)
	}

	// every new state is published, with the hat's name and colour, no detail
	ev := e.ev(-5, core.EvSwitch, "tickets", "Testen")
	ev.Detail = "MESH-2129"
	b, _ := json.Marshal([]core.Event{ev})
	e.do("POST", "/api/events", string(b))
	got := fake.lastState()
	if got.Mode != "wearing" || !got.Working || got.HatName != "Tickets" || got.Color != "#8c4ea3" || got.Activity != "Testen" ||
		got.Since != "2026-11-04T11:55:00Z" {
		t.Fatalf("state: %+v", got)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "MESH") {
		t.Fatal("no detail")
	}
	b, _ = json.Marshal([]core.Event{e.ev(-1, core.EvOff, "tickets", "")})
	e.do("POST", "/api/events", string(b))
	if got := fake.lastState(); got.Mode != "paused" || got.Working || got.HatName != "Tickets" {
		t.Fatalf("paused: %+v", got)
	}

	// mqtt is set in the defaults only: not on the hat page
	withPassword(t, e, "pw")
	br := newBrowser(t, e)
	br.login("pw")
	if _, out := br.do("GET", "/setup/hat?key=tickets", nil); strings.Contains(out, `name="m/mqtt/broker"`) || !strings.Contains(out, "Testen") {
		t.Fatal("no mqtt form on the hat page")
	}
	_, out := br.do("GET", "/setup/defaults", nil)
	var at []int
	for _, k := range []string{"enabled", "broker", "username", "password", "topic", "discovery", "discovery_prefix"} {
		at = append(at, strings.Index(out, `name="m/mqtt/`+k+`"`))
	}
	for i := 1; i < len(at); i++ {
		if at[i-1] < 0 || at[i] <= at[i-1] {
			t.Fatalf("mqtt fields in their order: %v", at)
		}
	}
	srv.Close()
	if !fake.closed {
		t.Fatal("close")
	}
}
