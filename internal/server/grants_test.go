package server

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// readyGrants makes the grants appear in Setup for one test; normally a
// ticket that ships the reader sets Ready.
func readyGrants(t *testing.T) {
	t.Helper()
	saved := make([]core.Grant, len(core.Grants))
	copy(saved, core.Grants)
	for i := range core.Grants {
		core.Grants[i].Ready = true
	}
	t.Cleanup(func() { copy(core.Grants, saved) })
}

// withTickets registers the ticket project ACME.
func withTickets(t *testing.T, e *env) {
	t.Helper()
	c := config(t, e)
	c.Tickets = []core.TicketSystem{{Project: "ACME", URL: "https://jira.example.com/browse/"}}
	if _, err := e.st.PutConfig(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestGrantTexts(t *testing.T) {
	for _, g := range core.Grants {
		for _, lang := range i18n.Languages() {
			parts := []string{"title", "lead", "what", "why", "stores", "risk", "not_needed"}
			for _, l := range g.Levels {
				parts = append(parts, "level."+l)
			}
			for _, p := range parts {
				key := "grant." + g.ID + "." + p
				if s := i18n.In(lang, key); s == key || strings.TrimSpace(s) == "" {
					t.Errorf("%s: missing %s", lang, key)
				}
			}
		}
	}
}

func TestGrantsPageWithoutReadyGrants(t *testing.T) {
	_, b := setupBrowser(t)
	_, out := b.do("GET", "/setup/grants", nil)
	if !strings.Contains(out, "No further permissions yet") || strings.Contains(out, `class="card grant"`) {
		t.Errorf("a grant without its reader is shown:\n%s", out)
	}
	if !strings.Contains(out, "No changes to permissions yet") {
		t.Error("history")
	}
	// the setup overview links both pages
	_, out = b.do("GET", "/setup", nil)
	if !strings.Contains(out, `href="/setup/grants"`) || !strings.Contains(out, `href="/setup/detection"`) {
		t.Error("setup overview lacks a link")
	}
}

func TestObservationsWithoutGrant(t *testing.T) {
	e, b := setupBrowser(t)
	withTickets(t, e)
	enableWindow(t, e, b)
	attrs := func(branch string) core.Attrs {
		return core.Attrs{"app": "kitty", core.AttrRepo: "shop", core.AttrBranch: branch, core.AttrBranchSrc: "proc"}
	}
	stored := func() []core.Observation {
		obs, _ := e.st.ObservationsBetween(context.Background(), time.Time{}, time.Time{}, "laptop", "window")
		return obs
	}
	// no grant: the server drops repo and branch, whatever the agent sends
	if code, got := e.postObs(e.obs("window", -10, attrs("feature/ACME-1"))); code != 200 || got["stored"] != 1 || got["stripped"] != 1 {
		t.Fatalf("post: %d %v", code, got)
	}
	if o := stored()[0]; o.Attrs[core.AttrRepo] != "" || o.Attrs[core.AttrBranch] != "" || o.Attrs["app"] != "kitty" {
		t.Fatalf("stored without grant: %v", o.Attrs)
	}
	// known_keys: a branch with a known key stays, another is dropped
	c := config(t, e)
	c.Observe.Grants = map[string]map[string]string{"laptop": {core.GrantWorkdir: core.LevelKnownKeys}}
	e.st.PutConfig(context.Background(), c)
	e.postObs(e.obs("window", -8, attrs("feature/ACME-2-login")), e.obs("window", -6, attrs("spike/try")), e.obs("window", -4, attrs("feature/UTF-8")))
	obs := stored()
	if got := obs[1].Attrs[core.AttrBranch]; got != "feature/ACME-2-login" {
		t.Errorf("known key dropped: %q", got)
	}
	for _, o := range obs[2:] {
		if o.Attrs[core.AttrBranch] != "" || o.Attrs[core.AttrRepo] != "shop" {
			t.Errorf("unknown branch kept or repo lost: %v", o.Attrs)
		}
	}
	// a session file needs its own grant
	s := attrs("feature/ACME-3")
	s[core.AttrBranchSrc] = core.BranchFromSession
	e.postObs(e.obs("window", -2, s))
	if o := stored()[4]; o.Attrs[core.AttrBranch] != "" || o.Attrs[core.AttrRepo] != "" {
		t.Errorf("session branch without ai_sessions: %v", o.Attrs)
	}
}

func TestGrantSave(t *testing.T) {
	readyGrants(t)
	e, b := setupBrowser(t)
	withTickets(t, e)
	enableWindow(t, e, b)
	laptop, desk := deviceID(t, e, "laptop"), addDevice(t, e, "desk")
	devs := laptop + "," + desk
	save := func(grant, level string, extra url.Values, on ...string) (string, string) {
		f := url.Values{"op": {"grant"}, "grant": {grant}, "level": {level}, "devices": {devs}}
		for _, id := range on {
			f.Set("on."+id, "1")
		}
		for k, v := range extra {
			f[k] = v
		}
		r, out := b.do("POST", "/setup/grants", f)
		return r.Header.Get("Location"), out
	}
	_, out := b.do("GET", "/setup/grants", nil)
	for _, want := range []string{`id="workdir"`, "Working directory and branch", "For every permission", "needs “Working directory and branch”", "Active window is off there",
		`<p class="risk"><b>Risk:</b>`, `<details class="more">`, "What does HatRack read, what for, what is stored?"} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if loc, _ := save(core.GrantWorkdir, core.LevelKnownKeys, nil, laptop); !strings.HasPrefix(loc, "/setup/grants?saved=1") {
		t.Fatalf("grant: %s", loc)
	}
	if g := config(t, e).Observe.Grants; g["laptop"][core.GrantWorkdir] != core.LevelKnownKeys || g["desk"] != nil {
		t.Fatalf("stored: %v", g)
	}
	save(core.GrantAISessions, core.LevelOn, nil, laptop)
	// data that the grants allow
	e.postObs(e.obs("window", -10, core.Attrs{"app": "kitty", core.AttrRepo: "shop", core.AttrBranch: "feature/ACME-1", core.AttrBranchSrc: "proc"}))
	_, out = b.do("GET", "/setup/grants", nil)
	if !strings.Contains(out, "given · 1 of 2 computers") || !strings.Contains(out, "last used 10 min ago") {
		t.Errorf("status or last use missing:\n%s", out)
	}
	// switching off asks first, because data is stored; nothing changes yet
	loc, out := save(core.GrantWorkdir, core.LevelKnownKeys, nil)
	if loc != "" || !strings.Contains(out, "Revoke “Working directory and branch” on laptop") || !strings.Contains(out, "(in 1 observations") {
		t.Fatalf("no question: %s\n%s", loc, out)
	}
	if config(t, e).Observe.Grants["laptop"][core.GrantWorkdir] == "" {
		t.Fatal("revoked before the answer")
	}
	// the question shows the submitted state, so confirming it revokes
	if strings.Contains(out, `name="on.`+laptop+`" value="1" id="g-workdir-`+laptop+`" checked`) {
		t.Fatal("the question shows laptop still switched on")
	}
	// keep: the grant goes, the data stays; ai_sessions goes with workdir
	loc, _ = save(core.GrantWorkdir, core.LevelKnownKeys, url.Values{"delete": {"0"}})
	if !strings.HasPrefix(loc, "/setup/grants?saved=1") || config(t, e).Observe.Grants != nil {
		t.Fatalf("keep: %s %v", loc, config(t, e).Observe.Grants)
	}
	obs, _ := e.st.ObservationsBetween(context.Background(), time.Time{}, time.Time{}, "laptop", "window")
	if obs[0].Attrs[core.AttrBranch] == "" {
		t.Fatal("kept data was deleted")
	}
	// grant again with all branches, then revoke with delete
	save(core.GrantWorkdir, core.LevelAll, nil, laptop)
	loc, _ = save(core.GrantWorkdir, core.LevelAll, url.Values{"delete": {"1"}})
	if !strings.Contains(loc, "cleaned=1") {
		t.Fatalf("delete: %s", loc)
	}
	obs, _ = e.st.ObservationsBetween(context.Background(), time.Time{}, time.Time{}, "laptop", "window")
	if a := obs[0].Attrs; a[core.AttrBranch] != "" || a[core.AttrRepo] != "" || a["app"] != "kitty" {
		t.Fatalf("not deleted: %v", a)
	}
	// nothing stored any more: switching off again does not ask
	save(core.GrantWorkdir, core.LevelAll, nil, laptop)
	if loc, _ := save(core.GrantWorkdir, core.LevelAll, nil); !strings.HasPrefix(loc, "/setup/grants?saved=1") {
		t.Fatalf("asked without data: %s", loc)
	}
	// the history names every change, newest first
	log, _ := e.st.GrantLog(context.Background(), time.Time{})
	if len(log) != 8 { // ai_sessions went with workdir once

		t.Fatalf("history: %+v", log)
	}
	if !log[2].Deleted || log[2].Old != core.LevelAll || log[2].New != "" {
		t.Errorf("delete not recorded: %+v", log[2])
	}
	_, out = b.do("GET", "/setup/grants", nil)
	if !strings.Contains(out, "all branches → off") || !strings.Contains(out, ">deleted<") {
		t.Errorf("history not shown:\n%s", out)
	}
}

func TestGrantNarrowAsks(t *testing.T) {
	readyGrants(t)
	e, b := setupBrowser(t)
	withTickets(t, e)
	enableWindow(t, e, b)
	laptop := deviceID(t, e, "laptop")
	post := func(level string, extra url.Values) (string, string) {
		f := url.Values{"op": {"grant"}, "grant": {core.GrantWorkdir}, "level": {level}, "devices": {laptop}, "on." + laptop: {"1"}}
		for k, v := range extra {
			f[k] = v
		}
		r, out := b.do("POST", "/setup/grants", f)
		return r.Header.Get("Location"), out
	}
	post(core.LevelAll, nil)
	e.postObs(e.obs("window", -10, core.Attrs{"app": "kitty", core.AttrRepo: "shop", core.AttrBranch: "spike/try"}),
		e.obs("window", -5, core.Attrs{"app": "kitty", core.AttrRepo: "shop", core.AttrBranch: "feature/ACME-1"}))
	loc, out := post(core.LevelKnownKeys, nil)
	if loc != "" || !strings.Contains(out, "Narrow “Working directory and branch” on laptop") || !strings.Contains(out, "(in 1 observations") {
		t.Fatalf("no question: %s", loc)
	}
	post(core.LevelKnownKeys, url.Values{"delete": {"1"}})
	obs, _ := e.st.ObservationsBetween(context.Background(), time.Time{}, time.Time{}, "laptop", "window")
	if obs[0].Attrs[core.AttrBranch] != "" || obs[1].Attrs[core.AttrBranch] != "feature/ACME-1" {
		t.Errorf("narrowing: %v / %v", obs[0].Attrs, obs[1].Attrs)
	}
	if config(t, e).Observe.Grants["laptop"][core.GrantWorkdir] != core.LevelKnownKeys {
		t.Error("level not stored")
	}
}

func TestGrantRejectsUnknown(t *testing.T) {
	_, b := setupBrowser(t) // no grant is ready
	r, _ := b.do("POST", "/setup/grants", url.Values{"op": {"grant"}, "grant": {core.GrantWorkdir}})
	if !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Error("a grant without its reader was saved")
	}
}
