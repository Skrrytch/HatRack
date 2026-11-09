package server

import (
	"net/url"
	"strconv"
	"strings"

	"hatrack/internal/i18n"
)

// One pattern for every setup page: a head (path, title, one sentence),
// tabs once a page has more than one topic (each with its state), one card
// and one save per thing, explanations folded under "More".

// pageTab is one tab of a setup page.
type pageTab struct {
	ID, Href, Title, Stat string
	On                    bool
}

// pickTab returns the requested tab if it is one of ids, else the first.
func pickTab(q url.Values, ids ...string) string {
	t := q.Get("tab")
	for _, id := range ids {
		if id == t {
			return t
		}
	}
	return ids[0]
}

// Module tabs of the defaults and hat pages, in order.
var modTabs = []struct{ ID, Key string }{
	{"look", "web.setup.tab_look"},
	{"switch", "web.setup.tab_switch"},
	{"report", "web.setup.tab_report"},
}

// modGroup puts a module into a tab; modules not listed (external ones) go
// to "switch", where scripts are.
var modGroup = map[string]string{
	"wallpaper": "look", "color_scheme": "look",
	"launch": "switch", "exec": "switch",
	"notify": "report", "mqtt": "report",
}

func groupOf(id string) string {
	if g, ok := modGroup[id]; ok {
		return g
	}
	return "switch"
}

// modsIn returns the module forms of one tab.
func modsIn(all []moduleForm, tab string) []moduleForm {
	var out []moduleForm
	for _, m := range all {
		if groupOf(m.ID) == tab {
			out = append(out, m)
		}
	}
	return out
}

// moduleTabs appends a tab per module group that has forms, with how many of
// its modules are set ("1 of 2").
func moduleTabs(lang, base, cur string, all []moduleForm, tabs []pageTab) []pageTab {
	for _, t := range modTabs {
		ms := modsIn(all, t.ID)
		if len(ms) == 0 {
			continue
		}
		set := 0
		for _, m := range ms {
			if m.Set {
				set++
			}
		}
		tabs = append(tabs, pageTab{ID: t.ID, Href: base + "tab=" + t.ID, Title: i18n.In(lang, t.Key),
			Stat: i18n.In(lang, "web.setup.tab_set", strconv.Itoa(set), strconv.Itoa(len(ms))), On: cur == t.ID})
	}
	return tabs
}

// formParts returns which parts a form holds ("" means all, the old forms)
// and the modules it holds (nil means all).
func formParts(part, mods string) (func(string) bool, []string) {
	has := func(string) bool { return true }
	if part != "" {
		has = func(p string) bool { return p == part }
	}
	var only []string
	if part == "mods" {
		only = splitList(mods)
	}
	return has, only
}

// tabQuery returns "&tab=x" for a known tab, else "".
func tabQuery(tab string) string {
	if tab == "" || strings.ContainsAny(tab, "&#?/") {
		return ""
	}
	return "&tab=" + url.QueryEscape(tab)
}
