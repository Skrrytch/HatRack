package server

import (
	"html"
	"net/url"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

// Repository and branch show in the day list, open the editor as the
// condition, and rules on them assign the span.
func TestRulesWorkdir(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e)
	cfg := strings.Replace(rulesCfg, "    laptop: {window: true}\n", "    laptop: {window: true}\n  grants:\n    laptop: {workdir: all}\n", 1)
	if r, _ := e.do("PUT", "/api/config", cfg); r.StatusCode != 200 {
		t.Fatalf("config: %d", r.StatusCode)
	}
	at := func(m int) time.Time { return e.now.Add(time.Duration(m) * time.Minute) }
	if n, _ := e.postObs(core.Observation{ID: core.NewID(), Sensor: "window", Start: at(-30), End: at(-20),
		Attrs: core.Attrs{"app": "Guake", "title": "Claude Code", core.AttrRepo: "tsobss-nms", core.AttrBranch: "feature/VEN-42-utc", core.AttrBranchSrc: "claude"}}); n != 200 {
		t.Fatalf("observation: %d", n)
	}
	_, out := b.do("GET", "/assign?tab=day&day=2026-11-04", nil)
	row := out[strings.Index(out, "Claude Code"):]
	row = row[:strings.Index(row, "</tr>")]
	if !strings.Contains(row, `class="wd"`) || !strings.Contains(row, "tsobss-nms") || !strings.Contains(row, "feature/VEN-42-utc") {
		t.Fatalf("row without repo and branch:\n%s", row)
	}
	// the key of a known project in the branch is the detail
	if !strings.Contains(row, "VEN-42") || !strings.Contains(row, "Ticket") {
		t.Errorf("no ticket from the branch:\n%s", row)
	}
	// "+ rule" starts from the repository, not from the terminal
	i := strings.Index(row, `class="btn plus" href="`)
	if i < 0 {
		t.Fatalf("no plus link:\n%s", row)
	}
	href := html.UnescapeString(row[i+len(`class="btn plus" href="`):])
	href = href[:strings.Index(href, `"`)]
	u, _ := url.Parse(href)
	if q := u.Query(); q.Get("ed_repo") != "^tsobss-nms$" || q.Get("ed_app") != "" || q.Get("ed_title") != "" {
		t.Fatalf("plus link: %v", q)
	}
	// a rule on the branch
	f := url.Values{"op": {"rule.save"}, "day": {"2026-11-04"}, "ed_scope": {"tag"}, "ed_branch": {`(VEN-[0-9]+)`}, "ed_detail": {"$1"}, "ed_act": {"support"}}
	if r, _ := b.do("POST", "/assign", f); r.StatusCode != 303 {
		t.Fatalf("save: %d", r.StatusCode)
	}
	h, _ := config(t, e).Hat("tag")
	if len(h.Rules) != 1 || h.Rules[0].Match["branch"] != "(VEN-[0-9]+)" {
		t.Fatalf("rules: %+v", h.Rules)
	}
	_, out = b.do("GET", "/assign?tab=rules", nil)
	if !strings.Contains(html.UnescapeString(out), "(VEN-[0-9]+)") {
		t.Error("the rule list does not show the branch condition")
	}
	// a bad pattern shows at the branch field
	f.Set("ed_branch", "(")
	if r, out := b.do("POST", "/assign", f); r.StatusCode != 400 || !strings.Contains(out, `name="ed_branch" value="("`) {
		t.Errorf("bad branch: %d", r.StatusCode)
	}
}
