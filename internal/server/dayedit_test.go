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

func TestDayWindow(t *testing.T) {
	day := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC) // a Wednesday
	w := core.DefaultWorkHours
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	hm := func(t time.Time) string { return t.Format("15:04") }
	iv := func(s, e time.Time) []core.Interval { return []core.Interval{{Hat: "x", Start: s, End: e}} }

	f, to, _, _, wd := dayWindow(day, w, nil)
	if hm(f) != "08:00" || hm(to) != "18:00" || !wd {
		t.Fatalf("empty workday: %s-%s", hm(f), hm(to))
	}
	f, to, _, _, _ = dayWindow(day, w, iv(at(9, 0), at(17, 0)))
	if hm(f) != "08:00" || hm(to) != "18:00" {
		t.Fatalf("inside: %s-%s", hm(f), hm(to))
	}
	f, to, _, _, _ = dayWindow(day, w, iv(at(6, 40), at(19, 10)))
	if hm(f) != "06:00" || hm(to) != "20:00" {
		t.Fatalf("widened to whole hours: %s-%s", hm(f), hm(to))
	}
	f, to, _, _, wd = dayWindow(day.AddDate(0, 0, 3), w, iv(day.AddDate(0, 0, 3).Add(10*time.Hour+15*time.Minute), day.AddDate(0, 0, 3).Add(11*time.Hour)))
	if hm(f) != "10:00" || hm(to) != "11:00" || wd {
		t.Fatalf("Saturday: %s-%s %v", hm(f), hm(to), wd)
	}
}

func TestDayWorkHours(t *testing.T) {
	e, b := setupBrowser(t)
	_, out := b.do("GET", "/day?date=2026-11-04", nil)
	if !strings.Contains(out, "Working hours 08:00–18:00") || strings.Contains(out, `class="off"`) {
		t.Fatal("a day inside the working hours shows them, without hatching")
	}
	r, _ := b.do("POST", "/setup/defaults", url.Values{"work_start": {"07:30"}, "work_end": {"16:00"}, "work_day": {"mon", "tue"}})
	if r.StatusCode != 303 {
		t.Fatalf("save: %d", r.StatusCode)
	}
	c, _, _ := e.st.GetConfig(context.Background())
	if w := c.Work(); w.Start != "07:30" || strings.Join(w.Days, ",") != "mon,tue" {
		t.Fatalf("saved: %+v", w)
	}
	// 2026-11-04 is a Wednesday: now a day off
	if _, out := b.do("GET", "/day?date=2026-11-04", nil); !strings.Contains(out, "Day off") || !strings.Contains(out, `class="off"`) {
		t.Fatal("day off")
	}
	if r, _ := b.do("POST", "/setup/defaults", url.Values{"work_start": {"07:30"}, "work_end": {"16:00"}}); r.StatusCode != 400 {
		t.Fatalf("no days: %d", r.StatusCode)
	}
	if r, _ := b.do("POST", "/setup/defaults", url.Values{"work_start": {"17:00"}, "work_end": {"16:00"}, "work_day": {"mon"}}); r.StatusCode != 400 {
		t.Fatalf("end before start: %d", r.StatusCode)
	}
}

func TestWebDetails(t *testing.T) {
	e, b := setupBrowser(t)
	b.do("POST", "/action", url.Values{"op": {"on"}, "hat": {"venom"}, "activity": {"review"}})
	e.now = e.now.Add(20 * time.Minute)
	if r, _ := b.do("POST", "/action", url.Values{"op": {"detail"}, "detail": {"https://jira.example.org/browse/TAG-1366"}}); r.StatusCode != 303 {
		t.Fatalf("detail: %d", r.StatusCode)
	}
	st, _ := e.st.CurrentState(context.Background())
	if st.Detail == "" || !st.Since.Equal(e.now.Add(-20*time.Minute)) {
		t.Fatalf("first detail names the running entry: %+v", st)
	}
	_, out := b.do("GET", "/", nil)
	if !strings.Contains(out, `<a class="ticket" href="https://jira.example.org/browse/TAG-1366"`) || !strings.Contains(out, ">TAG-1366</a>") {
		t.Fatal("Now shows the key, linked")
	}
	// a changed detail starts a new entry; the old one is offered again
	e.now = e.now.Add(10 * time.Minute)
	b.do("POST", "/action", url.Values{"op": {"detail"}, "detail": {"TAG-7"}})
	e.now = e.now.Add(10 * time.Minute)
	_, out = b.do("GET", "/", nil)
	if !strings.Contains(out, `class="recent"`) || !strings.Contains(out, ">TAG-1366</button>") {
		t.Fatal("recent details as one-click chips")
	}
	if !strings.Contains(out, `href="https://jira.example.org/browse/TAG-7"`) {
		t.Fatal("a plain key of a learned project is linked")
	}
	_, out = b.do("GET", "/day?date=2026-11-04", nil)
	if !strings.Contains(out, `class="dets"`) || !strings.Contains(out, `value="TAG-1366"`) {
		t.Fatal("Day: activity unfolds into details; the edit form holds the short detail")
	}
	_, csvOut := b.do("GET", "/week.csv?date=2026-11-04", nil)
	if !strings.Contains(csvOut, ",TAG-1366\n") || !strings.Contains(csvOut, ",TAG-7\n") {
		t.Fatalf("CSV note column:\n%s", csvOut)
	}
	b.do("POST", "/action", url.Values{"op": {"detail-clear"}})
	if st, _ := e.st.CurrentState(context.Background()); st.Detail != "" {
		t.Fatal("clear")
	}
}

func TestTicketLinksSetup(t *testing.T) {
	e, b := setupBrowser(t)
	b.do("POST", "/action", url.Values{"op": {"on"}, "hat": {"venom"}, "activity": {"review"}})
	b.do("POST", "/action", url.Values{"op": {"detail"}, "detail": {"https://wrong.example/browse/TAG-1"}})
	_, out := b.do("GET", "/setup", nil)
	if !strings.Contains(out, "https://wrong.example/browse/") {
		t.Fatal("learned link listed")
	}
	b.do("POST", "/setup/tickets", url.Values{"project": {"TAG"}})
	if links, _ := e.st.TicketLinks(context.Background()); len(links) != 0 {
		t.Fatal("reset")
	}
	// the next link teaches it again
	e.now = e.now.Add(5 * time.Minute)
	b.do("POST", "/action", url.Values{"op": {"detail"}, "detail": {"https://jira.example.org/browse/TAG-2"}})
	if links, _ := e.st.TicketLinks(context.Background()); links["TAG"] != "https://jira.example.org/browse/" {
		t.Fatalf("relearned: %v", links)
	}
}
