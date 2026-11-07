package tray

import (
	"strings"
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
	if find(m, "Pause") != nil || find(m, "Resume") != nil {
		t.Fatal("wearing: no Pause or Resume, both live in the panel")
	}
	// the four pages sit flat in the menu, in this order, then About and Quit
	var labels, links []string
	for _, it := range m[2:] {
		if it.Separator {
			labels = append(labels, "|")
			continue
		}
		labels = append(labels, it.Label)
		if it.Action != nil && it.Action.Link != "" {
			links = append(links, it.Action.Link)
		}
	}
	if strings.Join(labels, ",") != "Setup,Week,Day,Now,|,About HatRack,|,Quit tray" {
		t.Fatalf("menu order: %v", labels)
	}
	want := []string{"http://127.0.0.1:8421/setup", "http://127.0.0.1:8421/week", "http://127.0.0.1:8421/day", "http://127.0.0.1:8421/"}
	if strings.Join(links, "|") != strings.Join(want, "|") {
		t.Fatalf("links: %v", links)
	}
	if find(m, "Open HatRack") != nil {
		t.Fatal("no Open HatRack submenu anymore")
	}
	if !find(m, "About HatRack").Action.About {
		t.Fatal("about action")
	}
}

func TestMenuPausedAndNone(t *testing.T) {
	i18n.Set("en")
	m := BuildMenu(snapshot(core.ModePaused, "venom", "review"), time.Now())
	if find(m, "Resume") != nil || find(m, "Pause") != nil {
		t.Fatal("paused: no Resume or Pause in the menu, both live in the panel")
	}
	none := BuildMenu(snapshot(core.ModeNone, "", ""), time.Now())
	if find(none, "Pause") != nil || find(none, "Resume") != nil {
		t.Fatal("no pause or resume without a hat")
	}
	if m[0].Label == "" || none[0].Label != "No hat active" {
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

func TestReturnToShow(t *testing.T) {
	snap := func(id string) agent.Snapshot {
		s := snapshot(core.ModeWearing, "venom", "review")
		if id != "" {
			s.Status.Away = &agent.AwayQuestion{ID: id}
		}
		return s
	}
	shown := ""
	step := func(s agent.Snapshot, open bool) bool {
		var o bool
		shown, o = returnToShow(shown, s, open)
		return o
	}
	if step(snap(""), false) {
		t.Fatal("no question, no panel")
	}
	if !step(snap("a"), false) {
		t.Fatal("a new question opens the panel")
	}
	if step(snap("a"), false) {
		t.Fatal("never twice for the same id")
	}
	if step(snap("b"), true) {
		t.Fatal("not while a panel is open")
	}
	if !step(snap("b"), false) {
		t.Fatal("an unshown question opens once the panel is closed")
	}
	if step(snap(""), false) || step(snap("b"), false) {
		t.Fatal("answered and gone, or the same again: nothing")
	}
	if !step(snap("c"), false) {
		t.Fatal("the next question opens again")
	}
}
