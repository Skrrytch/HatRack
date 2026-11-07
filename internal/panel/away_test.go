package panel

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

func testQuestion(booked string) agent.AwayQuestion {
	to := time.Date(2026, 11, 7, 14, 47, 0, 0, time.Local)
	q := agent.AwayQuestion{ID: "q1", From: to.Add(-45 * time.Minute), To: to, Booked: booked,
		Hat: "venom", HatName: "VENOM", Activity: "development", Detail: "VEN-142"}
	if booked == agent.AwayWork {
		q.From, q.To = to.Add(-27*time.Minute), to.Add(-15*time.Minute)
	}
	return q
}

func returnScreen(q agent.AwayQuestion) *screen {
	s := &screen{o: Options{Away: &q}, snap: testSnap(core.ModeWearing, "venom", "development"), away: &q, scale: 1}
	s.rebuild()
	return s
}

func TestReturnLayout(t *testing.T) {
	i18n.Set("en")
	for _, booked := range []string{agent.AwayPause, agent.AwayWork} {
		q := testQuestion(booked)
		l := BuildReturn(testSnap(core.ModeWearing, "venom", "development"), q, "", Edit{Sel: -1}, 1, time.Now())
		img := Render(l, View{Hover: -1, Focus: l.chip(0)})
		if img.Bounds().Dx() != l.W || img.Bounds().Dy() != l.H {
			t.Fatal("image size")
		}
		for _, k := range []Kind{KindTool} {
			for _, h := range l.Hits {
				if h.Kind == k {
					t.Fatalf("%s: the return mode has no tool row: %+v", booked, h)
				}
			}
		}
		first, second := l.chip(0), l.chip(1)
		if first != 0 || second != 1 || !l.Hits[first].Current || l.Hits[second].Current {
			t.Fatalf("%s: two chips, the first preselected", booked)
		}
		if l.Hits[first].Action.Request.Answer != agent.AwayKeep {
			t.Fatalf("%s: the preselected chip keeps: %+v", booked, l.Hits[first].Action.Request)
		}
		want := agent.AwayWork
		if booked == agent.AwayWork {
			want = agent.AwayPause
		}
		if r := l.Hits[second].Action.Request; r.Answer != want || r.Away != "q1" || r.Cmd != "away" {
			t.Fatalf("%s: other chip: %+v", booked, r)
		}
		for i, a := range l.Hits {
			if a.R.X < 0 || a.R.Y < 0 || a.R.X+a.R.W > l.W || a.R.Y+a.R.H > l.H {
				t.Fatalf("%s: hit %d outside the panel: %+v of %dx%d", booked, i, a.R, l.W, l.H)
			}
			for j, b := range l.Hits {
				if i < j && a.R.X < b.R.X+b.R.W && b.R.X < a.R.X+a.R.W && a.R.Y < b.R.Y+b.R.H && b.R.Y < a.R.Y+a.R.H {
					t.Fatalf("%s: overlap: %q and %q", booked, a.Label, b.Label)
				}
			}
		}
		if l.HintFor(-1, -1) != l.Default || l.Default == "" {
			t.Fatal("footer hint")
		}
	}
}

func TestReturnKeys(t *testing.T) {
	i18n.Set("en")
	// booked as pause: Enter keeps
	s := returnScreen(testQuestion(agent.AwayPause))
	if s.focus != 0 {
		t.Fatalf("focus starts on the booked chip, got %d", s.focus)
	}
	if act, done := s.key("enter", 0); !done || act.Request.Answer != agent.AwayKeep || act.Request.Away != "q1" {
		t.Fatalf("enter on the preselected chip: %+v", act)
	}
	// right moves to the other chip, Enter takes it, left goes back
	s = returnScreen(testQuestion(agent.AwayPause))
	if _, done := s.key("right", 0); done || s.focus != 1 {
		t.Fatalf("right: focus %d", s.focus)
	}
	if act, done := s.key("enter", 0); !done || act.Request.Answer != agent.AwayWork {
		t.Fatalf("enter on the other chip: %+v", act)
	}
	s = returnScreen(testQuestion(agent.AwayWork))
	s.key("right", 0)
	if act, _ := s.key("enter", 0); act.Request.Answer != agent.AwayPause {
		t.Fatalf("booked work, other chip is pause: %+v", act)
	}
	s.key("left", 0)
	if s.focus != 0 {
		t.Fatalf("left: focus %d", s.focus)
	}
	// Esc closes with keep
	s = returnScreen(testQuestion(agent.AwayWork))
	if act, done := s.key("escape", 0); !done || act.Request == nil || act.Request.Answer != agent.AwayKeep || act.Request.Away != "q1" {
		t.Fatalf("escape: %+v", act)
	}
}

func TestReturnClicks(t *testing.T) {
	i18n.Set("en")
	s := returnScreen(testQuestion(agent.AwayPause))
	h := hit(s.l, KindActivity, "review")
	act, done := s.activate(s.l.At(h.R.X+2, h.R.Y+2))
	r := act.Request
	if !done || r == nil || r.Cmd != "away" || r.Answer != agent.AwayOther || r.Hat != "venom" || r.Activity != "review" || r.Away != "q1" {
		t.Fatalf("activity click: %+v", r)
	}
	h = hit(s.l, KindHat, "Private")
	act, _ = s.activate(s.l.At(h.R.X+2, h.R.Y+2))
	if r := act.Request; r.Answer != agent.AwayOther || r.Hat != "private" || r.Activity != "" {
		t.Fatalf("hat click: %+v", r)
	}
	// the detail field still works: typing and Enter answer with the detail
	s.startEdit("venom", "")
	for _, c := range "VEN-9" {
		s.key("", c)
	}
	act, done = s.key("enter", 0)
	if r := act.Request; !done || r == nil || r.Cmd != "away" || r.Answer != agent.AwayOther || r.Hat != "venom" || r.Detail != "VEN-9" {
		t.Fatalf("detail: %+v", r)
	}
	// no worn-state marks in return mode
	for _, h := range s.l.Hits {
		if h.Current && h.Kind != KindChip {
			t.Fatalf("worn mark in return mode: %+v", h)
		}
	}
}

// TestShots renders both variants to PNG files for comparing with the
// mockup: HATRACK_PANEL_SHOTS=<dir> go test ./internal/panel -run TestShots
func TestShots(t *testing.T) {
	dir := os.Getenv("HATRACK_PANEL_SHOTS")
	if dir == "" {
		t.Skip("HATRACK_PANEL_SHOTS not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	i18n.Set("de")
	snap := agent.Snapshot{
		Status: agent.StatusData{State: core.State{Mode: core.ModeWearing, Hat: "venom", Activity: "development"}, HatName: "VENOM", Connected: true},
		Hats: []agent.HatInfo{
			{Key: "venom", Name: "VENOM", Color: "#2a9d9a", Activities: []string{"Entwicklung", "Review", "Meeting"}},
			{Key: "office", Name: "Office", Color: "#f07030", Activities: []string{"Mails", "Orga", "Telefon"}},
			{Key: "private", Name: "Privat", Color: "#9b59b6", Activities: []string{"Haushalt"}},
		},
	}
	for _, c := range []struct{ name, booked string }{{"A-pause", agent.AwayPause}, {"B-work", agent.AwayWork}} {
		q := testQuestion(c.booked)
		q.Activity = "Entwicklung"
		l := BuildReturn(snap, q, "", Edit{Sel: -1}, 1, time.Now())
		f, err := os.Create(filepath.Join(dir, "return-"+c.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, Render(l, View{Hover: -1, Focus: l.chip(0)})); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
