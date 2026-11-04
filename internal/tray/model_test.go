package tray

import (
	"testing"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

func snapshot(mode core.Mode, hat, act string) agent.Snapshot {
	st := core.State{Mode: mode, Hat: hat, Activity: act, Since: time.Now().Add(-10 * time.Minute)}
	return agent.Snapshot{
		Server: "http://127.0.0.1:8421",
		Status: agent.StatusData{State: st, HatName: "VENOM", Color: "#7a1fa2", Connected: true},
		Hats: []agent.HatInfo{
			{Key: "venom", Name: "VENOM", Color: "#7a1fa2", Activities: []string{"development", "review"}, Resume: "last"},
			{Key: "private", Name: "Private", Activities: []string{}},
		},
	}
}

func find(items []Item, label string) *Item {
	for i := range items {
		if items[i].Label == label {
			return &items[i]
		}
	}
	return nil
}

func TestMenuWearing(t *testing.T) {
	i18n.Set("en")
	m := BuildMenu(snapshot(core.ModeWearing, "venom", "review"), time.Now())
	if !m[0].Disabled || m[0].Label[:5] != "VENOM" {
		t.Fatalf("first line: %+v", m[0])
	}
	// the hats live in the panel, not in the menu
	if find(m, "VENOM") != nil || find(m, "Private") != nil {
		t.Fatal("the menu does not list hats")
	}
	if find(m, "Pause") == nil || find(m, "Resume") != nil {
		t.Fatal("wearing: Pause, not Resume")
	}
	for _, l := range []string{"Open HatRack", "About HatRack", "Quit tray"} {
		if find(m, l) == nil {
			t.Fatalf("%s missing", l)
		}
	}
	if !find(m, "About HatRack").Action.About {
		t.Fatal("about action")
	}
}

func TestMenuPausedAndNone(t *testing.T) {
	i18n.Set("en")
	m := BuildMenu(snapshot(core.ModePaused, "venom", "review"), time.Now())
	if r := find(m, "Resume"); r == nil || r.Action.Request.Cmd != "on" || r.Action.Request.Hat != "" {
		t.Fatalf("resume: %+v", r)
	}
	none := BuildMenu(snapshot(core.ModeNone, "", ""), time.Now())
	if find(none, "Pause") != nil || find(none, "Resume") != nil {
		t.Fatal("no pause or resume without a hat")
	}
	if m[0].Label == "" || none[0].Label != "No hat on" {
		t.Fatalf("status line: %q", none[0].Label)
	}
	empty := agent.Snapshot{Status: agent.StatusData{Connected: true, State: core.NewState()}}
	if e := BuildMenu(empty, time.Now()); find(e, "Quit tray") == nil {
		t.Fatal("an empty rack still has a menu")
	}
}

func TestMenuOffline(t *testing.T) {
	i18n.Set("en")
	s := snapshot(core.ModeWearing, "venom", "")
	s.Status.Connected = false
	if m := BuildMenu(s, time.Now()); find(m, i18n.T("tray.offline")) == nil {
		t.Fatalf("offline hint missing: %+v", m)
	}
}

func TestIconColor(t *testing.T) {
	if IconColor(snapshot(core.ModeWearing, "venom", "")) != "#7a1fa2" || IconColor(snapshot(core.ModePaused, "venom", "")) != Grey ||
		IconColor(snapshot(core.ModeNone, "", "")) != Grey {
		t.Fatal("icon colour")
	}
}
