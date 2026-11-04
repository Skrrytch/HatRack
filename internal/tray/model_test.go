package tray

import (
	"bytes"
	"image/png"
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
	at := func(x, y int) [4]uint32 {
		r, g, b, a := img.At(x, y).RGBA()
		return [4]uint32{r >> 8, g >> 8, b >> 8, a >> 8}
	}
	// the frame has the hat colour, the crown is cream, the band has the hat colour again
	if f := at(32, 7); f != [4]uint32{0x7a, 0x1f, 0xa2, 255} {
		t.Fatalf("frame pixel: %v", f)
	}
	if c := at(32, 22); c != [4]uint32{0xf0, 0xe6, 0xdc, 255} {
		t.Fatalf("crown pixel: %v", c)
	}
	if b := at(32, 34); b != [4]uint32{0x7a, 0x1f, 0xa2, 255} {
		t.Fatalf("band pixel: %v", b)
	}
	if d := at(10, 30); d != [4]uint32{0x2a, 0x2d, 0x33, 255} {
		t.Fatalf("background pixel: %v", d)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("corner should be transparent")
	}
	if small, err := png.Decode(bytes.NewReader(Icon("#7a1fa2", 22))); err != nil || small.Bounds().Dx() != 22 {
		t.Fatalf("small icon: %v", err)
	}
	if got := SVG("#2FA7E0"); !strings.Contains(got, `stroke="#2fa7e0"`) || !strings.Contains(got, `fill="#2fa7e0"`) {
		t.Fatalf("svg: %s", got)
	}
	if c := ParseColor("nonsense"); c.R != 0x8a {
		t.Fatalf("bad colour: %+v", c)
	}
}
