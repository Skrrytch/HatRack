package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/modules"
)

// The Devices page: what each device can do, what went wrong on it lately,
// and adding or removing devices.

type devicesView struct {
	Devices []deviceCard
	Added   *addedDevice
}

type deviceCard struct {
	ID, Name  string
	Online    bool
	Seen      string
	Modules   []deviceModule
	Errors    []deviceError
	Reported  bool // the device has sent its capabilities
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
				if strings.HasPrefix(id, "sensor:") {
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
		v.Devices.Devices = append(v.Devices.Devices, c)
	}
	return v, nil
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
