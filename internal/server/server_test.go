package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
	"hatrack/internal/store"
)

type env struct {
	t     *testing.T
	ts    *httptest.Server
	st    *store.SQLite
	token string
	now   time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	srv := New(st, modules.Builtin(nil))
	srv.Now = func() time.Time { return e.now }
	e.ts = httptest.NewServer(srv.Handler())
	_, e.token, err = st.AddDevice(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.ts.Close(); st.Close() })
	return e
}

func (e *env) do(method, path, body string, hdr ...string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const cfgYAML = `version: 1
hats:
  venom:
    name: VENOM
    color: "#7a1fa2"
    activities: [development, review]
`

func (e *env) ev(min int, typ core.EventType, hat, act string) core.Event {
	ev := core.NewEvent("laptop", typ, e.now.Add(time.Duration(min)*time.Minute))
	ev.Hat, ev.Activity = hat, act
	return ev
}

func TestAuth(t *testing.T) {
	e := setup(t)
	e.token = ""
	if r, _ := e.do("GET", "/api/state", ""); r.StatusCode != 401 {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	e.token = "wrong"
	if r, _ := e.do("GET", "/api/state", ""); r.StatusCode != 401 {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	if r, _ := e.do("GET", "/healthz", ""); r.StatusCode != 200 {
		t.Fatalf("healthz: %d", r.StatusCode)
	}
}

func TestEventsStateIntervals(t *testing.T) {
	e := setup(t)
	a, b := e.ev(-120, core.EvSwitch, "venom", "review"), e.ev(-60, core.EvOff, "venom", "")
	body, _ := json.Marshal([]core.Event{a, b})
	r, out := e.do("POST", "/api/events", string(body))
	if r.StatusCode != 200 || !strings.Contains(out, `"inserted":2`) {
		t.Fatalf("post: %d %s", r.StatusCode, out)
	}
	// idempotent
	if _, out = e.do("POST", "/api/events", string(body)); !strings.Contains(out, `"inserted":0`) {
		t.Fatalf("repost: %s", out)
	}
	// a single object works too, bad events are rejected
	if r, _ = e.do("POST", "/api/events", `{"id":"x","type":"switch"}`); r.StatusCode != 400 {
		t.Fatalf("invalid event: %d", r.StatusCode)
	}

	_, out = e.do("GET", "/api/state", "")
	var st StateResponse
	json.Unmarshal([]byte(out), &st)
	if st.State.Mode != core.ModePaused || st.LastActivities["venom"] != "review" {
		t.Fatalf("state: %s", out)
	}

	from := e.now.Add(-90 * time.Minute).Format(time.RFC3339)
	_, out = e.do("GET", "/api/intervals?from="+from, "")
	var iv IntervalsResponse
	json.Unmarshal([]byte(out), &iv)
	if len(iv.Intervals) != 1 || iv.Intervals[0].Duration() != 30*time.Minute || iv.Totals[0].Duration != 30*time.Minute {
		t.Fatalf("intervals: %s", out)
	}
	_, out = e.do("GET", "/api/events", "")
	if !strings.Contains(out, a.ID) {
		t.Fatalf("events: %s", out)
	}
	if r, _ = e.do("GET", "/api/events?from=yesterday", ""); r.StatusCode != 400 {
		t.Fatalf("bad from: %d", r.StatusCode)
	}
}

func TestConfigImportExport(t *testing.T) {
	e := setup(t)
	r, out := e.do("PUT", "/api/config?dry_run=1", cfgYAML)
	if r.StatusCode != 200 || !strings.Contains(out, "hat added: venom") {
		t.Fatalf("dry run: %d %s", r.StatusCode, out)
	}
	if _, out = e.do("GET", "/api/config", ""); strings.Contains(out, "venom") {
		t.Fatalf("dry run changed the config: %s", out)
	}
	if r, out = e.do("PUT", "/api/config", cfgYAML); r.StatusCode != 200 || !strings.Contains(out, `"rev":1`) {
		t.Fatalf("import: %d %s", r.StatusCode, out)
	}
	_, out = e.do("GET", "/api/config", "")
	if !strings.Contains(out, "venom:") || !strings.Contains(out, "development") {
		t.Fatalf("export: %s", out)
	}
	// export → import is a no-op
	if _, out2 := e.do("PUT", "/api/config", out); !strings.Contains(out2, `"changes":[]`) {
		t.Fatalf("re-import: %s", out2)
	}
	// invalid input is rejected with a reason and changes nothing
	for name, y := range map[string]string{
		"yaml":   "version: [",
		"schema": "version: 1\nhats:\n  a:\n    modules:\n      wallpaper: {}\n",
	} {
		if r, out = e.do("PUT", "/api/config", y); r.StatusCode != 400 || !strings.Contains(out, "error") {
			t.Errorf("%s: %d %s", name, r.StatusCode, out)
		}
	}
}

func TestStreamPushesState(t *testing.T) {
	e := setup(t)
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	next := func() (event, data string) {
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "event: "):
				event = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				return event, strings.TrimPrefix(l, "data: ")
			}
		}
		t.Fatal("stream ended")
		return
	}
	if ev, data := next(); ev != "state" || !strings.Contains(data, `"mode":"none"`) {
		t.Fatalf("first message: %s %s", ev, data)
	}
	body, _ := json.Marshal([]core.Event{e.ev(0, core.EvSwitch, "venom", "")})
	e.do("POST", "/api/events", string(body))
	if ev, data := next(); ev != "state" || !strings.Contains(data, `"hat":"venom"`) {
		t.Fatalf("push: %s %s", ev, data)
	}
	e.do("PUT", "/api/config", cfgYAML)
	if ev, _ := next(); ev != "config" {
		t.Fatalf("config push: %s", ev)
	}
}

func TestNowPage(t *testing.T) {
	e := setup(t)
	e.do("PUT", "/api/config", cfgYAML)
	body, _ := json.Marshal([]core.Event{e.ev(-83, core.EvSwitch, "venom", "review")})
	e.do("POST", "/api/events", string(body))

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, cookie, lang string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: tokenCookie, Value: cookie})
		}
		req.Header.Set("Accept-Language", lang)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	if r, out := get("/", "", "de"); r.StatusCode != 401 || !strings.Contains(out, "Nicht angemeldet") {
		t.Fatalf("anonymous: %d %s", r.StatusCode, out)
	}
	if r, _ := get("/?token="+e.token, "", "en"); r.StatusCode != 303 || len(r.Cookies()) == 0 {
		t.Fatalf("token login: %d", r.StatusCode)
	}
	r, out := get("/", e.token, "en-US,en;q=0.9")
	if r.StatusCode != 200 || !strings.Contains(out, "VENOM") || !strings.Contains(out, "review") || !strings.Contains(out, "1h 23m") {
		t.Fatalf("now: %d %s", r.StatusCode, out)
	}
	if _, out = get("/", e.token, "de-DE"); !strings.Contains(out, "seit") {
		t.Fatalf("german page: %s", out)
	}
}
