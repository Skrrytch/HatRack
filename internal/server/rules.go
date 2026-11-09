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
	"unicode"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The tab "Assignment" of the Detection page: what HatRack would have
// assigned on a day of the user's own, why, and the rules and learned clues
// behind it. Rules are edited here and only here.

// maxTestRows is the most rows the day list shows at once; a test may lower it.
var maxTestRows = 300

const (
	maxLearned   = 8
	testMergeGap = time.Minute
	recentDays   = 7
	editorFields = "ed_scope ed_app ed_title ed_act ed_acts ed_detail ed_ignore ed_mode ed_prio ed_edit ed_id ed_booked ed_notification ed_media ed_new"
)

type rulesView struct {
	Counts                      string
	Saved                       bool
	Deleted                     bool
	NoSensor                    bool
	EmptyDay                    bool
	Day                         string
	Days                        []option
	Device                      string
	Devices                     []option
	Carry                       [][2]string // fields of an open draft, kept when day or device change
	Cover                       []coverPart
	Filters                     []filterLink
	Rows                        []testRow
	More                        int
	Tab                         string // of the page Zuordnung
	Tabs                        []pageTab
	NRules, NLearned, NProjects int
	Hour                        string       // "all", "0".."23", or "" for the default
	Hours                       []filterLink // the hours with rows, and the whole day
	NextHref                    string       // the next hour with rows
	NextHour                    string
	AddHref                     string
	Editor                      *editorView

	RuleList   []ruleLine // all rules in one list, filtered and sorted
	RuleChips  []filterLink
	RuleSorts  []option
	RuleSortBy string
	RuleFilter string
	GlobalAdd  string
	Learned    learnedView
	Filter     string
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
	Stat          string // short label: time in the last days
	StatHead      string // popover
	StatBody      string
	StatZero      bool
	NoAct         string
	Mode, ModeCls string
	Edit          string // link
	HatName       string
	HatColor      string
	Scope         string // hat key, or core.RulesScopeDefaults
	Noise         bool
	Minutes       int64 // time in the last days, for sorting
	App, Title    string
	Act           string
}

type ruleCond struct{ Word, Value string }

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
	rv.NRules, rv.NProjects, rv.NLearned = nRules, len(tk.Projects()), len(k.Details)+len(k.Parts)+len(k.Projects)
	rv.Counts = i18n.In(lang, "web.rules.counts", strconv.Itoa(rv.NRules), strconv.Itoa(rv.NProjects), strconv.Itoa(rv.NLearned))
	rv.Saved, rv.Deleted = q.Get("saved") != "", q.Get("deleted") != ""
	rv.Filter = q.Get("filter")
	if !slices.Contains([]string{"unknown", "diff", "noise"}, rv.Filter) {
		rv.Filter = ""
	}
	rv.RuleSortBy = q.Get("rs")
	if !slices.Contains(ruleSortKeys, rv.RuleSortBy) {
		rv.RuleSortBy = ruleSortKeys[0]
	}
	rv.RuleFilter = q.Get("rf") // checked against the chips of the rule list below
	if h := q.Get("hour"); h == "all" {
		rv.Hour = h
	} else if n, err := strconv.Atoi(h); err == nil && n >= 0 && n < 24 {
		rv.Hour = h
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

	base := "/assign?tab=day&day=" + url.QueryEscape(pick) + "&device=" + url.QueryEscape(rv.Device)
	rv.AddHref = base + rv.stateQuery(rv.Filter, rv.RuleFilter, rv.RuleSortBy, rv.Hour, false) + "&ed_new=1#try"

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
		active := cls
		if try != nil {
			active = try
		}
		reads := core.ReadDay(obsClip(day, next, obs), iv, active)
		if len(reads) == 0 {
			rv.EmptyDay = true
		}
		s.fillRows(rv, lang, loc, cfg, nm, pal, tk, k, reads, cls, hitID, base, tried, d, try != nil)
	}

	// one list of all rules: rules for all hats first, then each hat's
	rv.GlobalAdd = base + "&ed_new=1&ed_scope=" + core.RulesScopeDefaults + "#try"
	var all []ruleLine
	for n, rule := range cfg.Defaults.Rules {
		all = append(all, s.ruleLineOf(lang, cfg, nm, pal, stats, core.RulesScopeDefaults, n, rule, base))
	}
	for _, h := range cfg.ActiveHats() {
		for n, rule := range h.Rules {
			all = append(all, s.ruleLineOf(lang, cfg, nm, pal, stats, h.Key, n, rule, base))
		}
	}
	rv.RuleSorts = ruleSorts(lang, rv.RuleSortBy)
	chips := ruleChips(lang, cfg, all)
	if !slices.ContainsFunc(chips, func(c ruleChip) bool { return c.Value == rv.RuleFilter }) {
		rv.RuleFilter = ""
	}
	for _, c := range chips {
		rv.RuleChips = append(rv.RuleChips, filterLink{Href: strings.Replace(base, "?tab=day&", "?tab=rules&", 1) + rv.stateQuery(rv.Filter, c.Value, rv.RuleSortBy, rv.Hour, true) + "#regeln",
			Label: c.Label, N: c.N, On: c.Value == rv.RuleFilter})
	}
	sortRules(all, rv.RuleSortBy)
	for _, l := range all {
		if ruleShown(l, rv.RuleFilter) {
			rv.RuleList = append(rv.RuleList, l)
		}
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

// rawSpan is a read span of the rule test; base is its candidate without
// the rule being tried.
type rawSpan struct {
	o       core.Observation
	cand    core.Candidate
	base    core.Candidate
	booked  string
	differs bool
}

var ticketLike = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-[0-9]+\b`)
var fileTail = regexp.MustCompile(`\.[A-Za-z0-9]{1,5}$`)

func (s *Server) fillRows(rv *rulesView, lang string, loc *time.Location, cfg core.Config, nm map[string]string, pal palette, tk core.Tickets, k core.Knowledge,
	reads []core.ReadSpan, base *core.Classifier, hitID, link string, tried core.Rule, d draft, trying bool) {
	var raw []rawSpan
	for _, r := range reads {
		sp := rawSpan{o: r.Observation, cand: r.Cand, base: r.Cand, booked: r.Booked, differs: r.Differs()}
		if trying {
			sp.base = base.Classify(r.Attrs)
		}
		raw = append(raw, sp)
	}
	// the cover
	if pct := core.CoverageOf(reads).Percent(core.KindHat, core.KindPart, core.KindUnknown, core.KindNoise); pct != nil {
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
	var rowHour []int
	counts := map[string]int{}
	var hitN, hitNew int
	var hitDur time.Duration
	for _, g := range groups {
		sp := g.first
		c := sp.cand
		row := testRow{When: g.start.In(loc).Format("15:04") + "–" + g.end.In(loc).Format("15:04"), Dur: core.FormatDuration(g.end.Sub(g.start)),
			App: sp.o.Attrs["app"], Title: core.NormalizeTitle(sp.o.Attrs["title"])}
		var classes []string
		switch c.Kind() {
		case core.KindNoise:
			classes = append(classes, "noise")
			row.Noise = true
		case core.KindUnknown:
			classes = append(classes, "unk")
			row.Unknown = true
		case core.KindPart:
			classes = append(classes, "unk")
			row.NoHat = true
		}
		if sp.differs {
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
			row.Conf = i18n.In(lang, map[string]string{core.LevelSure: "web.rules.conf_sure",
				core.LevelLikely: "web.rules.conf_likely", core.LevelMaybe: "web.rules.conf_maybe"}[c.Level()])
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
		if !c.Ignore && (strings.Contains(row.Class, "unk") || strings.Contains(row.Class, "diff") || (c.Hat != "" && c.Level() != core.LevelSure)) {
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
			row.Plus = link + rv.stateQuery(rv.Filter, rv.RuleFilter, rv.RuleSortBy, rv.Hour, false) + "&" + f.Encode() + "#try"
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
		rowHour = append(rowHour, g.start.In(loc).Hour())
	}
	rows = rv.pickHour(lang, link, rows, rowHour)
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
		href := link + rv.stateQuery(f.id, rv.RuleFilter, rv.RuleSortBy, rv.Hour, true)
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
	days := strconv.Itoa(ruleStatsDays)
	d := stats[core.RuleID(scope, r)]
	l.Minutes = int64(d / time.Minute)
	l.StatHead = i18n.In(lang, "web.rules.stat_head", days)
	if d >= time.Minute {
		l.Stat = i18n.In(lang, "web.rules.stat_chip", core.FormatDuration(d), days)
		l.StatBody = i18n.In(lang, "web.rules.stat_time", core.FormatDuration(d))
	} else {
		l.Stat, l.StatZero = i18n.In(lang, "web.rules.stat_none"), true
		l.StatBody = i18n.In(lang, "web.rules.stat_none_body", days)
	}
	l.Scope, l.Noise = scope, r.Ignore
	l.App, l.Title = r.Match[core.MatchApp], r.Match[core.MatchTitle]
	l.Act = strings.Join(r.Activity, ", ")
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

// ruleSortKeys are the orders of the rule list; the first one is the default.
var ruleSortKeys = []string{"use", "app", "title", "hat", "act"}

// ruleSorts are the orders for the select, the one in use marked.
func ruleSorts(lang, cur string) []option {
	labels := map[string]string{"use": "web.rules.sort_use", "app": "web.rules.sort_app",
		"title": "web.rules.sort_title", "hat": "web.rules.sort_hat", "act": "web.rules.sort_act"}
	var out []option
	for _, k := range ruleSortKeys {
		out = append(out, option{Value: k, Label: i18n.In(lang, labels[k]), Selected: k == cur})
	}
	return out
}

// ruleChip is a filter above the rule list; Value is what ruleShown takes.
type ruleChip struct {
	Value, Label string
	N            int
}

// ruleChips are: all rules, rules for all hats, noise, and one per hat that
// has rules.
func ruleChips(lang string, cfg core.Config, all []ruleLine) []ruleChip {
	out := []ruleChip{{Label: i18n.In(lang, "web.rules.flt_all"), N: len(all)}}
	ohne, noise := 0, 0
	for _, l := range all {
		if l.Scope == core.RulesScopeDefaults {
			ohne++
		}
		if l.Noise {
			noise++
		}
	}
	out = append(out, ruleChip{"ohne", i18n.In(lang, "web.rules.flt_ohne"), ohne},
		ruleChip{"noise", i18n.In(lang, "web.rules.flt_noise"), noise})
	for _, h := range cfg.ActiveHats() {
		n := 0
		for _, l := range all {
			if l.Scope == h.Key {
				n++
			}
		}
		if n > 0 {
			out = append(out, ruleChip{"hat:" + h.Key, h.DisplayName(), n})
		}
	}
	return out
}

// ruleShown tells whether a rule is in the filter f ("" for all).
func ruleShown(l ruleLine, f string) bool {
	switch {
	case f == "ohne":
		return l.Scope == core.RulesScopeDefaults
	case f == "noise":
		return l.Noise
	case strings.HasPrefix(f, "hat:"):
		return l.Scope == strings.TrimPrefix(f, "hat:")
	}
	return true
}

// sortRules orders the rules by key; the most used first comes second.
func sortRules(rs []ruleLine, key string) {
	text := map[string]func(ruleLine) string{
		"app":   func(l ruleLine) string { return sortText(l.App) },
		"title": func(l ruleLine) string { return sortText(l.Title) },
		"hat": func(l ruleLine) string {
			if l.HatName == "" {
				return lastSort // rules for all hats last
			}
			return strings.ToLower(l.HatName)
		},
		"act": func(l ruleLine) string { return sortText(l.Act) },
	}
	f, byText := text[key]
	sort.SliceStable(rs, func(i, j int) bool {
		if byText {
			if x, y := f(rs[i]), f(rs[j]); x != y {
				return x < y
			}
		}
		return rs[i].Minutes > rs[j].Minutes
	})
}

// stateQuery is the state of the tab a link keeps, to be appended to a link
// with day and device: filter and hour of the day list, filter and order of
// the rule list, and with draft an open draft.
func (rv *rulesView) stateQuery(dayFilter, ruleFilter, sortKey, hour string, draft bool) string {
	q := "&filter=" + url.QueryEscape(dayFilter) + "&rf=" + url.QueryEscape(ruleFilter) +
		"&rs=" + url.QueryEscape(sortKey) + "&hour=" + url.QueryEscape(hour)
	if draft {
		for _, kv := range rv.Carry {
			q += "&" + kv[0] + "=" + url.QueryEscape(kv[1])
		}
	}
	return q
}

// pickHour keeps the rows of the chosen hour (hours[i] is the hour of
// rows[i]) and builds the hour chips. Without a choice the list shows the
// whole day when it fits, else the first hour with rows.
func (rv *rulesView) pickHour(lang, link string, rows []testRow, hours []int) []testRow {
	per := map[int]int{}
	var order []int
	for _, h := range hours {
		if per[h] == 0 {
			order = append(order, h)
		}
		per[h]++
	}
	sel := -1
	switch {
	case rv.Hour == "all":
	case rv.Hour != "":
		sel, _ = strconv.Atoi(rv.Hour)
	case len(rows) > maxTestRows && len(order) > 0:
		sel = order[0]
		rv.Hour = strconv.Itoa(sel)
	}
	if len(order) > 1 || sel >= 0 {
		href := func(h string) string {
			return link + rv.stateQuery(rv.Filter, rv.RuleFilter, rv.RuleSortBy, h, true) + "#rules-day"
		}
		rv.Hours = append(rv.Hours, filterLink{Href: href("all"), Label: i18n.In(lang, "web.rules.hour_all"), N: len(rows), On: sel < 0})
		for _, h := range order {
			rv.Hours = append(rv.Hours, filterLink{Href: href(strconv.Itoa(h)), Label: hourLabel(h), N: per[h], On: h == sel})
		}
		for _, h := range order {
			if sel >= 0 && h > sel {
				rv.NextHref, rv.NextHour = href(strconv.Itoa(h)), hourLabel(h)
				break
			}
		}
	}
	if sel < 0 {
		return rows
	}
	var out []testRow
	for i, r := range rows {
		if hours[i] == sel {
			out = append(out, r)
		}
	}
	return out
}

// hourLabel names an hour of the day as a range, "09–10".
func hourLabel(h int) string { return fmt.Sprintf("%02d–%02d", h, h+1) }

// sortText is a pattern as it reads, for sorting: without the signs of a
// regular expression and leading punctuation, in lower case; empty sorts last.
func sortText(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\\':
			// \b, \d, \s and the like stand for no text; \. is a dot
			if i+1 < len(rs) && !unicode.IsLetter(rs[i+1]) {
				b.WriteRune(rs[i+1])
			}
			i++
		case '^', '$', '(', ')', '[', ']', '{', '}', '*', '+', '?':
		case '|':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	s = strings.TrimSpace(strings.TrimLeftFunc(b.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
	if s == "" {
		return lastSort
	}
	return strings.ToLower(s)
}

// lastSort sorts after any text.
const lastSort = "\U0010ffff"

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
		return "/assign?tab=day&day=" + url.QueryEscape(day) + "&device=" + url.QueryEscape(device) +
			"&filter=" + url.QueryEscape(r.PostFormValue("filter")) + "&rf=" + url.QueryEscape(r.PostFormValue("rf")) +
			"&rs=" + url.QueryEscape(r.PostFormValue("rs")) + "&hour=" + url.QueryEscape(r.PostFormValue("hour")) + extra + "#rules-day"
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
func (s *Server) renderRulesFailure(w http.ResponseWriter, r *http.Request, acc access, _ core.Config, q url.Values, saveErr error) {
	v := s.base(r, "assign", acc)
	cur, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	if err := s.fillAssign(r, &v, cur, q, saveErr); err != nil {
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
	hr.AddHref = "/assign?tab=day&ed_new=1&ed_scope=" + url.QueryEscape(h.Key) + "#try"
	k, stats, err := s.knowledge(r)
	if err != nil {
		return
	}
	nm, pal := names(cfg), newPalette(cfg)
	for n, rule := range h.Rules {
		l := s.ruleLineOf(v.Lang, cfg, nm, pal, stats, h.Key, n, rule, "/assign?tab=day")
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
