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
	// palette lists the hat colours with their names in a language
	"palette": func(lang string) []swatch {
		var out []swatch
		for _, c := range core.HatColors {
			out = append(out, swatch{c.Name, c.Hex, i18n.In(lang, "web.color."+c.Name)})
		}
		return out
	},
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
	for _, p := range []string{"login", "now", "day", "week", "setup", "sethat", "setdefaults", "devices", "detection", "ai"} {
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
	Langs    []langLink // the language switch, for the page shown

	Login     loginView
	Now       nowView
	Day       dayView
	Week      weekView
	Setup     setupView
	Hat       hatEdit
	Defaults  defaultsEdit
	Devices   devicesView
	Detection detectionView
	AI        aiView
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
	Total            string // today
	BaseMS, SinceMS  int64  // the worn hat's total keeps counting in the browser
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
	TodaySum string
	SumBase  int64 // with SumSince, the day's sum keeps counting too
	SumSince int64
	Running  bool // the time log is running (a hat is worn)
	TL       timelineView
	Detail   []core.DetailPart // of the running entry, Jira keys linked
	DetailIn string            // its short form, for the field
	Recent   []string          // recent details of this hat and activity
	Suggest  []string          // all details of this hat in the last weeks, for the field
}

type segment struct {
	Left, Width float64
	Color       string
	Title       string
	Label       string // shown inside the segment if there is room
	Ink         string // text colour on Color
}

// timelineView is the day as a band: entries, the time outside the working
// hours, hour marks and where "now" is.
type timelineView struct {
	Segments []segment
	Off      []segment
	Ticks    []tick
	Work     string // "08:00–18:00", or "" on a day off
	NowLeft  float64
	ShowNow  bool
}

// timeline lays out the intervals of one day (see dayWindow).
func (s *Server) timeline(day time.Time, cfg core.Config, iv []core.Interval, lang string) timelineView {
	from, to, _, _, _ := dayWindow(day, cfg.Work(), iv)
	return s.layout(day, from, to, cfg, iv, lang)
}

// layout places the intervals of one day on a band from `from` to `to`.
func (s *Server) layout(day, from, to time.Time, cfg core.Config, iv []core.Interval, lang string) timelineView {
	var tl timelineView
	pal, nm := newPalette(cfg), names(cfg)
	_, _, wFrom, wTo, workday := dayWindow(day, cfg.Work(), nil)
	span := float64(to.Sub(from))
	pct := func(t time.Time) float64 { return min(max(float64(t.Sub(from))/span*100, 0), 100) }
	if workday {
		tl.Work = wFrom.Format("15:04") + "–" + wTo.Format("15:04")
		if wFrom.After(from) {
			tl.Off = append(tl.Off, segment{Left: 0, Width: pct(wFrom)})
		}
		if wTo.Before(to) {
			tl.Off = append(tl.Off, segment{Left: pct(wTo), Width: 100 - pct(wTo)})
		}
	} else {
		tl.Off = []segment{{Left: 0, Width: 100}}
	}
	step := time.Hour
	if to.Sub(from) > 12*time.Hour {
		step = 2 * time.Hour
	}
	for t := from; !t.After(to); t = t.Add(step) {
		tl.Ticks = append(tl.Ticks, tick{Left: pct(t), Label: t.Format("15")})
	}
	loc := day.Location()
	for _, i := range iv {
		if fleeting(i) {
			continue
		}
		label := nameOf(nm, i.Hat)
		if i.Activity != "" {
			label += " · " + i.Activity
		}
		end := i.End.In(loc).Format("15:04")
		if i.Open {
			end = i18n.In(lang, "cli.log.now")
		}
		col := pal.color(i.Hat)
		tl.Segments = append(tl.Segments, segment{Left: pct(i.Start), Width: float64(i.Duration()) / span * 100,
			Color: col, Ink: ink(col), Label: label,
			Title: fmt.Sprintf("%s–%s %s", i.Start.In(loc).Format("15:04"), end, label)})
	}
	if now := s.Now(); !now.Before(from) && now.Before(to) {
		tl.ShowNow, tl.NowLeft = true, pct(now)
	}
	return tl
}

// fleeting reports an entry that ended within a minute of its start, e.g.
// while clicking through hats. Day lists and bands leave it out.
func fleeting(i core.Interval) bool { return !i.Open && i.Duration() < core.FleetingMax }

type rowView struct {
	Start, End, Hat, Color, Activity, Duration string
	Open                                       bool
	Gap                                        bool   // a stretch without an entry
	ID                                         string // the event that opened the entry
	Detail                                     []core.DetailPart
	DetailIn                                   string // short form, for the edit form
	What                                       string // value of the choice: hat|activity
	StartIn, EndIn                             string // HH:MM for the edit form
}

// choiceGroup is one hat in the "what" list of the edit forms.
type choiceGroup struct {
	Label   string
	Options []option
}

type actTotal struct {
	Name, Duration string
	Details        []detailTotal
}

type detailTotal struct {
	Parts    []core.DetailPart // the detail, Jira keys linked
	Duration string
}

type totalView struct {
	NotWork                     bool // not working time: shown below the sum
	Hat, Color, Duration, Delta string
	Pct                         float64
	Activities                  []actTotal
}

type dayView struct {
	Title      string
	Date       string
	Prev, Next string
	TL         timelineView
	Obs        *obsView      // the band "Observed"; nil if there is nothing
	Booked     []bookedRange // for the add form's "replaces" note
	Rows       []rowView
	Totals     []totalView
	Sum        string
	Choices    []choiceGroup
	Done       string // id of the correction just made, for undo
	IsToday    bool
}

// bookedRange is a booked entry in minutes of the day, for the add form's
// note on what it replaces (app.js).
type bookedRange struct {
	S int    `json:"s"`
	E int    `json:"e"`
	T string `json:"t"`
}

type tick struct {
	Left  float64
	Label string
}

// dayWindow is the part of the day the timeline shows: the working hours,
// widened to whole hours for entries outside them. On a day off it is the
// stretch with entries (or the working hours, if there are none).
func dayWindow(day time.Time, work core.WorkHours, iv []core.Interval) (from, to, wFrom, wTo time.Time, workday bool) {
	ws, we := work.Minutes()
	at := func(min int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), min/60, min%60, 0, 0, day.Location())
	}
	wFrom, wTo = at(ws), at(we)
	workday = work.IsWorkday(day.Weekday())
	from, to = wFrom, wTo
	if !workday && len(iv) > 0 {
		from, to = iv[0].Start, iv[len(iv)-1].End
	}
	for _, i := range iv {
		if i.Start.Before(from) {
			from = i.Start
		}
		if i.End.After(to) {
			to = i.End
		}
	}
	from = time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), 0, 0, 0, from.Location())
	if t := time.Date(to.Year(), to.Month(), to.Day(), to.Hour(), 0, 0, 0, to.Location()); t.Before(to) {
		to = t.Add(time.Hour)
	}
	next := day.AddDate(0, 0, 1)
	if from.Before(day) {
		from = day
	}
	if to.After(next) {
		to = next
	}
	if to.Sub(from) < time.Hour {
		to = from.Add(time.Hour)
	}
	return
}

type dayBar struct {
	Label, Date, Duration string
	TL                    timelineView
}

type weekView struct {
	Title      string
	Prev, Next string
	CSV        string
	Days       []dayBar
	Ticks      []tick // the hours, the same for every day
	Totals     []totalView
	Compare    bool // the previous week has entries to compare with
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

// fallbackColors are the hat colours offered in Setup, in their order.
var fallbackColors = func() []string {
	var out []string
	for _, c := range core.HatColors {
		out = append(out, c.Hex)
	}
	return out
}()

// swatch is a palette colour for the colour pickers in Setup.
type swatch struct{ Name, Hex, Label string }

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

// langLink switches the language and stays on the page shown, with its
// query (the day, the week).
type langLink struct {
	Label, Href string
	On          bool
}

func langLinks(r *http.Request, cur string) []langLink {
	var out []langLink
	for _, l := range []string{"en", "de"} {
		q := r.URL.Query()
		for _, k := range []string{"e", "done", "saved", "imported"} {
			q.Del(k) // one-time messages
		}
		q.Set("lang", l)
		out = append(out, langLink{strings.ToUpper(l), r.URL.Path + "?" + q.Encode(), l == cur})
	}
	return out
}

func (s *Server) base(r *http.Request, page string, acc access) view {
	lang := s.langOf(r)
	v := view{Lang: lang, Page: page, CanWrite: acc == fullAccess, Path: r.URL.Path}
	if r.Method == http.MethodGet {
		v.Langs = langLinks(r, lang)
	} else {
		v.Langs = langLinks(&http.Request{URL: &url.URL{Path: r.URL.Path}}, lang)
	}
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
		if q.Get("token") != "" && r.Method == http.MethodGet {
			// The read-only view by device token is gone (a token in an
			// address ends up in histories); old links lose the token and
			// lead to the login.
			http.SetCookie(w, &http.Cookie{Name: tokenCookie, Path: "/", MaxAge: -1})
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
	hist, err := s.history(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	st := hist.State
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
		links, _ := s.st.TicketLinks(ctx)
		n.Detail, n.DetailIn = core.SplitDetail(st.Detail, links), core.ShortDetail(st.Detail)
		since := s.Now().Add(-core.RecentWindow)
		for _, d := range core.RecentDetails(hist.Events, since, 6)[st.Hat][st.Activity] {
			if d != n.DetailIn && len(n.Recent) < 5 {
				n.Recent = append(n.Recent, d)
			}
		}
		// the field suggests every detail of the hat, the worn activity's first
		byHat := s.recentDetails(hist)[st.Hat]
		for _, own := range []bool{true, false} {
			for _, d := range byHat {
				if (d.Activity == st.Activity) == own && d.Detail != n.DetailIn {
					n.Suggest = append(n.Suggest, d.Detail)
				}
			}
		}
	}
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
	if iv, tot, err := s.intervalsIn(ctx, day, day.AddDate(0, 0, 1)); err == nil {
		since := st.Since
		if since.Before(day) {
			since = day
		}
		byHat := map[string]time.Duration{}
		minByHat := map[string]int{}
		hatMin, _, _ := core.RoundTotals(tot) // the cards add up to the day
		for i, t := range tot {
			byHat[t.Hat], minByHat[t.Hat] = t.Duration, hatMin[i]
		}
		for i := range n.Cards {
			c := &n.Cards[i]
			if m, ok := minByHat[c.Key]; ok {
				c.Total = core.FormatMinutes(m)
			}
			if n.Running && c.Key == st.Hat {
				c.SinceMS = since.UnixMilli()
				c.BaseMS = (byHat[c.Key] - s.Now().Sub(since)).Milliseconds()
			}
		}
		n.TodaySum = core.FormatDuration(sumOf(tot, cfg))
		if n.Running && cfg.IsWork(st.Hat) {
			n.SumSince = since.UnixMilli()
			n.SumBase = (sumOf(tot, cfg) - s.Now().Sub(since)).Milliseconds()
		}
		n.TL = s.timeline(day, cfg, iv, s.langOf(r))
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
	hist, err := s.history(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	st, last := hist.State, hist.Last
	in := core.Input{Device: "web", Now: s.Now()}
	hat, act := r.PostFormValue("hat"), r.PostFormValue("activity")
	var ev *core.Event
	switch r.PostFormValue("op") {
	case "on":
		// "ask" is treated like "never": the activity buttons are right there.
		ev, err = core.PlanOn(cfg, st, last, core.OnArgs{Hat: hat, Activity: act, HasActivity: act != ""}, in)
	case "do":
		ev, err = core.PlanDo(cfg, st, act, r.PostFormValue("detail"), in)
	case "off":
		ev, err = core.PlanOff(st, r.PostFormValue("neutral") != "", in)
	case "resume":
		ev, err = core.PlanOn(cfg, st, last, core.OnArgs{}, in)
	case "detail":
		ev, err = core.PlanDetail(st, r.PostFormValue("detail"), in)
	case "detail-clear":
		ev, err = core.PlanDetail(st, "-", in)
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
	h, err := s.history(ctx)
	if err != nil {
		return nil, nil, err
	}
	ev := h.Before(to)
	iv := core.Clip(core.Settle(core.Intervals(ev, s.Now())), from, to)
	return iv, core.Totals(iv), nil
}

func dateStr(t time.Time) string { return t.Format("2006-01-02") }

// dayLabel shows a date the way the language writes it: "Mi 04.11.2026" in
// German, "Wed 2026-11-04" in English. Without year: "Mi 04.11." / "Wed 11-04".
func dayLabel(lang string, t time.Time, year bool) string {
	wd := i18n.In(lang, "web.wd."+core.Weekdays[t.Weekday()])
	switch {
	case lang == "de" && year:
		return wd + " " + t.Format("02.01.2006")
	case lang == "de":
		return wd + " " + t.Format("02.01.")
	case year:
		return wd + " " + t.Format("2006-01-02")
	}
	return wd + " " + t.Format("01-02")
}

func (s *Server) totalViews(tot []core.HatTotal, prev map[string]time.Duration, pal palette, nm map[string]string, lang string, links core.TicketLinks, cfg core.Config) []totalView {
	var max time.Duration
	for _, t := range tot {
		if t.Duration > max {
			max = t.Duration
		}
	}
	var out []totalView
	hatMin, actMin, detMin, _ := core.RoundTotalsDetails(tot) // rows add up to the sum
	for i, t := range tot {
		tv := totalView{Hat: nameOf(nm, t.Hat), Color: pal.color(t.Hat), Duration: core.FormatMinutes(hatMin[i]), NotWork: !cfg.IsWork(t.Hat)}
		if max > 0 {
			tv.Pct = float64(t.Duration) / float64(max) * 100
		}
		if prev != nil {
			tv.Delta = delta(t.Duration - prev[t.Hat])
		}
		if !(len(t.Activities) == 1 && t.Activities[0].Activity == "") {
			for j, a := range t.Activities {
				name := a.Activity
				if name == "" {
					name = i18n.In(lang, "cli.log.noactivity")
				}
				at := actTotal{Name: name, Duration: core.FormatMinutes(actMin[i][j])}
				for k, d := range a.Details {
					parts := core.SplitDetail(d.Detail, links)
					if d.Detail == "" {
						parts = []core.DetailPart{{Text: i18n.In(lang, "web.nodetail")}}
					}
					at.Details = append(at.Details, detailTotal{parts, core.FormatMinutes(detMin[i][j][k])})
				}
				tv.Activities = append(tv.Activities, at)
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

// sumOf adds up the working time: hats with work_time: false are left out.
func sumOf(tot []core.HatTotal, cfg core.Config) time.Duration {
	var s time.Duration
	for _, t := range tot {
		if cfg.IsWork(t.Hat) {
			s += t.Duration
		}
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
	d.Title = dayLabel(lang, day, true)
	d.Prev, d.Next = "/day?date="+dateStr(day.AddDate(0, 0, -1)), "/day?date="+dateStr(next)
	d.TL = s.timeline(day, cfg, iv, lang)
	obs, err := s.st.ObservationsBetween(r.Context(), day, next, "", "")
	if err != nil {
		serverError(w, err)
		return
	}
	d.Obs = buildObs(day, obsClip(day, next, obs), iv, pal, nm, lang, acc == fullAccess)
	links, _ := s.st.TicketLinks(r.Context())
	d.Date = dateStr(day)
	d.IsToday = v.Live
	d.Done = r.URL.Query().Get("done")
	if d.Done != "" {
		v.Msg = i18n.In(lang, "web.day.saved")
	}
	d.Choices = choices(cfg, iv, lang)
	var prevEnd time.Time
	var prevWhat string
	for _, i := range iv {
		if !prevEnd.IsZero() && i.Start.Sub(prevEnd) >= time.Minute {
			// a gap is most often filled with what ran before it
			d.Rows = append(d.Rows, rowView{Gap: true, What: prevWhat, Start: prevEnd.In(loc).Format("15:04"), End: i.Start.In(loc).Format("15:04"),
				StartIn: prevEnd.In(loc).Format("15:04"), EndIn: i.Start.In(loc).Format("15:04"),
				Duration: core.FormatDuration(i.Start.Sub(prevEnd))})
		}
		prevEnd, prevWhat = i.End, i.Hat+"|"+i.Activity
		if fleeting(i) {
			continue // a switch passed through within a minute; it still counts in the totals
		}
		end := i.End.In(loc).Format("15:04")
		if i.Open {
			end = i18n.In(lang, "cli.log.now")
		}
		row := rowView{Detail: core.SplitDetail(i.Detail, links), DetailIn: core.ShortDetail(i.Detail),
			Start: i.Start.In(loc).Format("15:04"), End: end, Hat: nameOf(nm, i.Hat),
			Color: pal.color(i.Hat), Activity: i.Activity, Duration: core.FormatDuration(i.Duration()), Open: i.Open,
			ID: i.StartID, What: i.Hat + "|" + i.Activity, StartIn: i.Start.In(loc).Format("15:04")}
		if !i.Open {
			row.EndIn = i.End.In(loc).Format("15:04")
		}
		d.Rows = append(d.Rows, row)
		label := row.Hat
		if i.Activity != "" {
			label += " · " + i.Activity
		}
		d.Booked = append(d.Booked, bookedRange{S: int(clockOf(day, i.Start.In(loc)) / time.Minute),
			E: int(clockOf(day, i.End.In(loc)) / time.Minute), T: label})
	}
	d.Totals = s.totalViews(tot, nil, pal, nm, lang, links, cfg)
	d.Sum = core.FormatDuration(sumOf(tot, cfg))
	s.render(w, v, http.StatusOK)
}

// clockOf returns t as the time of day on day (24h for the next midnight);
// atClock is its reverse. Both go by the wall clock, so a day with a clock
// change keeps its hours in line with the other days.
func clockOf(day, t time.Time) time.Duration {
	if !t.Before(day.AddDate(0, 0, 1)) {
		return 24 * time.Hour
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

func atClock(day time.Time, c time.Duration) time.Time {
	if c >= 24*time.Hour {
		return day.AddDate(0, 0, 1)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), int(c/time.Hour), int(c%time.Hour/time.Minute), 0, 0, day.Location())
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
	wv.Title = fmt.Sprintf("%s – %s", dayLabel(lang, start, true), dayLabel(lang, end.AddDate(0, 0, -1), true))
	wv.Prev, wv.Next = "/week?date="+dateStr(start.AddDate(0, 0, -7)), "/week?date="+dateStr(end)
	wv.CSV = "/week.csv?date=" + dateStr(start)

	// one band per day on a common time axis: the widest window of the days
	perDay := make([][]core.Interval, 7)
	for _, i := range core.Clip(iv, start, end) {
		for d := 0; d < 7; d++ {
			ds := start.AddDate(0, 0, d)
			for _, c := range core.Clip([]core.Interval{i}, ds, ds.AddDate(0, 0, 1)) {
				perDay[d] = append(perDay[d], c)
			}
		}
	}
	first, last := 24*time.Hour, time.Duration(0) // offsets from midnight
	daySum := make([]time.Duration, 7)
	for d, list := range perDay {
		ds := start.AddDate(0, 0, d)
		for _, i := range list {
			daySum[d] += i.Duration()
		}
		from, to, _, _, _ := dayWindow(ds, cfg.Work(), list)
		first, last = min(first, clockOf(ds, from)), max(last, clockOf(ds, to))
	}
	dayMin := core.Apportion(daySum) // the days add up to the week
	for d := 0; d < 7; d++ {
		ds := start.AddDate(0, 0, d)
		bar := dayBar{Label: dayLabel(lang, ds, false), Date: dateStr(ds), Duration: core.FormatMinutes(dayMin[d])}
		bar.TL = s.layout(ds, atClock(ds, first), atClock(ds, last), cfg, perDay[d], lang)
		wv.Days = append(wv.Days, bar)
	}
	if len(wv.Days) > 0 {
		wv.Ticks = wv.Days[0].TL.Ticks
	}
	links, _ := s.st.TicketLinks(r.Context())
	if len(prevTot) == 0 {
		prev = nil // nothing to compare with: no column of "+everything"
	}
	wv.Compare = prev != nil
	wv.Totals = s.totalViews(tot, prev, pal, nm, lang, links, cfg)
	wv.Sum = core.FormatDuration(sumOf(tot, cfg))
	if wv.Compare {
		wv.SumDelta = delta(sumOf(tot, cfg) - sumOf(prevTot, cfg))
	}
	s.render(w, v, http.StatusOK)
}

// weekCSV exports the intervals of a week: date, hat, activity, start, end,
// minutes, note. Intervals that cross midnight are split per day.
// cell keeps a spreadsheet from reading text as a formula.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

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
	if s.langOf(r) == "de" {
		// German spreadsheets expect ";" and recognise UTF-8 by its BOM
		io.WriteString(w, "\ufeff")
		cw.Comma = ';'
		cw.Write([]string{"Datum", "Hut", "Aktivität", "Beginn", "Ende", "Minuten", "Detail"})
	} else {
		cw.Write([]string{"date", "hat", "activity", "start", "end", "minutes", "note"})
	}
	type csvRow struct {
		cols []string
		d    time.Duration
	}
	var list []csvRow
	for _, i := range iv {
		for d := 0; d < 7; d++ {
			ds := start.AddDate(0, 0, d)
			for _, c := range core.Clip([]core.Interval{i}, ds, ds.AddDate(0, 0, 1)) {
				list = append(list, csvRow{[]string{dateStr(ds), cell(nameOf(nm, c.Hat)), cell(c.Activity),
					c.Start.In(loc).Format("15:04"), c.End.In(loc).Format("15:04"), "", cell(core.ShortDetail(c.Detail))}, c.Duration()})
			}
		}
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].cols[0]+list[a].cols[3] < list[b].cols[0]+list[b].cols[3] })
	// the minutes of the rows add up to the week's total
	ds := make([]time.Duration, len(list))
	for i, r := range list {
		ds[i] = r.d
	}
	var rows [][]string
	for i, m := range core.Apportion(ds) {
		list[i].cols[5] = fmt.Sprint(m)
		rows = append(rows, list[i].cols)
	}
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
