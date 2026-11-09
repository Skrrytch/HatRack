package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/modules"
	"hatrack/internal/store"
)

// The Detection setup page: how HatRack detects what you work on. One tab per
// topic; a new topic is one entry in detTabs plus one template block. What it
// may see (sensors, lists, grants) is on the page Freigaben (grants.go).

// detTabs are the tabs of the page, in order (id, message key). The first one
// is the default.
// The sensor table and the privacy lists moved to the page Freigaben (#19).
var detTabs = []struct{ ID, Key string }{
	{"idle", "web.detection.tab_idle"},
	{"rules", "web.detection.tab_rules"},
}

type detTab struct {
	ID, Title string
	On        bool
}

type detectionView struct {
	Tab       string // the tab shown
	Tabs      []detTab
	Devices   []detDevice // the matrix columns
	Rows      []detRow    // the matrix rows
	DeviceIDs string      // the columns, comma-separated
	SensorIDs string      // the rows, comma-separated
	Observe   observeView
	Idle      idleView
	Rules     rulesView
}

type detDevice struct {
	Name   string
	Online bool
}

// detRow is one sensor with its cell for every device.
type detRow struct {
	ID, Title, Sub string
	Cells          []detCell
}

// detCell is one sensor on one device.
type detCell struct {
	Device         string
	On, Disabled   bool
	Reported, OK   bool
	Backend, Today string
	Reason         string // why it is unavailable, or a warning
}

// observeView is the privacy tab.
type observeView struct {
	Ignore, Redact string   // one app per line
	IgnorePaths    string   // one folder per line
	Recent         []string // apps seen lately, not yet listed
	Retention      int      // days
}

// idleView is the absence tab: the effective values in minutes.
type idleView struct {
	After, AskAfter, StopAfter int
	Ask                        bool
}

// capsOf returns the capabilities a device has reported, nil if none.
func capsOf(d store.Device) map[string]modules.Capability {
	var caps map[string]modules.Capability
	if len(d.Capabilities) > 0 && json.Unmarshal(d.Capabilities, &caps) == nil {
		return caps
	}
	return nil
}

// sensorIDsOf lists the sensors any device reported: idle, window, then the
// rest alphabetically. Without reports it is idle and window.
func sensorIDsOf(devs []store.Device) []string {
	seen := map[string]bool{"idle": true, "window": true}
	for _, d := range devs {
		for id := range capsOf(d) {
			if sid, ok := strings.CutPrefix(id, "sensor:"); ok {
				seen[sid] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	rank := func(id string) int {
		switch id {
		case "idle":
			return 0
		case "window":
			return 1
		}
		return 2
	}
	sort.Slice(ids, func(i, j int) bool {
		if ri, rj := rank(ids[i]), rank(ids[j]); ri != rj {
			return ri < rj
		}
		return ids[i] < ids[j]
	})
	return ids
}

// sensorsOn lists the titles of the sensors that are on for a device.
func sensorsOn(lang string, o core.Observe, device string, ids []string) []string {
	var on []string
	for _, id := range ids {
		if o.Enabled(device, id) {
			on = append(on, tr(lang, "web.sensor."+id, id))
		}
	}
	return on
}

// detectionSummary names the sensors that are on for a device, or says that
// all are off.
func detectionSummary(lang string, o core.Observe, device string, ids []string) string {
	if on := sensorsOn(lang, o, device, ids); len(on) > 0 {
		return strings.Join(on, ", ")
	}
	return i18n.In(lang, "web.detection.sum_off")
}

// detectionOverview is the one-line summary on the setup page.
func detectionOverview(lang string, o core.Observe, devs []store.Device) string {
	ids := sensorIDsOf(devs)
	var parts []string
	for _, d := range devs {
		if on := sensorsOn(lang, o, d.Name, ids); len(on) > 0 {
			parts = append(parts, i18n.In(lang, "web.detection.sum_device", d.Name, strings.Join(on, ", ")))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, i18n.In(lang, "web.detection.sum_off"))
	}
	if len(o.Ignore) > 0 {
		parts = append(parts, i18n.In(lang, "web.detection.sum_ignored", strconv.Itoa(len(o.Ignore))))
	}
	parts = append(parts, i18n.In(lang, "web.detection.sum_retention", strconv.Itoa(int(o.Retention()/(24*time.Hour)))))
	return strings.Join(parts, " · ")
}

func (s *Server) detectionPage(w http.ResponseWriter, r *http.Request, acc access) {
	switch r.URL.Query().Get("tab") {
	case "sensors": // moved to the page Freigaben; old links still work
		http.Redirect(w, r, "/setup/grants#basics", http.StatusMovedPermanently)
		return
	case "privacy":
		http.Redirect(w, r, "/setup/grants#privacy", http.StatusMovedPermanently)
		return
	}
	ctx := r.Context()
	lang := s.langOf(r)
	v := s.base(r, "detection", acc)
	d := &v.Detection
	d.Tab = detTabs[0].ID
	for _, t := range detTabs {
		if r.URL.Query().Get("tab") == t.ID {
			d.Tab = t.ID
		}
	}
	for _, t := range detTabs {
		d.Tabs = append(d.Tabs, detTab{t.ID, i18n.In(lang, t.Key), t.ID == d.Tab})
	}
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	switch d.Tab {
	case "rules":
		err = s.rulesTab(r, &v, cfg, r.URL.Query(), nil)
	case "idle":
		i := cfg.Observe.Idle
		d.Idle = idleView{
			After:     int(i.AfterD() / time.Minute),
			AskAfter:  int(i.AskAfterD() / time.Minute),
			StopAfter: int(i.StopAfterD() / time.Minute),
			Ask:       i.Asks(),
		}
	}
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, v, http.StatusOK)
}

// fillMatrix builds the sensor matrix: a column per device, a row per sensor.
func (s *Server) fillMatrix(ctx context.Context, lang string, loc *time.Location, d *detectionView, o core.Observe) error {
	devs, err := s.st.Devices(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	stats, err := s.st.ObservationStats(ctx, today, now)
	if err != nil {
		return err
	}
	ids := sensorIDsOf(devs)
	var devIDs []string
	for _, dv := range devs {
		d.Devices = append(d.Devices, detDevice{Name: dv.Name, Online: !dv.LastSeen.IsZero() && now.Sub(dv.LastSeen) < time.Minute})
		devIDs = append(devIDs, dv.ID)
	}
	for _, id := range ids {
		row := detRow{ID: id, Title: tr(lang, "web.sensor."+id, id), Sub: tr(lang, "web.sensor."+id+"_sub", "")}
		for _, dv := range devs {
			c := detCell{Device: dv.ID, On: o.Enabled(dv.Name, id)}
			if cp, ok := capsOf(dv)["sensor:"+id]; ok {
				c.Reported, c.OK, c.Backend, c.Reason = true, cp.Available, cp.Backend, cp.Reason
				c.Disabled = !cp.Available && !c.On // an enabled sensor can always be switched off
				if st := stats[dv.Name][id]; st.Count > 0 {
					n, dur := strconv.Itoa(st.Count), core.FormatDuration(st.Duration)
					if id == "idle" {
						c.Today = i18n.In(lang, "web.sensor.today_idle", n, dur)
					} else {
						c.Today = i18n.In(lang, "web.sensor.today_spans", dur, n)
					}
				}
			}
			row.Cells = append(row.Cells, c)
		}
		d.Rows = append(d.Rows, row)
	}
	d.DeviceIDs, d.SensorIDs = strings.Join(devIDs, ","), strings.Join(ids, ",")
	return nil
}

func (s *Server) detectionAction(w http.ResponseWriter, r *http.Request, acc access) {
	tab := ""
	fail := func(msg string) {
		http.Redirect(w, r, "/setup/detection?tab="+tab+"&e="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	switch r.PostFormValue("op") {
	case "rule.save", "rule.delete":
		s.ruleAction(w, r, acc)
	case "idle":
		tab = "idle"
		s.idleSave(w, r, "/setup/detection?tab=idle", fail)
	default:
		tab = detTabs[0].ID
		fail("unknown action")
	}
}

// sensorsSave stores the matrix. The config stays minimal: idle is stored
// only when off, every other sensor only when on.
func (s *Server) sensorsSave(w http.ResponseWriter, r *http.Request, back string, fail func(string)) {
	ctx := r.Context()
	devs, err := s.st.Devices(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	names := map[string]string{} // device id -> name
	for _, d := range devs {
		names[d.ID] = d.Name
	}
	_, err = s.editConfig(ctx, false, func(c *core.Config) error {
		o := &c.Observe
		for _, did := range splitList(r.PostFormValue("devices")) {
			name, ok := names[did]
			if !ok {
				continue
			}
			for _, id := range splitList(r.PostFormValue("sensors")) {
				on := r.PostFormValue("on."+did+"."+id) != ""
				if on == (id == "idle") {
					delete(o.Devices[name], id) // the default
					continue
				}
				if o.Devices == nil {
					o.Devices = map[string]map[string]bool{}
				}
				if o.Devices[name] == nil {
					o.Devices[name] = map[string]bool{}
				}
				o.Devices[name][id] = on
			}
			if len(o.Devices[name]) == 0 {
				delete(o.Devices, name)
			}
		}
		if len(o.Devices) == 0 {
			o.Devices = nil
		}
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// maxRecentApps is how many "recently seen" apps are offered.
const maxRecentApps = 8

// recentApps returns the apps with the most window time in the last 7 days
// that are on neither list.
func (s *Server) recentApps(ctx context.Context, o core.Observe, now time.Time) []string {
	obs, err := s.st.ObservationsBetween(ctx, now.Add(-7*24*time.Hour), now, "", "window")
	if err != nil {
		return nil
	}
	type use struct {
		name string
		d    time.Duration
	}
	by := map[string]*use{}
	for _, ob := range obs {
		app := strings.TrimSpace(ob.Attrs["app"])
		if app == "" || ob.Attrs[core.AttrPrivate] == "1" || o.Ignored(app) || o.Redacted(app) {
			continue
		}
		u := by[strings.ToLower(app)]
		if u == nil {
			u = &use{name: app}
			by[strings.ToLower(app)] = u
		}
		u.d += ob.End.Sub(ob.Start)
	}
	list := make([]*use, 0, len(by))
	for _, u := range by {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].d != list[j].d {
			return list[i].d > list[j].d
		}
		return list[i].name < list[j].name
	})
	var out []string
	for _, u := range list[:min(len(list), maxRecentApps)] {
		out = append(out, u.name)
	}
	return out
}

// observeSave stores the settings that apply to all devices.
func (s *Server) observeSave(w http.ResponseWriter, r *http.Request, back string, fail func(string)) {
	lang := s.langOf(r)
	days, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("retention")))
	if err != nil || days < 1 || days > core.MaxRetentionDays {
		fail(i18n.In(lang, "web.observe.bad_retention"))
		return
	}
	if days == core.DefaultRetentionDays {
		days = 0 // the default
	}
	_, err = s.editConfig(r.Context(), false, func(c *core.Config) error {
		c.Observe.Ignore = splitLines(r.PostFormValue("ignore"))
		c.Observe.Redact = splitLines(r.PostFormValue("redact"))
		c.Observe.IgnorePaths = splitLines(r.PostFormValue("ignore_paths"))
		c.Observe.RetentionDays = days
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// idleSave stores the absence settings. Values equal to the default are
// stored as 0, a checked "ask" as nil, so the config stays minimal.
func (s *Server) idleSave(w http.ResponseWriter, r *http.Request, back string, fail func(string)) {
	lang := s.langOf(r)
	var mins [3]int
	for i, name := range []string{"after", "ask_after", "stop_after"} {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(name)))
		if err != nil || n < 1 || n > core.MaxIdleMinutes {
			fail(i18n.In(lang, "web.idle.bad_minutes", strconv.Itoa(core.MaxIdleMinutes)))
			return
		}
		mins[i] = n
	}
	if mins[0] > mins[1] || mins[0] > mins[2] {
		fail(i18n.In(lang, "web.idle.bad_order")) // say it in words, not as a config error
		return
	}
	for i, def := range []int{core.DefaultIdleAfter, core.DefaultIdleAskAfter, core.DefaultIdleStopAfter} {
		if mins[i] == def {
			mins[i] = 0 // the default
		}
	}
	var ask *bool
	if r.PostFormValue("ask") == "" {
		ask = new(bool) // false; checked is the default, stored as nil
	}
	_, err := s.editConfig(r.Context(), false, func(c *core.Config) error {
		c.Observe.Idle = core.Idle{After: mins[0], AskAfter: mins[1], StopAfter: mins[2], Ask: ask}
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) configFailed(w http.ResponseWriter, r *http.Request, err error, fail func(string)) {
	var bad badConfig
	if errors.As(err, &bad) {
		fail(userError(s.langOf(r), err))
		return
	}
	serverError(w, err)
}

// splitLines splits a list typed one entry per line: trimmed, no empty
// lines, no duplicates (case-insensitive, the first wins).
func splitLines(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		p := strings.TrimSpace(line)
		if p != "" && !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	return out
}
