package tray

import (
	"bytes"
	"image/png"
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
	venom := find(m, "VENOM")
	if venom == nil || !venom.Checked || len(venom.Children) != 3 {
		t.Fatalf("venom: %+v", venom)
	}
	review := find(venom.Children, "review")
	if !review.Checked || review.Action.Request.Cmd != "on" || review.Action.Request.Hat != "venom" ||
		review.Action.Request.Activity != "review" || !review.Action.Request.HasActivity {
		t.Fatalf("review: %+v", review)
	}
	if find(venom.Children, "development").Checked || find(venom.Children, "Hat only").Checked {
		t.Fatal("only the current activity is checked")
	}
	priv := find(m, "Private")
	if priv == nil || priv.Checked || priv.Children != nil || priv.Action.Request.Hat != "private" {
		t.Fatalf("private: %+v", priv)
	}
	if find(m, "Pause") == nil || find(m, "Resume") != nil {
		t.Fatal("wearing: Pause, not Resume")
	}
	if find(m, "Open HatRack") == nil || find(m, "Quit tray") == nil {
		t.Fatal("open and quit missing")
	}
}

func TestMenuPausedAndNone(t *testing.T) {
	i18n.Set("en")
	m := BuildMenu(snapshot(core.ModePaused, "venom", "review"), time.Now())
	if r := find(m, "Resume"); r == nil || r.Action.Request.Cmd != "on" || r.Action.Request.Hat != "" {
		t.Fatalf("resume: %+v", r)
	}
	if v := find(m, "VENOM"); !v.Checked {
		t.Fatal("the paused hat stays marked")
	}
	if find(find(m, "VENOM").Children, "review").Checked {
		t.Fatal("no activity is checked while paused")
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

func TestIconAndColor(t *testing.T) {
	if IconColor(snapshot(core.ModeWearing, "venom", "")) != "#7a1fa2" || IconColor(snapshot(core.ModePaused, "venom", "")) != Grey ||
		IconColor(snapshot(core.ModeNone, "", "")) != Grey {
		t.Fatal("icon colour")
	}
	img, err := png.Decode(bytes.NewReader(Icon("#7a1fa2", 64)))
	if err != nil || img.Bounds().Dx() != 64 {
		t.Fatalf("icon: %v", err)
	}
	// the crown is opaque and purple (red and blue dominate green), the corner is transparent
	r, g, b, a := img.At(32, 20).RGBA()
	if a>>8 < 250 || g>>8 >= r>>8 || g>>8 >= b>>8 {
		t.Fatalf("crown pixel: %d %d %d %d", r>>8, g>>8, b>>8, a>>8)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("corner should be transparent")
	}
	if c := ParseColor("nonsense"); c.R != 0x9a {
		t.Fatalf("bad colour: %+v", c)
	}
}
