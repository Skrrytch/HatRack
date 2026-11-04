package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hatrack/internal/core"
)

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func mcpSession(t *testing.T, e *env, token string) (*mcp.ClientSession, error) {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: e.ts.URL + "/mcp", MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}, DisableStandaloneSSE: true}
	cs, err := c.Connect(context.Background(), tr, nil)
	if err == nil {
		t.Cleanup(func() { cs.Close() })
	}
	return cs, err
}

// call runs a tool and decodes its structured result into out; it returns
// the error text if the tool failed.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		return res.Content[0].(*mcp.TextContent).Text
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return ""
}

func TestMCPAuth(t *testing.T) {
	e := setup(t)
	if _, err := mcpSession(t, e, e.token); err == nil {
		t.Fatal("a device token must not open MCP")
	}
	if _, err := mcpSession(t, e, "hrm_nonsense"); err == nil {
		t.Fatal("an unknown token must not open MCP")
	}
	tok, token, err := e.st.AddMCPToken(context.Background(), "claude", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcpSession(t, e, token)
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tl.Name)
		}
	}
	if strings.Join(names, ",") != "get_config,get_current,list_entries,list_hats,query_time" {
		t.Fatalf("tools: %v", names)
	}
	// the MCP token opens nothing else
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
		t.Fatalf("MCP token on the API: %d", r.StatusCode)
	}
	if list, _ := e.st.MCPTokens(context.Background()); list[0].LastUsed.IsZero() {
		t.Fatal("last use is recorded")
	}
	e.st.DeleteMCPToken(context.Background(), tok.ID)
	if _, err := mcpSession(t, e, token); err == nil {
		t.Fatal("a revoked token must not open MCP")
	}
}

func TestMCPTools(t *testing.T) {
	e := setup(t)
	seed(t, e) // Wed 2026-11-04: venom/development 9-10, venom/review 10-11, tag 11-11:30
	_, token, _ := e.st.AddMCPToken(context.Background(), "claude", []string{"read"})
	cs, err := mcpSession(t, e, token)
	if err != nil {
		t.Fatal(err)
	}
	var q queryTimeOut
	if msg := call(t, cs, "query_time", map[string]any{"period": "today"}, &q); msg != "" {
		t.Fatal(msg)
	}
	if q.TotalMinutes != 150 || len(q.Groups) != 2 || q.Groups[0].Hat != "venom" || q.Groups[0].Minutes != 120 || q.Groups[1].Minutes != 30 {
		t.Fatalf("by hat: %+v", q)
	}
	call(t, cs, "query_time", map[string]any{"period": "week", "group_by": "activity", "hat": "ven"}, &q)
	if len(q.Groups) != 2 || q.Groups[0].Activity == "" || q.Groups[0].Minutes != 60 || q.TotalMinutes != 120 {
		t.Fatalf("by activity for one hat: %+v", q)
	}
	call(t, cs, "query_time", map[string]any{"from": "2026-11-02", "to": "2026-11-08", "group_by": "day"}, &q)
	if len(q.Groups) != 1 || q.Groups[0].Day != "2026-11-04" || q.Groups[0].Minutes != 150 {
		t.Fatalf("by day: %+v", q)
	}
	call(t, cs, "query_time", map[string]any{"period": "last week"}, &q)
	if q.TotalMinutes != 0 || !strings.HasPrefix(q.From, "2026-10-26") {
		t.Fatalf("last week: %+v", q)
	}
	// errors are sentences the assistant can pass on
	for args, want := range map[string]map[string]any{
		"unknown period": {"period": "fortnight"},
		"group_by":       {"group_by": "year"},
		"not found":      {"hat": "zzz"},
		"either period":  {"period": "today", "from": "2026-11-01"},
	} {
		if msg := call(t, cs, "query_time", want, &q); !strings.Contains(msg, args) {
			t.Errorf("%v: %q", want, msg)
		}
	}

	// a correction made in the Day view counts
	b := newBrowser(t, e)
	withPassword(t, e, "pw")
	b.login("pw")
	ev, _ := e.st.Effective(context.Background(), e.now)
	var tagID string
	for _, i := range core.Intervals(ev, e.now) {
		if i.Hat == "tag" {
			tagID = i.StartID
		}
	}
	b.do("POST", "/day/edit", url.Values{"date": {"2026-11-04"}, "op": {"delete"}, "id": {tagID}})
	call(t, cs, "query_time", map[string]any{}, &q)
	if q.TotalMinutes != 120 {
		t.Fatalf("after deleting tag: %+v", q)
	}

	var en entriesOut
	call(t, cs, "list_entries", map[string]any{"period": "today"}, &en)
	if len(en.Entries) != 2 || en.Entries[0].Activity != "development" || en.Entries[1].Start[11:16] != "10:00" {
		t.Fatalf("entries: %+v", en)
	}

	var cur currentOut
	call(t, cs, "get_current", nil, &cur)
	if cur.State != "paused" || cur.Hat != "venom" || cur.TodayTotal != "2h 00m" {
		t.Fatalf("current: %+v", cur)
	}

	var hats hatsOut
	call(t, cs, "list_hats", nil, &hats)
	if len(hats.Hats) != 2 || hats.Hats[0].Name != "VENOM" || len(hats.Hats[0].Activities) != 2 || hats.WorkHours.Start != "08:00" {
		t.Fatalf("hats: %+v", hats)
	}
	e.do("PUT", "/api/config", "version: 1\nmodules:\n  mqtt:\n    broker: mqtt://b\n    password: klartext-pw\nhats:\n  venom:\n    name: VENOM\n    activities: [development, review]\n")
	var cfg configOut
	call(t, cs, "get_config", nil, &cfg)
	if !strings.Contains(cfg.YAML, "venom:") || strings.Contains(cfg.YAML, "klartext-pw") || !strings.Contains(cfg.YAML, `password: "****"`) {
		t.Fatalf("config, passwords masked: %s", cfg.YAML)
	}
}

func TestMCPOutsideWork(t *testing.T) {
	e := setup(t)
	seed(t, e)
	// an early entry 06:30-07:30 on the same day
	evs := []core.Event{e.ev(-330, core.EvSwitch, "tag", ""), e.ev(-270, core.EvOff, "tag", "")}
	b, _ := json.Marshal(evs)
	e.do("POST", "/api/events", string(b))
	_, token, _ := e.st.AddMCPToken(context.Background(), "c", []string{"read"})
	cs, _ := mcpSession(t, e, token)
	var q queryTimeOut
	call(t, cs, "query_time", map[string]any{"group_by": "day"}, &q)
	if q.TotalMinutes != 210 || q.OutsideMinutes != 60 || q.Groups[0].OutsideMinutes != 60 {
		t.Fatalf("outside the working hours: %+v", q)
	}
}

func TestAIAccessPage(t *testing.T) {
	e, b := setupBrowser(t)
	r, out := b.do("POST", "/setup/ai", url.Values{"op": {"add"}, "name": {"Claude Code laptop"}})
	if r.StatusCode != 200 || !strings.Contains(out, "claude mcp add --scope user --transport http hatrack http://") || !strings.Contains(out, "Bearer hrm_") {
		t.Fatalf("create: %d", r.StatusCode)
	}
	list, _ := e.st.MCPTokens(context.Background())
	if len(list) != 1 || list[0].Name != "Claude Code laptop" || list[0].Scopes[0] != "read" {
		t.Fatalf("tokens: %+v", list)
	}
	if _, out := b.do("GET", "/setup/ai", nil); strings.Contains(out, "hrm_") || !strings.Contains(out, "Claude Code laptop") {
		t.Fatal("the list shows names, never tokens")
	}
	b.do("POST", "/setup/ai", url.Values{"op": {"delete"}, "id": {list[0].ID}})
	if list, _ := e.st.MCPTokens(context.Background()); len(list) != 0 {
		t.Fatal("revoke")
	}
	ro := newBrowser(t, e)
	ro.do("GET", "/?token="+e.token, nil)
	if r, _ := ro.do("GET", "/setup/ai", nil); r.StatusCode != 303 {
		t.Fatal("AI access needs the login")
	}
}

func TestMCPShortEntries(t *testing.T) {
	e := setup(t)
	seed(t, e)
	// three entries of 40 seconds each from 11:35: venom, tag, venom
	at := func(sec int, typ core.EventType, hat, act string) core.Event {
		ev := core.NewEvent("laptop", typ, e.now.Add(-25*time.Minute+time.Duration(sec)*time.Second))
		ev.Hat, ev.Activity = hat, act
		return ev
	}
	b, _ := json.Marshal([]core.Event{at(0, core.EvSwitch, "venom", "review"), at(40, core.EvSwitch, "tag", ""),
		at(80, core.EvSwitch, "venom", "review"), at(120, core.EvOff, "venom", "")})
	e.do("POST", "/api/events", string(b))
	_, token, _ := e.st.AddMCPToken(context.Background(), "c", []string{"read"})
	cs, _ := mcpSession(t, e, token)
	for _, group := range []string{"hat", "activity", "day", "week"} {
		var q queryTimeOut
		call(t, cs, "query_time", map[string]any{"group_by": group}, &q)
		sum := 0
		for _, g := range q.Groups {
			sum += g.Minutes
		}
		if q.TotalMinutes != 152 || sum != 152 {
			t.Errorf("%s: total %d, groups add up to %d: %+v", group, q.TotalMinutes, sum, q.Groups)
		}
	}
}

func TestApportion(t *testing.T) {
	s, m := time.Second, time.Minute
	for _, c := range []struct {
		in   []time.Duration
		want []int
	}{
		{nil, []int{}},
		{[]time.Duration{30 * s, 30 * s}, []int{1, 0}},            // 1 minute in all, not 2
		{[]time.Duration{20 * s, 20 * s, 20 * s}, []int{1, 0, 0}}, // 1 minute, not 0
		{[]time.Duration{m + 10*s, 2*m + 50*s}, []int{1, 3}},
		{[]time.Duration{29 * s}, []int{0}},
	} {
		got := core.Apportion(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%v: %v", c.in, got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v: %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestMCPDetails(t *testing.T) {
	e, b := setupBrowser(t)
	b.do("POST", "/action", url.Values{"op": {"on"}, "hat": {"venom"}, "activity": {"review"}})
	e.now = e.now.Add(20 * time.Minute)
	b.do("POST", "/action", url.Values{"op": {"detail"}, "detail": {"https://jira.example.org/browse/TAG-1366"}})
	e.now = e.now.Add(10 * time.Minute)
	b.do("POST", "/action", url.Values{"op": {"detail"}, "detail": {"PR 482"}})
	e.now = e.now.Add(15 * time.Minute)
	_, token, _ := e.st.AddMCPToken(context.Background(), "c", []string{"read"})
	cs, _ := mcpSession(t, e, token)

	var q queryTimeOut
	if msg := call(t, cs, "query_time", map[string]any{"group_by": "detail", "hat": "venom"}, &q); msg != "" {
		t.Fatal(msg)
	}
	got := map[string]int{}
	for _, g := range q.Groups {
		got[g.Activity+"/"+g.Detail] = g.Minutes
		if g.Detail == "TAG-1366" && g.DetailURL != "https://jira.example.org/browse/TAG-1366" {
			t.Errorf("detail url: %+v", g)
		}
	}
	if got["review/TAG-1366"] != 30 || got["review/PR 482"] != 15 || got["development/"] != 60 {
		t.Fatalf("by detail: %v", got)
	}
	var en entriesOut
	call(t, cs, "list_entries", map[string]any{}, &en)
	last := en.Entries[len(en.Entries)-1]
	if last.Detail != "PR 482" || !last.Running {
		t.Fatalf("entries: %+v", last)
	}
	var cur currentOut
	call(t, cs, "get_current", nil, &cur)
	if cur.Detail != "PR 482" {
		t.Fatalf("current: %+v", cur)
	}
}
