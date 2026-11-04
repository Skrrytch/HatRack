package core

import (
	"strings"
	"testing"
)

func TestDetailLifecycle(t *testing.T) {
	cfg, _ := ParseConfig([]byte("version: 1\nhats:\n  venom:\n    activities: [review, meeting]\n"))
	var evs []Event
	st := func() State { return Fold(evs) }
	do := func(min int, f func(State, Input) (*Event, error)) {
		t.Helper()
		e, err := f(st(), Input{Device: "d", Now: at(min)})
		if err != nil {
			t.Fatal(err)
		}
		if e != nil {
			evs = append(evs, *e)
		}
	}
	do(0, func(s State, in Input) (*Event, error) {
		return PlanOn(cfg, s, nil, OnArgs{Hat: "venom", Activity: "review", HasActivity: true}, in)
	})
	// the first detail after 20 minutes names the whole entry
	do(20, func(s State, in Input) (*Event, error) { return PlanDetail(s, "PR 482", in) })
	if got := show(evs, 30); got != "venom/review 0-30+" {
		t.Fatalf("first detail splits nothing: %s", got)
	}
	if iv := Intervals(evs, at(30)); iv[0].Detail != "PR 482" {
		t.Fatalf("detail from the start: %+v", iv[0])
	}
	// a changed detail starts a new entry
	do(30, func(s State, in Input) (*Event, error) { return PlanDetail(s, "PR 479", in) })
	iv := Intervals(evs, at(50))
	if len(iv) != 2 || iv[0].Detail != "PR 482" || iv[1].Detail != "PR 479" || st().Detail != "PR 479" {
		t.Fatalf("changed detail: %+v", iv)
	}
	// same detail again: nothing
	if e, _ := PlanDetail(st(), "PR 479", Input{Now: at(51)}); e != nil {
		t.Fatal("same detail is no change")
	}
	// another activity starts without a detail; pause and resume keep it
	do(50, func(s State, in Input) (*Event, error) { return PlanDo(cfg, s, "meet", "", in) })
	if st().Detail != "" {
		t.Fatal("a new activity has no detail")
	}
	do(55, func(s State, in Input) (*Event, error) { return PlanDo(cfg, s, "meeting", "Sprint", in) })
	if iv := Intervals(evs, at(60)); len(iv) != 3 || iv[2].Detail != "Sprint" {
		t.Fatalf("first detail via do: %+v", iv)
	}
	do(60, func(s State, in Input) (*Event, error) { return PlanOff(s, false, in) })
	do(70, func(s State, in Input) (*Event, error) { return PlanOn(cfg, s, nil, OnArgs{}, in) })
	if s := st(); s.Detail != "Sprint" || s.Activity != "meeting" {
		t.Fatalf("resume keeps the detail: %+v", s)
	}
	// "on venom do review: X" in one go
	do(80, func(s State, in Input) (*Event, error) {
		return PlanOn(cfg, s, nil, OnArgs{Hat: "venom", Activity: "review", HasActivity: true, Detail: "TAG-1"}, in)
	})
	if s := st(); s.Detail != "TAG-1" {
		t.Fatalf("switch with detail: %+v", s)
	}
	if _, err := PlanDetail(NewState(), "x", Input{Now: at(1)}); err != ErrNoHat {
		t.Fatalf("no hat: %v", err)
	}
}

func TestDetailSurvivesEdits(t *testing.T) {
	evs := []Event{ev(0, EvSwitch, "venom", "review"), ev(60, EvSwitch, "tag", ""), ev(90, EvOff, "tag", "")}
	d := NewEvent("d", EvDetail, at(20))
	d.Hat, d.Activity, d.Detail = "venom", "review", "PR 482"
	evs = append(evs, d)
	// moving tag's start earlier must not move the detail into tag
	iv := Intervals(evs, at(120))
	c, err := PlanChange(evs, iv[1].StartID, Edit{Start: at(10), End: at(90), Hat: "tag"}, Input{Device: "web", Now: at(120)})
	if err != nil {
		t.Fatal(err)
	}
	evs2 := append(evs, *c)
	iv = Intervals(evs2, at(120))
	if len(iv) != 2 || iv[0].Detail != "PR 482" || iv[1].Detail != "" || iv[0].End != at(10) {
		t.Fatalf("detail stays with its entry: %+v", iv)
	}
	// the detail is editable in the Day view
	c, err = PlanChange(evs2, iv[0].StartID, Edit{Start: at(0), End: at(10), Hat: "venom", Activity: "review", Detail: "PR 999"}, Input{Device: "web", Now: at(120)})
	if err != nil {
		t.Fatal(err)
	}
	if iv := Intervals(append(evs2, *c), at(120)); iv[0].Detail != "PR 999" {
		t.Fatalf("edit the detail: %+v", iv)
	}
	// unchanged edit of an entry with a detail event: no change
	if _, err := PlanChange(evs, Intervals(evs, at(120))[0].StartID, Edit{Start: at(0), End: at(60), Hat: "venom", Activity: "review", Detail: "PR 482"}, Input{Now: at(120)}); err != ErrNoChange {
		t.Fatalf("no change: %v", err)
	}
}

func TestTicketKeys(t *testing.T) {
	links := LearnTicketLinks("see https://jira.example.org/browse/TAG-1366 and https://other.example/browse/TAG-2", nil)
	if links["TAG"] != "https://jira.example.org/browse/" || len(links) != 1 {
		t.Fatalf("learn, first wins: %v", links)
	}
	LearnTicketLinks("https://x.atlassian.net/browse/VKB2-17 ?q=https://evil/?a=/browse/EVL-1", links)
	if links["VKB2"] != "https://x.atlassian.net/browse/" || links["EVL"] != "" {
		t.Fatalf("cloud link, no link in a query: %v", links)
	}
	if got := ShortDetail("https://jira.example.org/browse/TAG-1366 review"); got != "TAG-1366 review" {
		t.Fatalf("short: %q", got)
	}
	parts := SplitDetail("Fix TAG-1366, not x.TAG-1 or TAG-1-2 or TAG-12.txt; ABC-3 unknown", links)
	var linked []string
	for _, p := range parts {
		if p.Href != "" {
			linked = append(linked, p.Text+"="+p.Href)
		}
	}
	if strings.Join(linked, "|") != "TAG-1366=https://jira.example.org/browse/TAG-1366" {
		t.Fatalf("linked: %v", linked)
	}
	var joined string
	for _, p := range parts {
		joined += p.Text
	}
	if joined != "Fix TAG-1366, not x.TAG-1 or TAG-1-2 or TAG-12.txt; ABC-3 unknown" {
		t.Fatalf("parts lose text: %q", joined)
	}
	if DetailURL("https://jira.example.org/browse/TAG-5", links) != "https://jira.example.org/browse/TAG-5" {
		t.Fatal("detail url from a link")
	}
}

func TestRecentDetails(t *testing.T) {
	var evs []Event
	for i, d := range []string{"A", "B", "a", "C", "D", "E", "F"} {
		e := ev(i*10, EvSwitch, "venom", "review")
		e.Detail = d
		evs = append(evs, e)
	}
	r := RecentDetails(evs, at(0), 5)["venom"]["review"]
	if strings.Join(r, ",") != "F,E,D,C,a" {
		t.Fatalf("recent: %v", r)
	}
}

func TestRecentByHat(t *testing.T) {
	var evs []Event
	for i, x := range [][2]string{{"review", "A"}, {"dev", "B"}, {"dev", "a"}, {"review", "C"}, {"dev", "D"}} {
		e := ev(i*10, EvSwitch, "venom", x[0])
		e.Detail = x[1]
		evs = append(evs, e)
	}
	r := RecentByHat(evs, at(0), 3)["venom"]
	var got []string
	for _, d := range r {
		got = append(got, d.Activity+":"+d.Detail)
	}
	if strings.Join(got, ",") != "dev:D,review:C,dev:a" {
		t.Fatalf("by hat: %v", got)
	}
	// intervals that ended before the window are left out
	if r := RecentByHat(evs, at(35), 9)["venom"]; len(r) != 2 || r[1].Detail != "C" {
		t.Fatalf("window: %+v", r)
	}
}

func TestSecretsInConfig(t *testing.T) {
	t.Setenv("HATRACK_PW", "geheim")
	t.Setenv("HOME_SECRET", "nicht-fuer-dich")
	m := ModuleCfg{"password": "${env:HATRACK_PW}", "user": "${env:HOME_SECRET}",
		"nested": map[string]any{"api_token": "klartext", "list": []any{"a"}}}
	got := ExpandEnvPrefix(m, "HATRACK_").(ModuleCfg)
	if got["password"] != "geheim" || got["user"] != "" {
		t.Fatalf("only HATRACK_ variables: %v", got)
	}
	if ExpandEnv(m).(ModuleCfg)["user"] != "nicht-fuer-dich" {
		t.Fatal("the agent still expands every variable")
	}
	cfg := Config{Version: 1, Modules: map[string]ModuleCfg{"mqtt": m},
		Hats: []Hat{{Key: "a", Activities: []string{}, Modules: map[string]ModuleCfg{"x": {"secret": "s3", "image": "i"}}}}}
	r := cfg.Redacted()
	if r.Modules["mqtt"]["password"] != "${env:HATRACK_PW}" || r.Modules["mqtt"]["nested"].(map[string]any)["api_token"] != "****" ||
		r.Hats[0].Modules["x"]["secret"] != "****" || r.Hats[0].Modules["x"]["image"] != "i" {
		t.Fatalf("redacted: %+v %+v", r.Modules, r.Hats[0].Modules)
	}
	if cfg.Modules["mqtt"]["nested"].(map[string]any)["api_token"] != "klartext" {
		t.Fatal("the original stays untouched")
	}
	w := strings.Join(cfg.Warnings(false), "\n")
	if !strings.Contains(w, "api_token") || !strings.Contains(w, "hats.a.modules.x.secret") {
		t.Fatalf("nested secrets warn: %s", w)
	}
}

func TestImportChangesOnlyModules(t *testing.T) {
	cur, _ := ParseConfig([]byte("version: 1\nhats:\n  k:\n    activities: []\n    modules:\n      wallpaper: {options: zoom}\n"))
	in, _ := ParseConfig([]byte("version: 1\nhats:\n  k:\n    activities: []\n    modules:\n      wallpaper: {image: x.jpg}\n"))
	m, changes := Merge(cur, in, false)
	if len(changes) == 0 || m.Hats[0].Modules["wallpaper"]["image"] != "x.jpg" {
		t.Fatalf("a change of module settings is an import: %v %+v", changes, m.Hats[0].Modules)
	}
	if _, ok := cur.Hats[0].Modules["wallpaper"]["image"]; ok || cur.Hats[0].Modules["wallpaper"]["options"] != "zoom" {
		t.Fatal("the current configuration stays untouched")
	}
}
