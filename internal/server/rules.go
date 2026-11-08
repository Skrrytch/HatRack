package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The tab "Assignment" of the Detection page: what HatRack would have
// assigned on a day of the user's own, why, and the rules and learned clues
// behind it. Rules are edited here and only here.

const (
	maxTestRows  = 300
	maxLearned   = 8
	testMergeGap = time.Minute
	recentDays   = 7
	surePct      = 0.9
	likelyPct    = 0.6
	editorFields = "ed_scope ed_app ed_title ed_act ed_acts ed_detail ed_ignore ed_mode ed_prio ed_edit ed_id ed_booked ed_notification ed_media ed_new"
)

type rulesView struct {
	Counts   string
	Saved    bool
	Deleted  bool
	NoSensor bool
	EmptyDay bool
	Day      string
	Days     []option
	Device   string
	Devices  []option
	Carry    [][2]string // fields of an open draft, kept when day or device change
	Cover    []coverPart
	Filters  []filterLink
	Rows     []testRow
	More     int
	AddHref  string
	Editor   *editorView

	Global    []ruleLine
	GlobalAdd string
	HatLinks  []hatRuleLink
	Learned   learnedView
	Filter    string
}

type coverPart struct {
	Class, Label string
	Pct          int
	Color        string
}

type filterLink struct {
	Href, Label string
	N           int
	On          bool
}

type testRow struct {
	Class, When, Dur, App, Title string
	Noise, Unknown, NoHat        bool
	HatName, Color, Rest, Conf   string
	Whys                         []whyChip
	WhyNone                      string
	BookedName, BookedColor      string
	Plus                         string // link that opens the editor with this row
}

type whyChip struct {
	Class, Label, Field, Text, Title string
}

type ruleLine struct {
	Conds         []ruleCond
	Then          string
	Hits          string
	Zero          bool
	NoAct         string
	Mode, ModeCls string
	Edit          string // link
	HatName       string
	HatColor      string
}

type ruleCond struct{ Word, Value string }

type hatRuleLink struct {
	Href, Name, Color, Count string
}

type learnedView struct {
	Parts, Details, Projects []learnedItem
	PartsSub, ProjectsLink   string
	Empty                    bool
}

type learnedItem struct {
	Name, Color, Sub, Meter string
}

type editorView struct {
	Scope, App, Title, Act, Acts, Detail, Mode, Prio, Edit, ID, Booked, Notif, Media string
	Ignore, Defaults, IsEdit                                                         bool
	Hats                                                                             []option
	Activities                                                                       []option
	Modes                                                                            []option
	ErrApp, ErrTitle, ErrAct, ErrDetail, ErrPrio, ErrTop                             string
	Hit                                                                              string
}

// draft is the rule being tried, as the editor fields describe it.
type draft struct {
	Scope, App, Title, Act, Acts, Detail, Mode, Prio, Edit, ID, Booked, Notif, Media string
	Ignore, Present, New                                                             bool
}

func draftOf(q url.Values) draft {
	d := draft{Scope: q.Get("ed_scope"), App: strings.TrimSpace(q.Get("ed_app")), Title: strings.TrimSpace(q.Get("ed_title")),
		Act: q.Get("ed_act"), Acts: q.Get("ed_acts"), Detail: strings.TrimSpace(q.Get("ed_detail")), Mode: q.Get("ed_mode"),
		Prio: strings.TrimSpace(q.Get("ed_prio")), Edit: q.Get("ed_edit"), ID: q.Get("ed_id"), Booked: q.Get("ed_booked"),
		Notif: q.Get("ed_notification"), Media: q.Get("ed_media"), Ignore: q.Get("ed_ignore") != "", New: q.Get("ed_new") != ""}
	d.Present = q.Has("ed_scope") || q.Has("ed_edit") || d.New
	return d
}

// fromRule fills a draft with an existing rule, for "Edit".
func (d *draft) fromRule(scope string, r core.Rule, edit string) {
	d.Scope, d.Edit, d.ID = scope, edit, r.ID
	d.App, d.Title = r.Match[core.MatchApp], r.Match[core.MatchTitle]
	d.Notif, d.Media = r.Match[core.MatchNotification], r.Match[core.MatchMedia]
	if scope == core.RulesScopeDefaults {
		d.Acts = strings.Join(r.Activity, ", ")
	} else if len(r.Activity) > 0 {
		d.Act = r.Activity[0]
	}
	d.Detail, d.Ignore, d.Mode = r.Detail, r.Ignore, r.Mode
	if r.Priority != 0 {
		d.Prio = strconv.Itoa(r.Priority)
	}
}

// rule builds the rule the draft describes. An unreadable priority is an
// error for the field.
func (d draft) rule() (core.Rule, error) {
	r := core.Rule{ID: d.ID, Match: map[string]string{}, Detail: d.Detail, Mode: d.Mode}
	for k, v := range map[string]string{core.MatchApp: d.App, core.MatchTitle: d.Title, core.MatchNotification: d.Notif, core.MatchMedia: d.Media} {
		if v != "" {
			r.Match[k] = v
		}
	}
	if d.Scope == core.RulesScopeDefaults {
		for _, a := range strings.Split(d.Acts, ",") {
			if a = strings.TrimSpace(a); a != "" {
				r.Activity = append(r.Activity, a)
			}
		}
		r.Ignore = d.Ignore
	} else if d.Act != "" {
		r.Activity = core.Names{d.Act}
	}
	if r.Ignore {
		r.Activity, r.Detail, r.Mode = nil, "", ""
	}
	if d.Prio != "" {
		n, err := strconv.Atoi(d.Prio)
		if err != nil {
			return r, fmt.Errorf("config: rule.priority %q: want a number", d.Prio)
		}
		r.Priority = n
	}
	return r, nil
}

func (d draft) hasMatch() bool { return d.App != "" || d.Title != "" || d.Notif != "" || d.Media != "" }

// rulesOf returns the rules list of a scope inside cfg, which the caller
// owns (see cloneRules).
func rulesOf(cfg *core.Config, scope string) (*[]core.Rule, bool) {
	if scope == core.RulesScopeDefaults {
		return &cfg.Defaults.Rules, true
	}
	for i := range cfg.Hats {
		if cfg.Hats[i].Key == scope {
			return &cfg.Hats[i].Rules, true
		}
	}
	return nil, false
}

// cloneRules makes the slices that applyRule changes private to cfg.
func cloneRules(cfg *core.Config) {
	cfg.Hats = slices.Clone(cfg.Hats)
	cfg.Defaults.Rules = slices.Clone(cfg.Defaults.Rules)
	for i := range cfg.Hats {
		cfg.Hats[i].Rules = slices.Clone(cfg.Hats[i].Rules)
	}
}

// splitEdit reads "scope:N".
func splitEdit(edit string) (string, int, bool) {
	i := strings.LastIndex(edit, ":")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(edit[i+1:])
	return edit[:i], n, err == nil && n >= 0
}

// applyRule puts rule r into scope of cfg: replacing the rule named by edit
// ("scope:N") in place, moving it if the scope changed, or appending it.
func applyRule(cfg *core.Config, scope, edit string, r core.Rule) error {
	list, ok := rulesOf(cfg, scope)
	if !ok {
		return fmt.Errorf("unknown hat %q", scope)
	}
	if edit != "" {
		oldScope, n, ok := splitEdit(edit)
		old, found := rulesOf(cfg, oldScope)
		if !ok || !found || n >= len(*old) {
			return fmt.Errorf("unknown rule %q", edit)
		}
		if oldScope == scope {
			(*list)[n] = r
			return nil
		}
		*old = slices.Delete(*old, n, n+1)
	}
	*list = append(*list, r)
	return nil
}

// removeRule deletes the rule named by edit.
func removeRule(cfg *core.Config, edit string) error {
	scope, n, ok := splitEdit(edit)
	list, found := rulesOf(cfg, scope)
	if !ok || !found || n >= len(*list) {
		return fmt.Errorf("unknown rule %q", edit)
	}
	*list = slices.Delete(*list, n, n+1)
	return nil
}

// fieldError splits a configuration error into the field it names and the
// words to show.
func fieldError(err error) (field, text string) {
	s := strings.TrimPrefix(err.Error(), "config: ")
	i := strings.IndexAny(s, " :")
	if i < 0 {
		return "", s
	}
	path, text := s[:i], strings.TrimLeft(s[i:], ": ")
	switch {
	case strings.HasSuffix(path, ".match.app"):
		field = "app"
	case strings.HasSuffix(path, ".match.title"):
		field = "title"
	case strings.HasSuffix(path, ".activity"), strings.Contains(path, ".activity["):
		field = "act"
	case strings.HasSuffix(path, ".detail"):
		field = "detail"
	case strings.HasSuffix(path, ".priority"):
		field = "prio"
	}
	return field, text
}

// ---- the tab ------------------------------------------------------------------

// rulesTab builds the tab. q holds the page's parameters (the URL query, or
// the form of a failed save); saveErr is that failure.
func (s *Server) rulesTab(r *http.Request, v *view, cfg core.Config, q url.Values, saveErr error) error {
	ctx := r.Context()
	lang, loc := v.Lang, s.locOf(r)
	rv := &v.Detection.Rules
	nm, pal := names(cfg), newPalette(cfg)
	k, stats, err := s.knowledge(r)
	if err != nil {
		return err
	}
	tk := s.tickets(ctx, cfg)
	cls := core.NewClassifier(cfg, k, tk)
	now := s.Now()
	d := draftOf(q)
	if d.Edit != "" && !q.Has("ed_scope") {
		if scope, n, ok := splitEdit(d.Edit); ok {
			if list, found := rulesOf(&cfg, scope); found && n < len(*list) {
				d.fromRule(scope, (*list)[n], d.Edit)
			}
		}
	}
	if d.Present && d.Scope == "" {
		d.Scope = core.RulesScopeDefaults
		if a := cfg.ActiveHats(); len(a) > 0 && !q.Has("ed_scope") {
			d.Scope = a[0].Key
		}
	}

	nRules := len(cfg.Defaults.Rules)
	for _, h := range cfg.Hats {
		nRules += len(h.Rules)
	}
	rv.Counts = i18n.In(lang, "web.rules.counts", strconv.Itoa(nRules), strconv.Itoa(len(tk.Projects())),
		strconv.Itoa(len(k.Details)+len(k.Parts)+len(k.Projects)))
	rv.Saved, rv.Deleted = q.Get("saved") != "", q.Get("deleted") != ""
	rv.Filter = q.Get("filter")
	if !slices.Contains([]string{"unknown", "diff", "noise"}, rv.Filter) {
		rv.Filter = ""
	}

	// the sensor
	devs, err := s.st.Devices(ctx)
	if err != nil {
		return err
	}
	rv.NoSensor = true
	for _, dv := range devs {
		if cfg.Observe.Enabled(dv.Name, "window") {
			rv.NoSensor = false
		}
	}

	// the day
	since := now.In(loc).AddDate(0, 0, -recentDays)
	first := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, loc)
	recent, err := s.st.ObservationsBetween(ctx, first, now, "", "window")
	if err != nil {
		return err
	}
	have := map[string]bool{}
	devSeen := map[string]bool{}
	for _, o := range recent {
		for t := o.Start.In(loc); t.Before(o.End.In(loc)); t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc) {
			have[dateStr(t)] = true
		}
		devSeen[o.Device] = true
	}
	var days []string
	for ds := range have {
		days = append(days, ds)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	todayStr := dateStr(now.In(loc))
	pick := todayStr
	if len(days) > 0 {
		pick = days[0]
	}
	if c := q.Get("day"); c != "" {
		pick = dateStr(parseDate(c, loc, now))
	}
	if !slices.Contains(days, pick) {
		days = append(days, pick)
		sort.Sort(sort.Reverse(sort.StringSlice(days)))
	}
	rv.Day = pick
	for _, ds := range days {
		rv.Days = append(rv.Days, option{Value: ds, Label: dayLabel(lang, parseDate(ds, loc, now), false), Selected: ds == pick})
	}
	rv.Device = q.Get("device")
	rv.Devices = append(rv.Devices, option{Value: "", Label: i18n.In(lang, "web.rules.all_devices"), Selected: rv.Device == ""})
	var devNames []string
	for n := range devSeen {
		devNames = append(devNames, n)
	}
	sort.Strings(devNames)
	for _, n := range devNames {
		rv.Devices = append(rv.Devices, option{Value: n, Label: n, Selected: n == rv.Device})
	}

	base := "/setup/detection?tab=rules&day=" + url.QueryEscape(pick) + "&device=" + url.QueryEscape(rv.Device)
	rv.AddHref = base + "&filter=" + rv.Filter + "&ed_new=1#try"

	// the draft
	var try *core.Classifier
	hitID := q.Get("saved")
	var tried core.Rule
	if d.Present {
		rv.Editor = s.editorOf(lang, cfg, nm, d)
		if d.Edit != "" {
			rv.Editor.IsEdit = true
		}
		for _, f := range strings.Fields(editorFields) {
			if q.Has(f) {
				rv.Carry = append(rv.Carry, [2]string{f, q.Get(f)})
			}
		}
		rule, rerr := d.rule()
		err := rerr
		if err == nil && saveErr == nil && d.hasMatch() {
			c2 := cfg
			cloneRules(&c2)
			if err = applyRule(&c2, d.Scope, d.Edit, rule); err == nil {
				err = c2.Validate()
			}
			if err == nil {
				try, tried = core.NewClassifier(c2, k, tk), rule
				hitID = core.RuleID(d.Scope, rule)
			}
		}
		if saveErr != nil {
			err = saveErr
		}
		if err != nil {
			field, text := fieldError(err)
			e := rv.Editor
			switch field {
			case "app":
				e.ErrApp = text
			case "title":
				e.ErrTitle = text
			case "act":
				e.ErrAct = text
			case "detail":
				e.ErrDetail = text
			case "prio":
				e.ErrPrio = text
			default:
				e.ErrTop = text
			}
		}
	}

	// the rows of the day
	if !rv.NoSensor {
		day := parseDate(pick, loc, now)
		next := day.AddDate(0, 0, 1)
		obs, err := s.st.ObservationsBetween(ctx, day, next, rv.Device, "")
		if err != nil {
			return err
		}
		iv, _, err := s.intervalsIn(ctx, day, next)
		if err != nil {
			return err
		}
		var window, idle []core.Observation
		for _, o := range obsClip(day, next, obs) {
			if o.Sensor == "window" {
				window = append(window, o)
			} else {
				idle = append(idle, o)
			}
		}
		spans := core.ActiveSpans(window, idle)
		if len(spans) == 0 {
			rv.EmptyDay = true
		}
		active := cls
		if try != nil {
			active = try
		}
		s.fillRows(rv, lang, loc, cfg, nm, pal, tk, k, spans, iv, cls, active, hitID, base, tried, d, try != nil)
	}

	// rules for all hats
	rv.GlobalAdd = base + "&ed_new=1&ed_scope=" + core.RulesScopeDefaults + "#try"
	for n, rule := range cfg.Defaults.Rules {
		rv.Global = append(rv.Global, s.ruleLineOf(lang, cfg, nm, pal, stats, core.RulesScopeDefaults, n, rule, base))
	}
	for _, h := range cfg.ActiveHats() {
		c := i18n.In(lang, "web.rules.no_rules")
		switch n := len(h.Rules); n {
		case 0:
		case 1:
			c = i18n.In(lang, "web.rules.one_rule")
		default:
			c = i18n.In(lang, "web.rules.n_rules", strconv.Itoa(n))
		}
		rv.HatLinks = append(rv.HatLinks, hatRuleLink{Href: "/setup/hat?key=" + url.QueryEscape(h.Key), Name: h.DisplayName(), Color: pal.color(h.Key), Count: c})
	}
	s.fillLearned(rv, lang, cfg, nm, pal, k)
	return nil
}

func (s *Server) editorOf(lang string, cfg core.Config, nm map[string]string, d draft) *editorView {
	e := &editorView{Scope: d.Scope, App: d.App, Title: d.Title, Act: d.Act, Acts: d.Acts, Detail: d.Detail, Mode: d.Mode, Prio: d.Prio,
		Edit: d.Edit, ID: d.ID, Booked: d.Booked, Notif: d.Notif, Media: d.Media, Ignore: d.Ignore, Defaults: d.Scope == core.RulesScopeDefaults}
	for _, h := range cfg.ActiveHats() {
		label := h.DisplayName()
		if h.Key == d.Booked {
			label += " " + i18n.In(lang, "web.rules.e_booked")
		}
		e.Hats = append(e.Hats, option{Value: h.Key, Label: label, Selected: h.Key == d.Scope})
	}
	e.Hats = append(e.Hats, option{Value: core.RulesScopeDefaults, Label: i18n.In(lang, "web.rules.e_all_hats"), Selected: e.Defaults})
	if h, ok := cfg.Hat(d.Scope); ok && !e.Defaults {
		e.Activities = append(e.Activities, option{Value: "", Label: i18n.In(lang, "web.rules.f_none"), Selected: d.Act == ""})
		for _, a := range h.Activities {
			e.Activities = append(e.Activities, option{Value: a, Label: a, Selected: strings.EqualFold(a, d.Act)})
		}
	}
	for _, m := range []struct{ v, key string }{{"", "web.rules.mode_suggest"}, {core.ModeAuto, "web.rules.mode_auto_long"}, {core.ModeOff, "web.rules.mode_off"}} {
		e.Modes = append(e.Modes, option{Value: m.v, Label: i18n.In(lang, m.key), Selected: m.v == d.Mode || (m.v == "" && d.Mode == core.ModeSuggest)})
	}
	return e
}

// bookedHat is the hat booked at t, or "".
func bookedHat(iv []core.Interval, t time.Time) string {
	for _, i := range iv {
		if fleeting(i) {
			continue
		}
		if !i.Start.After(t) && (i.Open || i.End.After(t)) {
			return i.Hat
		}
	}
	return ""
}

type rawSpan struct {
	o      core.Observation
	cand   core.Candidate
	base   core.Candidate
	booked string
}

var ticketLike = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-[0-9]+\b`)
var fileTail = regexp.MustCompile(`\.[A-Za-z0-9]{1,5}$`)

func (s *Server) fillRows(rv *rulesView, lang string, loc *time.Location, cfg core.Config, nm map[string]string, pal palette, tk core.Tickets, k core.Knowledge,
	spans []core.Observation, iv []core.Interval, base, cls *core.Classifier, hitID, link string, tried core.Rule, d draft, trying bool) {
	var raw []rawSpan
	for _, o := range spans {
		mid := o.Start.Add(o.End.Sub(o.Start) / 2)
		sp := rawSpan{o: o, cand: cls.Classify(o.Attrs), booked: bookedHat(iv, mid)}
		sp.base = sp.cand
		if trying {
			sp.base = base.Classify(o.Attrs)
		}
		raw = append(raw, sp)
	}
	// the cover
	var dur [4]time.Duration // hat, part, unknown, noise
	for _, sp := range raw {
		n := o2(sp.o)
		switch {
		case sp.cand.Ignore:
			dur[3] += n
		case sp.cand.Hat != "":
			dur[0] += n
		case sp.cand.IsZero():
			dur[2] += n
		default:
			dur[1] += n
		}
	}
	if dur[0]+dur[1]+dur[2]+dur[3] > 0 {
		pct := core.ApportionTo(dur[:], 100)
		for i, c := range []struct{ cls, key, color string }{
			{"hat", "web.rules.cov_hat", "#5b8db8"}, {"part", "web.rules.cov_part", "#a9c4dc"},
			{"unk", "web.rules.cov_unknown", "#d9d6cf"}, {"noise", "web.rules.cov_noise", "#c3c6ca"}} {
			rv.Cover = append(rv.Cover, coverPart{Class: c.cls, Label: i18n.In(lang, c.key), Pct: pct[i], Color: c.color})
		}
	}

	// merge neighbours that read the same
	type group struct {
		first, last rawSpan
		start, end  time.Time
		noHat       bool // before the draft, no span of it had a hat
	}
	var groups []*group
	for _, sp := range raw {
		if n := len(groups); n > 0 {
			g := groups[n-1]
			if g.last.o.Device == sp.o.Device && g.last.o.Attrs["app"] == sp.o.Attrs["app"] &&
				core.NormalizeTitle(g.last.o.Attrs["title"]) == core.NormalizeTitle(sp.o.Attrs["title"]) &&
				reflect.DeepEqual(g.last.cand, sp.cand) && g.last.booked == sp.booked && sp.o.Start.Sub(g.end) < testMergeGap {
				g.last, g.end = sp, sp.o.End
				g.noHat = g.noHat || sp.base.Hat == ""
				continue
			}
		}
		groups = append(groups, &group{first: sp, last: sp, start: sp.o.Start, end: sp.o.End, noHat: sp.base.Hat == ""})
	}

	var rows []testRow
	counts := map[string]int{}
	var hitN, hitNew int
	var hitDur time.Duration
	for _, g := range groups {
		sp := g.first
		c := sp.cand
		row := testRow{When: g.start.In(loc).Format("15:04") + "–" + g.end.In(loc).Format("15:04"), Dur: core.FormatDuration(g.end.Sub(g.start)),
			App: sp.o.Attrs["app"], Title: core.NormalizeTitle(sp.o.Attrs["title"])}
		var classes []string
		switch {
		case c.Ignore:
			classes = append(classes, "noise")
			row.Noise = true
		case c.Hat == "" && c.IsZero():
			classes = append(classes, "unk")
			row.Unknown = true
		case c.Hat == "":
			classes = append(classes, "unk")
			row.NoHat = true
		}
		if c.Hat != "" && sp.booked != "" && sp.booked != c.Hat {
			classes = append(classes, "diff")
		}
		hit := false
		if hitID != "" {
			for _, w := range c.Why {
				if w.Source == core.SourceRule && w.Ref == hitID {
					hit = true
				}
			}
		}
		if hit {
			classes = append(classes, "hit")
			if trying {
				hitN++
				hitDur += g.end.Sub(g.start)
				if g.noHat && c.Hat != "" {
					hitNew++
				}
			}
		}
		row.Class = strings.Join(classes, " ")
		if c.Hat != "" {
			row.HatName, row.Color = nameOf(nm, c.Hat), pal.color(c.Hat)
			switch {
			case c.Conf >= surePct:
				row.Conf = i18n.In(lang, "web.rules.conf_sure")
			case c.Conf >= likelyPct:
				row.Conf = i18n.In(lang, "web.rules.conf_likely")
			default:
				row.Conf = i18n.In(lang, "web.rules.conf_maybe")
			}
		}
		var rest []string
		act := c.Activity
		if act == "" && len(c.Activities) > 0 {
			act = strings.Join(c.Activities, ", ")
			for _, a := range c.Activities {
				if hatsHave(cfg, a) {
					act = a
					break
				}
			}
		}
		if act != "" {
			rest = append(rest, act)
		}
		if c.Detail != "" {
			rest = append(rest, c.Detail)
		}
		row.Rest = strings.Join(rest, " · ")
		if sp.booked != "" {
			row.BookedName, row.BookedColor = nameOf(nm, sp.booked), pal.color(sp.booked)
		}
		row.Whys = s.whys(lang, cfg, nm, tk, k, tried, d, c.Why)
		if len(c.Why) == 0 {
			row.WhyNone = i18n.In(lang, "web.rules.why_none")
			if m := ticketLike.FindString(sp.o.Attrs["title"]); m != "" {
				row.WhyNone = i18n.In(lang, "web.rules.why_unknown_ticket", m)
			}
		}
		if !c.Ignore && (strings.Contains(row.Class, "unk") || strings.Contains(row.Class, "diff") || (c.Hat != "" && c.Conf < surePct)) {
			scope := sp.booked
			if scope == "" {
				scope = c.Hat
			}
			if scope == "" {
				scope = core.RulesScopeDefaults
			}
			f := url.Values{"ed_new": {"1"}, "ed_scope": {scope}, "ed_booked": {sp.booked}}
			if a := sp.o.Attrs["app"]; a != "" {
				f.Set("ed_app", regexp.QuoteMeta(a))
			}
			for _, p := range core.TitleParts(sp.o.Attrs["title"], sp.o.Attrs["app"]) {
				if !fileTail.MatchString(p) {
					f.Set("ed_title", regexp.QuoteMeta(p))
					break
				}
			}
			row.Plus = link + "&filter=" + rv.Filter + "&" + f.Encode() + "#try"
		}
		counts["all"]++
		for _, cl := range classes {
			if cl != "hit" {
				counts[map[string]string{"unk": "unknown", "diff": "diff", "noise": "noise"}[cl]]++
			}
		}
		if rv.Filter != "" && !strings.Contains(row.Class, map[string]string{"unknown": "unk", "diff": "diff", "noise": "noise"}[rv.Filter]) {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) > maxTestRows {
		rv.More = len(rows) - maxTestRows
		rows = rows[:maxTestRows]
	}
	rv.Rows = rows
	for _, f := range []struct{ id, key string }{{"", "web.rules.flt_all"}, {"unknown", "web.rules.flt_unknown"}, {"diff", "web.rules.flt_diff"}, {"noise", "web.rules.flt_noise"}} {
		n := counts["all"]
		if f.id != "" {
			n = counts[f.id]
		}
		href := link + "&filter=" + f.id
		for _, kv := range rv.Carry {
			href += "&" + kv[0] + "=" + url.QueryEscape(kv[1])
		}
		rv.Filters = append(rv.Filters, filterLink{Href: href + "#rules-day", Label: i18n.In(lang, f.key), N: n, On: f.id == rv.Filter})
	}
	if rv.Editor != nil && trying {
		switch {
		case hitN == 0:
			rv.Editor.Hit = i18n.In(lang, "web.rules.hits_none")
		default:
			rv.Editor.Hit = i18n.In(lang, "web.rules.hits", strconv.Itoa(hitN), core.FormatDuration(hitDur))
			if hitNew > 0 {
				rv.Editor.Hit += " " + i18n.In(lang, "web.rules.hits_new", strconv.Itoa(hitNew))
			}
		}
	}
}

func o2(o core.Observation) time.Duration { return o.End.Sub(o.Start) }

func hatsHave(cfg core.Config, activity string) bool {
	for _, h := range cfg.ActiveHats() {
		for _, a := range h.Activities {
			if strings.EqualFold(a, activity) {
				return true
			}
		}
	}
	return false
}

// whys turns the reasons of a candidate into chips.
func (s *Server) whys(lang string, cfg core.Config, nm map[string]string, tk core.Tickets, k core.Knowledge, tried core.Rule, d draft, why []core.Reason) []whyChip {
	var out []whyChip
	field := func(f string) string {
		switch f {
		case core.FieldHat:
			return i18n.In(lang, "web.rules.f_hat")
		case core.FieldActivity:
			return i18n.In(lang, "web.rules.f_activity")
		case core.FieldDetail:
			return i18n.In(lang, "web.rules.f_detail")
		}
		return ""
	}
	for _, w := range why {
		ch := whyChip{Field: field(w.Field)}
		switch w.Source {
		case core.SourceRule:
			ch.Class, ch.Label = "rule", i18n.In(lang, "web.rules.src_rule")
			if r, ok := findRule(cfg, w.Ref); ok {
				ch.Text, ch.Title = ruleShort(lang, r), ruleSentence(lang, nm, r)
			} else if core.RuleID(d.Scope, tried) == w.Ref {
				ch.Text, ch.Title = ruleShort(lang, tried), ruleSentence(lang, nm, tried)
			} else {
				ch.Text = w.Ref
			}
		case core.SourceTicket:
			ch.Class, ch.Label = "ticket", i18n.In(lang, "web.rules.src_ticket")
			ch.Text = w.Ref
			if w.Field == core.FieldHat {
				p := w.Ref
				if refs := tk.Find(w.Ref); len(refs) > 0 {
					p = refs[0].Project
				}
				ch.Text = i18n.In(lang, "web.rules.why_project", p, nameOf(nm, w.Hat))
			}
		case core.SourceDetail:
			ch.Class, ch.Label = "learned", i18n.In(lang, "web.rules.src_learned")
			ch.Text = w.Ref
			ch.Title = i18n.In(lang, "web.rules.booked_as", nameOf(nm, w.Hat), core.FormatMinutes(w.Minutes))
		case core.SourcePart:
			ch.Class, ch.Label = "learned", i18n.In(lang, "web.rules.src_learned")
			ch.Text = i18n.In(lang, "web.rules.why_part", strconv.Itoa(int(w.Share*100+.5)), nameOf(nm, w.Hat))
			ch.Title = w.Ref
		default:
			continue
		}
		out = append(out, ch)
	}
	return out
}

// findRule looks a rule up by its id.
func findRule(cfg core.Config, id string) (core.Rule, bool) {
	for _, h := range cfg.Hats {
		for _, r := range h.Rules {
			if core.RuleID(h.Key, r) == id {
				return r, true
			}
		}
	}
	for _, r := range cfg.Defaults.Rules {
		if core.RuleID(core.RulesScopeDefaults, r) == id {
			return r, true
		}
	}
	return core.Rule{}, false
}

var condKeys = []struct{ field, word, short string }{
	{core.MatchApp, "web.rules.sent_app", "web.rules.short_app"},
	{core.MatchTitle, "web.rules.sent_title", "web.rules.short_title"},
	{core.MatchNotification, "web.rules.sent_notif", "web.rules.short_notif"},
	{core.MatchMedia, "", "web.rules.short_media"},
}

// ruleShort is the short text of a rule in a chip: its own id, or its first
// condition.
func ruleShort(lang string, r core.Rule) string {
	if r.ID != "" {
		return r.ID
	}
	var parts []string
	for _, c := range condKeys {
		if v, ok := r.Match[c.field]; ok {
			if c.field == core.MatchMedia {
				v = mediaWord(lang, v)
				parts = append(parts, i18n.In(lang, c.short))
				continue
			}
			parts = append(parts, i18n.In(lang, c.short)+" "+v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 1 {
		return parts[0] + " …"
	}
	return parts[0]
}

func mediaWord(lang, v string) string {
	if v == "cam" {
		return i18n.In(lang, "web.rules.sent_cam")
	}
	return i18n.In(lang, "web.rules.sent_mic")
}

func conds(lang string, r core.Rule) []ruleCond {
	var out []ruleCond
	for _, c := range condKeys {
		v, ok := r.Match[c.field]
		if !ok {
			continue
		}
		if c.field == core.MatchMedia {
			out = append(out, ruleCond{Word: mediaWord(lang, v)})
			continue
		}
		out = append(out, ruleCond{Word: i18n.In(lang, c.word), Value: v})
	}
	return out
}

// thenOf is the result of a rule in words.
func thenOf(lang string, nm map[string]string, hat string, r core.Rule) string {
	if r.Ignore {
		return i18n.In(lang, "web.rules.then_noise")
	}
	var parts []string
	if hat != "" {
		parts = append(parts, nameOf(nm, hat))
	}
	switch n := len(r.Activity); {
	case n == 1 && hat != "":
		parts = append(parts, r.Activity[0])
	case n == 1:
		parts = append(parts, i18n.In(lang, "web.rules.then_act", r.Activity[0]))
	case n > 1:
		parts = append(parts, i18n.In(lang, "web.rules.then_acts", r.Activity[0], strings.Join(r.Activity[1:], ", ")))
	}
	if r.Detail != "" {
		if hat != "" {
			parts = append(parts, r.Detail)
		} else {
			parts = append(parts, i18n.In(lang, "web.rules.then_detail", r.Detail))
		}
	}
	if hat != "" {
		return strings.Join(parts, " · ")
	}
	return strings.Join(parts, ", ")
}

// ruleSentence is the whole rule as one sentence, for a tooltip.
func ruleSentence(lang string, nm map[string]string, r core.Rule) string {
	var b strings.Builder
	b.WriteString(i18n.In(lang, "web.rules.sent_if"))
	for i, c := range conds(lang, r) {
		if i > 0 {
			b.WriteString(" " + i18n.In(lang, "web.rules.sent_and"))
		}
		b.WriteString(" " + strings.TrimSpace(c.Word+" "+c.Value))
	}
	return b.String() + " → " + thenOf(lang, nm, "", r)
}

func (s *Server) ruleLineOf(lang string, cfg core.Config, nm map[string]string, pal palette, stats ruleStats, scope string, n int, r core.Rule, base string) ruleLine {
	l := ruleLine{Conds: conds(lang, r), Edit: base + "&ed_edit=" + url.QueryEscape(scope+":"+strconv.Itoa(n)) + "#try"}
	hat := ""
	if scope != core.RulesScopeDefaults {
		hat = scope
		l.HatName, l.HatColor = nameOf(nm, scope), pal.color(scope)
	}
	l.Then = thenOf(lang, nm, hat, r)
	if d := stats[core.RuleID(scope, r)]; d >= time.Minute {
		l.Hits = i18n.In(lang, "web.rules.hits7", core.FormatDuration(d))
	} else {
		l.Hits, l.Zero = i18n.In(lang, "web.rules.hits7_none"), true
	}
	if !r.Ignore {
		switch r.Mode {
		case core.ModeAuto:
			l.Mode, l.ModeCls = i18n.In(lang, "web.rules.mode_auto"), "auto"
		case core.ModeOff:
			l.Mode = i18n.In(lang, "web.rules.mode_off")
		default:
			l.Mode = i18n.In(lang, "web.rules.mode_suggest")
		}
	}
	if scope == core.RulesScopeDefaults && len(r.Activity) > 0 {
		var miss []string
		for _, h := range cfg.ActiveHats() {
			ok := false
			for _, a := range r.Activity {
				if slices.ContainsFunc(h.Activities, func(x string) bool { return strings.EqualFold(x, a) }) {
					ok = true
				}
			}
			if !ok {
				miss = append(miss, h.DisplayName())
			}
		}
		if len(miss) > 0 {
			l.NoAct = i18n.In(lang, "web.rules.no_act_hats", strings.Join(miss, ", "))
		}
	}
	return l
}

func (s *Server) fillLearned(rv *rulesView, lang string, cfg core.Config, nm map[string]string, pal palette, k core.Knowledge) {
	l := &rv.Learned
	active := func(hat string) bool { h, ok := cfg.Hat(hat); return ok && !h.Archived }
	days := int(cfg.Observe.Retention() / (24 * time.Hour))
	l.PartsSub = i18n.In(lang, "web.rules.last_days", strconv.Itoa(days))
	for _, p := range k.Parts {
		if active(p.Hat) && len(l.Parts) < maxLearned {
			l.Parts = append(l.Parts, learnedItem{Name: p.Part, Color: pal.color(p.Hat), Meter: core.FormatMinutes(p.Minutes)})
		}
	}
	for _, d := range k.Details {
		if active(d.Hat) && len(l.Details) < maxLearned {
			l.Details = append(l.Details, learnedItem{Name: d.Detail, Color: pal.color(d.Hat), Sub: d.Activity, Meter: core.FormatMinutes(d.Minutes)})
		}
	}
	var ps []string
	for p, u := range k.Projects {
		if active(u.Hat) {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(a, b int) bool {
		if ca, cb := k.Projects[ps[a]].Count, k.Projects[ps[b]].Count; ca != cb {
			return ca > cb
		}
		return ps[a] < ps[b]
	})
	for _, p := range ps[:min(len(ps), maxLearned)] {
		u := k.Projects[p]
		l.Projects = append(l.Projects, learnedItem{Name: p, Color: pal.color(u.Hat),
			Meter: fmt.Sprintf("%d %% · %d×", int(u.Share*100+.5), u.Count)})
	}
	l.Empty = len(l.Parts)+len(l.Details)+len(l.Projects) == 0
}

// ---- saving ---------------------------------------------------------------------

// ruleAction saves or deletes a rule from the editor.
func (s *Server) ruleAction(w http.ResponseWriter, r *http.Request, acc access) {
	op := r.PostFormValue("op")
	q := r.PostForm
	d := draftOf(q)
	day, device := r.PostFormValue("day"), r.PostFormValue("device")
	dest := func(extra string) string {
		return "/setup/detection?tab=rules&day=" + url.QueryEscape(day) + "&device=" + url.QueryEscape(device) +
			"&filter=" + url.QueryEscape(r.PostFormValue("filter")) + extra + "#rules-day"
	}
	var id string
	var saveErr error
	cfg, err := s.editConfig(r.Context(), false, func(c *core.Config) error {
		cloneRules(c)
		if op == "rule.delete" {
			return removeRule(c, d.Edit)
		}
		rule, err := d.rule()
		if err != nil {
			return badConfig{err}
		}
		if err := applyRule(c, d.Scope, d.Edit, rule); err != nil {
			return badConfig{err}
		}
		id = core.RuleID(d.Scope, rule)
		return nil
	})
	if err != nil {
		var bad badConfig
		if !errors.As(err, &bad) {
			serverError(w, err)
			return
		}
		saveErr = bad.error
		s.renderRulesFailure(w, r, acc, cfg, q, saveErr)
		return
	}
	s.know.drop()
	if op == "rule.delete" {
		http.Redirect(w, r, dest("&deleted=1"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, dest("&saved="+url.QueryEscape(id)), http.StatusSeeOther)
}

// renderRulesFailure shows the tab again with the editor, the input and the
// error at the field.
func (s *Server) renderRulesFailure(w http.ResponseWriter, r *http.Request, acc access, cfg core.Config, q url.Values, saveErr error) {
	v := s.base(r, "detection", acc)
	d := &v.Detection
	d.Tab = "rules"
	for _, t := range detTabs {
		d.Tabs = append(d.Tabs, detTab{t.ID, i18n.In(v.Lang, t.Key), t.ID == d.Tab})
	}
	cur, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	_ = cfg
	if err := s.rulesTab(r, &v, cur, q, saveErr); err != nil {
		serverError(w, err)
		return
	}
	s.render(w, v, http.StatusBadRequest)
}

// hatRules is the section "Detection" of the hat page: the rules of the hat
// to read (they are edited in the tab Assignment), and what else finds it.
type hatRules struct {
	Name     string
	Lines    []ruleLine
	AddHref  string
	Projects []string
	Learned  string // "3 title parts, 2 details", empty if nothing
}

func (s *Server) fillHatRules(r *http.Request, v *view, cfg core.Config, h core.Hat) {
	hr := &v.Hat.Rules
	hr.Name = h.DisplayName()
	hr.AddHref = "/setup/detection?tab=rules&ed_new=1&ed_scope=" + url.QueryEscape(h.Key) + "#try"
	k, stats, err := s.knowledge(r)
	if err != nil {
		return
	}
	nm, pal := names(cfg), newPalette(cfg)
	for n, rule := range h.Rules {
		l := s.ruleLineOf(v.Lang, cfg, nm, pal, stats, h.Key, n, rule, "/setup/detection?tab=rules")
		l.HatName = ""
		hr.Lines = append(hr.Lines, l)
	}
	for p, u := range k.Projects {
		if u.Hat == h.Key {
			hr.Projects = append(hr.Projects, p)
		}
	}
	sort.Strings(hr.Projects)
	var parts, details int
	for _, p := range k.Parts {
		if p.Hat == h.Key {
			parts++
		}
	}
	for _, d := range k.Details {
		if d.Hat == h.Key {
			details++
		}
	}
	if parts+details > 0 {
		hr.Learned = i18n.In(v.Lang, "web.hat.also_learned", strconv.Itoa(parts), strconv.Itoa(details))
	}
}
