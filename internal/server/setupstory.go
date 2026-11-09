package server

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/store"
)

// The setup overview tells a story in four steps: what you do (hats), how
// HatRack tells what you work on (permissions → detection → tickets), where
// it runs (devices, AI), and how to keep the setup (YAML). Each step after
// the hats is a tile that shows the current state instead of a bare
// "Edit" button, and names what needs the user.

// tile is one step of the overview: a link with a purpose and its state.
type tile struct {
	Href, Title, What string
	Lines             []string
	Chips             []deviceRow // devices, for the device tile
	Need              string      // what needs the user, "" if nothing
}

// inheritLine summarises what every hat inherits from the defaults.
func inheritLine(lang string, cfg core.Config) string {
	w := cfg.Work()
	var days []string
	for _, d := range w.Days {
		days = append(days, i18n.In(lang, "web.wd."+d))
	}
	parts := []string{i18n.In(lang, "web.setup.inherit_work", daysText(lang, w.Days, days), w.Start+"–"+w.End)}
	resume := cfg.Defaults.ResumeActivity
	if resume == "" {
		resume = "never"
	}
	parts = append(parts, tr(lang, "web.setup.resume."+resume, resume))
	var mods []string
	for id := range cfg.Modules {
		mods = append(mods, tr(lang, "web.mod."+id, id))
	}
	sort.Strings(mods)
	if len(mods) > 0 {
		parts = append(parts, strings.Join(mods, ", "))
	}
	return strings.Join(parts, " · ")
}

// daysText writes a run of weekdays as "Mo–Fr", other sets as a list.
func daysText(lang string, ids, names []string) string {
	idx := func(d string) int { return slices.Index(core.Weekdays[:], d) }
	if len(ids) > 2 {
		run := true
		for i := 1; i < len(ids); i++ {
			if idx(ids[i]) != idx(ids[i-1])+1 {
				run = false
			}
		}
		if run {
			return names[0] + "–" + names[len(names)-1]
		}
	}
	return strings.Join(names, ", ")
}

// fillTiles builds the tiles of steps 2 and 3.
func (s *Server) fillTiles(r *http.Request, v *view, cfg core.Config, devs []store.Device) {
	ctx := r.Context()
	lang := v.Lang
	st := &v.Setup
	st.Inherit = inheritLine(lang, cfg)

	// permissions: per device what is on, then the lists
	g := tile{Href: "/setup/grants", Title: i18n.In(lang, "web.grants.title"), What: i18n.In(lang, "web.setup.t_grants")}
	ids := sensorIDsOf(devs)
	for _, d := range devs {
		on := sensorsOn(lang, cfg.Observe, d.Name, ids)
		for _, gr := range core.Grants {
			if gr.Ready && cfg.Observe.Granted(d.Name, gr.ID) != "" {
				on = append(on, grantText(lang, gr.ID, "title"))
			}
		}
		if len(on) == 0 {
			on = []string{i18n.In(lang, "web.detection.sum_off")}
		}
		g.Lines = append(g.Lines, d.Name+": "+strings.Join(on, ", "))
	}
	var rest []string
	if n := len(cfg.Observe.Ignore); n > 0 {
		rest = append(rest, i18n.In(lang, "web.detection.sum_ignored", strconv.Itoa(n)))
	}
	rest = append(rest, i18n.In(lang, "web.detection.sum_retention", strconv.Itoa(int(cfg.Observe.Retention()/(24*time.Hour)))))
	g.Lines = append(g.Lines, strings.Join(rest, " · "))
	if len(devs) == 0 {
		g.Lines = []string{i18n.In(lang, "web.dev.none")}
	}

	// detection: rules, what was learned, absence
	k, _, _ := s.knowledge(r)
	nRules := len(cfg.Defaults.Rules)
	for _, h := range cfg.Hats {
		nRules += len(h.Rules)
	}
	d := tile{Href: "/assign", Title: i18n.In(lang, "web.assign.title"), What: i18n.In(lang, "web.setup.t_detection")}
	d.Lines = append(d.Lines,
		i18n.In(lang, "web.setup.t_rules", strconv.Itoa(nRules), strconv.Itoa(len(k.Details)+len(k.Parts)+len(k.Projects))))
	away := tile{Href: "/setup/detection", Title: i18n.In(lang, "web.detection.tab_idle"), What: i18n.In(lang, "web.setup.t_idle"),
		Lines: []string{i18n.In(lang, "web.setup.t_away", strconv.Itoa(int(cfg.Observe.Idle.AfterD()/time.Minute)))}}
	window := false
	for _, dv := range devs {
		window = window || cfg.Observe.Enabled(dv.Name, "window")
	}
	if !window {
		d.Need = i18n.In(lang, "web.setup.need_window")
	}

	// tickets: registered, then learned
	t := tile{Href: "/assign?tab=tickets", Title: i18n.In(lang, "web.tickets.title"), What: i18n.In(lang, "web.setup.t_tickets")}
	var reg, learned []string
	pattern := 0
	for _, row := range st.Tickets {
		switch {
		case row.Pattern:
			pattern++
		case row.Registered:
			reg = append(reg, row.Project)
		default:
			learned = append(learned, row.Project)
		}
	}
	if len(reg) > 0 {
		t.Lines = append(t.Lines, i18n.In(lang, "web.setup.t_registered", strings.Join(reg, ", ")))
	}
	if len(learned) > 0 {
		t.Lines = append(t.Lines, i18n.In(lang, "web.setup.t_learned", strings.Join(learned, ", ")))
	}
	if pattern > 0 {
		t.Lines = append(t.Lines, i18n.In(lang, "web.setup.t_patterns", strconv.Itoa(pattern)))
	}
	if len(t.Lines) == 0 {
		t.Lines = []string{i18n.In(lang, "web.tickets.none")}
	}
	st.Recognise, st.Away = []tile{g, d, t}, away

	// devices: chips, and a device that never connected
	dv := tile{Href: "/setup/devices", Title: i18n.In(lang, "web.devices"), What: i18n.In(lang, "web.setup.t_devices"), Chips: st.Devices}
	var never []string
	for _, x := range devs {
		if x.LastSeen.IsZero() {
			never = append(never, x.Name)
		}
	}
	switch {
	case len(never) == 1:
		dv.Need = i18n.In(lang, "web.setup.need_device", never[0])
	case len(never) > 1:
		dv.Need = i18n.In(lang, "web.setup.need_devices", strings.Join(never, ", "))
	}
	if len(devs) == 0 {
		dv.Lines = []string{i18n.In(lang, "web.dev.none")}
		dv.Need = i18n.In(lang, "web.setup.need_nodevice")
	}

	// AI: the access tokens
	ai := tile{Href: "/setup/ai", Title: i18n.In(lang, "web.ai.title"), What: i18n.In(lang, "web.setup.t_ai")}
	ai.Lines = s.aiLines(ctx, lang, r)
	st.Runs = []tile{dv, ai}
}

// aiLines describes the MCP tokens: one line each, newest use first.
func (s *Server) aiLines(ctx context.Context, lang string, r *http.Request) []string {
	toks, err := s.st.MCPTokens(ctx)
	if err != nil || len(toks) == 0 {
		return []string{i18n.In(lang, "web.setup.t_ai_none")}
	}
	sort.Slice(toks, func(a, b int) bool { return toks[a].LastUsed.After(toks[b].LastUsed) })
	loc := s.locOf(r)
	var out []string
	for _, t := range toks {
		reads := i18n.In(lang, "web.setup.t_ai_log")
		if slices.Contains(t.Scopes, "observe") {
			reads = i18n.In(lang, "web.setup.t_ai_obs")
		}
		used := i18n.In(lang, "web.setup.t_ai_unused")
		if !t.LastUsed.IsZero() {
			used = i18n.In(lang, "web.setup.t_ai_used", dayLabel(lang, t.LastUsed.In(loc), false))
		}
		out = append(out, t.Name+": "+reads+" · "+used)
	}
	return out
}

// ticketsPage moved to the page Zuordnung; old links still work.
func (s *Server) ticketsPage(w http.ResponseWriter, r *http.Request, _ access) {
	http.Redirect(w, r, "/assign?tab=tickets", http.StatusMovedPermanently)
}
