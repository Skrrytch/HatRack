package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func ev(min int, typ EventType, hat, act string) Event {
	e := NewEvent("dev", typ, at(min))
	e.Hat, e.Activity = hat, act
	return e
}

func TestResolve(t *testing.T) {
	hats := []Named{{"venom", "VENOM"}, {"verwaltung", "Verwaltung"}, {"private", "Private"}, {"tag", "TAG"}}
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"venom", "venom", ""},
		{"VENOM", "venom", ""},
		{"ven", "venom", ""},
		{"pri", "private", ""},
		{"ve", "", "ambiguous"},
		{"x", "", "not found"},
		{"", "", "not found"},
	}
	for _, tc := range tests {
		got, err := Resolve(tc.in, hats)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Resolve(%q) error = %v, want %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.Key != tc.want {
			t.Errorf("Resolve(%q) = %v, %v; want %s", tc.in, got, err, tc.want)
		}
	}
}

func TestResolveExactBeatsPrefix(t *testing.T) {
	got, err := Resolve("tag", []Named{{"tag", "TAG"}, {"tagebuch", "Tagebuch"}})
	if err != nil || got.Key != "tag" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestFoldStates(t *testing.T) {
	off := ev(60, EvOff, "venom", "")
	offN := off
	offN.Neutral = true
	tests := []struct {
		name   string
		events []Event
		mode   Mode
		hat    string
		act    string
		look   string
	}{
		{"empty", nil, ModeNone, "", "", ""},
		{"wearing", []Event{ev(0, EvSwitch, "venom", "review")}, ModeWearing, "venom", "review", "venom"},
		{"activity", []Event{ev(0, EvSwitch, "venom", ""), ev(5, EvActivity, "venom", "meeting")}, ModeWearing, "venom", "meeting", "venom"},
		{"pause keeps look", []Event{ev(0, EvSwitch, "venom", "review"), off}, ModePaused, "venom", "review", "venom"},
		{"pause neutral", []Event{ev(0, EvSwitch, "venom", "review"), offN}, ModePaused, "venom", "review", ""},
		{"off in none", []Event{off}, ModeNone, "", "", ""},
		{"switch hat", []Event{ev(0, EvSwitch, "venom", "a"), ev(5, EvSwitch, "tag", "")}, ModeWearing, "tag", "", "tag"},
		{"out of order", []Event{ev(5, EvSwitch, "tag", ""), ev(0, EvSwitch, "venom", "")}, ModeWearing, "tag", "", "tag"},
		{"module error ignored", []Event{ev(0, EvSwitch, "venom", ""), ev(5, EvModuleError, "", "")}, ModeWearing, "venom", "", "venom"},
	}
	for _, tc := range tests {
		s := Fold(tc.events)
		if s.Mode != tc.mode || s.Hat != tc.hat || s.Activity != tc.act || s.Look() != tc.look {
			t.Errorf("%s: got %+v look=%q", tc.name, s, s.Look())
		}
	}
}

func TestBackdatedEventKeepsState(t *testing.T) {
	s := Fold([]Event{ev(60, EvSwitch, "venom", "")})
	s2 := s.Apply(ev(10, EvSwitch, "tag", ""))
	if s2.Hat != "venom" {
		t.Fatalf("backdated event changed the state: %+v", s2)
	}
}

func TestIntervals(t *testing.T) {
	events := []Event{
		ev(0, EvSwitch, "venom", "review"),
		ev(30, EvActivity, "venom", "meeting"),
		ev(60, EvOff, "venom", ""),
		ev(90, EvSwitch, "tag", ""),
	}
	got := Intervals(events, at(120))
	want := []Interval{
		{"venom", "review", at(0), at(30), false, events[0].ID, events[1].ID},
		{"venom", "meeting", at(30), at(60), false, events[1].ID, events[2].ID},
		{"tag", "", at(90), at(120), true, events[3].ID, ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestIntervalsBackdatedSplits(t *testing.T) {
	events := []Event{
		ev(0, EvSwitch, "venom", ""),
		ev(120, EvSwitch, "tag", ""),
		ev(60, EvSwitch, "private", ""), // forgotten switch entered later
		ev(90, EvSwitch, "venom", ""),
	}
	got := Intervals(events, at(180))
	var hats []string
	for _, i := range got {
		hats = append(hats, i.Hat)
	}
	if strings.Join(hats, ",") != "venom,private,venom,tag" {
		t.Fatalf("got %v", hats)
	}
}

func TestTotalsAndClip(t *testing.T) {
	events := []Event{
		ev(0, EvSwitch, "venom", "review"),
		ev(30, EvActivity, "venom", ""),
		ev(60, EvSwitch, "tag", ""),
		ev(70, EvOff, "tag", ""),
	}
	iv := Intervals(events, at(100))
	tot := Totals(iv)
	if len(tot) != 2 || tot[0].Hat != "venom" || tot[0].Duration != time.Hour || len(tot[0].Activities) != 2 {
		t.Fatalf("totals: %+v", tot)
	}
	clipped := Clip(iv, at(15), at(65))
	if len(clipped) != 3 || clipped[0].Start != at(15) || clipped[2].End != at(65) {
		t.Fatalf("clip: %+v", clipped)
	}
}

func testConfig() Config {
	c := NewConfig()
	c.Defaults.ResumeActivity = ResumeAsk
	c.Hats = HatList{
		{Key: "venom", Name: "VENOM", ResumeActivity: ResumeLast, Activities: []string{"development", "review", "meeting"}},
		{Key: "private", Name: "Private", Activities: []string{}},
	}
	return c
}

func TestPlan(t *testing.T) {
	cfg := testConfig()
	in := Input{Device: "d", Now: at(100)}
	last := map[string]string{"venom": "review"}
	none := NewState()
	wearing := Fold([]Event{ev(0, EvSwitch, "venom", "development")})
	paused := Fold([]Event{ev(0, EvSwitch, "venom", "development"), ev(10, EvOff, "venom", "")})

	// resume = last
	e, err := PlanOn(cfg, none, last, OnArgs{Hat: "ve"}, in)
	if err != nil || e == nil || e.Hat != "venom" || e.Activity != "review" {
		t.Fatalf("on ve: %+v %v", e, err)
	}
	// explicit activity wins
	e, err = PlanOn(cfg, none, last, OnArgs{Hat: "ve", Activity: "mee", HasActivity: true}, in)
	if err != nil || e.Activity != "meeting" {
		t.Fatalf("on ve do med: %+v %v", e, err)
	}
	// unknown activity: error, no event
	e, err = PlanOn(cfg, none, last, OnArgs{Hat: "ve", Activity: "zzz", HasActivity: true}, in)
	var nf *NotFoundError
	if e != nil || !errors.As(err, &nf) {
		t.Fatalf("unknown activity: %+v %v", e, err)
	}
	// ask without answer = never; with yes = last (priv uses default ask but has no activities)
	cfg2 := testConfig()
	cfg2.Hats[0].ResumeActivity = ""
	e, _ = PlanOn(cfg2, none, last, OnArgs{Hat: "venom"}, in)
	if e.Activity != "" {
		t.Fatalf("ask without answer should start without activity: %+v", e)
	}
	e, _ = PlanOn(cfg2, none, last, OnArgs{Hat: "venom", Answer: AnswerYes}, in)
	if e.Activity != "review" {
		t.Fatalf("ask yes: %+v", e)
	}
	// same hat worn: nothing to do
	if e, err = PlanOn(cfg, wearing, last, OnArgs{Hat: "venom"}, in); e != nil || err != nil {
		t.Fatalf("on same hat: %+v %v", e, err)
	}
	// resume keeps the activity
	e, err = PlanOn(cfg, paused, last, OnArgs{}, in)
	if err != nil || e.Hat != "venom" || e.Activity != "development" {
		t.Fatalf("resume: %+v %v", e, err)
	}
	if _, err = PlanOn(cfg, none, last, OnArgs{}, in); !errors.Is(err, ErrNoHatToResume) {
		t.Fatalf("resume none: %v", err)
	}

	// do
	if _, err = PlanDo(cfg, none, "x", in); !errors.Is(err, ErrNoHat) {
		t.Fatalf("do none: %v", err)
	}
	if _, err = PlanDo(cfg, paused, "review", in); !errors.Is(err, ErrPaused) {
		t.Fatalf("do paused: %v", err)
	}
	e, err = PlanDo(cfg, wearing, "rev", in)
	if err != nil || e.Type != EvActivity || e.Activity != "review" {
		t.Fatalf("do: %+v %v", e, err)
	}
	e, _ = PlanDo(cfg, wearing, "-", in)
	if e == nil || e.Activity != "" {
		t.Fatalf("do -: %+v", e)
	}

	// off
	if _, err = PlanOff(none, false, in); !errors.Is(err, ErrNoHat) {
		t.Fatalf("off none: %v", err)
	}
	if e, _ = PlanOff(paused, false, in); e != nil {
		t.Fatalf("off paused should be a no-op")
	}
	if e, _ = PlanOff(paused, true, in); e == nil || !e.Neutral {
		t.Fatalf("off --neutral when paused: %+v", e)
	}
	e, _ = PlanOff(wearing, false, in)
	if e == nil || e.Neutral {
		t.Fatalf("off: %+v", e)
	}

	// future timestamps are rejected
	_, err = PlanOff(wearing, false, Input{Now: at(0), At: at(30)})
	if !errors.Is(err, ErrFuture) {
		t.Fatalf("future: %v", err)
	}
}

const sampleYAML = `version: 1
defaults:
  resume_activity: ask
modules:
  notify:
    enabled: true
hats:
  venom:
    name: VENOM
    color: "#7a1fa2"
    resume_activity: last
    activities:
      - development
      - review
    modules:
      wallpaper:
        image: ~/Pictures/hats/venom.jpg
      exec:
        enter: ~/bin/venom-on.sh
  private:
    name: Private
    activities: []
`

func TestConfigRoundTrip(t *testing.T) {
	c, err := ParseConfig([]byte(sampleYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hats) != 2 || c.Hats[0].Key != "venom" || c.Hats[1].Key != "private" {
		t.Fatalf("hats: %+v", c.Hats)
	}
	out, err := c.Export()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := ParseConfig(out)
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	if !sameJSON(c, c2) {
		t.Fatalf("round trip differs:\n%s", out)
	}
	out2, _ := c2.Export()
	if string(out) != string(out2) {
		t.Fatalf("export not stable:\n%s\n---\n%s", out, out2)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := map[string]string{
		"version":  "version: 2\nhats: {}\n",
		"key":      "version: 1\nhats:\n  Bad Key:\n    name: x\n",
		"color":    "version: 1\nhats:\n  a:\n    color: red\n",
		"resume":   "version: 1\nhats:\n  a:\n    resume_activity: sometimes\n",
		"dupe act": "version: 1\nhats:\n  a:\n    activities: [x, X]\n",
		"unknown":  "version: 1\nhats: {}\nfoo: 1\n",
	}
	for name, y := range bad {
		if _, err := ParseConfig([]byte(y)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestMerge(t *testing.T) {
	cur, _ := ParseConfig([]byte(sampleYAML))
	in, err := ParseConfig([]byte(`version: 1
hats:
  venom:
    name: Venom GmbH
    activities: [review, planning]
  tag:
    name: TAG
    activities: [support]
`))
	if err != nil {
		t.Fatal(err)
	}
	m, changes := Merge(cur, in, false)
	venom, _ := m.Hat("venom")
	if venom.Name != "Venom GmbH" || !reflect.DeepEqual(venom.Activities, []string{"review", "planning", "development"}) {
		t.Fatalf("venom: %+v", venom)
	}
	if _, ok := venom.Modules["wallpaper"]; !ok {
		t.Fatal("module settings of the existing hat were lost")
	}
	if p, ok := m.Hat("private"); !ok || p.Archived {
		t.Fatalf("missing hat should be kept: %+v", p)
	}
	if _, ok := m.Hat("tag"); !ok || len(changes) == 0 {
		t.Fatalf("tag not added: %v", changes)
	}

	r, _ := Merge(cur, in, true)
	if p, _ := r.Hat("private"); !p.Archived {
		t.Fatal("--replace should archive private")
	}
	v, _ := r.Hat("venom")
	if !reflect.DeepEqual(v.ArchivedActivity, []string{"development"}) || len(v.Modules) != 0 {
		t.Fatalf("replace venom: %+v", v)
	}
	if _, err := ResolveHat(r, "pri"); err == nil {
		t.Fatal("archived hats must not resolve")
	}
	// archived state survives an export/import cycle
	out, _ := r.Export()
	r2, err := ParseConfig(out)
	if err != nil || !sameJSON(r, r2) {
		t.Fatalf("archived round trip: %v\n%s", err, out)
	}
}

func TestModuleConfigAndEnv(t *testing.T) {
	c, _ := ParseConfig([]byte(sampleYAML))
	venom, _ := c.Hat("venom")
	if _, ok := c.ModuleConfig(venom, "notify"); !ok {
		t.Fatal("notify should come from the defaults")
	}
	if m, ok := c.ModuleConfig(venom, "wallpaper"); !ok || m["image"] == nil {
		t.Fatal("wallpaper config missing")
	}
	priv, _ := c.Hat("private")
	if _, ok := c.ModuleConfig(priv, "wallpaper"); ok {
		t.Fatal("private has no wallpaper")
	}
	t.Setenv("HAT_TEST", "x")
	if got := ExpandEnv(map[string]any{"a": "${env:HAT_TEST}-y"}).(map[string]any)["a"]; got != "x-y" {
		t.Fatalf("env: %v", got)
	}
	c.Modules["mqtt"] = ModuleCfg{"password": "hunter2"}
	if w := c.Warnings(false); len(w) != 1 || !strings.Contains(w[0], "secret") {
		t.Fatalf("warnings: %v", w)
	}
}

func TestWorkHours(t *testing.T) {
	c := NewConfig()
	if w := c.Work(); w.Start != "08:00" || !w.IsWorkday(time.Monday) || w.IsWorkday(time.Sunday) {
		t.Fatalf("default: %+v", w)
	}
	cfg, err := ParseConfig([]byte("version: 1\ndefaults:\n  work_hours: {start: \"07:30\", end: \"16:00\", days: [mon, tue, sat]}\nhats: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s, e := cfg.Work().Minutes(); s != 450 || e != 960 || !cfg.Work().IsWorkday(time.Saturday) || cfg.Work().IsWorkday(time.Friday) {
		t.Fatalf("configured: %+v", cfg.Work())
	}
	for _, bad := range []string{`{start: "8", end: "16:00"}`, `{start: "16:00", end: "08:00"}`, `{start: "08:00", end: "16:00", days: [monday]}`} {
		if _, err := ParseConfig([]byte("version: 1\ndefaults:\n  work_hours: " + bad + "\nhats: {}\n")); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
	merged, ch := Merge(NewConfig(), cfg, false)
	if merged.Work().Start != "07:30" || len(ch) != 1 {
		t.Fatalf("merge: %+v %v", merged.Defaults, ch)
	}
}
