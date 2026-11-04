package server

import (
	"context"
	"embed"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

//go:embed templates/*.html static/*
var webFS embed.FS

const (
	tokenCookie = "hatrack_token"
	langCookie  = "hatrack_lang"
	tzCookie    = "hatrack_tz"
)

var pageFuncs = template.FuncMap{
	"t": func(lang, key string, args ...any) string { return i18n.In(lang, key, args...) },
	// dict passes several values to a sub-template: dict "A" 1 "B" 2
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[fmt.Sprint(kv[i])] = kv[i+1]
		}
		return m
	},
}

var pages = map[string]*template.Template{}

func init() {
	for _, p := range []string{"login", "now", "day", "week", "setup", "sethat", "setdefaults"} {
		pages[p] = template.Must(template.New("layout").Funcs(pageFuncs).
			ParseFS(webFS, "templates/layout.html", "templates/"+p+".html", "templates/forms.html"))
	}
}

// ---- views ----------------------------------------------------------------

type navItem struct {
	Href, Label string
	Active      bool
}

type view struct {
	Lang     string
	Page     string
	Nav      []navItem
	CanWrite bool
	Live     bool
	Msg      string
	Err      string
	Path     string

	Login    loginView
	Now      nowView
	Day      dayView
	Week     weekView
	Setup    setupView
	Hat      hatEdit
	Defaults defaultsEdit
}

type loginView struct {
	NoPassword bool
	Failed     bool
}

type activityChip struct {
	Name    string
	Current bool
}

type hatCard struct {
	Key, Name, Color string
	Ink              string // text colour on the hat colour
	Current          bool
	Activities       []activityChip
}

type nowView struct {
	Mode     string
	Hat      string
	Color    string
	Activity string
	Since    string
	SinceMS  int64 // for the live duration
	Duration string
	Cards    []hatCard
	Today    []todayRow
	TodaySum string
	SumBase  int64 // with Today[i].SinceMS, the sum keeps counting too
	SumSince int64
	Running  bool // the time log is running (a hat is worn)
}

// todayRow is one hat in the "today" summary of the Now page.
type todayRow struct {
	Name, Color, Duration string
	Pct                   float64
	BaseMS, SinceMS       int64 // the worn hat keeps counting in the browser
}

type segment struct {
	Left, Width float64
	Color       string
	Title       string
}

type rowView struct {
	Start, End, Hat, Color, Activity, Duration string
	Open                                       bool
	Gap                                        bool   // a stretch without an entry
	ID                                         string // the event that opened the entry
	What                                       string // value of the choice: hat|activity
	StartIn, EndIn                             string // HH:MM for the edit form
}

// choiceGroup is one hat in the "what" list of the edit forms.
type choiceGroup struct {
	Label   string
	Options []option
}

type actTotal struct{ Name, Duration string }

type totalView struct {
	Hat, Color, Duration, Delta string
	Pct                         float64
	Activities                  []actTotal
}

type dayView struct {
	Title      string
	Date       string
	Prev, Next string
	Segments   []segment
	Rows       []rowView
	Totals     []totalView
	Sum        string
	Choices    []choiceGroup
	Done       string // id of the correction just made, for undo
	IsToday    bool
}

type dayBar struct {
	Label, Date, Duration string
	Segments              []segment
}

type weekView struct {
	Title      string
	Prev, Next string
	CSV        string
	Days       []dayBar
	Totals     []totalView
	Sum        string
	SumDelta   string
}

// ---- helpers ---------------------------------------------------------------

func (s *Server) langOf(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil {
		if l := i18n.Normalize(c.Value); l != "" {
			return l
		}
	}
	return i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

// locOf returns the time zone for display: the browser's (cookie), else the
// server's.
func (s *Server) locOf(r *http.Request) *time.Location {
	if c, err := r.Cookie(tzCookie); err == nil {
		if l, err := time.LoadLocation(c.Value); err == nil {
			return l
		}
	}
	return s.Loc
}

var fallbackColors = []string{"#4e79a7", "#f28e2b", "#59a14f", "#e15759", "#76b7b2", "#b07aa1", "#edc948", "#9c755f"}

type palette map[string]string

func newPalette(cfg core.Config) palette {
	p := palette{}
	for i, h := range cfg.Hats {
		c := h.Color
		if c == "" {
			c = fallbackColors[i%len(fallbackColors)]
		}
		p[h.Key] = c
	}
	return p
}

// ink picks light or dark text for a background colour.
func ink(hex string) string {
	var r, g, b int
	if _, err := fmt.Sscanf(strings.ToLower(hex), "#%02x%02x%02x", &r, &g, &b); err != nil {
		return "#fff"
	}
	if 0.299*float64(r)+0.587*float64(g)+0.114*float64(b) > 150 {
		return "#181a1e"
	}
	return "#fff"
}

func (p palette) color(key string) string {
	if c, ok := p[key]; ok {
		return c
	}
	return "#888888"
}

func names(cfg core.Config) map[string]string {
	m := map[string]string{}
	for _, h := range cfg.Hats {
		m[h.Key] = h.DisplayName()
	}
	return m
}

func nameOf(m map[string]string, key string) string {
	if n, ok := m[key]; ok {
		return n
	}
	return key
}

func (s *Server) base(r *http.Request, page string, acc access) view {
	lang := s.langOf(r)
	v := view{Lang: lang, Page: page, CanWrite: acc == fullAccess, Path: r.URL.Path}
	if acc != noAccess {
		for _, n := range []struct{ page, href, key string }{
			{"now", "/", "ui.nav.now"}, {"day", "/day", "ui.nav.day"}, {"week", "/week", "ui.nav.week"},
		} {
			v.Nav = append(v.Nav, navItem{n.href, i18n.In(lang, n.key), n.page == page})
		}
		if acc == fullAccess {
			v.Nav = append(v.Nav, navItem{"/setup", i18n.In(lang, "web.nav.setup"), strings.HasPrefix(page, "set")})
		}
	}
	if m := r.URL.Query().Get("e"); m != "" {
		v.Err = m
	}
	return v
}

func (s *Server) render(w http.ResponseWriter, v view, code int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := pages[v.Page].ExecuteTemplate(w, "layout", v); err != nil {
		log.Printf("render %s: %v", v.Page, err)
	}
}

// page wraps a browser page: language switch, access check, redirect to the
// login page.
func (s *Server) page(needWrite bool, h func(http.ResponseWriter, *http.Request, access)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if l := i18n.Normalize(q.Get("lang")); l != "" && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", MaxAge: 365 * 86400, SameSite: http.SameSiteLaxMode})
			q.Del("lang")
			http.Redirect(w, r, r.URL.Path+queryString(q), http.StatusSeeOther)
			return
		}
		if tok := q.Get("token"); tok != "" && r.Method == http.MethodGet {
			// A device token gives a read-only view; the login gives more.
			http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: tok, Path: "/", MaxAge: 365 * 86400,
				HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode})
			q.Del("token")
			http.Redirect(w, r, r.URL.Path+queryString(q), http.StatusSeeOther)
			return
		}
		acc := s.accessOf(r)
		if acc == noAccess || (needWrite && acc != fullAccess) {
			if r.Method != http.MethodGet {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-site request", http.StatusForbidden)
			return
		}
		h(w, r, acc)
	}
}

func queryString(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// ---- login -----------------------------------------------------------------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && (r.URL.Query().Get("lang") != "") {
		if l := i18n.Normalize(r.URL.Query().Get("lang")); l != "" {
			http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", MaxAge: 365 * 86400, SameSite: http.SameSiteLaxMode})
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	hash, err := s.st.PasswordHash(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	v := s.base(r, "login", noAccess)
	v.Login.NoPassword = hash == ""
	if r.Method == http.MethodPost {
		if !sameOrigin(r) {
			http.Error(w, "cross-site request", http.StatusForbidden)
			return
		}
		s.limiter.wait()
		pw := r.PostFormValue("password")
		if hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil {
			s.limiter.result(true)
			tok := newSessionToken()
			if err := s.st.CreateSession(r.Context(), tokenHash(tok), s.Now().Add(sessionTTL)); err != nil {
				serverError(w, err)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(sessionTTL.Seconds()),
				HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		s.limiter.result(false)
		v.Login.Failed = true
		s.render(w, v, http.StatusUnauthorized)
		return
	}
	s.render(w, v, http.StatusOK)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ access) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.st.DeleteSession(r.Context(), tokenHash(c.Value))
	}
	for _, n := range []string{sessionCookie, tokenCookie} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1})
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- Now -------------------------------------------------------------------

func (s *Server) nowPage(w http.ResponseWriter, r *http.Request, acc access) {
	ctx := r.Context()
	st, err := s.st.CurrentState(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	cfg, _, _ := s.st.GetConfig(ctx)
	pal := newPalette(cfg)
	loc := s.locOf(r)
	v := s.base(r, "now", acc)
	v.Live = true
	n := &v.Now
	n.Mode = string(st.Mode)
	if h, ok := cfg.Hat(st.Hat); ok {
		n.Hat = h.DisplayName()
	} else {
		n.Hat = st.Hat
	}
	n.Color = pal.color(st.Hat)
	n.Activity = st.Activity
	if st.Mode != core.ModeNone {
		n.Since = st.Since.In(loc).Format("15:04")
		if st.Mode == core.ModeWearing {
			n.Duration = core.FormatDuration(s.Now().Sub(st.Since))
			n.SinceMS = st.Since.UnixMilli()
			n.Running = true
		}
	}
	for _, h := range cfg.ActiveHats() {
		col := pal.color(h.Key)
		c := hatCard{Key: h.Key, Name: h.DisplayName(), Color: col, Ink: ink(col), Current: st.Mode != core.ModeNone && st.Hat == h.Key}
		for _, a := range h.Activities {
			c.Activities = append(c.Activities, activityChip{a, c.Current && st.Mode == core.ModeWearing && st.Activity == a})
		}
		n.Cards = append(n.Cards, c)
	}
	// today, per hat; the worn hat keeps counting in the browser
	day := parseDate("", loc, s.Now())
	if _, tot, err := s.intervalsIn(ctx, day, day.AddDate(0, 0, 1)); err == nil {
		var most time.Duration
		for _, t := range tot {
			most = max(most, t.Duration)
		}
		for _, t := range tot {
			row := todayRow{Name: nameOf(names(cfg), t.Hat), Color: pal.color(t.Hat), Duration: core.FormatDuration(t.Duration)}
			if most > 0 {
				row.Pct = float64(t.Duration) / float64(most) * 100
			}
			if n.Running && t.Hat == st.Hat {
				since := st.Since
				if since.Before(day) {
					since = day
				}
				row.SinceMS = since.UnixMilli()
				row.BaseMS = (t.Duration - s.Now().Sub(since)).Milliseconds()
			}
			n.Today = append(n.Today, row)
		}
		n.TodaySum = core.FormatDuration(sumOf(tot))
		for _, r := range n.Today {
			if r.SinceMS != 0 {
				n.SumSince = r.SinceMS
				n.SumBase = sumOf(tot).Milliseconds() - (s.Now().UnixMilli() - r.SinceMS)
			}
		}
	}
	s.render(w, v, http.StatusOK)
}

// action performs a switch from the browser. The server writes the event
// itself, with the same rules as the CLI; the agents follow through SSE.
func (s *Server) action(w http.ResponseWriter, r *http.Request, _ access) {
	ctx := r.Context()
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	st, err := s.st.CurrentState(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	last, _ := s.st.LastActivities(ctx)
	in := core.Input{Device: "web", Now: s.Now()}
	hat, act := r.PostFormValue("hat"), r.PostFormValue("activity")
	var ev *core.Event
	switch r.PostFormValue("op") {
	case "on":
		// "ask" is treated like "never": the activity buttons are right there.
		ev, err = core.PlanOn(cfg, st, last, core.OnArgs{Hat: hat, Activity: act, HasActivity: act != ""}, in)
	case "do":
		ev, err = core.PlanDo(cfg, st, act, in)
	case "off":
		ev, err = core.PlanOff(st, r.PostFormValue("neutral") != "", in)
	case "resume":
		ev, err = core.PlanOn(cfg, st, last, core.OnArgs{}, in)
	default:
		err = fmt.Errorf("unknown action")
	}
	dest := "/"
	if err != nil {
		dest += "?e=" + url.QueryEscape(webError(s.langOf(r), err))
	} else if ev != nil {
		if _, err := s.st.AppendEvents(ctx, []core.Event{*ev}); err != nil {
			serverError(w, err)
			return
		}
		s.broadcastState(ctx)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func webError(lang string, err error) string {
	var nf *core.NotFoundError
	var am *core.AmbiguousError
	switch {
	case errors.As(err, &nf):
		return i18n.In(lang, "err."+nf.Kind+".notfound", nf.Input, strings.Join(nf.Available, ", "))
	case errors.As(err, &am):
		return i18n.In(lang, "err."+am.Kind+".ambiguous", am.Input, strings.Join(am.Candidates, ", "))
	case err == core.ErrNoHatToResume:
		return i18n.In(lang, "err.noresume")
	case err == core.ErrNoHat:
		return i18n.In(lang, "err.nohat")
	case err == core.ErrPaused:
		return i18n.In(lang, "err.paused")
	}
	return err.Error()
}

// ---- Day and Week ------------------------------------------------------------

func parseDate(v string, loc *time.Location, now time.Time) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t
	}
	n := now.In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
}

// intervalsIn returns the intervals of [from, to) and their totals.
func (s *Server) intervalsIn(ctx context.Context, from, to time.Time) ([]core.Interval, []core.HatTotal, error) {
	ev, err := s.st.Effective(ctx, to)
	if err != nil {
		return nil, nil, err
	}
	iv := core.Clip(core.Intervals(ev, s.Now()), from, to)
	return iv, core.Totals(iv), nil
}

func dateStr(t time.Time) string { return t.Format("2006-01-02") }

func (s *Server) totalViews(tot []core.HatTotal, prev map[string]time.Duration, pal palette, nm map[string]string, lang string) []totalView {
	var max time.Duration
	for _, t := range tot {
		if t.Duration > max {
			max = t.Duration
		}
	}
	var out []totalView
	for _, t := range tot {
		tv := totalView{Hat: nameOf(nm, t.Hat), Color: pal.color(t.Hat), Duration: core.FormatDuration(t.Duration)}
		if max > 0 {
			tv.Pct = float64(t.Duration) / float64(max) * 100
		}
		if prev != nil {
			tv.Delta = delta(t.Duration - prev[t.Hat])
		}
		if !(len(t.Activities) == 1 && t.Activities[0].Activity == "") {
			for _, a := range t.Activities {
				name := a.Activity
				if name == "" {
					name = i18n.In(lang, "cli.log.noactivity")
				}
				tv.Activities = append(tv.Activities, actTotal{name, core.FormatDuration(a.Duration)})
			}
		}
		out = append(out, tv)
	}
	return out
}

func delta(d time.Duration) string {
	if d.Round(time.Minute) == 0 {
		return ""
	}
	if d < 0 {
		return "−" + core.FormatDuration(-d)
	}
	return "+" + core.FormatDuration(d)
}

func sumOf(tot []core.HatTotal) time.Duration {
	var s time.Duration
	for _, t := range tot {
		s += t.Duration
	}
	return s
}

func (s *Server) dayPage(w http.ResponseWriter, r *http.Request, acc access) {
	loc, lang := s.locOf(r), s.langOf(r)
	day := parseDate(r.URL.Query().Get("date"), loc, s.Now())
	next := day.AddDate(0, 0, 1)
	iv, tot, err := s.intervalsIn(r.Context(), day, next)
	if err != nil {
		serverError(w, err)
		return
	}
	cfg, _, _ := s.st.GetConfig(r.Context())
	pal, nm := newPalette(cfg), names(cfg)
	v := s.base(r, "day", acc)
	v.Live = dateStr(day) == dateStr(s.Now().In(loc))
	d := &v.Day
	d.Title = day.Format("Mon 2006-01-02")
	d.Prev, d.Next = "/day?date="+dateStr(day.AddDate(0, 0, -1)), "/day?date="+dateStr(next)
	span := float64(next.Sub(day))
	d.Date = dateStr(day)
	d.IsToday = v.Live
	d.Done = r.URL.Query().Get("done")
	if d.Done != "" {
		v.Msg = i18n.In(lang, "web.day.saved")
	}
	d.Choices = choices(cfg, iv, lang)
	var prevEnd time.Time
	for _, i := range iv {
		if !prevEnd.IsZero() && i.Start.Sub(prevEnd) >= time.Minute {
			d.Rows = append(d.Rows, rowView{Gap: true, Start: prevEnd.In(loc).Format("15:04"), End: i.Start.In(loc).Format("15:04"),
				StartIn: prevEnd.In(loc).Format("15:04"), EndIn: i.Start.In(loc).Format("15:04"),
				Duration: core.FormatDuration(i.Start.Sub(prevEnd))})
		}
		prevEnd = i.End
		what := nameOf(nm, i.Hat)
		if i.Activity != "" {
			what += " · " + i.Activity
		}
		end := i.End.In(loc).Format("15:04")
		if i.Open {
			end = i18n.In(lang, "cli.log.now")
		}
		d.Segments = append(d.Segments, segment{
			Left: float64(i.Start.Sub(day)) / span * 100, Width: float64(i.Duration()) / span * 100,
			Color: pal.color(i.Hat), Title: fmt.Sprintf("%s–%s %s", i.Start.In(loc).Format("15:04"), end, what)})
		row := rowView{Start: i.Start.In(loc).Format("15:04"), End: end, Hat: nameOf(nm, i.Hat),
			Color: pal.color(i.Hat), Activity: i.Activity, Duration: core.FormatDuration(i.Duration()), Open: i.Open,
			ID: i.StartID, What: i.Hat + "|" + i.Activity, StartIn: i.Start.In(loc).Format("15:04")}
		if !i.Open {
			row.EndIn = i.End.In(loc).Format("15:04")
		}
		d.Rows = append(d.Rows, row)
	}
	d.Totals = s.totalViews(tot, nil, pal, nm, lang)
	d.Sum = core.FormatDuration(sumOf(tot))
	s.render(w, v, http.StatusOK)
}

func weekStart(t time.Time) time.Time {
	back := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -back)
}

func (s *Server) weekPage(w http.ResponseWriter, r *http.Request, acc access) {
	loc, lang := s.locOf(r), s.langOf(r)
	start := weekStart(parseDate(r.URL.Query().Get("date"), loc, s.Now()))
	end := start.AddDate(0, 0, 7)
	iv, tot, err := s.intervalsIn(r.Context(), start, end)
	if err != nil {
		serverError(w, err)
		return
	}
	_, prevTot, err := s.intervalsIn(r.Context(), start.AddDate(0, 0, -7), start)
	if err != nil {
		serverError(w, err)
		return
	}
	prev := map[string]time.Duration{}
	for _, t := range prevTot {
		prev[t.Hat] = t.Duration
	}
	cfg, _, _ := s.st.GetConfig(r.Context())
	pal, nm := newPalette(cfg), names(cfg)
	v := s.base(r, "week", acc)
	v.Live = !s.Now().Before(start) && s.Now().Before(end)
	wv := &v.Week
	wv.Title = fmt.Sprintf("%s – %s", start.Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02"))
	wv.Prev, wv.Next = "/week?date="+dateStr(start.AddDate(0, 0, -7)), "/week?date="+dateStr(end)
	wv.CSV = "/week.csv?date=" + dateStr(start)

	// one bar per day, scaled to the busiest day
	perDay := make([][]core.Interval, 7)
	for _, i := range core.Clip(iv, start, end) {
		for d := 0; d < 7; d++ {
			ds := start.AddDate(0, 0, d)
			for _, c := range core.Clip([]core.Interval{i}, ds, ds.AddDate(0, 0, 1)) {
				perDay[d] = append(perDay[d], c)
			}
		}
	}
	var busiest time.Duration
	daySum := make([]time.Duration, 7)
	for d, list := range perDay {
		for _, i := range list {
			daySum[d] += i.Duration()
		}
		busiest = max(busiest, daySum[d])
	}
	for d := 0; d < 7; d++ {
		ds := start.AddDate(0, 0, d)
		bar := dayBar{Label: ds.Format("Mon 02.01."), Date: dateStr(ds), Duration: core.FormatDuration(daySum[d])}
		var x float64
		for _, i := range perDay[d] {
			wd := float64(i.Duration()) / float64(max(busiest, time.Minute)) * 100
			bar.Segments = append(bar.Segments, segment{Left: x, Width: wd, Color: pal.color(i.Hat),
				Title: fmt.Sprintf("%s %s", nameOf(nm, i.Hat), core.FormatDuration(i.Duration()))})
			x += wd
		}
		wv.Days = append(wv.Days, bar)
	}
	wv.Totals = s.totalViews(tot, prev, pal, nm, lang)
	wv.Sum = core.FormatDuration(sumOf(tot))
	wv.SumDelta = delta(sumOf(tot) - sumOf(prevTot))
	s.render(w, v, http.StatusOK)
}

// weekCSV exports the intervals of a week: date, hat, activity, start, end,
// minutes, note. Intervals that cross midnight are split per day.
func (s *Server) weekCSV(w http.ResponseWriter, r *http.Request, _ access) {
	loc := s.locOf(r)
	start := weekStart(parseDate(r.URL.Query().Get("date"), loc, s.Now()))
	end := start.AddDate(0, 0, 7)
	iv, _, err := s.intervalsIn(r.Context(), start, end)
	if err != nil {
		serverError(w, err)
		return
	}
	cfg, _, _ := s.st.GetConfig(r.Context())
	nm := names(cfg)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="hatrack-%s.csv"`, dateStr(start)))
	cw := csv.NewWriter(w)
	cw.Write([]string{"date", "hat", "activity", "start", "end", "minutes", "note"})
	var rows [][]string
	for _, i := range iv {
		for d := 0; d < 7; d++ {
			ds := start.AddDate(0, 0, d)
			for _, c := range core.Clip([]core.Interval{i}, ds, ds.AddDate(0, 0, 1)) {
				rows = append(rows, []string{dateStr(ds), nameOf(nm, c.Hat), c.Activity,
					c.Start.In(loc).Format("15:04"), c.End.In(loc).Format("15:04"),
					fmt.Sprint(int(c.Duration().Round(time.Minute) / time.Minute)), ""})
			}
		}
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a][0]+rows[a][3] < rows[b][0]+rows[b][3] })
	cw.WriteAll(rows)
	cw.Flush()
}

// ---- live updates -------------------------------------------------------------

func (s *Server) uiStream(w http.ResponseWriter, r *http.Request, _ access) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return
	}
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "event: state\ndata: {}\n\n") // the first message only says "connected"
	fl.Flush()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.Event == "state" || m.Event == "config" {
				writeSSE(w, message{Event: "state", Data: "{}"})
				fl.Flush()
			}
		case <-tick.C:
			io.WriteString(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("static/" + r.PathValue("file"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(r.URL.Path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	case strings.HasSuffix(r.URL.Path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}
