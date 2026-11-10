package server

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

func TestGlance(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e)
	at := func(sec int) time.Time { return e.now.Add(-30*time.Minute + time.Duration(sec)*time.Second) }
	span := func(from, to int, app, title string) core.Observation {
		return core.Observation{ID: core.NewID(), Sensor: "window", Start: at(from), End: at(to), Attrs: core.Attrs{"app": app, "title": title}}
	}
	term := "bert@laptop: ~/hatrack"
	if n, _ := e.postObs(
		span(0, 120, "Guake", term),
		span(120, 140, "SshAskpass", "OpenSSH"),
		span(140, 200, "Guake", term),
		span(200, 203, "teams", "Chat"),
		span(203, 260, "Guake", term),
	); n != 200 {
		t.Fatalf("observations: %d", n)
	}
	// one row for the terminal, the side windows named at it
	_, out := b.do("GET", "/assign?tab=day&day=2026-11-04", nil)
	if strings.Count(out, term) != 1 || !strings.Contains(out, "incl. 2 side windows, 23 s") ||
		!strings.Contains(out, "Counted for this window: SshAskpass 20 s, teams 3 s") {
		t.Fatalf("row:\n%s", out[strings.Index(out, `<table class="test">`):])
	}

	// the tab shows the defaults
	_, out = b.do("GET", "/assign?tab=glance", nil)
	if !strings.Contains(out, `name="seconds" min="1" max="60" value="5"`) || !strings.Contains(out, "KeePassXC") || strings.Contains(out, "Restore defaults") {
		t.Fatalf("tab:\n%s", out)
	}
	save := func(f url.Values) string {
		t.Helper()
		f.Set("op", "glance")
		r, _ := b.do("POST", "/assign", f)
		if r.StatusCode != 303 {
			t.Fatalf("save: %d", r.StatusCode)
		}
		u, _ := url.Parse(r.Header.Get("Location"))
		return u.Query().Get("e")
	}
	// the defaults stay unset
	if msg := save(url.Values{"seconds": {"5"}, "helpers": {strings.Join(core.DefaultHelpers, "\n")}}); msg != "" {
		t.Fatal(msg)
	}
	if o := config(t, e).Observe; o.Helpers != nil || o.GlanceSeconds != 0 {
		t.Fatalf("defaults stored: %+v", o)
	}
	// an own list and an empty one
	save(url.Values{"seconds": {"10"}, "helpers": {" gearlever \n\nGearlever\nSshAskpass"}})
	if o := config(t, e).Observe; o.Helpers == nil || strings.Join(*o.Helpers, ",") != "gearlever,SshAskpass" || o.GlanceSeconds != 10 {
		t.Fatalf("own: %+v", o)
	}
	save(url.Values{"seconds": {"10"}, "helpers": {""}})
	if o := config(t, e).Observe; o.Helpers == nil || len(*o.Helpers) != 0 {
		t.Fatalf("empty list: %+v", o)
	}
	// without helpers the password prompt keeps its row
	_, out = b.do("GET", "/assign?tab=day&day=2026-11-04", nil)
	if !strings.Contains(out, "OpenSSH") {
		t.Error("with no helpers, SshAskpass has a row")
	}
	if msg := save(url.Values{"seconds": {"61"}}); msg == "" {
		t.Error("61 s accepted")
	}
	save(url.Values{"reset": {"1"}})
	if o := config(t, e).Observe; o.Helpers != nil || o.GlanceSeconds != 0 {
		t.Fatalf("reset: %+v", o)
	}
}
