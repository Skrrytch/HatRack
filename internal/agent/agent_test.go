package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
	"hatrack/internal/server"
	"hatrack/internal/store"
)

// fakeLook is a module with a look: it records what it is asked to do.
type fakeLook struct {
	mu    sync.Mutex
	value string // current "wallpaper"
	log   []string
}

func (f *fakeLook) ID() string                  { return "look" }
func (f *fakeLook) RunsIn() modules.RunsIn      { return modules.RunsInAgent }
func (f *fakeLook) ConfigSchema() []byte        { return nil }
func (f *fakeLook) Backends() []modules.Backend { return nil }
func (f *fakeLook) Enter(_ context.Context, sw modules.Switch, cfg json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var c struct{ Value string }
	json.Unmarshal(cfg, &c)
	f.value = c.Value
	f.log = append(f.log, "enter:"+sw.Hat)
	return nil
}
func (f *fakeLook) Leave(_ context.Context, sw modules.Switch, _ json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "leave:"+sw.Hat)
	return nil
}
func (f *fakeLook) Snapshot(context.Context) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(map[string]string{"value": f.value})
}
func (f *fakeLook) Restore(_ context.Context, snap json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var c struct{ Value string }
	json.Unmarshal(snap, &c)
	f.value = c.Value
	f.log = append(f.log, "restore")
	return nil
}
func (f *fakeLook) snapshotLog() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, strings.Join(f.log, ",")
}

const testConfig = `version: 1
hats:
  venom:
    name: VENOM
    resume_activity: last
    activities: [development, review]
    modules:
      look: {value: purple}
  tag:
    name: TAG
    activities: [support]
    modules:
      look: {value: green}
  private:
    name: Private
    activities: []
`

type rig struct {
	t      *testing.T
	st     *store.SQLite
	srv    *httptest.Server
	handle http.Handler
	down   atomic.Bool
	token  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st}
	r.handle = server.New(st, modules.Builtin(nil)).Handler()
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		r.handle.ServeHTTP(w, req)
	}))
	_, r.token, err = st.AddDevice(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.srv.Close(); st.Close() })
	return r
}

func (r *rig) agent(name string) (*Agent, *fakeLook) {
	r.t.Helper()
	db, err := OpenDB(filepath.Join(r.t.TempDir(), name+".db"))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { db.Close() })
	look := &fakeLook{value: "neutral"}
	reg := &modules.Registry{}
	reg.Register(look)
	a := New(Config{Server: r.srv.URL, Device: name, Token: r.token}, db, reg)
	return a, look
}

func (r *rig) start(a *Agent) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go a.Run(ctx)
	r.t.Cleanup(cancel)
	return cancel
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func (r *rig) importConfig() {
	r.t.Helper()
	cfg, err := core.ParseConfig([]byte(testConfig))
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.PutConfig(context.Background(), cfg); err != nil {
		r.t.Fatal(err)
	}
}

func call(t *testing.T, a *Agent, req Request) Response {
	t.Helper()
	return a.Handle(context.Background(), req)
}

func statusOf(t *testing.T, r Response) StatusData {
	t.Helper()
	var d StatusData
	if err := json.Unmarshal(r.Data, &d); err != nil {
		t.Fatalf("status data: %v (%s)", err, r.Data)
	}
	return d
}

func TestSwitchFlowWithServer(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, look := r.agent("laptop")
	r.start(a)
	eventually(t, "connection", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.connected && len(a.config.Hats) == 3 })

	resp := call(t, a, Request{Cmd: "on", Hat: "ve", HasActivity: true, Activity: "rev"})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if d := statusOf(t, resp); d.State.Hat != "venom" || d.State.Activity != "review" || d.HatName != "VENOM" {
		t.Fatalf("status: %+v", d)
	}
	if v, log := look.snapshotLog(); v != "purple" || log != "enter:venom" {
		t.Fatalf("look: %s %s", v, log)
	}
	// the event reaches the server
	eventually(t, "event on server", func() bool { st, _ := r.st.CurrentState(context.Background()); return st.Hat == "venom" })
	eventually(t, "empty queue", func() bool { return a.db.QueueLen() == 0 })

	// hat -> hat runs leave then enter; activity change leaves the look alone
	call(t, a, Request{Cmd: "do", Activity: "dev"})
	call(t, a, Request{Cmd: "on", Hat: "tag"})
	if v, log := look.snapshotLog(); v != "green" || log != "enter:venom,leave:venom,enter:tag" {
		t.Fatalf("look: %s %s", v, log)
	}
	// pause keeps the look, resume does not rerun hooks
	call(t, a, Request{Cmd: "off"})
	call(t, a, Request{Cmd: "on"})
	if _, log := look.snapshotLog(); log != "enter:venom,leave:venom,enter:tag" {
		t.Fatalf("pause/resume changed the look: %s", log)
	}
	// off --neutral gives the neutral look back, resume re-enters
	call(t, a, Request{Cmd: "off", Neutral: true})
	if v, _ := look.snapshotLog(); v != "neutral" {
		t.Fatalf("neutral look not restored: %s", v)
	}
	call(t, a, Request{Cmd: "on"})
	if v, _ := look.snapshotLog(); v != "green" {
		t.Fatalf("resume after neutral: %s", v)
	}
}

func TestErrorsWriteNoEvent(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, _ := r.agent("laptop")
	r.start(a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	for name, req := range map[string]Request{
		"do without hat":  {Cmd: "do", Activity: "x"},
		"off without hat": {Cmd: "off"},
		"resume nothing":  {Cmd: "on"},
		"unknown hat":     {Cmd: "on", Hat: "zzz"},
		"unknown act":     {Cmd: "on", Hat: "venom", HasActivity: true, Activity: "zzz"},
		"future":          {Cmd: "on", Hat: "venom", At: time.Now().Add(time.Hour).Format(time.RFC3339)},
	} {
		if resp := call(t, a, req); resp.OK || resp.Error == "" {
			t.Errorf("%s: expected an error, got %+v", name, resp)
		}
	}
	if a.db.QueueLen() != 0 {
		t.Fatalf("errors queued %d events", a.db.QueueLen())
	}
}

func TestResumeLastAndAsk(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, _ := r.agent("laptop")
	r.start(a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	call(t, a, Request{Cmd: "on", Hat: "venom", HasActivity: true, Activity: "review"})
	call(t, a, Request{Cmd: "on", Hat: "tag"})
	d := statusOf(t, call(t, a, Request{Cmd: "on", Hat: "venom"}))
	if d.State.Activity != "review" {
		t.Fatalf("resume_activity: last should bring back review: %+v", d.State)
	}
	var info InfoData
	json.Unmarshal(call(t, a, Request{Cmd: "info", Hat: "ven"}).Data, &info)
	if info.Hat != "venom" || info.Mode != "last" || info.Last != "review" {
		t.Fatalf("info: %+v", info)
	}
}

func TestOfflineQueueAndReplay(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, look := r.agent("laptop")
	r.start(a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.connected && len(a.config.Hats) == 3 })

	r.down.Store(true)
	// the switch still happens on the desktop
	resp := call(t, a, Request{Cmd: "on", Hat: "tag", At: time.Now().Add(-10 * time.Minute).Format(time.RFC3339)})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if v, _ := look.snapshotLog(); v != "green" {
		t.Fatalf("offline switch did not apply: %s", v)
	}
	if a.db.QueueLen() != 1 {
		t.Fatalf("queue: %d", a.db.QueueLen())
	}
	if st, _ := r.st.CurrentState(context.Background()); st.Mode != core.ModeNone {
		t.Fatal("server saw the event while down")
	}
	// server comes back; the kick or the next retry delivers the event
	r.down.Store(false)
	a.kickFlush()
	eventually(t, "replay", func() bool { st, _ := r.st.CurrentState(context.Background()); return st.Hat == "tag" })
	eventually(t, "empty queue", func() bool { return a.db.QueueLen() == 0 })
}

func TestOtherDeviceSwitchIsApplied(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, look := r.agent("laptop")
	r.start(a)
	eventually(t, "stream", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.connected && len(a.config.Hats) == 3 })

	// a second device (here: a direct post) switches
	ev := core.NewEvent("pc", core.EvSwitch, time.Now())
	ev.Hat = "venom"
	if _, err := NewClient(r.srv.URL, r.token).PostEvents(context.Background(), []core.Event{ev}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "push applied", func() bool { v, _ := look.snapshotLog(); return v == "purple" })
	d := statusOf(t, call(t, a, Request{Cmd: "status"}))
	if d.State.Hat != "venom" {
		t.Fatalf("status after push: %+v", d.State)
	}
}

func TestBackdatedSwitchKeepsLook(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, look := r.agent("laptop")
	r.start(a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	call(t, a, Request{Cmd: "on", Hat: "venom"})
	// a forgotten switch of an hour ago is logged, the look stays
	resp := call(t, a, Request{Cmd: "on", Hat: "tag", At: time.Now().Add(-time.Hour).Format(time.RFC3339)})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	if v, _ := look.snapshotLog(); v != "purple" {
		t.Fatalf("backdated switch changed the look: %s", v)
	}
	if d := statusOf(t, resp); d.State.Hat != "venom" {
		t.Fatalf("state: %+v", d.State)
	}
	eventually(t, "both events on server", func() bool {
		ev, _ := r.st.EventsBetween(context.Background(), time.Time{}, time.Time{})
		return len(ev) == 2
	})
}

func TestSocketRoundTrip(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, _ := r.agent("laptop")
	r.start(a)
	sock := filepath.Join(t.TempDir(), "s", "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Serve(ctx, sock)
	eventually(t, "socket", func() bool { _, err := Call(sock, Request{Cmd: "ping"}); return err == nil })
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	resp, err := Call(sock, Request{Cmd: "on", Hat: "pri"})
	if err != nil || !resp.OK {
		t.Fatalf("%v %+v", err, resp)
	}
	if _, err := Call(filepath.Join(t.TempDir(), "none.sock"), Request{Cmd: "ping"}); err != ErrNotRunning {
		t.Fatalf("no agent: %v", err)
	}
	if err := a.Serve(ctx, sock); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second agent: %v", err)
	}
}

func TestConfigImportThroughAgent(t *testing.T) {
	r := newRig(t)
	a, _ := r.agent("laptop")
	r.start(a)
	resp := call(t, a, Request{Cmd: "config_import", Data: testConfig, DryRun: true})
	if !resp.OK || !strings.Contains(string(resp.Data), "hat added: venom") {
		t.Fatalf("dry run: %+v", resp)
	}
	if cfg, _, _ := r.st.GetConfig(context.Background()); len(cfg.Hats) != 0 {
		t.Fatal("dry run wrote the config")
	}
	resp = call(t, a, Request{Cmd: "config_import", Data: testConfig})
	if !resp.OK {
		t.Fatal(resp.Error)
	}
	exp := call(t, a, Request{Cmd: "config_export"})
	var yamlText string
	json.Unmarshal(exp.Data, &yamlText)
	if !exp.OK || !strings.Contains(yamlText, "venom:") {
		t.Fatalf("export: %+v", exp)
	}
	// offline export comes from the cache
	r.down.Store(true)
	exp = call(t, a, Request{Cmd: "config_export"})
	if !exp.OK {
		t.Fatalf("offline export: %+v", exp)
	}
	if resp = call(t, a, Request{Cmd: "log", From: time.Now().Add(-time.Hour).Format(time.RFC3339), To: time.Now().Format(time.RFC3339)}); resp.OK {
		t.Fatal("log without a server should fail")
	}
}

func TestHatsAndWatch(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, _ := r.agent("laptop")
	r.start(a)
	sock := filepath.Join(t.TempDir(), "w.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Serve(ctx, sock)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	eventually(t, "socket", func() bool { _, err := Call(sock, Request{Cmd: "ping"}); return err == nil })

	resp, err := Call(sock, Request{Cmd: "hats"})
	if err != nil || !resp.OK {
		t.Fatalf("hats: %v %+v", err, resp)
	}
	var snap Snapshot
	json.Unmarshal(resp.Data, &snap)
	if len(snap.Hats) != 3 || snap.Hats[0].Key != "venom" || snap.Hats[0].Resume != "last" ||
		len(snap.Hats[0].Activities) != 2 || snap.Hats[2].Activities == nil || snap.Server != r.srv.URL {
		t.Fatalf("snapshot: %+v", snap)
	}

	// watch: the first snapshot comes at once, the next after a change
	snaps := make(chan Snapshot, 10)
	go Watch(ctx, sock, func(s Snapshot) { snaps <- s })
	next := func() Snapshot {
		select {
		case s := <-snaps:
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("no snapshot")
			return Snapshot{}
		}
	}
	if first := next(); first.Status.State.Mode != core.ModeNone {
		t.Fatalf("first snapshot: %+v", first.Status.State)
	}
	call(t, a, Request{Cmd: "on", Hat: "tag"})
	for i := 0; i < 5; i++ {
		if s := next(); s.Status.State.Hat == "tag" && s.Status.HatName == "TAG" {
			return
		}
	}
	t.Fatal("no snapshot with the new hat")
}

// A state the server sent before our own events arrived (for example the
// one from connecting) must not undo them when it is handled late.
func TestStaleStateIgnored(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, _ := r.agent("laptop")
	r.start(a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	stale, err := NewClient(r.srv.URL, r.token).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	call(t, a, Request{Cmd: "on", Hat: "venom", HasActivity: true, Activity: "review"})
	eventually(t, "sent", func() bool { return a.db.QueueLen() == 0 })
	eventually(t, "rev", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.minRev > stale.EventRev })
	a.adoptState(context.Background(), stale)
	a.mu.Lock()
	st, last := a.state, a.last["venom"]
	a.mu.Unlock()
	if st.Hat != "venom" || st.Activity != "review" || last != "review" {
		t.Fatalf("stale state was adopted: %+v last=%q", st, last)
	}
}

func TestDetailOverSocket(t *testing.T) {
	r := newRig(t)
	r.importConfig()
	a, _ := r.agent("laptop")
	r.start(a)
	eventually(t, "config", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.config.Hats) == 3 })
	call(t, a, Request{Cmd: "on", Hat: "venom", HasActivity: true, Activity: "review"})
	d := statusOf(t, call(t, a, Request{Cmd: "detail", Detail: "https://jira.example.org/browse/TAG-1366"}))
	if d.State.Detail == "" || !strings.HasPrefix(StatusLine(d, time.Now()), "VENOM · review · TAG-1366 · ") {
		t.Fatalf("detail: %+v %q", d.State, StatusLine(d, time.Now()))
	}
	d = statusOf(t, call(t, a, Request{Cmd: "do", Activity: "development", Detail: "Sprint"}))
	if d.State.Activity != "development" || d.State.Detail != "Sprint" {
		t.Fatalf("do with detail: %+v", d.State)
	}
	if resp := call(t, a, Request{Cmd: "detail", Detail: "x"}); !resp.OK {
		t.Fatalf("detail: %+v", resp)
	}
	// the server learns the ticket link and tells the agent the recent details
	eventually(t, "recent details", func() bool {
		var snap Snapshot
		json.Unmarshal(call(t, a, Request{Cmd: "hats"}).Data, &snap)
		for _, h := range snap.Hats {
			if h.Key == "venom" && len(h.Recent["review"]) > 0 && h.Recent["review"][0] == "TAG-1366" {
				return true
			}
		}
		return false
	})
}
