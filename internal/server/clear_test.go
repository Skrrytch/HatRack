package server

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClearObservations(t *testing.T) {
	e, b := setupBrowser(t)
	rulesDay(t, e) // spans from now-170 to now-35 min, events too
	evBefore, _ := e.st.Effective(t.Context(), time.Time{})
	_, out := b.do("GET", "/setup/grants?tab=basics", nil)
	if !strings.Contains(out, `id="clear"`) || !strings.Contains(out, "Stored: 6 observations") {
		t.Fatalf("card:\n%s", out[strings.Index(out, `id="privacy"`):])
	}
	post := func(until string) (int, string) {
		t.Helper()
		r, _ := b.do("POST", "/setup/grants", url.Values{"op": {"clear"}, "tab": {"basics"}, "until": {until}})
		u, _ := url.Parse(r.Header.Get("Location"))
		return r.StatusCode, u.Query().Get("e") + u.Query().Get("cleared")
	}
	at := func(m int) string { return e.now.Add(time.Duration(m) * time.Minute).UTC().Format(clearLayout) }
	if _, msg := post(at(60)); msg != "The moment must not lie in the future." {
		t.Fatalf("future: %q", msg)
	}
	if _, msg := post("yesterday"); msg == "" {
		t.Fatal("bad date accepted")
	}
	// up to an hour ago: the spans that ended by then go
	if code, n := post(at(-60)); code != 303 || n != "4" {
		t.Fatalf("clear: %d %q", code, n)
	}
	obs, _ := e.st.ObservationsBetween(t.Context(), time.Time{}, time.Time{}, "", "")
	if len(obs) != 2 {
		t.Fatalf("left: %+v", obs)
	}
	// everything up to now; the log stays
	if _, n := post(at(0)); n != "2" {
		t.Fatalf("clear all: %q", n)
	}
	_, out = b.do("GET", "/setup/grants?tab=basics&cleared=2", nil)
	if !strings.Contains(out, "2 observations deleted.") || !strings.Contains(out, "No observations stored.") {
		t.Error("message or empty state missing")
	}
	if evAfter, _ := e.st.Effective(t.Context(), time.Time{}); len(evAfter) != len(evBefore) {
		t.Errorf("events changed: %d -> %d", len(evBefore), len(evAfter))
	}
}
