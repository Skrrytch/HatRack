package server

import (
	"net/url"
	"strings"
	"testing"
)

// A card saves only its own part: the other modules and fields stay.
func TestCardSavesOnlyItsPart(t *testing.T) {
	e, b := setupBrowser(t)
	e.do("PUT", "/api/config", "version: 1\nmodules:\n  wallpaper: {image: /pics/d.jpg}\n  notify: {enabled: true}\nhats:\n  venom:\n    name: Venom\n    activities: [code, review]\n    modules:\n      launch: {apps: [firefox]}\n")
	// defaults: the wallpaper card alone
	r, _ := b.do("POST", "/setup/defaults", url.Values{"part": {"mods"}, "mods": {"wallpaper"}, "tab": {"look"}, "m/wallpaper/image": {"/pics/new.jpg"}})
	if r.Header.Get("Location") != "/setup/defaults?saved=1&tab=look" {
		t.Fatalf("redirect: %s", r.Header.Get("Location"))
	}
	c := config(t, e)
	if c.Modules["wallpaper"]["image"] != "/pics/new.jpg" || c.Modules["notify"]["enabled"] != true {
		t.Fatalf("defaults: %v", c.Modules)
	}
	// defaults: working hours alone keep the resume setting and the modules
	b.do("POST", "/setup/defaults", url.Values{"part": {"resume"}, "resume": {"last"}})
	b.do("POST", "/setup/defaults", url.Values{"part": {"work"}, "work_start": {"09:00"}, "work_end": {"17:00"}, "work_day": {"mon", "tue"}})
	c = config(t, e)
	if c.Defaults.ResumeActivity != "last" || c.Defaults.WorkHours == nil || c.Defaults.WorkHours.Start != "09:00" || c.Modules["notify"] == nil {
		t.Fatalf("work hours: %+v %v", c.Defaults, c.Modules)
	}
	// hat: the name card keeps activities and modules
	before, _ := config(t, e).Hat("venom")
	acts := strings.Join(before.Activities, ",")
	b.do("POST", "/setup/hat", url.Values{"key": {"venom"}, "part": {"basics"}, "name": {"VENOM"}, "color": {"#8e44ad"}, "work": {"1"}, "observe": {"1"}, "op": {"save"}})
	h, _ := config(t, e).Hat("venom")
	if h.Name != "VENOM" || strings.Join(h.Activities, ",") != acts || h.Modules["launch"] == nil {
		t.Fatalf("basics: %+v", h)
	}
	// hat: the activities card keeps the name and the modules
	b.do("POST", "/setup/hat", url.Values{"key": {"venom"}, "part": {"activities"}, "newact": {"meeting"}, "op": {"add"}})
	h, _ = config(t, e).Hat("venom")
	if h.Name != "VENOM" || strings.Join(h.Activities, ",") != acts+",meeting" || h.Modules["launch"] == nil {
		t.Fatalf("activities: %+v", h)
	}
	// hat: one module card keeps the others
	r, _ = b.do("POST", "/setup/hat", url.Values{"key": {"venom"}, "part": {"mods"}, "mods": {"notify"}, "tab": {"report"}, "op": {"save"}, "m/notify/enabled": {"false"}})
	h, _ = config(t, e).Hat("venom")
	if h.Modules["launch"] == nil || h.Name != "VENOM" || !strings.HasSuffix(r.Header.Get("Location"), "&tab=report") {
		t.Fatalf("module card: %+v %s", h, r.Header.Get("Location"))
	}
}

func TestSetupPagesShareTheHead(t *testing.T) {
	_, b := setupBrowser(t)
	for path, tabs := range map[string][]string{
		"/setup/defaults":      {"General", "Look", "On switching", "Report"},
		"/setup/hat?key=venom": {"General", "Look", "On switching"},
		"/setup/grants":        {"Basics", "Further permissions", "History"},
		"/setup/detection":     {"Absence", "Assignment"},
		"/setup/tickets":       nil,
		"/setup/ai":            nil,
		"/setup/devices":       nil,
	} {
		_, out := b.do("GET", path, nil)
		if !strings.Contains(out, `<nav class="crumbs"><a href="/setup">`) {
			t.Errorf("%s: no path head", path)
		}
		if strings.Contains(out, `class="nav2"`) {
			t.Errorf("%s: old back button", path)
		}
		for _, tab := range tabs {
			if !strings.Contains(out, ">"+tab+"<span class=\"cnt\">") && !strings.Contains(out, ">"+tab+"</a>") {
				t.Errorf("%s: tab %q missing", path, tab)
			}
		}
	}
}
