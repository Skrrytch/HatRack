package server

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The page Zuordnung (assignment): the rules, the rule test on a day of the
// user's own with the rule editor, what was learned and the ticket projects.
// Rules change whenever the work changes, so this is a main page and not a
// part of the setup.

var assignTabs = []struct{ ID, Key string }{
	{"rules", "web.assign.tab_rules"},
	{"day", "web.assign.tab_day"},
	{"learned", "web.assign.tab_learned"},
	{"tickets", "web.assign.tab_tickets"},
	{"glance", "web.assign.tab_glance"},
}

func (s *Server) assignPage(w http.ResponseWriter, r *http.Request, acc access) {
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	v := s.base(r, "assign", acc)
	if err := s.fillAssign(r, &v, cfg, r.URL.Query(), nil); err != nil {
		serverError(w, err)
		return
	}
	s.render(w, v, http.StatusOK)
}

// fillAssign builds the page. The editor sits in the tab "day", right above
// the test; links to it name that tab.
func (s *Server) fillAssign(r *http.Request, v *view, cfg core.Config, q url.Values, saveErr error) error {
	if err := s.rulesTab(r, v, cfg, q, saveErr); err != nil {
		return err
	}
	rv := &v.Detection.Rules
	rv.Tab = assignTabs[0].ID
	for _, t := range assignTabs {
		if q.Get("tab") == t.ID {
			rv.Tab = t.ID
		}
	}
	if (saveErr != nil && q.Get("tab") != "rules") || (rv.Editor != nil && !q.Has("tab")) {
		rv.Tab = "day" // the error shows at the editor; an old link with a draft
	}
	switch rv.Tab {
	case "tickets":
		v.Setup.Tickets = s.ticketRows(r, cfg)
	case "glance":
		rv.Glance = s.glanceTab(r.Context(), cfg.Observe)
		rv.Glance.Saved = q.Get("glance_saved") != ""
	}
	keep := "&day=" + url.QueryEscape(rv.Day) + "&device=" + url.QueryEscape(rv.Device)
	for _, t := range assignTabs {
		tab := pageTab{ID: t.ID, Href: "/assign?tab=" + t.ID + keep, Title: i18n.In(v.Lang, t.Key), On: t.ID == rv.Tab}
		switch t.ID {
		case "rules":
			tab.Stat = count(v.Lang, rv.NRules, "web.assign.st_rules_one", "web.assign.st_rules")
		case "day":
			if len(rv.Cover) > 0 {
				tab.Stat = i18n.In(v.Lang, "web.assign.st_day", strconv.Itoa(rv.Cover[0].Pct))
			}
		case "learned":
			tab.Stat = count(v.Lang, rv.NLearned, "web.assign.st_learned_one", "web.assign.st_learned")
		case "tickets":
			tab.Stat = count(v.Lang, rv.NProjects, "web.assign.st_tickets_one", "web.assign.st_tickets")
		case "glance":
			tab.Stat = strconv.Itoa(int(cfg.Observe.Glance()/time.Second)) + " s"
		}
		rv.Tabs = append(rv.Tabs, tab)
	}
	return nil
}

// count says n things with the text for one or for many.
func count(lang string, n int, one, many string) string {
	if n == 1 {
		return i18n.In(lang, one)
	}
	return i18n.In(lang, many, strconv.Itoa(n))
}

// assignAction saves or deletes a rule, stores the side window settings, or
// changes a ticket project.
func (s *Server) assignAction(w http.ResponseWriter, r *http.Request, acc access) {
	switch r.PostFormValue("op") {
	case "rule.save", "rule.delete":
		s.ruleAction(w, r, acc)
	case "glance":
		s.glanceSave(w, r)
	default:
		s.ticketsAction(w, r, acc)
	}
}
