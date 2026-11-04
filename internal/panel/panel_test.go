package panel

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

func testSnap(mode core.Mode, hat, act string) agent.Snapshot {
	st := core.State{Mode: mode, Hat: hat, Activity: act, Since: time.Now().Add(-10 * time.Minute)}
	return agent.Snapshot{
		Server: "http://127.0.0.1:8421",
		Status: agent.StatusData{State: st, HatName: "VENOM", Color: "#7a1fa2", Connected: true},
		Hats: []agent.HatInfo{
			{Key: "venom", Name: "VENOM", Color: "#7a1fa2", Activities: []string{"development", "review", "meeting"}, Resume: "last", Last: "review"},
			{Key: "private", Name: "Private", Color: "#59a14f", Activities: []string{"garden"}},
			{Key: "other", Name: "Other", Activities: []string{}},
		},
	}
}

func hit(l Layout, kind Kind, label string) *Hit {
	for i := range l.Hits {
		if l.Hits[i].Kind == kind && l.Hits[i].Label == label {
			return &l.Hits[i]
		}
	}
	return nil
}

func TestBuildHatsAndActivities(t *testing.T) {
	i18n.Set("en")
	l := Build(testSnap(core.ModeWearing, "venom", "review"), "", 1, time.Now())
	venom := hit(l, KindHat, "VENOM")
	if venom == nil || !venom.Current || venom.Action.Request.Cmd != "on" || venom.Action.Request.Hat != "venom" {
		t.Fatalf("venom: %+v", venom)
	}
	review := hit(l, KindActivity, "review")
	if review == nil || !review.Current || review.Action.Request.Activity != "review" || !review.Action.Request.HasActivity {
		t.Fatalf("review: %+v", review)
	}
	if hit(l, KindActivity, "development").Current {
		t.Fatal("only the worn activity is marked")
	}
	if !strings.Contains(hit(l, KindHat, "Private").Hint, "Private") {
		t.Fatal("hat hint names the hat")
	}
	if h := hit(l, KindActivity, "garden").Hint; !strings.Contains(h, "Private") || !strings.Contains(h, "garden") {
		t.Fatalf("activity hint: %q", h)
	}
	if hit(l, KindTool, "Pause") == nil || hit(l, KindTool, "Resume") != nil {
		t.Fatal("wearing shows Pause")
	}
	if hit(l, KindTool, "Open HatRack") == nil || hit(l, KindTool, "Take off") == nil {
		t.Fatal("open and take-off tools")
	}
	if hit(l, KindTool, "About") != nil {
		t.Fatal("About lives in the tray menu")
	}
	for i, a := range l.Hits {
		for j, b := range l.Hits {
			if i < j && a.R.X < b.R.X+b.R.W && b.R.X < a.R.X+a.R.W && a.R.Y < b.R.Y+b.R.H && b.R.Y < a.R.Y+a.R.H {
				t.Fatalf("overlap: %q and %q", a.Label, b.Label)
			}
		}
		if a.R.X < 0 || a.R.Y < 0 || a.R.X+a.R.W > l.W || a.R.Y+a.R.H > l.H {
			t.Fatalf("%q outside the panel: %+v of %dx%d", a.Label, a.R, l.W, l.H)
		}
	}
}

func TestBuildPausedAndFilter(t *testing.T) {
	i18n.Set("en")
	l := Build(testSnap(core.ModePaused, "venom", ""), "", 1, time.Now())
	if hit(l, KindTool, "Resume") == nil || hit(l, KindTool, "Pause") != nil {
		t.Fatal("paused shows Resume")
	}
	if h := hit(l, KindHat, "VENOM"); h.Action.Request.Hat != "" || h.Action.Request.Cmd != "on" {
		t.Fatalf("clicking the paused hat resumes: %+v", h.Action.Request)
	}
	f := Build(testSnap(core.ModeNone, "", ""), "gar", 1, time.Now())
	if hit(f, KindHat, "VENOM") != nil || hit(f, KindHat, "Private") == nil {
		t.Fatal("filter keeps only hats with a match")
	}
	if !hit(f, KindActivity, "garden").Match {
		t.Fatal("matching activity is marked")
	}
	if i := f.FirstFocus(); !f.Hits[i].Match {
		t.Fatalf("first focus is the match: %+v", f.Hits[i])
	}
	if e := Build(testSnap(core.ModeNone, "", ""), "zzz", 1, time.Now()); len(e.Hits) == 0 || hit(e, KindTool, "Open HatRack") == nil {
		t.Fatal("a filter without matches still has the toolbar")
	}
}

func TestRenderAndNavigation(t *testing.T) {
	i18n.Set("en")
	for _, scale := range []float64{1, 2} {
		l := Build(testSnap(core.ModeWearing, "venom", "review"), "", scale, time.Now())
		img := Render(l, View{Hover: 1, Focus: l.FirstFocus()})
		if img.Bounds().Dx() != l.W || img.Bounds().Dy() != l.H {
			t.Fatal("image size")
		}
	}
	l := Build(testSnap(core.ModeWearing, "venom", "review"), "", 1, time.Now())
	f := l.FirstFocus()
	if l.Hits[f].Label != "VENOM" {
		t.Fatalf("focus starts on the current hat, got %q", l.Hits[f].Label)
	}
	n := l.Step(f, 1)
	if n == f || l.Step(n, -1) != f {
		t.Fatal("step forward and back")
	}
	if l.HintFor(-1, -1) != l.Default || l.HintFor(f, -1) == l.Default {
		t.Fatal("hint falls back to the default")
	}
	a := BuildAbout("1.2.3", "https://example.org", 1)
	if a.IconRect == nil || len(a.Hits) != 1 || a.Hits[0].Action.Link != "https://example.org" {
		t.Fatalf("about: %+v", a)
	}
	Render(a, View{Hover: -1, Focus: 0})
}

func TestScreenKeys(t *testing.T) {
	i18n.Set("en")
	s := &screen{o: Options{Version: "x", ProjectURL: "u"}, snap: testSnap(core.ModeNone, "", ""), scale: 1}
	s.rebuild()
	if _, done := s.key("", 'p'); done || s.filter != "p" || hit(s.l, KindHat, "VENOM") != nil {
		t.Fatalf("typing filters: %q", s.filter)
	}
	s.key("backspace", 0)
	if s.filter != "" {
		t.Fatal("backspace")
	}
	act, done := s.key("enter", 0)
	if !done || act.Request == nil || act.Request.Hat != "venom" {
		t.Fatalf("enter on first hat: %+v", act)
	}
	if _, done := s.key("escape", 0); !done {
		t.Fatal("escape closes")
	}
}

// The X11 test needs Xvfb, xdotool and ImageMagick; it never touches the real
// display.
func TestX11(t *testing.T) {
	for _, tool := range []string{"Xvfb", "xdotool", "import"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	i18n.Set("en")
	disp := ":97"
	xvfb := exec.Command("Xvfb", disp, "-screen", "0", "1280x800x24")
	if err := xvfb.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { xvfb.Process.Kill(); xvfb.Wait() }()
	t.Setenv("DISPLAY", disp)
	for i := 0; i < 50; i++ {
		if exec.Command("xdotool", "getdisplaygeometry").Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	xdo := func(args ...string) string {
		out, err := exec.Command("xdotool", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("xdotool %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	type result struct {
		act Action
		err error
	}
	run := func(snap agent.Snapshot, w Where, about bool) chan result {
		ch := make(chan result, 1)
		go func() {
			a, err := Run(context.Background(), Options{Snapshot: func() agent.Snapshot { return snap }, Where: w, About: about, Version: "t", ProjectURL: "u"})
			ch <- result{a, err}
		}()
		time.Sleep(600 * time.Millisecond)
		return ch
	}
	wait := func(ch chan result) result {
		select {
		case r := <-ch:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("panel did not close")
			return result{}
		}
	}
	snap := testSnap(core.ModeWearing, "venom", "review")
	l := Build(snap, "", 1, time.Now())

	// Escape closes without an action.
	ch := run(snap, Centered, false)
	if shot := os.Getenv("HATRACK_PANEL_SHOT"); shot != "" {
		exec.Command("import", "-window", "root", shot).Run()
	}
	xdo("key", "Escape")
	if r := wait(ch); r.err != nil || r.act != (Action{}) {
		t.Fatalf("escape: %+v", r)
	}

	// A click outside closes.
	ch = run(snap, Centered, false)
	xdo("mousemove", "3", "3", "click", "1")
	if r := wait(ch); r.err != nil || r.act != (Action{}) {
		t.Fatalf("click outside: %+v", r)
	}

	// A click on an activity chip returns its request. The window is centred
	// on the 1280x800 screen, so the chip's screen position follows.
	ch = run(snap, Centered, false)
	ox, oy := 640-l.W/2, 400-l.H/2-80
	chip := hit(l, KindActivity, "meeting")
	xdo("mousemove", strconv.Itoa(ox+chip.R.X+chip.R.W/2), strconv.Itoa(oy+chip.R.Y+chip.R.H/2), "click", "1")
	r := wait(ch)
	if r.err != nil || r.act.Request == nil || r.act.Request.Activity != "meeting" || r.act.Request.Hat != "venom" {
		t.Fatalf("chip click: %+v", r)
	}

	// Typing filters, Enter picks the first match.
	ch = run(snap, Centered, false)
	xdo("type", "pri")
	xdo("key", "Return")
	r = wait(ch)
	if r.err != nil || r.act.Request == nil || r.act.Request.Hat != "private" {
		t.Fatalf("type and enter: %+v", r)
	}
}
