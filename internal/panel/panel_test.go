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

func TestTypedDetail(t *testing.T) {
	i18n.Set("en")
	snap := testSnap(core.ModeWearing, "venom", "review")
	snap.Hats[0].Activities = append(snap.Hats[0].Activities, "code review")
	for _, c := range []struct{ in, filter, detail string }{
		{"rev", "rev", ""},
		{"rev PR 482", "rev", "PR 482"},
		{"code rev TAG-1", "code rev", "TAG-1"},
		{"zzz x", "zzz x", ""},
	} {
		if f, d := splitQuery(snap, c.in); f != c.filter || d != c.detail {
			t.Errorf("%q: %q %q", c.in, f, d)
		}
	}
	l := Build(snap, "rev PR 482", 1, time.Now())
	h := hit(l, KindActivity, "review")
	if h == nil || h.Action.Request.Detail != "PR 482" || !strings.Contains(h.Hint, `Detail "PR 482"`) {
		t.Fatalf("activity with the typed detail: %+v", h)
	}
	if i := l.FirstFocus(); l.Hits[i].Label != "review" {
		t.Fatalf("Enter picks review: %q", l.Hits[i].Label)
	}
}

func recent(pairs ...string) []core.RecentDetail {
	var out []core.RecentDetail
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, core.RecentDetail{Activity: pairs[i], Detail: pairs[i+1]})
	}
	return out
}

func TestDetailChips(t *testing.T) {
	i18n.Set("en")
	snap := testSnap(core.ModeWearing, "venom", "review")
	snap.Status.State.Detail = "https://jira.example.org/browse/TAG-1366"
	snap.Hats[0].Recent = recent("review", "TAG-1366", "development", "MESH-1", "review", "PR 479", "review", "PR 470", "review", "PR 460")
	l := Build(snap, "", 1, time.Now())
	if hit(l, KindDetail, "TAG-1366") != nil || hit(l, KindDetail, "MESH-1") != nil || hit(l, KindDetail, "PR 460") != nil {
		t.Fatal("chips: only the two latest other details of the worn activity")
	}
	if h := hit(l, KindDetail, "PR 479"); h == nil || h.Action.Request.Cmd != "detail" || h.Action.Request.Detail != "PR 479" {
		t.Fatalf("recent detail chip: %+v", h)
	}
	if hit(l, KindDetail, "PR 470") == nil {
		t.Fatal("second chip")
	}
	// every card has a field; the worn one shows the current detail
	f := l.Hits[l.Field("venom")]
	if f.Kind != KindInput || f.Label != "TAG-1366" || f.Action.Edit != "venom" {
		t.Fatalf("worn field: %+v", f)
	}
	if g := l.Hits[l.Field("private")]; g.Label != "" || g.Sub == "" {
		t.Fatalf("other field: %+v", g)
	}
	Render(l, View{Hover: -1, Focus: l.FirstFocus()})
}

func TestSuggestions(t *testing.T) {
	snap := testSnap(core.ModeWearing, "venom", "review")
	snap.Status.State.Detail = "PR 479"
	h := snap.Hats[0]
	h.Recent = recent("development", "MESH-2129", "review", "PR 479", "development", "MESH-2075", "review", "TAG-1611", "review", "x MESH")
	var got []string
	for _, d := range Suggestions(h, snap.Status.State, "") {
		got = append(got, d.Detail)
	}
	if strings.Join(got, ",") != "TAG-1611,x MESH,MESH-2129,MESH-2075" {
		t.Fatalf("worn activity first, current left out: %v", got)
	}
	got = nil
	for _, d := range Suggestions(h, snap.Status.State, "mesh") {
		got = append(got, d.Detail)
	}
	if strings.Join(got, ",") != "MESH-2129,MESH-2075,x MESH" {
		t.Fatalf("prefix first, then anywhere: %v", got)
	}
	many := make([]core.RecentDetail, 20)
	for i := range many {
		many[i] = core.RecentDetail{Detail: "D" + strconv.Itoa(i)}
	}
	h.Recent = many
	if n := len(Suggestions(h, core.State{}, "")); n != maxSuggest {
		t.Fatalf("at most %d: %d", maxSuggest, n)
	}
}

func TestDetailField(t *testing.T) {
	i18n.Set("en")
	snap := testSnap(core.ModeWearing, "venom", "review")
	snap.Hats[0].Recent = recent("review", "TAG-1611", "development", "MESH-2129")
	snap.Hats[1].Recent = recent("garden", "roses")
	s := &screen{snap: snap, scale: 1}
	s.rebuild()
	h0 := s.l.H

	// Tab goes from the focused (worn) hat into its field
	s.key("next", 0)
	if s.edit.Hat != "venom" || !s.l.Hits[s.focus].Editing || s.l.SuggestCount() != 2 {
		t.Fatalf("tab into the field: %+v %d", s.edit, s.l.SuggestCount())
	}
	Render(s.l, s.view())
	// typing filters, arrows pick, Enter takes the pick
	s.key("", 'm')
	if s.l.SuggestCount() != 1 || s.l.Hits[s.l.Suggest(0)].Label != "MESH-2129" {
		t.Fatalf("filter: %d", s.l.SuggestCount())
	}
	s.key("down", 0)
	if !s.l.Hits[s.l.Suggest(0)].Selected {
		t.Fatal("down picks")
	}
	act, done := s.key("enter", 0)
	if !done || act.Request.Cmd != "detail" || act.Request.Detail != "MESH-2129" {
		t.Fatalf("enter takes the pick: %+v", act)
	}

	// free text on another hat puts it on with the detail
	s = &screen{snap: snap, scale: 1}
	s.rebuild()
	if _, done := s.activate(s.l.Field("private")); done || s.edit.Hat != "private" {
		t.Fatal("click into the field")
	}
	for _, r := range "pond" {
		s.key("", r)
	}
	act, done = s.key("enter", 0)
	if !done || act.Request.Cmd != "on" || act.Request.Hat != "private" || act.Request.Detail != "pond" {
		t.Fatalf("enter on another hat: %+v", act)
	}

	// a click on a suggestion takes it; Escape leaves the field, a second closes
	s = &screen{snap: snap, scale: 1}
	s.rebuild()
	s.activate(s.l.Field("private"))
	sg := s.l.Hits[s.l.Suggest(0)]
	if i := s.l.At(sg.R.X+2, sg.R.Y+2); i != s.l.Suggest(0) {
		t.Fatalf("suggestion is on top: %d", i)
	}
	if act, done := s.activate(s.l.Suggest(0)); !done || act.Request.Detail != "roses" || act.Request.Hat != "private" {
		t.Fatalf("click on a suggestion: %+v", act)
	}
	s.key("escape", 0)
	if s.edit.Hat != "" || s.l.Hits[s.focus].Kind != KindInput {
		t.Fatal("escape leaves the field and keeps the focus on it")
	}
	if _, done := s.key("escape", 0); !done {
		t.Fatal("second escape closes")
	}
	if s.l.H < h0 {
		t.Fatal("height")
	}

	// a paused hat resumes its activity with the new detail
	p := testSnap(core.ModePaused, "venom", "review")
	a := detailAction(p.Hats[0], p.Status.State, "X")
	if a.Request.Cmd != "on" || a.Request.Activity != "review" || !a.Request.HasActivity || a.Request.Detail != "X" {
		t.Fatalf("paused: %+v", a.Request)
	}
}
