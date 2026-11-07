package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// The Devices page: what each device can do, what went wrong on it lately,
// and adding or removing devices.

type devicesView struct {
	Devices []deviceCard
	Added   *addedDevice
	Observe observeView
}

// observeView is the "detection for all devices" card.
type observeView struct {
	Ignore, Redact string   // one app per line
	Recent         []string // apps seen lately, not yet listed
	Retention      int      // days
}

// sensorRow is one sensor of one device in the detection block.
type sensorRow struct {
	ID, Title, Backend string
	On, Disabled, OK   bool
	Today              string
	Note               string // the sensor's reason: why it is unavailable, or a warning
}

type deviceCard struct {
	ID, Name  string
	Online    bool
	Seen      string
	Modules   []deviceModule
	Errors    []deviceError
	Reported  bool // the device has sent its capabilities
	Sensors   []sensorRow
	SensorIDs string // the sensors shown, comma-separated
	CanConfig bool
}

type deviceModule struct {
	Title, Backend, Choices string
	OK                      bool
}

type deviceError struct{ When, Text string }

type addedDevice struct{ Name, Token, Command string }

// serverURL guesses the address agents use to reach this server.
func serverURL(r *http.Request) string {
	scheme := "http"
	if secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) devicesPage(w http.ResponseWriter, r *http.Request, acc access) {
	v, err := s.devicesView(r, acc)
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, v, http.StatusOK)
}

func (s *Server) devicesView(r *http.Request, acc access) (view, error) {
	ctx := r.Context()
	loc, lang := s.locOf(r), s.langOf(r)
	v := s.base(r, "devices", acc)
	devs, err := s.st.Devices(ctx)
	if err != nil {
		return v, err
	}
	// module errors of the last week, newest first
	since := s.Now().Add(-7 * 24 * time.Hour)
	evs, _ := s.st.EventsBetween(ctx, since, time.Time{})
	errs := map[string][]deviceError{}
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Type == core.EvModuleError && len(errs[e.Device]) < 10 {
			errs[e.Device] = append(errs[e.Device], deviceError{When: dayLabel(lang, e.TS.In(loc), false) + " " + e.TS.In(loc).Format("15:04"), Text: e.Note})
		}
	}
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		return v, err
	}
	now := s.Now()
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	stats, err := s.st.ObservationStats(ctx, today, now)
	if err != nil {
		return v, err
	}
	for _, d := range devs {
		c := deviceCard{ID: d.ID, Name: d.Name, Errors: errs[d.Name], CanConfig: d.CanConfig}
		if !d.LastSeen.IsZero() {
			c.Online = s.Now().Sub(d.LastSeen) < time.Minute
			c.Seen = dayLabel(lang, d.LastSeen.In(loc), true) + " " + d.LastSeen.In(loc).Format("15:04")
		}
		var caps map[string]modules.Capability
		if len(d.Capabilities) > 0 && json.Unmarshal(d.Capabilities, &caps) == nil {
			c.Reported = true
			ids := make([]string, 0, len(caps))
			for id := range caps {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				if sid, ok := strings.CutPrefix(id, "sensor:"); ok {
					c.Sensors = append(c.Sensors, newSensorRow(lang, cfg.Observe, d.Name, sid, caps[id], stats[d.Name][sid]))
					continue // sensors get their own block
				}
				cp := caps[id]
				m := deviceModule{Title: tr(lang, "web.mod."+id, id), Backend: cp.Backend, OK: cp.Available}
				var parts []string
				for prop, list := range cp.Choices {
					parts = append(parts, fmt.Sprintf("%d × %s", len(list), tr(lang, "web.prop."+prop, prop)))
				}
				sort.Strings(parts)
				m.Choices = strings.Join(parts, ", ")
				c.Modules = append(c.Modules, m)
			}
		}
		sortSensors(c.Sensors)
		ids := make([]string, len(c.Sensors))
		for i, sr := range c.Sensors {
			ids[i] = sr.ID
		}
		c.SensorIDs = strings.Join(ids, ",")
		v.Devices.Devices = append(v.Devices.Devices, c)
	}
	v.Devices.Observe = observeView{
		Ignore:    strings.Join(cfg.Observe.Ignore, "\n"),
		Redact:    strings.Join(cfg.Observe.Redact, "\n"),
		Retention: int(cfg.Observe.Retention() / (24 * time.Hour)),
		Recent:    s.recentApps(ctx, cfg.Observe, now),
	}
	return v, nil
}

func newSensorRow(lang string, o core.Observe, device, id string, cp modules.Capability, st store.ObsStat) sensorRow {
	r := sensorRow{ID: id, Title: tr(lang, "web.sensor."+id, id), Backend: cp.Backend, OK: cp.Available,
		On: o.Enabled(device, id), Note: cp.Reason}
	r.Disabled = !cp.Available && !r.On // an enabled sensor can always be switched off
	if st.Count > 0 {
		n, d := strconv.Itoa(st.Count), core.FormatDuration(st.Duration)
		if id == "idle" {
			r.Today = i18n.In(lang, "web.sensor.today_idle", n, d)
		} else {
			r.Today = i18n.In(lang, "web.sensor.today_spans", d, n)
		}
	}
	return r
}

// sortSensors puts idle first, then window, then the rest alphabetically.
func sortSensors(rows []sensorRow) {
	rank := func(id string) int {
		switch id {
		case "idle":
			return 0
		case "window":
			return 1
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := rank(rows[i].ID), rank(rows[j].ID); ri != rj {
			return ri < rj
		}
		return rows[i].ID < rows[j].ID
	})
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

func (s *Server) devicesAction(w http.ResponseWriter, r *http.Request, acc access) {
	ctx := r.Context()
	lang := s.langOf(r)
	fail := func(msg string) { http.Redirect(w, r, "/setup/devices?e="+url.QueryEscape(msg), http.StatusSeeOther) }
	switch r.PostFormValue("op") {
	case "add":
		name := strings.TrimSpace(r.PostFormValue("name"))
		if name == "" {
			fail(i18n.In(lang, "web.setup.needname"))
			return
		}
		dev, token, err := s.st.AddDevice(ctx, name)
		if err != nil {
			fail(i18n.In(lang, "web.dev.exists", name))
			return
		}
		if r.PostFormValue("can_config") != "" {
			if err := s.st.SetDeviceConfig(ctx, dev.ID, true); err != nil {
				serverError(w, err)
				return
			}
		}
		// The token is shown once, in the answer to this POST: it never
		// appears in an address (history, logs) and is not kept in clear.
		v, err := s.devicesView(r, acc)
		if err != nil {
			serverError(w, err)
			return
		}
		v.Devices.Added = &addedDevice{Name: name, Token: token,
			Command: "hat setup --server " + serverURL(r) + " --token " + token}
		s.render(w, v, http.StatusOK)
	case "sensors":
		s.sensorsSave(w, r, fail)
	case "observe":
		s.observeSave(w, r, fail)
	case "config":
		if err := s.st.SetDeviceConfig(ctx, r.PostFormValue("id"), r.PostFormValue("allow") == "1"); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/setup/devices", http.StatusSeeOther)
	case "delete":
		if err := s.st.DeleteDevice(ctx, r.PostFormValue("id")); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/setup/devices", http.StatusSeeOther)
	default:
		fail("unknown action")
	}
}

// sensorsSave switches the listed sensors of one device. The config stays
// minimal: idle is stored only when off, every other sensor only when on.
func (s *Server) sensorsSave(w http.ResponseWriter, r *http.Request, fail func(string)) {
	ctx := r.Context()
	devs, err := s.st.Devices(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	name := ""
	for _, d := range devs {
		if d.ID == r.PostFormValue("id") {
			name = d.Name
		}
	}
	if name == "" {
		fail("unknown device")
		return
	}
	_, err = s.editConfig(ctx, false, func(c *core.Config) error {
		o := &c.Observe
		for _, id := range splitList(r.PostFormValue("sensors")) {
			on := r.PostFormValue("on."+id) != ""
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
		if len(o.Devices) == 0 {
			o.Devices = nil
		}
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	http.Redirect(w, r, "/setup/devices", http.StatusSeeOther)
}

// observeSave stores the settings that apply to all devices.
func (s *Server) observeSave(w http.ResponseWriter, r *http.Request, fail func(string)) {
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
		c.Observe.RetentionDays = days
		return nil
	})
	if err != nil {
		s.configFailed(w, r, err, fail)
		return
	}
	http.Redirect(w, r, "/setup/devices", http.StatusSeeOther)
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
