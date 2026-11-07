package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

var obsDay = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

func at(h, m int) time.Time {
	return obsDay.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func win(dev, app, title string, a, b time.Time) core.Observation {
	attrs := core.Attrs{"app": app}
	if title != "" {
		attrs["title"] = title
	}
	return core.Observation{Device: dev, Sensor: "window", Start: a, End: b, Attrs: attrs}
}

func idleSpan(dev, kind string, a, b time.Time) core.Observation {
	return core.Observation{Device: dev, Sensor: "idle", Start: a, End: b, Attrs: core.Attrs{"kind": kind}}
}

func build(obs []core.Observation, write bool) *obsView {
	obs = obsClip(obsDay, obsDay.AddDate(0, 0, 1), obs)
	return buildObs(obsDay, at(8, 0), at(20, 0), obs, nil, "en", write)
}

func TestObsMerge(t *testing.T) {
	v := build([]core.Observation{
		win("a", "Firefox", "Jira", at(9, 0), at(9, 10)),
		win("a", "firefox", "Docs", at(9, 10), at(9, 40)), // same app, other case and title: merged
		win("a", "firefox", "Docs", at(9, 40), at(9, 50)),
		win("a", "Code", "x.go", at(9, 50), at(10, 0)), // another app
		win("a", "Code", "y.go", at(10, 1), at(10, 5)), // gap of exactly 1 minute: no merge
		win("a", "Code", "y.go", at(10, 5), at(10, 7)), // no gap: merged
		win("a", "Code", "y.go", at(10, 8), at(10, 9)), // gap of 1 minute: no merge
	}, true)
	var labels []string
	for _, b := range v.Rows[0].Blocks {
		labels = append(labels, b.Label)
	}
	if got := strings.Join(labels, ","); got != "Firefox,Code,Code,Code" {
		t.Fatalf("blocks: %s", got)
	}
	ff := v.Rows[0].Blocks[0]
	if ff.Start != "09:00" || ff.End != "09:50" {
		t.Fatalf("merged range: %s-%s", ff.Start, ff.End)
	}
	// titles by time: Docs (40m) before Jira (10m)
	want := "09:00–09:50 · 50m\nFirefox\n– Docs (40m)\n– Jira (10m)"
	if ff.Title != want {
		t.Fatalf("tooltip:\n%s", ff.Title)
	}
	if v.ShowDevice {
		t.Fatal("one device: no label")
	}
}

func TestObsTopTitles(t *testing.T) {
	v := build([]core.Observation{
		win("a", "Firefox", "one", at(9, 0), at(9, 5)),
		win("a", "Firefox", "two", at(9, 5), at(9, 25)),
		win("a", "Firefox", "three", at(9, 25), at(9, 40)),
		win("a", "Firefox", "four", at(9, 40), at(9, 50)),
	}, false)
	b := v.Rows[0].Blocks[0]
	if b.Start != "" || strings.Contains(b.Title, "Click") {
		t.Fatal("read-only: not clickable")
	}
	if strings.Contains(b.Title, "– one") || !strings.Contains(b.Title, "– two (20m)\n– three (15m)\n– four (10m)") {
		t.Fatalf("top three titles: %q", b.Title)
	}
}

func TestObsIdleOverTotals(t *testing.T) {
	v := build([]core.Observation{
		win("a", "Firefox", "x", at(9, 0), at(11, 0)),
		idleSpan("a", "locked", at(10, 0), at(10, 45)),
		win("b", "Firefox", "x", at(9, 0), at(9, 30)), // another device: not subtracted
		win("a", "Code", "", at(12, 0), at(12, 20)),
	}, true)
	if !v.ShowDevice || v.Rows[0].Device != "a" || v.Rows[1].Device != "b" {
		t.Fatal("device rows")
	}
	blocks := v.Rows[0].Blocks
	last := blocks[len(blocks)-1]
	if !last.Away || last.Start != "" || last.Title != "10:00–10:45 · 45m\naway (locked)" {
		t.Fatalf("away block last, not clickable: %+v", last)
	}
	// Firefox: 120 - 45 + 30 = 105 min; Code 20 min
	if len(v.Legend) != 2 || v.Legend[0].Name != "Firefox" || v.Legend[0].Duration != "1h 45m" || v.Legend[1].Duration != "20m" {
		t.Fatalf("legend: %+v", v.Legend)
	}
	if v.Away == nil || v.Away.Duration != "45m" || v.Private != nil {
		t.Fatalf("away %+v private %+v", v.Away, v.Private)
	}
}

func TestObsOthersAndPrivate(t *testing.T) {
	var obs []core.Observation
	for i := 0; i < 10; i++ {
		obs = append(obs, win("a", "app"+string(rune('a'+i)), "", at(9, i*10), at(9, i*10+10-i%2)))
	}
	obs = append(obs, core.Observation{Device: "a", Sensor: "window", Start: at(11, 0), End: at(11, 5), Attrs: core.Attrs{core.AttrPrivate: "1"}})
	v := build(obs, true)
	if len(v.Legend) != 9 || v.Legend[8].Name != "others" {
		t.Fatalf("legend: %+v", v.Legend)
	}
	if v.Private == nil || v.Private.Duration != "5m" {
		t.Fatalf("private: %+v", v.Private)
	}
	p := v.Rows[0].Blocks[10]
	if !p.Private || p.Label != "" || p.Title != "11:00–11:05 · 5m\nprivate" {
		t.Fatalf("private block: %+v", p)
	}
}

func TestObsClipAndRange(t *testing.T) {
	next := obsDay.AddDate(0, 0, 1)
	got := obsClip(obsDay, next, []core.Observation{
		win("a", "Night", "", obsDay.Add(-2*time.Hour), at(1, 0)),
		win("a", "Late", "", at(23, 0), next.Add(3*time.Hour)),
		win("a", "Before", "", obsDay.Add(-2*time.Hour), obsDay),
		{Device: "a", Sensor: "other", Start: at(1, 0), End: at(2, 0)},
	})
	if len(got) != 2 || !got[0].Start.Equal(obsDay) || !got[1].End.Equal(next) {
		t.Fatalf("clipped: %+v", got)
	}
	for _, c := range []struct {
		s, e   time.Time
		ws, we string
	}{
		{at(9, 40).Add(20 * time.Second), at(9, 55).Add(5 * time.Second), "09:40", "09:56"},
		{at(9, 40), at(9, 55), "09:40", "09:55"},
		{at(23, 30), next, "23:30", "23:59"},
		{at(10, 0).Add(10 * time.Second), at(10, 0).Add(20 * time.Second), "10:00", "10:01"},
		{at(23, 59), next, "23:58", "23:59"},
	} {
		if s, e := obsClock(obsDay, c.s, c.e); s != c.ws || e != c.we {
			t.Errorf("%v-%v: %s-%s, want %s-%s", c.s, c.e, s, e, c.ws, c.we)
		}
	}
}

func TestDayPageObserved(t *testing.T) {
	e, b := setupBrowser(t)
	_, out := b.do("GET", "/day?date=2026-11-04", nil)
	if strings.Contains(out, "obsband") || strings.Contains(out, "Observed") {
		t.Fatal("no observations, no band")
	}
	ctx := context.Background()
	e.st.PutObservations(ctx, "laptop", []core.Observation{
		{ID: "1", Sensor: "window", Start: at(19, 30), End: at(19, 40), Attrs: core.Attrs{"app": "Firefox", "title": "Secret Plan"}},
		{ID: "2", Sensor: "window", Start: at(19, 40), End: at(19, 45), Attrs: core.Attrs{core.AttrPrivate: "1"}},
		{ID: "3", Sensor: "idle", Start: at(19, 45), End: at(19, 50), Attrs: core.Attrs{"kind": "inactive"}},
	})
	_, out = b.do("GET", "/day?date=2026-11-04", nil)
	for _, want := range []string{"obsband", "Observed", `<span style="left:100.000%">20</span>`, `data-start="19:30"`, `data-end="19:40"`, "Firefox", "away (inactive)"} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(out, "obsdev") {
		t.Error("one device: no device label")
	}
	// the add form knows the booked entries for its "replaces" note
	if !strings.Contains(out, `<script type="application/json" id="booked">[{"s":`) || !strings.Contains(out, `data-prefix="Replaces: %s"`) {
		t.Error("booked ranges or replaces note missing")
	}
	// the private block names neither app nor title
	i := strings.Index(out, `data-tip="19:40–19:45`)
	if i < 0 {
		t.Fatal("private block missing")
	}
	blk := out[i : i+strings.Index(out[i:], "</")]
	if strings.Contains(blk, "Firefox") || strings.Contains(blk, "Secret") || !strings.Contains(blk, "private") {
		t.Fatalf("private block: %s", blk)
	}
	e.st.PutObservations(ctx, "desktop", []core.Observation{
		{ID: "4", Sensor: "window", Start: at(9, 0), End: at(9, 10), Attrs: core.Attrs{"app": "Code"}},
	})
	_, out = b.do("GET", "/day?date=2026-11-04", nil)
	if !strings.Contains(out, `class="obsdev">desktop<`) || !strings.Contains(out, `class="obsdev">laptop<`) {
		t.Error("two devices: labels")
	}
	var many []core.Observation
	for i := 0; i < 9; i++ {
		many = append(many, core.Observation{ID: "m" + string(rune('a'+i)), Sensor: "window", Start: at(12, i*5), End: at(12, i*5+5), Attrs: core.Attrs{"app": "app" + string(rune('a'+i))}})
	}
	e.st.PutObservations(ctx, "desktop", many)
	_, out = b.do("GET", "/day?date=2026-11-04", nil)
	if strings.Contains(out, "ZgotmplZ") || !strings.Contains(out, "--c:#c3c6ca") {
		t.Error("the colour of the others key must survive the template")
	}
}

func TestObsColorMap(t *testing.T) {
	// firefox and zoom share a hash slot; the busier one keeps it
	m := obsColorMap([]string{"firefox", "zoom"})
	if m["firefox"] == m["zoom"] {
		t.Fatalf("same colour: %v", m)
	}
	if obsColorMap([]string{"zoom"})["zoom"] != m["firefox"] {
		t.Fatalf("zoom alone should get the slot firefox had: %v", m)
	}
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	m = obsColorMap(keys)
	seen := map[string]bool{}
	for _, k := range keys[:8] {
		seen[m[k]] = true
	}
	if len(seen) != 8 || m["i"] == "" {
		t.Fatalf("colours: %v", m)
	}
}

func TestObsWhat(t *testing.T) {
	iv := []core.Interval{
		{Hat: "venom", Activity: "dev", Start: at(9, 0), End: at(10, 0)},
		{Hat: "intern", Start: at(10, 0), End: at(10, 0).Add(30 * time.Second)}, // fleeting: ignored
		{Hat: "intern", Activity: "mail", Start: at(11, 0), End: at(12, 0), Open: true},
	}
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{at(8, 0), ""},             // nothing before
		{at(9, 30), "venom|dev"},   // booked then
		{at(10, 30), "venom|dev"},  // in a gap: what ran before
		{at(13, 0), "intern|mail"}, // the open entry goes on
	} {
		if got := obsWhat(iv, c.t); got != c.want {
			t.Errorf("%s: %q, want %q", c.t.Format("15:04"), got, c.want)
		}
	}
	// a block gets the hat booked at its middle
	v := buildObs(obsDay, at(8, 0), at(20, 0), []core.Observation{win("a", "Code", "x", at(9, 50), at(10, 20))}, iv, "en", true)
	if w := v.Rows[0].Blocks[0].What; w != "venom|dev" {
		t.Fatalf("block what: %q", w)
	}
}
