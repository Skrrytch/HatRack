package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/store"
)

// The page Freigaben (/setup/grants): what HatRack may see on which device.
// It holds the basics (the sensor table, the privacy lists, retention), one
// card per grant that is ready (core.Grants) and the history of changes
// (docs/auto-detection.md, 1b).

type grantsView struct {
	Tab     string // basics, more, history
	Tabs    []pageTab
	Cards   []grantCard
	History []grantLogRow
}

type grantCard struct {
	ID                                              string
	Title, Lead, What, Why, Stores, Risk, NotNeeded string
	Status                                          string
	On                                              bool // granted on at least one device
	Levels                                          []option
	Devices                                         []grantDev
	DeviceIDs                                       string
	Revoke                                          *revokeView
}

type grantDev struct {
	ID, Name string
	On       bool
	Disabled bool
	Note     string
}

// revokeView is the one question before a grant is narrowed: delete what it
// stored, or keep it until the retention ends. It carries the form again.
type revokeView struct {
	Title    string
	Affected string // "in 1240 observations of the last 28 days"
	Level    string
	On       []string // device ids switched on
	Devices  string
}

type grantLogRow struct {
	When, Device, Grant, Change string
	Deleted                     bool
}

// grantText returns a card text of a grant.
func grantText(lang, id, part string) string { return tr(lang, "grant."+id+"."+part, "") }

// levelLabel names a level of a grant; "" is off.
func levelLabel(lang, id, lvl string) string {
	if lvl == "" {
		return i18n.In(lang, "web.grants.off")
	}
	return tr(lang, "grant."+id+".level."+lvl, lvl)
}

// stricter reports whether going from old to new takes something away.
// Levels are listed from narrow to wide.
func stricter(g core.Grant, old, new string) bool {
	if old == "" {
		return false
	}
	if new == "" {
		return true
	}
	return slices.Index(g.Levels, new) < slices.Index(g.Levels, old)
}

func (s *Server) grantsPage(w http.ResponseWriter, r *http.Request, acc access) {
	v, err := s.grantsView(r, acc, nil)
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, v, http.StatusOK)
}

// pendingRevoke is a save that waits for the answer to the revoke question.
type pendingRevoke struct {
	grant string
	view  revokeView
}

func (s *Server) grantsView(r *http.Request, acc access, pending *pendingRevoke) (view, error) {
	ctx := r.Context()
	lang := s.langOf(r)
	loc := s.locOf(r)
	v := s.base(r, "grants", acc)
	v.Grants.Tab = pickTab(r.URL.Query(), "basics", "more", "history")
	if pending != nil {
		v.Grants.Tab = "more"
	}
	if r.URL.Query().Get("saved") != "" {
		v.Msg = i18n.In(lang, "web.grants.saved")
	}
	if n := r.URL.Query().Get("cleaned"); n != "" {
		v.Msg = i18n.In(lang, "web.grants.cleaned", n)
	}
	if n := r.URL.Query().Get("cleared"); n != "" {
		v.Msg = i18n.In(lang, "web.clear.done", n)
	}
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		return v, err
	}
	d := &v.Detection
	if err := s.fillMatrix(ctx, lang, loc, d, cfg.Observe); err != nil {
		return v, err
	}
	d.Observe = observeView{
		Ignore:      strings.Join(cfg.Observe.Ignore, "\n"),
		Redact:      strings.Join(cfg.Observe.Redact, "\n"),
		IgnorePaths: strings.Join(cfg.Observe.IgnorePaths, "\n"),
		Retention:   int(cfg.Observe.Retention() / (24 * time.Hour)),
		Recent:      s.recentApps(ctx, cfg.Observe, s.Now()),
		ClearMax:    s.Now().In(loc).Format(clearLayout),
	}
	if n, first, err := s.st.ObservationSummary(ctx); err != nil {
		return v, err
	} else if n > 0 {
		d.Observe.Stored = i18n.In(lang, "web.clear.stored", strconv.Itoa(n), first.In(loc).Format("02.01.2006 15:04"))
	}
	devs, err := s.st.Devices(ctx)
	if err != nil {
		return v, err
	}
	for _, g := range core.Grants {
		if !g.Ready {
			continue
		}
		c, err := s.grantCard(ctx, lang, cfg.Observe, devs, g)
		if err != nil {
			return v, err
		}
		if pending != nil && pending.grant == g.ID {
			// the card shows what was submitted, so confirming sends it again
			rv := pending.view
			c.Revoke = &rv
			for i := range c.Devices {
				c.Devices[i].On = slices.Contains(rv.On, c.Devices[i].ID)
			}
			for i := range c.Levels {
				c.Levels[i].Selected = c.Levels[i].Value == rv.Level
			}
		}
		v.Grants.Cards = append(v.Grants.Cards, c)
	}
	log, err := s.st.GrantLog(ctx, s.Now().AddDate(-1, 0, 0))
	if err != nil {
		return v, err
	}
	for _, e := range log {
		t := e.At.In(loc)
		v.Grants.History = append(v.Grants.History, grantLogRow{
			When:    dayLabel(lang, t, false) + " " + t.Format("15:04"),
			Device:  e.Device,
			Grant:   tr(lang, "grant."+e.Grant+".title", e.Grant),
			Change:  levelLabel(lang, e.Grant, e.Old) + " → " + levelLabel(lang, e.Grant, e.New),
			Deleted: e.Deleted,
		})
	}
	// tabs with their state
	var basics []string
	for _, id := range sensorIDsOf(devs) {
		for _, dv := range devs {
			if cfg.Observe.Enabled(dv.Name, id) {
				basics = append(basics, tr(lang, "web.sensor."+id, id))
				break
			}
		}
	}
	if len(basics) == 0 {
		basics = []string{i18n.In(lang, "web.detection.sum_off")}
	}
	granted := 0
	for _, c := range v.Grants.Cards {
		if c.On {
			granted++
		}
	}
	g := &v.Grants
	g.Tabs = []pageTab{
		{ID: "basics", Href: "/setup/grants?tab=basics", Title: i18n.In(lang, "web.grants.basics"), Stat: strings.Join(basics, ", ")},
		{ID: "more", Href: "/setup/grants?tab=more", Title: i18n.In(lang, "web.grants.more"), Stat: i18n.In(lang, "web.grants.tab_granted", strconv.Itoa(granted))},
		{ID: "history", Href: "/setup/grants?tab=history", Title: i18n.In(lang, "web.grants.history"), Stat: i18n.In(lang, "web.grants.tab_changes", strconv.Itoa(len(g.History)))},
	}
	for i := range g.Tabs {
		g.Tabs[i].On = g.Tabs[i].ID == g.Tab
	}
	return v, nil
}

func (s *Server) grantCard(ctx context.Context, lang string, o core.Observe, devs []store.Device, g core.Grant) (grantCard, error) {
	c := grantCard{
		ID:        g.ID,
		Title:     grantText(lang, g.ID, "title"),
		Lead:      grantText(lang, g.ID, "lead"),
		What:      grantText(lang, g.ID, "what"),
		Why:       grantText(lang, g.ID, "why"),
		Stores:    grantText(lang, g.ID, "stores"),
		Risk:      grantText(lang, g.ID, "risk"),
		NotNeeded: grantText(lang, g.ID, "not_needed"),
	}
	level := ""
	on := 0
	var ids []string
	for _, dv := range devs {
		ids = append(ids, dv.ID)
		lvl := o.Grants[dv.Name][g.ID]
		gd := grantDev{ID: dv.ID, Name: dv.Name, On: lvl != ""}
		switch {
		case g.Needs != "" && o.Granted(dv.Name, g.Needs) == "":
			gd.Disabled = !gd.On
			gd.Note = i18n.In(lang, "web.grants.needs", grantText(lang, g.Needs, "title"))
		case g.NeedsSensor != "" && !o.Enabled(dv.Name, g.NeedsSensor):
			gd.Note = i18n.In(lang, "web.grants.needs_sensor", tr(lang, "web.sensor."+g.NeedsSensor, g.NeedsSensor))
		case gd.On:
			last, err := s.st.LastObservationWith(ctx, dv.Name, g.Attrs, g.Sensor)
			if err != nil {
				return c, err
			}
			if last.IsZero() {
				gd.Note = i18n.In(lang, "web.grants.never_used")
			} else {
				gd.Note = i18n.In(lang, "web.grants.last_used", ago(lang, s.Now().Sub(last)))
			}
		}
		if caps := capsOf(dv); caps != nil && !gd.On {
			if cp, ok := caps["grant:"+g.ID]; !ok || !cp.Available {
				gd.Disabled = true
				gd.Note = i18n.In(lang, "web.grants.unavailable")
			}
		}
		if gd.On {
			on++
			if level == "" {
				level = lvl
			}
		}
		c.Devices = append(c.Devices, gd)
	}
	c.DeviceIDs = strings.Join(ids, ",")
	c.On = on > 0
	if c.On {
		c.Status = i18n.In(lang, "web.grants.on_count", strconv.Itoa(on), strconv.Itoa(len(devs)))
	} else {
		c.Status = i18n.In(lang, "web.grants.off")
	}
	if level == "" {
		level = g.Levels[0]
	}
	if len(g.Levels) > 1 {
		for _, l := range g.Levels {
			c.Levels = append(c.Levels, option{Value: l, Label: levelLabel(lang, g.ID, l), Selected: l == level})
		}
	}
	return c, nil
}

// ago says how long ago something was, in words.
func ago(lang string, d time.Duration) string {
	switch {
	case d < time.Minute:
		return i18n.In(lang, "web.grants.ago_now")
	case d < time.Hour:
		return i18n.In(lang, "web.grants.ago_min", strconv.Itoa(int(d/time.Minute)))
	case d < 48*time.Hour:
		return i18n.In(lang, "web.grants.ago_h", strconv.Itoa(int(d/time.Hour)))
	}
	return i18n.In(lang, "web.grants.ago_d", strconv.Itoa(int(d/(24*time.Hour))))
}

func (s *Server) grantsAction(w http.ResponseWriter, r *http.Request, acc access) {
	fail := func(msg string) {
		http.Redirect(w, r, "/setup/grants?tab="+url.QueryEscape(r.PostFormValue("tab"))+"&e="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	switch r.PostFormValue("op") {
	case "sensors":
		s.sensorsSave(w, r, "/setup/grants?tab=basics&saved=1", fail)
	case "observe":
		s.observeSave(w, r, "/setup/grants?tab=basics&saved=1#privacy", fail)
	case "grant":
		s.grantSave(w, r, acc, fail)
	case "clear":
		s.clearObservations(w, r, fail)
	default:
		fail("unknown action")
	}
}

// grantChange is one device's change of one grant.
type grantChange struct {
	device   string
	grant    core.Grant
	old, new string
}

// grantSave stores a card. A change that takes something away asks first
// whether to delete what it stored, unless nothing is stored.
func (s *Server) grantSave(w http.ResponseWriter, r *http.Request, acc access, fail func(string)) {
	ctx := r.Context()
	lang := s.langOf(r)
	g, ok := core.GrantByID(r.PostFormValue("grant"))
	if !ok || !g.Ready {
		fail("unknown grant")
		return
	}
	level := r.PostFormValue("level")
	if !slices.Contains(g.Levels, level) {
		level = g.Levels[0]
	}
	devs, err := s.st.Devices(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	byID := map[string]store.Device{}
	for _, d := range devs {
		byID[d.ID] = d
	}
	var changes []grantChange
	var onIDs []string
	for _, did := range splitList(r.PostFormValue("devices")) {
		dv, ok := byID[did]
		if !ok {
			continue
		}
		old := cfg.Observe.Grants[dv.Name][g.ID]
		new := ""
		if r.PostFormValue("on."+did) != "" {
			new = level
			onIDs = append(onIDs, did)
		}
		if old == new {
			continue
		}
		changes = append(changes, grantChange{dv.Name, g, old, new})
		if new == "" {
			// what needs this grant goes with it
			for _, dep := range core.Grants {
				if dep.Needs == g.ID {
					if l := cfg.Observe.Grants[dv.Name][dep.ID]; l != "" {
						changes = append(changes, grantChange{dv.Name, dep, l, ""})
					}
				}
			}
		}
	}
	if len(changes) == 0 {
		http.Redirect(w, r, "/setup/grants?tab=more&saved=1#"+g.ID, http.StatusSeeOther)
		return
	}
	next := cfg.Observe
	next.Grants = applyGrantChanges(cfg.Observe.Grants, changes)
	isKey := s.isTicketKey(ctx, cfg)
	var narrowed []string
	for _, c := range changes {
		if stricter(c.grant, c.old, c.new) && !slices.Contains(narrowed, c.device) {
			narrowed = append(narrowed, c.device)
		}
	}
	answer := r.PostFormValue("delete") // "", "1" (delete) or "0" (keep)
	if len(narrowed) > 0 && answer == "" {
		n, err := s.affected(ctx, next, narrowed, isKey)
		if err != nil {
			serverError(w, err)
			return
		}
		if n > 0 {
			key := "web.grants.revoke_title"
			for _, c := range changes {
				if c.grant.ID == g.ID && c.new != "" {
					key = "web.grants.narrow_title"
				}
			}
			p := &pendingRevoke{grant: g.ID, view: revokeView{
				Title:    i18n.In(lang, key, grantText(lang, g.ID, "title"), strings.Join(narrowed, ", ")),
				Affected: i18n.In(lang, "web.grants.revoke_affected", strconv.Itoa(n), strconv.Itoa(int(cfg.Observe.Retention()/(24*time.Hour)))),
				Level:    level,
				On:       onIDs,
				Devices:  r.PostFormValue("devices"),
			}}
			v, err := s.grantsView(r, acc, p)
			if err != nil {
				serverError(w, err)
				return
			}
			s.render(w, v, http.StatusOK)
			return
		}
	}
	del := answer == "1"
	_, err = s.editConfig(ctx, false, func(c *core.Config) error {
		c.Observe.Grants = applyGrantChanges(c.Observe.Grants, changes)
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	now := s.Now()
	for _, c := range changes {
		err := s.st.AddGrantChange(ctx, store.GrantChange{At: now, Device: c.device, Grant: c.grant.ID, Old: c.old, New: c.new,
			Deleted: del && stricter(c.grant, c.old, c.new)})
		if err != nil {
			serverError(w, err)
			return
		}
	}
	cleaned := 0
	if del {
		for _, dev := range narrowed {
			ch, dl, err := s.st.FilterObservations(ctx, dev, func(o core.Observation) (core.Attrs, bool) {
				return next.FilterGranted(dev, o.Sensor, o.Attrs, isKey)
			})
			if err != nil {
				serverError(w, err)
				return
			}
			cleaned += ch + dl
		}
	}
	if cleaned > 0 {
		http.Redirect(w, r, "/setup/grants?tab=more&cleaned="+strconv.Itoa(cleaned)+"#"+g.ID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/setup/grants?tab=more&saved=1#"+g.ID, http.StatusSeeOther)
}

// applyGrantChanges returns a copy of the grants with the changes; empty
// maps are dropped so the configuration stays minimal.
func applyGrantChanges(in map[string]map[string]string, changes []grantChange) map[string]map[string]string {
	out := map[string]map[string]string{}
	for dev, m := range in {
		out[dev] = map[string]string{}
		for k, v := range m {
			out[dev][k] = v
		}
	}
	for _, c := range changes {
		if c.new == "" {
			delete(out[c.device], c.grant.ID)
			continue
		}
		if out[c.device] == nil {
			out[c.device] = map[string]string{}
		}
		out[c.device][c.grant.ID] = c.new
	}
	for dev, m := range out {
		if len(m) == 0 {
			delete(out, dev)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// affected counts the stored spans of the devices that the new settings
// would change or delete.
func (s *Server) affected(ctx context.Context, o core.Observe, devices []string, isKey func(string) bool) (int, error) {
	n := 0
	for _, dev := range devices {
		obs, err := s.st.ObservationsBetween(ctx, time.Time{}, time.Time{}, dev, "")
		if err != nil {
			return 0, err
		}
		for _, ob := range obs {
			a, keep := o.FilterGranted(dev, ob.Sensor, ob.Attrs, isKey)
			if !keep || len(a) != len(ob.Attrs) {
				n++
			}
		}
	}
	return n, nil
}

// clearLayout is the value of a datetime-local field, to the minute.
const clearLayout = "2006-01-02T15:04"

// clearObservations deletes what was observed up to the chosen moment, which
// may be now but not later. Hats, activities and the log stay.
func (s *Server) clearObservations(w http.ResponseWriter, r *http.Request, fail func(string)) {
	lang := s.langOf(r)
	until, err := time.ParseInLocation(clearLayout, strings.TrimSpace(r.PostFormValue("until")), s.locOf(r))
	if err != nil {
		fail(i18n.In(lang, "web.clear.bad"))
		return
	}
	now := s.Now()
	if until.After(now.Add(time.Minute)) { // the field shows minutes only
		fail(i18n.In(lang, "web.clear.future"))
		return
	}
	if until.After(now) {
		until = now
	}
	n, err := s.st.ClearObservations(r.Context(), until)
	if err != nil {
		serverError(w, err)
		return
	}
	s.know.mu.Lock()
	s.know.cur = nil // learned from what is gone
	s.know.mu.Unlock()
	http.Redirect(w, r, "/setup/grants?tab=basics&cleared="+strconv.Itoa(n)+"#clear", http.StatusSeeOther)
}
