package core

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
)

// clsCfg is the configuration of the classifier tests: venom and atlas with
// development activities, office for chat, private.
func clsCfg() Config {
	return Config{Version: ConfigVersion, Hats: HatList{
		{Key: "venom", Activities: []string{"Implementierung", "Entwicklung", "Code-Review"}},
		{Key: "atlas", Activities: []string{"Entwicklung", "Betrieb"}},
		{Key: "office", Activities: []string{"Kommunikation"}},
		{Key: "old", Activities: []string{"x"}, Archived: true},
	}}
}

func win(app, title string) Attrs {
	a := Attrs{"app": app}
	if title != "" {
		a["title"] = title
	}
	return a
}

func hatRule(cfg *Config, hat string, r Rule) {
	for i := range cfg.Hats {
		if cfg.Hats[i].Key == hat {
			cfg.Hats[i].Rules = append(cfg.Hats[i].Rules, r)
		}
	}
}

func mustValid(t *testing.T, cfg Config) {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func classify(t *testing.T, cfg Config, k Knowledge, tk Tickets, a Attrs) Candidate {
	t.Helper()
	mustValid(t, cfg)
	return NewClassifier(cfg, k, tk).Classify(a)
}

func sources(c Candidate) string {
	var s []string
	for _, r := range c.Why {
		s = append(s, r.Field+":"+r.Source)
	}
	return strings.Join(s, ",")
}

// 1. app ignores case; (?-i) switches that off
func TestClassifyCase(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"app": "code"}})
	if c := classify(t, cfg, Knowledge{}, Tickets{}, win("Code", "x")); c.Hat != "venom" {
		t.Fatalf("app code matches Code: %+v", c)
	}
	cfg = clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"title": "(?-i)VEN"}})
	if c := classify(t, cfg, Knowledge{}, Tickets{}, win("x", "ven board")); c.Hat != "" {
		t.Fatalf("(?-i)VEN must not match ven: %+v", c)
	}
}

// 2. priority wins; without it the earlier rule
func TestClassifyPriority(t *testing.T) {
	a := win("Slack", "venom – Slack")
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"title": "venom"}})
	hatRule(&cfg, "office", Rule{Match: map[string]string{"app": "slack"}, Priority: 10})
	if c := classify(t, cfg, Knowledge{}, Tickets{}, a); c.Hat != "office" || c.Conf != ConfRule {
		t.Fatalf("priority 10 wins: %+v", c)
	}
	cfg.Hats[2].Rules[0].Priority = 0
	if c := classify(t, cfg, Knowledge{}, Tickets{}, a); c.Hat != "venom" {
		t.Fatalf("equal: the earlier rule (venom comes first): %+v", c)
	}
}

// 3. at equal priority two conditions beat one
func TestClassifyConditions(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"app": "code"}})
	hatRule(&cfg, "atlas", Rule{Match: map[string]string{"app": "code", "title": "atlas"}})
	if c := classify(t, cfg, Knowledge{}, Tickets{}, win("code", "main.go - atlas - Visual Studio Code")); c.Hat != "atlas" {
		t.Fatalf("two conditions win: %+v", c)
	}
}

// 4. different fields from different rules
func TestClassifyFieldsFromRules(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"app": "code", "title": "venom"}, Activity: Names{"Code-Review"}})
	cfg.Defaults.Rules = []Rule{{Match: map[string]string{"title": `\b(feature/[a-z0-9-]+)`}, Detail: "$1"}}
	c := classify(t, cfg, Knowledge{}, Tickets{}, win("code", "x.go - venom (feature/retry-io) - Visual Studio Code"))
	if c.Hat != "venom" || c.Activity != "Code-Review" || c.Detail != "feature/retry-io" {
		t.Fatalf("all three fields: %+v", c)
	}
	if got := sources(c); got != "hat:rule,activity:rule,detail:rule" {
		t.Fatalf("reasons: %s", got)
	}
}

// 5. a global activity list resolves against the hat
func TestClassifyActivityList(t *testing.T) {
	cfg := clsCfg()
	cfg.Defaults.Rules = []Rule{{Match: map[string]string{"app": "^code$|jetbrains-"}, Activity: Names{"Implementierung", "Entwicklung"}}}
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"title": "venom"}})
	hatRule(&cfg, "atlas", Rule{Match: map[string]string{"title": "atlas"}})
	hatRule(&cfg, "office", Rule{Match: map[string]string{"title": "office"}})
	for _, tc := range []struct{ title, hat, act string }{
		{"venom", "venom", "Implementierung"}, // has both: the first
		{"atlas", "atlas", "Entwicklung"},     // has only the second
		{"office", "office", ""},              // has neither
	} {
		c := classify(t, cfg, Knowledge{}, Tickets{}, win("Code", tc.title))
		if c.Hat != tc.hat || c.Activity != tc.act || len(c.Activities) > 0 {
			t.Errorf("%s: %+v", tc.title, c)
		}
	}
	c := classify(t, cfg, Knowledge{}, Tickets{}, win("jetbrains-idea", "billing – Main.java"))
	if c.Hat != "" || c.Activity != "" || !slices.Equal(c.Activities, []string{"Implementierung", "Entwicklung"}) {
		t.Fatalf("no hat: the list stays: %+v", c)
	}
	if w := c.WithHat(cfg, "atlas"); w.Hat != "atlas" || w.Activity != "Entwicklung" || w.Activities != nil {
		t.Fatalf("WithHat: %+v", w)
	}
	if w := c.WithHat(cfg, "office"); w.Activity != "" {
		t.Fatalf("WithHat office: %+v", w)
	}
}

// 6. noise is never a candidate
func TestClassifyIgnore(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"app": "spotify"}})
	cfg.Defaults.Rules = []Rule{{Match: map[string]string{"app": "spotify|keepassxc"}, Ignore: true}}
	c := classify(t, cfg, Knowledge{}, Tickets{}, win("Spotify", "venom radio"))
	if !c.Ignore || c.Hat != "" || c.Activity != "" || c.Detail != "" {
		t.Fatalf("ignore rule: %+v", c)
	}
	c = classify(t, cfg, Knowledge{}, Tickets{}, Attrs{AttrPrivate: "1"})
	if !c.Ignore || c.Hat != "" {
		t.Fatalf("private: %+v", c)
	}
}

// 7. tickets of known projects only
func TestClassifyTickets(t *testing.T) {
	cfg := clsCfg()
	cfg.Tickets = []TicketSystem{{Project: "VEN", URL: "https://jira.example.org/browse/"}}
	tk := NewTickets(nil, cfg.Tickets)
	k := Knowledge{Projects: map[string]ProjectUse{"VEN": {Hat: "venom", Share: 0.95, Count: 7}}}
	c := classify(t, cfg, k, tk, win("Vivaldi-stable", "[VEN-142] Retry bei Timeout - Jira - Vivaldi"))
	if c.Hat != "venom" || c.Conf != ConfTicket || c.Detail != "VEN-142" {
		t.Fatalf("known project: %+v", c)
	}
	c = classify(t, cfg, k, tk, win("Vivaldi-stable", "[ABC-142] Something - Jira"))
	if !c.IsZero() {
		t.Fatalf("unknown project: %+v", c)
	}
	c = classify(t, cfg, k, NewTickets(TicketLinks{"VEN": "https://x/browse/"}, nil), win("firefox", "UTF-8 Encoding in Java - Stack Overflow"))
	if !c.IsZero() {
		t.Fatalf("UTF-8 is no ticket: %+v", c)
	}
	// known project without a hat: the key is the detail, no hat
	c = classify(t, cfg, Knowledge{}, tk, win("x", "VEN-9 review"))
	if c.Hat != "" || c.Detail != "VEN-9" {
		t.Fatalf("project without hat: %+v", c)
	}
}

// 8. a pattern system
func TestTicketPattern(t *testing.T) {
	cfg := clsCfg()
	cfg.Tickets = []TicketSystem{{System: "ServiceNow", Pattern: `\b(INC)[0-9]{7}\b`, URL: "https://sn.example.com/inc?n={key}"}}
	mustValid(t, cfg)
	refs := NewTickets(nil, cfg.Tickets).Find("Incident INC0012345 – Printer")
	if len(refs) != 1 || refs[0].Project != "INC" || refs[0].Key != "INC0012345" || refs[0].URL != "https://sn.example.com/inc?n=INC0012345" {
		t.Fatalf("pattern: %+v", refs)
	}
	parts := NewTickets(TicketLinks{"VEN": "https://j/browse/"}, cfg.Tickets).Split("VEN-1 and INC0000001")
	if len(parts) != 3 || parts[0].Href != "https://j/browse/VEN-1" || parts[2].Href == "" {
		t.Fatalf("split: %+v", parts)
	}
}

// 9. learned details: whole words, branch-like or ticket keys only
func TestClassifyLearnedDetail(t *testing.T) {
	cfg := clsCfg()
	k := Knowledge{Details: []LearnedDetail{{Detail: "feature/ven-142-retry", Hat: "venom", Activity: "Code-Review", Minutes: 90}}}
	c := classify(t, cfg, k, Tickets{}, win("Guake", "git log feature/ven-142-retry"))
	if c.Hat != "venom" || c.Activity != "Code-Review" || c.Detail != "feature/ven-142-retry" || c.Conf != ConfDetail {
		t.Fatalf("learned detail: %+v", c)
	}
	if c := classify(t, cfg, k, Tickets{}, win("Guake", "feature/ven-142-retry-2")); !c.IsZero() {
		t.Fatalf("part of a longer word: %+v", c)
	}
	tk := NewTickets(TicketLinks{"VEN": "https://j/browse/"}, nil)
	for detail, want := range map[string][]string{
		"Abstimmung mit Team":           nil,
		"review":                        nil,
		"release-2.4":                   {"release-2.4"},
		"https://j/browse/VEN-7 klären": {"VEN-7"},
		"VEN-7 und VEN-8":               {"VEN-7", "VEN-8"},
		"feature/ven-142-retry":         {"feature/ven-142-retry"},
		"Planung":                       nil,
		"ABC-12":                        {"ABC-12"}, // unknown project, but branch-like
	} {
		if got := learnables(detail, tk); !slices.Equal(got, want) {
			t.Errorf("learnables(%q) = %v, want %v", detail, got, want)
		}
	}
}

// 10. learned title parts
func TestClassifyParts(t *testing.T) {
	cfg := clsCfg()
	k := Knowledge{Parts: []LearnedPart{{Part: "venom-api", Hat: "venom", Share: 0.9, Minutes: 40}}}
	c := classify(t, cfg, k, Tickets{}, win("Code", "Order.java - venom-api - Visual Studio Code"))
	if c.Hat != "venom" || c.Conf != ConfPart || c.Why[0].Source != SourcePart || c.Why[0].Ref != "venom-api" {
		t.Fatalf("part: %+v", c)
	}
	if got := TitleParts("HatRack - Vivaldi", "Vivaldi-stable"); !slices.Equal(got, []string{"HatRack"}) {
		t.Fatalf("app name is no part: %v", got)
	}
	if got := TitleParts("Order.java - venom-api - Visual Studio Code", "Code"); !slices.Equal(got, []string{"Order.java", "venom-api"}) {
		t.Fatalf("parts: %v", got)
	}
}

// 10b. BuildKnowledge: thresholds for parts, projects and details
func TestBuildKnowledge(t *testing.T) {
	now := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	day := now.Add(-24 * time.Hour)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	sw := func(ts time.Time, hat, act, detail string) Event {
		e := NewEvent("d", EvSwitch, ts)
		e.Hat, e.Activity, e.Detail = hat, act, detail
		return e
	}
	off := func(ts time.Time) Event { return NewEvent("d", EvOff, ts) }
	events := []Event{
		sw(at(8, 0), "venom", "Entwicklung", "VEN-1"), off(at(9, 0)),
		sw(at(9, 0), "venom", "Code-Review", "VEN-2"), off(at(9, 30)),
		sw(at(10, 0), "venom", "Entwicklung", "VEN-3"), off(at(10, 30)),
		sw(at(11, 0), "atlas", "Betrieb", "release-2.4"), off(at(12, 0)),
	}
	w := func(dev, app, title string, a, b time.Time) Observation {
		return Observation{Device: dev, Sensor: "window", Start: a, End: b, Attrs: Attrs{"app": app, "title": title}}
	}
	obs := []Observation{
		w("l", "Code", "A.java - venom-api - Visual Studio Code", at(8, 0), at(8, 50)),
		w("l", "Code", "B.java - venom-api - Visual Studio Code", at(11, 0), at(11, 5)), // 5 of 55 min on atlas
		w("l", "Guake", "short-topic", at(9, 0), at(9, 20)),                             // 20 min only
		w("l", "Vivaldi-stable", "Mixed - Vivaldi", at(9, 0), at(9, 30)),                // 30 min venom
		w("l", "Vivaldi-stable", "Mixed - Vivaldi", at(11, 0), at(11, 30)),              // 30 min atlas
		{Device: "l", Sensor: "idle", Start: at(8, 40), End: at(8, 50), Attrs: Attrs{"kind": "locked"}},
	}
	tk := NewTickets(TicketLinks{"VEN": "https://j/browse/"}, nil)
	k := BuildKnowledge(events, obs, tk, 28*24*time.Hour, now)
	parts := map[string]LearnedPart{}
	for _, p := range k.Parts {
		parts[p.Part] = p
	}
	// venom-api: 50 min minus 10 min idle = 40 on venom, 5 on atlas: 89 %
	if p, ok := parts["venom-api"]; !ok || p.Hat != "venom" || p.Minutes != 40 {
		t.Fatalf("venom-api: %+v (all %+v)", p, k.Parts)
	}
	if _, ok := parts["short-topic"]; ok {
		t.Fatal("20 minutes are not enough")
	}
	if _, ok := parts["mixed"]; ok {
		t.Fatal("50 % is not enough")
	}
	if u := k.Projects["VEN"]; u.Hat != "venom" || u.Count != 3 {
		t.Fatalf("project VEN: %+v", k.Projects)
	}
	found := false
	for _, d := range k.Details {
		if d.Detail == "release-2.4" {
			found = d.Hat == "atlas" && d.Activity == "Betrieb" && d.Minutes == 60
		}
	}
	if !found {
		t.Fatalf("details: %+v", k.Details)
	}
	// parts older than the retention are not learned
	if k := BuildKnowledge(events, obs, tk, time.Hour, now); len(k.Parts) != 0 {
		t.Fatalf("retention: %+v", k.Parts)
	}
}

// 11. a rule beats anything learned, also for another hat
func TestClassifyRuleBeatsLearned(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "atlas", Rule{Match: map[string]string{"app": "code"}})
	k := Knowledge{Parts: []LearnedPart{{Part: "venom-api", Hat: "venom", Share: 1, Minutes: 300}}}
	if c := classify(t, cfg, k, Tickets{}, win("Code", "A.java - venom-api - Visual Studio Code")); c.Hat != "atlas" {
		t.Fatalf("rule wins: %+v", c)
	}
}

// 12. no title (redact): title rules do not match, app rules do
func TestClassifyRedacted(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"title": "venom"}})
	hatRule(&cfg, "office", Rule{Match: map[string]string{"app": "thunderbird"}})
	if c := classify(t, cfg, Knowledge{}, Tickets{}, win("thunderbird", "")); c.Hat != "office" {
		t.Fatalf("app rule: %+v", c)
	}
	if c := classify(t, cfg, Knowledge{}, Tickets{}, win("x", "")); !c.IsZero() {
		t.Fatalf("title rule without title: %+v", c)
	}
}

// 13. rules of an archived hat do not work
func TestClassifyArchived(t *testing.T) {
	cfg := clsCfg()
	hatRule(&cfg, "old", Rule{Match: map[string]string{"app": "code"}})
	k := Knowledge{Parts: []LearnedPart{{Part: "legacy", Hat: "old", Share: 1, Minutes: 99}}}
	if c := classify(t, cfg, k, Tickets{}, win("Code", "legacy")); !c.IsZero() {
		t.Fatalf("archived: %+v", c)
	}
}

// 14. validation names the rule and the field
func TestValidateRules(t *testing.T) {
	for _, tc := range []struct {
		hat  string
		r    Rule
		want string
	}{
		{"venom", Rule{Match: map[string]string{"title": "venom("}}, "hats.venom.rules[0].match.title: missing closing )"},
		{"venom", Rule{Match: map[string]string{"window": "x"}}, `hats.venom.rules[0].match: unknown field "window"`},
		{"venom", Rule{Match: map[string]string{}}, "hats.venom.rules[0].match: give at least one"},
		{"", Rule{Match: map[string]string{"app": "x"}, Ignore: true, Activity: Names{"a"}}, "defaults.rules[0]: an ignore rule has no activity"},
		{"venom", Rule{Match: map[string]string{"app": "x"}, Activity: Names{"Kochen"}}, `hats.venom.rules[0].activity "Kochen": hat "venom" has no such activity`},
		{"venom", Rule{Match: map[string]string{"title": "(a)"}, Detail: "$2"}, "hats.venom.rules[0].detail: $2: the pattern has no such group"},
		{"venom", Rule{Match: map[string]string{"title": "a"}, Detail: "${key}"}, "${key}: no pattern has a group"},
		{"", Rule{Match: map[string]string{"app": "x"}}, "defaults.rules[0]: a rule for all hats needs"},
		{"venom", Rule{Match: map[string]string{"media": "speaker"}}, `match.media "speaker"`},
		{"venom", Rule{Match: map[string]string{"app": "x"}, Mode: "always"}, `mode "always"`},
		{"venom", Rule{Match: map[string]string{"app": "x"}, Priority: 101}, "priority 101"},
		{"venom", Rule{Match: map[string]string{"app": "x"}, ID: "Bad ID"}, `id "Bad ID"`},
	} {
		cfg := clsCfg()
		if tc.hat == "" {
			cfg.Defaults.Rules = []Rule{tc.r}
		} else {
			hatRule(&cfg, tc.hat, tc.r)
		}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
	// archived activities still count, so archiving one does not break the config
	cfg := clsCfg()
	cfg.Hats[0].ArchivedActivity = []string{"Alt"}
	hatRule(&cfg, "venom", Rule{Match: map[string]string{"app": "x"}, Activity: Names{"alt"}})
	mustValid(t, cfg)
	// duplicate ids
	cfg = clsCfg()
	hatRule(&cfg, "venom", Rule{ID: "ide", Match: map[string]string{"app": "x"}})
	cfg.Defaults.Rules = []Rule{{ID: "ide", Match: map[string]string{"app": "y"}, Detail: "d"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `id "ide": already used`) {
		t.Fatalf("duplicate id: %v", err)
	}
	// tickets
	for _, tc := range []struct {
		ts   TicketSystem
		want string
	}{
		{TicketSystem{Project: "VEN", Pattern: "x", URL: "https://x/"}, "either project or pattern"},
		{TicketSystem{Project: "ven", URL: "https://x/browse/"}, `project "ven"`},
		{TicketSystem{Project: "VEN", URL: "https://x/browse"}, "ending in /"},
		{TicketSystem{Project: "VEN", URL: "ftp://x/"}, "http or https"},
		{TicketSystem{Pattern: "INC[0-9]+", URL: "https://x/{key}"}, "needs a group"},
		{TicketSystem{Pattern: "(INC)[0-9]+", URL: "https://x/"}, "needs {key}"},
	} {
		cfg := clsCfg()
		cfg.Tickets = []TicketSystem{tc.ts}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("tickets: want %q, got %v", tc.want, err)
		}
	}
}

// 15. the same input gives the same candidate, whatever the order of what
// was learned
func TestClassifyDeterministic(t *testing.T) {
	cfg := clsCfg()
	k := Knowledge{
		Parts: []LearnedPart{
			{Part: "shared", Hat: "venom", Share: 0.9, Minutes: 50},
			{Part: "board", Hat: "atlas", Share: 0.9, Minutes: 50},
			{Part: "plan", Hat: "office", Share: 0.85, Minutes: 50},
		},
		Details: []LearnedDetail{{Detail: "ops-12a", Hat: "atlas", Minutes: 5}, {Detail: "ops-12b", Hat: "venom", Minutes: 5}},
	}
	a := win("x", "shared - board - plan - ops-12a ops-12b")
	want := classify(t, cfg, k, Tickets{}, a)
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		k2 := Knowledge{Parts: slices.Clone(k.Parts), Details: slices.Clone(k.Details)}
		r.Shuffle(len(k2.Parts), func(x, y int) { k2.Parts[x], k2.Parts[y] = k2.Parts[y], k2.Parts[x] })
		r.Shuffle(len(k2.Details), func(x, y int) { k2.Details[x], k2.Details[y] = k2.Details[y], k2.Details[x] })
		got := NewClassifier(cfg, k2, Tickets{}).Classify(a)
		if got.Hat != want.Hat || got.Detail != want.Detail || got.Activity != want.Activity || sources(got) != sources(want) {
			t.Fatalf("run %d: %+v, want %+v", i, got, want)
		}
	}
}

// 16. ActiveSpans cuts out the absences of the same device
func TestActiveSpans(t *testing.T) {
	d := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	m := func(n int) time.Time { return d.Add(time.Duration(n) * time.Minute) }
	got := ActiveSpans(
		[]Observation{{ID: "w", Device: "a", Sensor: "window", Start: m(0), End: m(60), Attrs: Attrs{"app": "x"}}},
		[]Observation{
			{Device: "a", Sensor: "idle", Start: m(20), End: m(30)},
			{Device: "a", Sensor: "idle", Start: m(25), End: m(35)}, // overlaps: counts once
			{Device: "b", Sensor: "idle", Start: m(40), End: m(50)}, // another device
			{Device: "a", Sensor: "idle", Start: m(55), End: m(70)}, // past the end
		})
	if len(got) != 2 || !got[0].End.Equal(m(20)) || !got[1].Start.Equal(m(35)) || !got[1].End.Equal(m(55)) || got[1].ID != "w" {
		t.Fatalf("active: %+v", got)
	}
}

func TestClassifierWith(t *testing.T) {
	cfg := clsCfg()
	mustValid(t, cfg)
	c := NewClassifier(cfg, Knowledge{}, Tickets{})
	r := Rule{Match: map[string]string{"title": "confluence"}, Activity: Names{"Kommunikation"}}
	c2, err := c.With("office", r)
	if err != nil {
		t.Fatal(err)
	}
	got := c2.Classify(win("Vivaldi", "Handbuch - ATLAS - Confluence - Vivaldi"))
	if got.Hat != "office" || got.Activity != "Kommunikation" || got.Why[0].Ref != c2.RuleIDOf("office", r) {
		t.Fatalf("tried rule: %+v", got)
	}
	if !c.Classify(win("Vivaldi", "Confluence")).IsZero() {
		t.Fatal("the original classifier is unchanged")
	}
	if _, err := c.With("venom", Rule{Match: map[string]string{"title": "("}}); err == nil {
		t.Fatal("an invalid rule is refused")
	}
}

func TestRulesYAML(t *testing.T) {
	src := `version: 1
defaults:
  rules:
    - match: {app: "spotify|keepassxc"}
      ignore: true
    - match: {app: "jetbrains-|^code$"}
      activity: [Implementierung, Entwicklung]
tickets:
  - project: VEN
    url: https://jira.example.org/browse/
  - system: ServiceNow
    pattern: "\\b(INC)[0-9]{7}\\b"
    url: "https://sn.example.com/?n={key}"
hats:
  venom:
    activities: [Entwicklung, Abstimmung]
    rules:
      - match: {app: code, title: venom}
        activity: Entwicklung
      - match: {title: "Retro"}
        activity: Abstimmung
        mode: auto
        priority: 10
`
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.Hat("venom")
	if len(h.Rules) != 2 || !slices.Equal(h.Rules[0].Activity, []string{"Entwicklung"}) || h.Rules[1].Mode != ModeAuto ||
		len(cfg.Defaults.Rules) != 2 || !slices.Equal(cfg.Defaults.Rules[1].Activity, []string{"Implementierung", "Entwicklung"}) || len(cfg.Tickets) != 2 {
		t.Fatalf("parsed: %+v %+v", h.Rules, cfg.Defaults.Rules)
	}
	out, err := cfg.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "activity: Entwicklung\n") {
		t.Fatalf("one activity is written as a string:\n%s", out)
	}
	again, err := ParseConfig(out)
	if err != nil {
		t.Fatalf("export does not parse: %v\n%s", err, out)
	}
	if !sameJSON(cfg, again) {
		t.Fatalf("export and import are not lossless:\n%s", out)
	}
	// import: rules and tickets come along, an import without them keeps them
	merged, _ := Merge(NewConfig(), cfg, false)
	if mh, _ := merged.Hat("venom"); len(mh.Rules) != 2 || len(merged.Defaults.Rules) != 2 || len(merged.Tickets) != 2 {
		t.Fatalf("merge: %+v", merged)
	}
	bare := cfg
	bare.Defaults.Rules, bare.Tickets = nil, nil
	bare.Hats = HatList{{Key: "venom", Activities: []string{"Entwicklung", "Abstimmung"}}}
	kept, _ := Merge(cfg, bare, false)
	if kh, _ := kept.Hat("venom"); len(kh.Rules) != 2 || len(kept.Defaults.Rules) != 2 || len(kept.Tickets) != 2 {
		t.Fatalf("merge without rules keeps them: %+v", kept)
	}
	gone, _ := Merge(cfg, bare, true)
	if gh, _ := gone.Hat("venom"); len(gh.Rules) != 0 || len(gone.Defaults.Rules) != 0 || len(gone.Tickets) != 0 {
		t.Fatalf("replace drops them: %+v", gone)
	}
}

func TestRuleID(t *testing.T) {
	r := Rule{Match: map[string]string{"app": "code"}, Activity: Names{"Entwicklung"}}
	a, b := RuleID("venom", r), RuleID("venom", r)
	if a != b || !strings.HasPrefix(a, "venom:") || len(a) != len("venom:")+8 {
		t.Fatalf("derived id: %s %s", a, b)
	}
	r2 := r
	r2.Mode, r2.Priority = ModeAuto, 5 // mode and priority do not change what a rule is
	if RuleID("venom", r2) != a {
		t.Fatal("mode and priority keep the id")
	}
	r2.Activity = Names{"Betrieb"}
	if RuleID("venom", r2) == a {
		t.Fatal("a changed rule gets a new id")
	}
	r.ID = "ide"
	if RuleID("venom", r) != "ide" {
		t.Fatal("own id")
	}
}
