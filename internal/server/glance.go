package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The tab Zwischenfenster on the page Zuordnung: helper apps and short side
// trips count for the window before them when a day is read (core/absorb.go).

// glanceView is the tab's form.
type glanceView struct {
	Seconds, Max int
	Helpers      string // one app per line, the effective list
	IsDefault    bool   // both settings are the defaults
	Saved        bool
	Recent       []string // apps that look like helpers and are not listed
}

func (s *Server) glanceTab(ctx context.Context, o core.Observe) glanceView {
	return glanceView{
		Seconds:   int(o.Glance() / time.Second),
		Max:       core.MaxGlanceSeconds,
		Helpers:   strings.Join(o.HelperApps(), "\n"),
		IsDefault: o.Helpers == nil && o.GlanceSeconds == 0,
		Recent:    s.helperCandidates(ctx, o, s.Now()),
	}
}

// Helper candidates: apps of the last 7 days that pop up often and briefly,
// little time in all; they are offered, never added by themselves.
const (
	candMinSpans  = 5
	candMaxMedian = 5 * time.Second
	candMaxTotal  = 5 * time.Minute
)

func (s *Server) helperCandidates(ctx context.Context, o core.Observe, now time.Time) []string {
	obs, err := s.st.ObservationsBetween(ctx, now.Add(-7*24*time.Hour), now, "", "window")
	if err != nil {
		return nil
	}
	type use struct {
		name  string
		durs  []time.Duration
		total time.Duration
	}
	by := map[string]*use{}
	for _, ob := range obs {
		app := strings.TrimSpace(ob.Attrs["app"])
		if app == "" || ob.Attrs[core.AttrPrivate] == "1" || o.Ignored(app) || o.Helper(app) {
			continue
		}
		u := by[strings.ToLower(app)]
		if u == nil {
			u = &use{name: app}
			by[strings.ToLower(app)] = u
		}
		d := ob.End.Sub(ob.Start)
		u.durs = append(u.durs, d)
		u.total += d
	}
	var list []*use
	for _, u := range by {
		slices.Sort(u.durs)
		if len(u.durs) >= candMinSpans && u.durs[len(u.durs)/2] < candMaxMedian && u.total < candMaxTotal {
			list = append(list, u)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if len(list[i].durs) != len(list[j].durs) {
			return len(list[i].durs) > len(list[j].durs)
		}
		return list[i].name < list[j].name
	})
	var out []string
	for _, u := range list[:min(len(list), maxRecentApps)] {
		out = append(out, u.name)
	}
	return out
}

// glanceSave stores the tab. Values equal to the defaults are stored as
// unset, so the config stays minimal and later default lists apply.
func (s *Server) glanceSave(w http.ResponseWriter, r *http.Request) {
	lang := s.langOf(r)
	back := "/assign?tab=glance"
	fail := func(msg string) { http.Redirect(w, r, back+"&e="+url.QueryEscape(msg), http.StatusSeeOther) }
	secs, helpers := 0, (*[]string)(nil)
	if r.PostFormValue("reset") == "" {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("seconds")))
		if err != nil || n < 1 || n > core.MaxGlanceSeconds {
			fail(i18n.In(lang, "web.glance.bad_secs", strconv.Itoa(core.MaxGlanceSeconds)))
			return
		}
		if n != core.DefaultGlanceSeconds {
			secs = n
		}
		if list := splitLines(r.PostFormValue("helpers")); !sameApps(list, core.DefaultHelpers) {
			if list == nil {
				list = []string{} // none, not the defaults
			}
			helpers = &list
		}
	}
	_, err := s.editConfig(r.Context(), false, func(c *core.Config) error {
		c.Observe.GlanceSeconds, c.Observe.Helpers = secs, helpers
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	http.Redirect(w, r, back+"&glance_saved=1", http.StatusSeeOther)
}

// sameApps reports two app lists with the same entries, ignoring order and
// case.
func sameApps(a, b []string) bool {
	norm := func(l []string) []string {
		out := make([]string, len(l))
		for i, x := range l {
			out[i] = strings.ToLower(strings.TrimSpace(x))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(norm(a), norm(b))
}
