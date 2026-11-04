package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

// intervals of the seeded day as "hat/act HH:MM-HH:MM", open ones with "+".
func dayLine(t *testing.T, e *env) string {
	t.Helper()
	ev, err := e.st.Effective(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, i := range core.Intervals(ev, e.now) {
		s := fmt.Sprintf("%s/%s %s-%s", i.Hat, i.Activity, i.Start.Format("15:04"), i.End.Format("15:04"))
		if i.Open {
			s += "+"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func idOf(t *testing.T, e *env, hat string) string {
	ev, _ := e.st.Effective(context.Background(), time.Time{})
	for _, i := range core.Intervals(ev, e.now) {
		if i.Hat == hat {
			return i.StartID
		}
	}
	t.Fatalf("no interval of %s", hat)
	return ""
}

func TestDayEdit(t *testing.T) {
	e, b := setupBrowser(t)
	start := "venom/development 09:00-10:00, venom/review 10:00-11:00, tag/ 11:00-11:30"
	if got := dayLine(t, e); got != start {
		t.Fatalf("seed: %s", got)
	}
	_, out := b.do("GET", "/day", nil)
	if !strings.Contains(out, `name="op" value="change"`) {
		t.Fatal("the day has edit forms")
	}
	post := func(v url.Values) string {
		t.Helper()
		v.Set("date", "2026-11-04")
		r, _ := b.do("POST", "/day/edit", v)
		if r.StatusCode != 303 {
			t.Fatalf("%v: %d", v, r.StatusCode)
		}
		u, _ := url.Parse(r.Header.Get("Location"))
		if m := u.Query().Get("e"); m != "" {
			t.Fatalf("%v: %s", v, m)
		}
		return u.Query().Get("done")
	}
	// tag began at 10:45: review ends earlier
	done := post(url.Values{"op": {"change"}, "id": {idOf(t, e, "tag")}, "start": {"10:45"}, "end": {"11:30"},
		"what": {"tag|"}, "shown_start": {"11:00"}, "shown_end": {"11:30"}})
	if got := dayLine(t, e); got != "venom/development 09:00-10:00, venom/review 10:00-10:45, tag/ 10:45-11:30" {
		t.Fatalf("change: %s", got)
	}
	_, out = b.do("GET", "/day?date=2026-11-04&done="+done, nil)
	if !strings.Contains(out, `value="undo"`) {
		t.Fatal("undo offered after a change")
	}
	post(url.Values{"op": {"undo"}, "id": {done}})
	if got := dayLine(t, e); got != start {
		t.Fatalf("undo: %s", got)
	}
	// fill the gap until now with review
	post(url.Values{"op": {"insert"}, "start": {"11:30"}, "end": {"12:00"}, "what": {"venom|review"}})
	if got := dayLine(t, e); got != start+", venom/review 11:30-12:00" {
		t.Fatalf("fill: %s", got)
	}
	// "since 11:45 I am at tag": open, and the current state follows
	post(url.Values{"op": {"insert"}, "start": {"11:45"}, "end": {""}, "what": {"tag|"}})
	if got := dayLine(t, e); got != start+", venom/review 11:30-11:45, tag/ 11:45-12:00+" {
		t.Fatalf("since: %s", got)
	}
	if st, _ := e.st.CurrentState(context.Background()); st.Hat != "tag" || st.Mode != core.ModeWearing {
		t.Fatalf("state: %+v", st)
	}
	// delete the first tag entry: a gap
	post(url.Values{"op": {"delete"}, "id": {idOf(t, e, "tag")}})
	if got := dayLine(t, e); got != "venom/development 09:00-10:00, venom/review 10:00-11:00, venom/review 11:30-11:45, tag/ 11:45-12:00+" {
		t.Fatalf("delete: %s", got)
	}
	if _, out := b.do("GET", "/day?date=2026-11-04", nil); !strings.Contains(out, "nothing recorded") {
		t.Fatal("a gap between entries is shown")
	}
	// errors come back as messages
	r, _ := b.do("POST", "/day/edit", url.Values{"date": {"2026-11-04"}, "op": {"insert"}, "start": {"13:00"}, "end": {"14:00"}, "what": {"tag|"}})
	if u, _ := url.Parse(r.Header.Get("Location")); u.Query().Get("e") != "That time is in the future." {
		t.Fatalf("future: %s", r.Header.Get("Location"))
	}
	r, _ = b.do("POST", "/day/edit", url.Values{"date": {"2026-11-04"}, "op": {"insert"}, "start": {"9h"}, "what": {"tag|"}})
	if u, _ := url.Parse(r.Header.Get("Location")); u.Query().Get("e") == "" {
		t.Fatal("a bad time is an error")
	}
	// read-only access cannot edit
	ro := newBrowser(t, e)
	ro.do("GET", "/?token="+e.token, nil)
	if r, _ := ro.do("POST", "/day/edit", url.Values{"op": {"delete"}, "id": {"x"}}); r.StatusCode != 403 {
		t.Fatalf("read-only edit: %d", r.StatusCode)
	}
	if _, out := ro.do("GET", "/day", nil); strings.Contains(out, `value="change"`) {
		t.Fatal("no edit forms without login")
	}
}
