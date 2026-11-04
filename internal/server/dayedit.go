package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// Editing the time log in the Day view. Every edit becomes one correction
// event (see core.PlanChange and friends); nothing is deleted, and every
// correction can be undone.

// choices lists what an entry can be: each active hat, alone and with each
// activity, plus the hats and activities of the day's entries, even if they
// are archived by now.
func choices(cfg core.Config, iv []core.Interval, lang string) []choiceGroup {
	var out []choiceGroup
	seen := map[string]bool{}
	add := func(g *choiceGroup, hat, act, label string) {
		v := hat + "|" + act
		if !seen[v] {
			seen[v] = true
			g.Options = append(g.Options, option{Value: v, Label: label})
		}
	}
	for _, h := range cfg.ActiveHats() {
		g := choiceGroup{Label: h.DisplayName()}
		add(&g, h.Key, "", h.DisplayName()+" – "+i18n.In(lang, "web.day.noactivity"))
		for _, a := range h.Activities {
			add(&g, h.Key, a, h.DisplayName()+" · "+a)
		}
		out = append(out, g)
	}
	extra := choiceGroup{Label: i18n.In(lang, "web.day.other")}
	for _, i := range iv {
		name := i.Hat
		if h, ok := cfg.Hat(i.Hat); ok {
			name = h.DisplayName()
		}
		label := name
		if i.Activity != "" {
			label += " · " + i.Activity
		}
		add(&extra, i.Hat, i.Activity, label)
	}
	if len(extra.Options) > 0 {
		out = append(out, extra)
	}
	return out
}

// clock reads "HH:MM" on the given day.
func clock(day time.Time, v string) (time.Time, bool) {
	t, err := time.ParseInLocation("15:04", strings.TrimSpace(v), day.Location())
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, day.Location()), true
}

func (s *Server) dayEdit(w http.ResponseWriter, r *http.Request, _ access) {
	ctx := r.Context()
	loc, lang := s.locOf(r), s.langOf(r)
	day := parseDate(r.PostFormValue("date"), loc, s.Now())
	back := "/day?date=" + dateStr(day)
	fail := func(msg string) { http.Redirect(w, r, back+"&e="+url.QueryEscape(msg), http.StatusSeeOther) }

	events, err := s.st.Effective(ctx, time.Time{})
	if err != nil {
		serverError(w, err)
		return
	}
	// undo needs the corrections themselves, so read the raw log for it
	in := core.Input{Device: "web", Now: s.Now()}
	op, id := r.PostFormValue("op"), r.PostFormValue("id")

	var ed core.Edit
	if op == "change" || op == "insert" {
		var ok bool
		if ed.Start, ok = clock(day, r.PostFormValue("start")); !ok {
			fail(i18n.In(lang, "web.day.badtime", r.PostFormValue("start")))
			return
		}
		if v := strings.TrimSpace(r.PostFormValue("end")); v != "" {
			if ed.End, ok = clock(day, v); !ok {
				fail(i18n.In(lang, "web.day.badtime", v))
				return
			}
			if !ed.End.After(ed.Start) {
				// over midnight only for a night entry of at most 12 hours
				// (22:00-02:00); 14:00-13:00 is a typo, not 23 hours
				if over := ed.End.AddDate(0, 0, 1); over.Sub(ed.Start) <= overnightMax {
					ed.End = over
				} else {
					fail(i18n.In(lang, "web.day.endbeforestart"))
					return
				}
			}
		}
		ed.Hat, ed.Activity, _ = strings.Cut(r.PostFormValue("what"), "|")
		ed.Detail = strings.TrimSpace(r.PostFormValue("detail"))
		if ed.Hat == "" {
			fail(i18n.In(lang, "web.day.needwhat"))
			return
		}
	}

	var c *core.Event
	switch op {
	case "change":
		// An entry that crosses midnight is shown cut at the day's edge; a
		// time left as shown keeps the real start or end.
		for _, i := range core.Intervals(events, s.Now()) {
			if i.StartID != id {
				continue
			}
			if r.PostFormValue("start") == r.PostFormValue("shown_start") {
				ed.Start = i.Start
			}
			if !i.Open && r.PostFormValue("end") == r.PostFormValue("shown_end") {
				ed.End = i.End
			}
		}
		c, err = core.PlanChange(events, id, ed, in)
	case "insert":
		c, err = core.PlanInsert(events, ed, in)
	case "delete":
		c, err = core.PlanDelete(events, id, in)
	case "undo":
		raw, rerr := s.st.EventsBetween(ctx, time.Time{}, time.Time{})
		if rerr != nil {
			serverError(w, rerr)
			return
		}
		c, err = core.PlanUndo(raw, id, in)
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		fail(editError(lang, err))
		return
	}
	if _, err := s.st.AppendEvents(ctx, []core.Event{*c}); err != nil {
		serverError(w, err)
		return
	}
	s.broadcastState(ctx)
	if op == "undo" {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"&done="+url.QueryEscape(c.ID), http.StatusSeeOther)
}

// overnightMax is the longest entry an end before the start may mean.
const overnightMax = 12 * time.Hour

func editError(lang string, err error) string {
	switch {
	case errors.Is(err, core.ErrFuture):
		return i18n.In(lang, "err.future")
	case errors.Is(err, core.ErrEmptyRange):
		return i18n.In(lang, "web.day.emptyrange")
	case errors.Is(err, core.ErrNoInterval):
		return i18n.In(lang, "web.day.gone")
	case errors.Is(err, core.ErrNoChange):
		return i18n.In(lang, "web.day.nochange")
	}
	return err.Error()
}
