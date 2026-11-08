package server

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

const rulesCfg = `version: 1
observe:
  devices:
    laptop: {window: true}
hats:
  venom:
    name: VENOM
    color: "#7a1fa2"
    activities: [development, review]
    rules:
      - match: {title: venom}
  tag:
    name: TAG
    activities: [support]
defaults:
  rules:
    - match: {app: ^code$}
      activity: development
    - match: {app: spotify}
      ignore: true
tickets:
  - project: VEN
    url: https://jira.example.org/browse/
`

// rulesDay stores the observations of one day: windows, one absence.
func rulesDay(t *testing.T, e *env) {
	t.Helper()
	seed(t, e)
	if r, _ := e.do("PUT", "/api/config", rulesCfg); r.StatusCode != 200 {
		t.Fatalf("config: %d", r.StatusCode)
	}
	span := func(sensor string, from, to int, attrs core.Attrs) core.Observation {
		return core.Observation{ID: core.NewID(), Sensor: sensor, Start: e.now.Add(time.Duration(from) * time.Minute),
			End: e.now.Add(time.Duration(to) * time.Minute), Attrs: attrs}
	}
	code := func(title string) core.Attrs { return core.Attrs{"app": "code", "title": title} }
	if n, got := e.postObs(
		span("window", -170, -150, code("OrderService.java - venom-api - Visual Studio Code")),
		span("window", -150, -140, core.Attrs{"app": "firefox", "title": "UTF-8 Encoding in Java - Stack Overflow"}),
		span("window", -100, -90, core.Attrs{"app": "Spotify", "title": "Song"}),
		span("window", -58, -52, code("billing - Visual Studio Code")),
		span("window", -45, -35, code("venom-api - Visual Studio Code")),
		span("idle", -165, -160, core.Attrs{"state": "away"}),
	); n != 200 || got["stored"] != 6 {
		t.Fatalf("observations: %d %v", n, got)
	}
}

func pcts(out string) (sum int) {
	for _, m := range regexp.MustCompile(`<span style="--k:[^"]*">[^<]*? (\d+) %</span>`).FindAllStringSubmatch(out, -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		sum += n
	}
	return sum
}

func TestRulesTab(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e)
	r, out := b.do("GET", "/setup/detection?tab=rules&day=2026-11-04", nil)
	if r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	for _, want := range []string{`class="card flow"`, `<tr class="">`, `class="src rule"`, `class="src ticket"`, `tr class="noise"`,
		`tr class="unk"`, `tr class="diff"`, "billing - Visual Studio Code", "hat open", "unknown", "VENOM"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if got := pcts(out); got != 100 {
		t.Errorf("cover adds up to %d %%", got)
	}
	// 6 rows: venom code twice (an absence between), firefox, spotify, billing (no hat), venom-api (booked TAG)
	for _, want := range []string{"All 6", "Unknown 2", "Differs from booking 1", "Noise 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("filter %q missing", want)
		}
	}
	// the unknown row names the ticket-like word that is no ticket
	if !strings.Contains(out, "UTF-8 is no known ticket") {
		t.Error("unknown ticket hint")
	}
	// a "+ rule" link carries the app, the first title part (quoted) and the booked hat
	if !regexp.MustCompile(`href="[^"]*ed_app=code[^"]*ed_booked=tag[^"]*"|href="[^"]*ed_booked=tag[^"]*ed_app=code[^"]*"`).MatchString(out) {
		t.Error("+ rule link of the billing row")
	}
	if !strings.Contains(out, "ed_title=billing") {
		t.Error("first title part in the link")
	}
	// filter
	_, out = b.do("GET", "/setup/detection?tab=rules&day=2026-11-04&filter=noise", nil)
	if strings.Contains(out, "billing - Visual") || !strings.Contains(out, "Spotify") {
		t.Error("noise filter")
	}
	// lists below
	_, out = b.do("GET", "/setup/detection?tab=rules&day=2026-11-04", nil)
	for _, want := range []string{"If</span>", "activity development", "noise, never a suggestion", "last 7 days:", "Nothing learned yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules list: missing %q", want)
		}
	}
}

func TestRulesTry(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e)
	q := "/setup/detection?tab=rules&day=2026-11-04&ed_scope=tag&ed_app=code&ed_title=billing"
	r, out := b.do("GET", q, nil)
	if r.StatusCode != 200 || !strings.Contains(out, `class="ruleedit"`) {
		t.Fatalf("try: %d", r.StatusCode)
	}
	if !strings.Contains(out, `class="hitnote"`) || !strings.Contains(out, "matches 1 windows, 6m") || !strings.Contains(out, "1 of them without a hat so far") {
		t.Errorf("hit note:\n%s", out[strings.Index(out, `class="ruleedit"`):])
	}
	if !strings.Contains(out, `unk hit`) && !strings.Contains(out, `hit"`) {
		t.Error("hit row")
	}
	// an invalid expression is an error under the field, the page still shows
	r, out = b.do("GET", "/setup/detection?tab=rules&day=2026-11-04&ed_scope=tag&ed_title=(", nil)
	if r.StatusCode != 200 || !strings.Contains(out, `class="err"`) {
		t.Errorf("invalid: %d", r.StatusCode)
	}
}

func TestRulesSave(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e)
	form := func(over map[string]string) url.Values {
		f := url.Values{"op": {"rule.save"}, "tab": {"rules"}, "day": {"2026-11-04"}, "ed_scope": {"tag"}, "ed_app": {"code"}, "ed_title": {"billing"}, "ed_act": {"support"}}
		for k, v := range over {
			f.Set(k, v)
		}
		return f
	}
	r, _ := b.do("POST", "/setup/detection", form(nil))
	if r.StatusCode != 303 || !strings.Contains(r.Header.Get("Location"), "saved=") {
		t.Fatalf("save: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	h, _ := config(t, e).Hat("tag")
	if len(h.Rules) != 1 || h.Rules[0].Match["title"] != "billing" || len(h.Rules[0].Activity) != 1 {
		t.Fatalf("rules: %+v", h.Rules)
	}
	// invalid: 400, the error at the field, nothing stored
	r, out := b.do("POST", "/setup/detection", form(map[string]string{"ed_title": "("}))
	if r.StatusCode != 400 || !strings.Contains(out, `class="err"`) || !strings.Contains(out, `value="("`) {
		t.Errorf("invalid save: %d", r.StatusCode)
	}
	if h, _ := config(t, e).Hat("tag"); len(h.Rules) != 1 {
		t.Error("an invalid rule was stored")
	}
	// edit in place, then move to another hat
	b.do("POST", "/setup/detection", form(map[string]string{"ed_edit": "tag:0", "ed_title": "billing-service"}))
	if h, _ := config(t, e).Hat("tag"); len(h.Rules) != 1 || h.Rules[0].Match["title"] != "billing-service" {
		t.Fatalf("edit: %+v", h.Rules)
	}
	b.do("POST", "/setup/detection", form(map[string]string{"ed_edit": "tag:0", "ed_scope": "venom", "ed_act": "review"}))
	c := config(t, e)
	if h, _ := c.Hat("tag"); len(h.Rules) != 0 {
		t.Errorf("moved rule stayed: %+v", h.Rules)
	}
	if h, _ := c.Hat("venom"); len(h.Rules) != 2 {
		t.Errorf("moved rule missing: %+v", h.Rules)
	}
	// the editor opens with the rule's values
	_, out = b.do("GET", "/setup/detection?tab=rules&day=2026-11-04&ed_edit=venom:1", nil)
	if !strings.Contains(out, `name="ed_title" value="billing"`) || !strings.Contains(out, `name="ed_edit" value="venom:1"`) {
		t.Errorf("edit prefill:\n%s", out[strings.Index(out, `class="ruleedit"`):])
	}
	// delete
	r, _ = b.do("POST", "/setup/detection", url.Values{"op": {"rule.delete"}, "day": {"2026-11-04"}, "ed_edit": {"venom:1"}})
	if r.StatusCode != 303 || !strings.Contains(r.Header.Get("Location"), "deleted=1") {
		t.Fatalf("delete: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if h, _ := config(t, e).Hat("venom"); len(h.Rules) != 1 {
		t.Errorf("after delete: %+v", h.Rules)
	}
}

func TestRulesEmptyStates(t *testing.T) {
	e, b := setupBrowser(t)
	// no window sensor on any device
	_, out := b.do("GET", "/setup/detection?tab=rules", nil)
	for _, want := range []string{"No windows observed yet", "To the sensors", "None yet. Typical", "Nothing learned yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// the sensor is on, the day is empty
	e.do("PUT", "/api/config", "version: 1\nobserve:\n  devices:\n    laptop: {window: true}\nhats: {}\n")
	_, out = b.do("GET", "/setup/detection?tab=rules", nil)
	if !strings.Contains(out, "Nothing was observed on this day") {
		t.Error("empty day")
	}
}
