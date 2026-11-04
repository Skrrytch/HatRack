package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hatrack/internal/core"
	"hatrack/internal/store"
)

// MCP: an AI assistant reads the configuration and the time log at /mcp
// (Streamable HTTP). Access needs an MCP token; for now everything is read
// only. The tools use the same functions as the Day and Week views, so the
// numbers agree with the web UI.

// mcpHandler builds the endpoint: authentication around the SDK's handler.
func (s *Server) mcpHandler(version string) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "hatrack", Title: "HatRack", Version: version}, &mcp.ServerOptions{
		Instructions: "HatRack tracks which work context (hat) and activity the user is in. " +
			"Use query_time for questions like \"how long did I work for X last week\", list_entries for the details of a day, " +
			"get_current for what runs now, list_hats and get_config for the configuration. Times are in the server's time zone.",
	})
	s.addMCPTools(srv)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !strings.HasPrefix(token, "hrm_") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hatrack"`)
			httpError(w, http.StatusUnauthorized, "an MCP token is needed (Setup → AI access)")
			return
		}
		t, err := s.st.MCPTokenByToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, http.StatusUnauthorized, "unknown or revoked MCP token")
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		s.st.TouchMCPToken(r.Context(), t.ID)
		h.ServeHTTP(w, r)
	})
}

// ---- periods -----------------------------------------------------------------------

// mcpPeriod reads from/to or a period word into [from, to), in the server's
// time zone. Dates count as whole days (to inclusive); times are exact.
func (s *Server) mcpPeriod(period, from, to string) (time.Time, time.Time, error) {
	loc, now := s.Loc, s.Now()
	today := parseDate("", loc, now)
	if period != "" && (from != "" || to != "") {
		return time.Time{}, time.Time{}, errors.New("give either period or from/to, not both")
	}
	if from == "" && to == "" {
		if period == "" {
			period = "today"
		}
		month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
		switch strings.ToLower(strings.TrimSpace(period)) {
		case "today":
			return today, today.AddDate(0, 0, 1), nil
		case "yesterday":
			return today.AddDate(0, 0, -1), today, nil
		case "week", "this week":
			w := weekStart(today)
			return w, w.AddDate(0, 0, 7), nil
		case "last week":
			w := weekStart(today)
			return w.AddDate(0, 0, -7), w, nil
		case "month", "this month":
			return month, month.AddDate(0, 1, 0), nil
		case "last month":
			return month.AddDate(0, -1, 0), month, nil
		}
		return time.Time{}, time.Time{}, fmt.Errorf("unknown period %q (use today, yesterday, week, last week, month or last month, or from/to)", period)
	}
	parse := func(v string, end bool) (time.Time, error) {
		v = strings.TrimSpace(v)
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		if d, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
			if end {
				return d.AddDate(0, 0, 1), nil
			}
			return d, nil
		}
		return time.Time{}, fmt.Errorf("%q is neither a date (2026-11-04) nor a time (2026-11-04T09:00:00+01:00)", v)
	}
	if from == "" {
		return time.Time{}, time.Time{}, errors.New("from is missing")
	}
	f, err := parse(from, false)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	t := now
	if to != "" {
		if t, err = parse(to, true); err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else if _, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(from), loc); err == nil {
		t = f.AddDate(0, 0, 1) // a single date: that day
	}
	if !t.After(f) {
		return time.Time{}, time.Time{}, errors.New("to must be after from")
	}
	return f, t, nil
}

// mcpHat resolves a hat name or prefix, archived hats included (the log
// still knows them).
func mcpHat(cfg core.Config, name string) (core.Hat, error) {
	items := make([]core.Named, len(cfg.Hats))
	for i, h := range cfg.Hats {
		items[i] = core.Named{Key: h.Key, Name: h.DisplayName()}
	}
	n, err := core.Resolve(name, items)
	if err != nil {
		return core.Hat{}, err
	}
	h, _ := cfg.Hat(n.Key)
	return h, nil
}

func minutes(d time.Duration) int { return int(d.Round(time.Minute) / time.Minute) }

// insideWork is the part of an interval within the working hours.
func insideWork(i core.Interval, w core.WorkHours, loc *time.Location) time.Duration {
	ws, we := w.Minutes()
	var in time.Duration
	start, end := i.Start.In(loc), i.End.In(loc)
	for d := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc); d.Before(end); d = d.AddDate(0, 0, 1) {
		if !w.IsWorkday(d.Weekday()) {
			continue
		}
		a, b := d.Add(time.Duration(ws)*time.Minute), d.Add(time.Duration(we)*time.Minute)
		if start.After(a) {
			a = start
		}
		if end.Before(b) {
			b = end
		}
		if b.After(a) {
			in += b.Sub(a)
		}
	}
	return in
}

// ---- tools -------------------------------------------------------------------------

type periodIn struct {
	Period string `json:"period,omitempty" jsonschema:"today, yesterday, week, last week, month or last month; default today. Leave empty when using from/to."`
	From   string `json:"from,omitempty" jsonschema:"start: a date (2026-11-04) or an RFC 3339 time"`
	To     string `json:"to,omitempty" jsonschema:"end, inclusive for dates (2026-11-08 includes that day); default: now, or the end of the from date"`
	Hat    string `json:"hat,omitempty" jsonschema:"only this hat; name or key, a prefix is enough"`
}

type queryTimeIn struct {
	periodIn
	GroupBy string `json:"group_by,omitempty" jsonschema:"hat (default), activity (hat and activity), detail (hat, activity and detail), day or week"`
}

type timeGroup struct {
	Hat            string `json:"hat,omitempty" jsonschema:"hat key"`
	HatName        string `json:"hat_name,omitempty"`
	Activity       string `json:"activity,omitempty" jsonschema:"empty: time without an activity"`
	Detail         string `json:"detail,omitempty" jsonschema:"for group_by detail; Jira links shortened to their key"`
	DetailURL      string `json:"detail_url,omitempty" jsonschema:"link of a Jira key in the detail, if known"`
	Day            string `json:"day,omitempty" jsonschema:"YYYY-MM-DD; for week: the Monday"`
	Minutes        int    `json:"minutes"`
	Duration       string `json:"duration" jsonschema:"like 1h 23m"`
	OutsideMinutes int    `json:"outside_work_minutes" jsonschema:"minutes outside the configured working hours"`
}

type queryTimeOut struct {
	From           string      `json:"from"`
	To             string      `json:"to"`
	GroupBy        string      `json:"group_by"`
	TotalMinutes   int         `json:"total_minutes"`
	Total          string      `json:"total"`
	OutsideMinutes int         `json:"outside_work_minutes"`
	Groups         []timeGroup `json:"groups"`
}

type entry struct {
	Hat       string `json:"hat,omitempty"`
	HatName   string `json:"hat_name,omitempty"`
	Activity  string `json:"activity,omitempty"`
	Detail    string `json:"detail,omitempty" jsonschema:"free text below the activity, e.g. a ticket key"`
	DetailURL string `json:"detail_url,omitempty" jsonschema:"link of a Jira key in the detail, if known"`
	Start     string `json:"start"`
	End       string `json:"end"`
	Minutes   int    `json:"minutes"`
	Duration  string `json:"duration"`
	Running   bool   `json:"running,omitempty" jsonschema:"still running; end is now"`
	Gap       bool   `json:"gap,omitempty" jsonschema:"nothing was recorded in this time"`
}

type entriesOut struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Entries   []entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty" jsonschema:"more than 500 entries; ask for a shorter period"`
}

type hatTotal struct {
	Hat      string `json:"hat"`
	HatName  string `json:"hat_name"`
	Minutes  int    `json:"minutes"`
	Duration string `json:"duration"`
}

type currentOut struct {
	State      string     `json:"state" jsonschema:"wearing, paused or none"`
	Hat        string     `json:"hat,omitempty"`
	HatName    string     `json:"hat_name,omitempty"`
	Activity   string     `json:"activity,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	DetailURL  string     `json:"detail_url,omitempty"`
	Since      string     `json:"since,omitempty"`
	Minutes    int        `json:"minutes,omitempty" jsonschema:"since then, while wearing"`
	Today      []hatTotal `json:"today"`
	TodayTotal string     `json:"today_total"`
}

type hatInfo struct {
	Key                string   `json:"key"`
	Name               string   `json:"name"`
	Color              string   `json:"color,omitempty"`
	Activities         []string `json:"activities"`
	ArchivedActivities []string `json:"archived_activities,omitempty"`
	Archived           bool     `json:"archived,omitempty"`
	ResumeActivity     string   `json:"resume_activity" jsonschema:"never, last or ask"`
}

type hatsIn struct {
	IncludeArchived bool `json:"include_archived,omitempty" jsonschema:"also list archived hats (they still appear in the time log)"`
}

type hatsOut struct {
	Hats      []hatInfo      `json:"hats"`
	WorkHours core.WorkHours `json:"work_hours"`
}

type configOut struct {
	YAML string `json:"yaml" jsonschema:"the configuration as hat config export writes it"`
}

func text(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}
}

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: new(bool)}
}

func (s *Server) addMCPTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "query_time", Annotations: readOnly("Time per hat, activity, day or week"),
		Description: "Total time in a period, grouped by hat, activity (hat and activity), detail (hat, activity and the free-text detail such as a ticket key), day or week, optionally for one hat. " +
			"Corrections made in the Day view are included. Also tells how much was outside the working hours."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in queryTimeIn) (*mcp.CallToolResult, queryTimeOut, error) {
			from, to, err := s.mcpPeriod(in.Period, in.From, in.To)
			if err != nil {
				return nil, queryTimeOut{}, err
			}
			cfg, _, err := s.st.GetConfig(ctx)
			if err != nil {
				return nil, queryTimeOut{}, err
			}
			iv, _, err := s.intervalsIn(ctx, from, to)
			if err != nil {
				return nil, queryTimeOut{}, err
			}
			if in.Hat != "" {
				h, err := mcpHat(cfg, in.Hat)
				if err != nil {
					return nil, queryTimeOut{}, err
				}
				iv = filterHat(iv, h.Key)
			}
			group := strings.ToLower(in.GroupBy)
			if group == "" {
				group = "hat"
			}
			nm, work := names(cfg), cfg.Work()
			links, _ := s.st.TicketLinks(ctx)
			out := queryTimeOut{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), GroupBy: group, Groups: []timeGroup{}}
			idx := map[string]int{}
			var durs, outs []time.Duration // exact per group, rounded at the end
			add := func(key string, g timeGroup, d, outside time.Duration) {
				i, ok := idx[key]
				if !ok {
					i = len(out.Groups)
					idx[key] = i
					out.Groups = append(out.Groups, g)
					durs, outs = append(durs, 0), append(outs, 0)
				}
				durs[i] += d
				outs[i] += outside
			}
			var total, outside time.Duration
			for _, i := range iv {
				parts := []core.Interval{i}
				if group == "day" || group == "week" {
					parts = nil
					for d := parseDate(dateStr(i.Start.In(s.Loc)), s.Loc, s.Now()); d.Before(i.End); d = d.AddDate(0, 0, 1) {
						parts = append(parts, core.Clip([]core.Interval{i}, d, d.AddDate(0, 0, 1))...)
					}
				}
				for _, p := range parts {
					d := p.Duration()
					o := d - insideWork(p, work, s.Loc)
					total, outside = total+d, outside+o
					switch group {
					case "hat":
						add(p.Hat, timeGroup{Hat: p.Hat, HatName: nameOf(nm, p.Hat)}, d, o)
					case "activity":
						add(p.Hat+"\x00"+p.Activity, timeGroup{Hat: p.Hat, HatName: nameOf(nm, p.Hat), Activity: p.Activity}, d, o)
					case "detail":
						det := core.ShortDetail(p.Detail)
						add(p.Hat+"\x00"+p.Activity+"\x00"+det, timeGroup{Hat: p.Hat, HatName: nameOf(nm, p.Hat), Activity: p.Activity,
							Detail: det, DetailURL: core.DetailURL(p.Detail, links)}, d, o)
					case "day":
						day := dateStr(p.Start.In(s.Loc))
						add(day, timeGroup{Day: day}, d, o)
					case "week":
						wk := dateStr(weekStart(parseDate(dateStr(p.Start.In(s.Loc)), s.Loc, s.Now())))
						add(wk, timeGroup{Day: wk}, d, o)
					default:
						return nil, queryTimeOut{}, fmt.Errorf("group_by %q: use hat, activity, detail, day or week", in.GroupBy)
					}
				}
			}
			mins, outMins := core.Apportion(durs), core.Apportion(outs)
			for i := range out.Groups {
				// outside the working hours is part of the group, never more
				out.Groups[i].Minutes, out.Groups[i].OutsideMinutes = mins[i], min(outMins[i], mins[i])
				out.Groups[i].Duration = core.FormatDuration(time.Duration(mins[i]) * time.Minute)
			}
			if group == "hat" || group == "activity" || group == "detail" {
				sort.SliceStable(out.Groups, func(a, b int) bool { return out.Groups[a].Minutes > out.Groups[b].Minutes })
			} else {
				sort.SliceStable(out.Groups, func(a, b int) bool { return out.Groups[a].Day < out.Groups[b].Day })
			}
			out.TotalMinutes, out.Total, out.OutsideMinutes = minutes(total), core.FormatDuration(total), minutes(outside)
			return nil, out, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "list_entries", Annotations: readOnly("Entries of a period"),
		Description: "The single entries (who wore which hat with which activity from when to when) of a period, oldest first, " +
			"with gaps where nothing was recorded. Use it for the details of a day; for totals use query_time."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in periodIn) (*mcp.CallToolResult, entriesOut, error) {
			from, to, err := s.mcpPeriod(in.Period, in.From, in.To)
			if err != nil {
				return nil, entriesOut{}, err
			}
			cfg, _, err := s.st.GetConfig(ctx)
			if err != nil {
				return nil, entriesOut{}, err
			}
			iv, _, err := s.intervalsIn(ctx, from, to)
			if err != nil {
				return nil, entriesOut{}, err
			}
			if in.Hat != "" {
				h, err := mcpHat(cfg, in.Hat)
				if err != nil {
					return nil, entriesOut{}, err
				}
				iv = filterHat(iv, h.Key)
			}
			nm := names(cfg)
			links, _ := s.st.TicketLinks(ctx)
			out := entriesOut{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Entries: []entry{}}
			ts := func(t time.Time) string { return t.In(s.Loc).Format(time.RFC3339) }
			var prevEnd time.Time
			for _, i := range iv {
				if len(out.Entries) >= 500 {
					out.Truncated = true
					break
				}
				if in.Hat == "" && !prevEnd.IsZero() && i.Start.Sub(prevEnd) >= time.Minute {
					g := i.Start.Sub(prevEnd)
					out.Entries = append(out.Entries, entry{Start: ts(prevEnd), End: ts(i.Start), Minutes: minutes(g), Duration: core.FormatDuration(g), Gap: true})
				}
				prevEnd = i.End
				out.Entries = append(out.Entries, entry{Hat: i.Hat, HatName: nameOf(nm, i.Hat), Activity: i.Activity,
					Detail: core.ShortDetail(i.Detail), DetailURL: core.DetailURL(i.Detail, links),
					Start: ts(i.Start), End: ts(i.End), Minutes: minutes(i.Duration()), Duration: core.FormatDuration(i.Duration()), Running: i.Open})
			}
			return nil, out, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "get_current", Annotations: readOnly("What runs now"),
		Description: "The hat and activity worn now (or paused, or none), since when, and today's time per hat."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, currentOut, error) {
			st, err := s.st.CurrentState(ctx)
			if err != nil {
				return nil, currentOut{}, err
			}
			cfg, _, err := s.st.GetConfig(ctx)
			if err != nil {
				return nil, currentOut{}, err
			}
			nm := names(cfg)
			out := currentOut{State: string(st.Mode), Today: []hatTotal{}}
			if st.Mode != core.ModeNone {
				out.Hat, out.HatName, out.Activity = st.Hat, nameOf(nm, st.Hat), st.Activity
				links, _ := s.st.TicketLinks(ctx)
				out.Detail, out.DetailURL = core.ShortDetail(st.Detail), core.DetailURL(st.Detail, links)
				out.Since = st.Since.In(s.Loc).Format(time.RFC3339)
				if st.Mode == core.ModeWearing {
					out.Minutes = minutes(s.Now().Sub(st.Since))
				}
			}
			day := parseDate("", s.Loc, s.Now())
			_, tot, err := s.intervalsIn(ctx, day, day.AddDate(0, 0, 1))
			if err != nil {
				return nil, currentOut{}, err
			}
			ds := make([]time.Duration, len(tot))
			for i, t := range tot {
				ds[i] = t.Duration
			}
			for i, m := range core.Apportion(ds) {
				out.Today = append(out.Today, hatTotal{Hat: tot[i].Hat, HatName: nameOf(nm, tot[i].Hat), Minutes: m,
					Duration: core.FormatDuration(time.Duration(m) * time.Minute)})
			}
			out.TodayTotal = core.FormatDuration(sumOf(tot))
			return nil, out, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "list_hats", Annotations: readOnly("Hats and activities"),
		Description: "The configured hats with their activities, colours and resume setting, plus the working hours."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in hatsIn) (*mcp.CallToolResult, hatsOut, error) {
			cfg, _, err := s.st.GetConfig(ctx)
			if err != nil {
				return nil, hatsOut{}, err
			}
			out := hatsOut{Hats: []hatInfo{}, WorkHours: cfg.Work()}
			for _, h := range cfg.Hats {
				if h.Archived && !in.IncludeArchived {
					continue
				}
				acts := h.Activities
				if acts == nil {
					acts = []string{}
				}
				out.Hats = append(out.Hats, hatInfo{Key: h.Key, Name: h.DisplayName(), Color: h.Color, Activities: acts,
					ArchivedActivities: h.ArchivedActivity, Archived: h.Archived, ResumeActivity: cfg.ResumeMode(h)})
			}
			return nil, out, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "get_config", Annotations: readOnly("Whole configuration"),
		Description: "The whole configuration as YAML: hats, activities, module settings (wallpaper, theme, programs, scripts) and defaults."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, configOut, error) {
			cfg, _, err := s.st.GetConfig(ctx)
			if err != nil {
				return nil, configOut{}, err
			}
			b, err := cfg.Redacted().Export() // an AI reads, it needs no passwords
			if err != nil {
				return nil, configOut{}, err
			}
			return text("%s", b), configOut{YAML: string(b)}, nil
		})
}

func filterHat(iv []core.Interval, key string) []core.Interval {
	var out []core.Interval
	for _, i := range iv {
		if i.Hat == key {
			out = append(out, i)
		}
	}
	return out
}
