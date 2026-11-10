package server

import (
	"html"
	"net/url"
	"strings"
	"testing"
)

// The editor opens over the tab Rules and saving returns there.
func TestRuleEditorStaysOnRules(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e)
	_, out := b.do("GET", "/assign?tab=rules", nil)
	out = html.UnescapeString(out)
	i := strings.Index(out, "&ed_edit=")
	if i < 0 {
		t.Fatal("no edit link")
	}
	start := strings.LastIndex(out[:i], `href="`) + len(`href="`)
	link := out[start : i+strings.Index(out[i:], `"`)]
	if !strings.HasPrefix(link, "/assign?tab=rules&") {
		t.Fatalf("edit link leaves the tab: %s", link)
	}
	if !strings.Contains(out, `aria-label="Duplicate"`) {
		t.Error("duplicate is not an icon with a label")
	}
	_, out = b.do("GET", link, nil)
	if !strings.Contains(out, `class="ruleedit drawer"`) || !strings.Contains(out, `name="tab" value="rules"`) || !strings.Contains(out, "See the matches in the day") {
		t.Fatalf("editor on the rules tab missing")
	}
	u, _ := url.Parse(link)
	f := url.Values{"op": {"rule.save"}, "tab": {"rules"}, "ed_edit": {u.Query().Get("ed_edit")}, "ed_scope": {"venom"}, "ed_title": {"venom"}}
	r, _ := b.do("POST", "/assign", f)
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), "/assign?tab=rules&") {
		t.Fatalf("save: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	// from the day it stays on the day
	f.Set("tab", "day")
	if r, _ := b.do("POST", "/assign", f); !strings.HasPrefix(r.Header.Get("Location"), "/assign?tab=day&") {
		t.Fatalf("save from the day: %s", r.Header.Get("Location"))
	}
}
