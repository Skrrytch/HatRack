package core

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Confidences of the sources of a candidate's hat.
const (
	ConfRule   = 0.9
	ConfDetail = 0.6
	ConfTicket = 0.6
	ConfPart   = 0.5
)

// Sources of a reason.
const (
	SourceRule   = "rule"
	SourceDetail = "detail" // a detail booked before
	SourceTicket = "ticket"
	SourcePart   = "part" // a learned title part
)

// Fields of a reason.
const (
	FieldHat      = "hat"
	FieldActivity = "activity"
	FieldDetail   = "detail"
	FieldIgnore   = "ignore"
)

// Candidate is what a span most likely was. Hat is empty when nothing
// points to one; a tool alone never makes a hat.
type Candidate struct {
	Hat        string   `json:"hat,omitempty"`
	Activity   string   `json:"activity,omitempty"`
	Activities []string `json:"activities,omitempty"` // without a hat: the list a rule gave, see WithHat
	Detail     string   `json:"detail,omitempty"`
	Conf       float64  `json:"conf,omitempty"` // of the hat
	Ignore     bool     `json:"ignore,omitempty"`
	Why        []Reason `json:"why,omitempty"`
}

// Reason says where a field of a candidate came from.
type Reason struct {
	Field   string  `json:"field"`
	Source  string  `json:"source"`
	Ref     string  `json:"ref"`             // rule id, detail, ticket key or title part
	Hat     string  `json:"hat,omitempty"`   // the hat behind a learned source
	Share   float64 `json:"share,omitempty"` // learned: share of the time on that hat
	Minutes int     `json:"minutes,omitempty"`
}

// IsZero reports whether the candidate says nothing.
func (c Candidate) IsZero() bool {
	return c.Hat == "" && c.Activity == "" && len(c.Activities) == 0 && c.Detail == "" && !c.Ignore
}

type compiledRule struct {
	id       string
	hat      string // "" for defaults
	order    int    // position over all rules: hats in config order, then defaults
	rule     Rule
	re       map[string]*regexp.Regexp
	media    string
	conds    int
	numbered string // field whose groups $1.. refer to
}

// Classifier turns a span's attributes into a candidate. It is built from a
// validated configuration and gives the same result for the same input
// everywhere (agent live, server afterwards).
type Classifier struct {
	cfg      Config
	hats     map[string]Hat
	rules    []compiledRule // hat rules of active hats, then defaults
	know     Knowledge
	parts    map[string]LearnedPart
	tickets  Tickets
	detailLC []string // lower-case details, same order as know.Details
}

// NewClassifier builds a classifier. cfg must be validated; rules that do
// not compile are skipped.
func NewClassifier(cfg Config, k Knowledge, t Tickets) *Classifier {
	c := &Classifier{cfg: cfg, hats: map[string]Hat{}, know: k, tickets: t, parts: map[string]LearnedPart{}}
	n := 0
	for _, h := range cfg.Hats {
		c.hats[h.Key] = h
		if h.Archived {
			continue
		}
		for _, r := range h.Rules {
			if cr, ok := compileRule(h.Key, h.Key, n, r); ok {
				c.rules = append(c.rules, cr)
			}
			n++
		}
	}
	for _, r := range cfg.Defaults.Rules {
		if cr, ok := compileRule(RulesScopeDefaults, "", n, r); ok {
			c.rules = append(c.rules, cr)
		}
		n++
	}
	for _, p := range k.Parts {
		if h, ok := c.hats[p.Hat]; !ok || h.Archived {
			continue
		}
		if old, ok := c.parts[p.Part]; !ok || p.Minutes > old.Minutes {
			c.parts[p.Part] = p
		}
	}
	for _, d := range k.Details {
		c.detailLC = append(c.detailLC, strings.ToLower(d.Detail))
	}
	return c
}

func compileRule(scope, hat string, order int, r Rule) (compiledRule, bool) {
	cr := compiledRule{id: RuleID(scope, r), hat: hat, order: order, rule: r, re: map[string]*regexp.Regexp{}}
	for f, v := range r.Match {
		switch f {
		case MatchMedia:
			cr.media = v
		case MatchApp, MatchTitle, MatchNotification, MatchBranch, MatchRepo:
			re, err := compileMatch(v)
			if err != nil {
				return cr, false
			}
			cr.re[f] = re
		default:
			return cr, false
		}
		cr.conds++
	}
	for _, f := range matchFields {
		if _, ok := cr.re[f]; ok {
			cr.numbered = f
			break
		}
	}
	return cr, cr.conds > 0
}

// With returns a classifier that also has rule r in scope (a hat key or
// RulesScopeDefaults), for trying a rule before it is saved.
func (c *Classifier) With(scope string, r Rule) (*Classifier, error) {
	cfg := c.cfg
	if scope == RulesScopeDefaults {
		if err := ValidateRule("rule", nil, r); err != nil {
			return nil, err
		}
		cfg.Defaults.Rules = append(slices.Clone(cfg.Defaults.Rules), r)
	} else {
		i := slices.IndexFunc(cfg.Hats, func(h Hat) bool { return h.Key == scope })
		if i < 0 {
			return nil, fmt.Errorf("unknown hat %q", scope)
		}
		h := cfg.Hats[i]
		if err := ValidateRule("rule", &h, r); err != nil {
			return nil, err
		}
		h.Rules = append(slices.Clone(h.Rules), r)
		cfg.Hats = slices.Clone(cfg.Hats)
		cfg.Hats[i] = h
	}
	return NewClassifier(cfg, c.know, c.tickets), nil
}

// RuleIDOf returns the id the classifier gives a rule of a scope; tests and
// the rule test use it to find what a tried rule matched.
func (c *Classifier) RuleIDOf(scope string, r Rule) string { return RuleID(scope, r) }

// match reports whether the rule matches, and the submatches per field.
func (cr compiledRule) match(a Attrs) (map[string][]string, bool) {
	if cr.media != "" && !truthy(a[cr.media]) {
		return nil, false
	}
	subs := map[string][]string{}
	for f, re := range cr.re {
		v, ok := a[f]
		if !ok || v == "" {
			return nil, false
		}
		m := re.FindStringSubmatch(v)
		if m == nil {
			return nil, false
		}
		subs[f] = m
	}
	return subs, true
}

func truthy(s string) bool { return s == "1" || s == "true" }

// expand fills $0, $1, ${name} in a detail template; $$ is a dollar.
func (cr compiledRule) expand(subs map[string][]string) string {
	t := cr.rule.Detail
	if t == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] != '$' || i+1 == len(t) {
			b.WriteByte(t[i])
			continue
		}
		switch {
		case t[i+1] == '$':
			b.WriteByte('$')
			i++
		case t[i+1] >= '0' && t[i+1] <= '9':
			j := i + 1
			for j < len(t) && t[j] >= '0' && t[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(t[i+1 : j])
			if m := subs[cr.numbered]; n < len(m) {
				b.WriteString(m[n])
			}
			i = j - 1
		case t[i+1] == '{':
			end := strings.IndexByte(t[i:], '}')
			if end < 0 {
				b.WriteString(t[i:])
				i = len(t)
				continue
			}
			name := t[i+2 : i+end]
			for _, f := range matchFields {
				if re, ok := cr.re[f]; ok {
					if x := re.SubexpIndex(name); x >= 0 && x < len(subs[f]) {
						b.WriteString(subs[f][x])
						break
					}
				}
			}
			i += end
		default:
			b.WriteByte('$')
		}
	}
	return strings.TrimSpace(b.String())
}

// ruleBetter orders rules: higher priority, then more conditions, then the
// earlier one.
func ruleBetter(a, b compiledRule) bool {
	if a.rule.Priority != b.rule.Priority {
		return a.rule.Priority > b.rule.Priority
	}
	if a.conds != b.conds {
		return a.conds > b.conds
	}
	return a.order < b.order
}

type hit struct {
	cr   compiledRule
	subs map[string][]string
}

// learned is a hat claim from what was learned.
type learned struct {
	hat     string
	conf    float64
	rank    int // at equal confidence: detail 0, ticket 1, part 2
	minutes int
	reason  Reason
	act     string // a learned detail's activity
	detail  string // a learned detail or ticket key
}

func learnedBetter(a, b learned) bool {
	if a.conf != b.conf {
		return a.conf > b.conf
	}
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	if a.minutes != b.minutes {
		return a.minutes > b.minutes
	}
	return a.reason.Ref < b.reason.Ref
}

// Classify returns the candidate for a span's attributes.
func (c *Classifier) Classify(a Attrs) Candidate {
	if a[AttrPrivate] == "1" {
		return Candidate{Ignore: true, Why: []Reason{{Field: FieldIgnore, Source: AttrPrivate}}}
	}
	if t, ok := a[MatchTitle]; ok {
		a = maps.Clone(a)
		if t = NormalizeTitle(t); t == "" {
			delete(a, MatchTitle)
		} else {
			a[MatchTitle] = t
		}
	}

	var hits []hit
	for _, cr := range c.rules {
		if subs, ok := cr.match(a); ok {
			if cr.rule.Ignore {
				return Candidate{Ignore: true, Why: []Reason{{Field: FieldIgnore, Source: SourceRule, Ref: cr.id}}}
			}
			hits = append(hits, hit{cr, subs})
		}
	}
	slices.SortStableFunc(hits, func(x, y hit) int {
		if ruleBetter(x.cr, y.cr) {
			return -1
		}
		if ruleBetter(y.cr, x.cr) {
			return 1
		}
		return 0
	})

	var cand Candidate
	// 1. the hat: a hat rule beats anything learned
	var claims []learned
	for _, h := range hits {
		if h.cr.hat != "" {
			cand.Hat, cand.Conf = h.cr.hat, ConfRule
			cand.Why = append(cand.Why, Reason{Field: FieldHat, Source: SourceRule, Ref: h.cr.id})
			break
		}
	}
	title := a[MatchTitle]
	claims = c.learnedClaims(a)
	var best *learned
	if cand.Hat == "" {
		for i := range claims {
			if claims[i].hat == "" {
				continue
			}
			if best == nil || learnedBetter(claims[i], *best) {
				best = &claims[i]
			}
		}
		if best != nil {
			cand.Hat, cand.Conf = best.hat, best.conf
			cand.Why = append(cand.Why, best.reason)
		}
	}

	// 2. the activity: hat rule of that hat, global rule, learned detail
	var hat Hat
	if cand.Hat != "" {
		hat = c.hats[cand.Hat]
	}
	for _, h := range hits {
		if len(h.cr.rule.Activity) == 0 || (h.cr.hat != "" && h.cr.hat != cand.Hat) {
			continue
		}
		if cand.Hat == "" {
			cand.Activities = slices.Clone(h.cr.rule.Activity)
			cand.Why = append(cand.Why, Reason{Field: FieldActivity, Source: SourceRule, Ref: h.cr.id})
			break
		}
		if act := activityOf(hat, h.cr.rule.Activity); act != "" {
			cand.Activity = act
			cand.Why = append(cand.Why, Reason{Field: FieldActivity, Source: SourceRule, Ref: h.cr.id})
			break
		}
	}
	if cand.Activity == "" && len(cand.Activities) == 0 && cand.Hat != "" {
		for _, l := range claims {
			if l.reason.Source == SourceDetail && l.hat == cand.Hat && l.act != "" {
				if act := activityOf(hat, []string{l.act}); act != "" {
					cand.Activity = act
					r := l.reason
					r.Field = FieldActivity
					cand.Why = append(cand.Why, r)
					break
				}
			}
		}
	}

	// 3. the detail: hat rule of that hat, global rule, ticket, learned detail
	for _, h := range hits {
		if h.cr.rule.Detail == "" || (h.cr.hat != "" && h.cr.hat != cand.Hat) {
			continue
		}
		if d := h.cr.expand(h.subs); d != "" {
			cand.Detail = d
			cand.Why = append(cand.Why, Reason{Field: FieldDetail, Source: SourceRule, Ref: h.cr.id})
			break
		}
	}
	// a key in the branch of a working tool beats one in a title
	if keys := c.tickets.BranchKeys(a[MatchBranch]); cand.Detail == "" && len(keys) > 0 {
		cand.Detail = keys[0]
		cand.Why = append(cand.Why, Reason{Field: FieldDetail, Source: SourceTicket, Ref: keys[0]})
	}
	if cand.Detail == "" && title != "" {
		if refs := c.tickets.Find(title); len(refs) > 0 {
			cand.Detail = refs[0].Key
			cand.Why = append(cand.Why, Reason{Field: FieldDetail, Source: SourceTicket, Ref: refs[0].Key})
		}
	}
	if cand.Detail == "" {
		for _, l := range claims {
			if l.reason.Source == SourceDetail && (cand.Hat == "" || l.hat == cand.Hat) {
				cand.Detail = l.detail
				r := l.reason
				r.Field = FieldDetail
				cand.Why = append(cand.Why, r)
				break
			}
		}
	}
	return cand
}

// learnedClaims collects what was learned about a span, best first: details
// and ticket keys in the title, ticket keys in the branch, learned title
// parts and the repository.
func (c *Classifier) learnedClaims(a Attrs) []learned {
	title, app := a[MatchTitle], a[MatchApp]
	lt := strings.ToLower(title)
	var out []learned
	if title != "" {
		for i, d := range c.know.Details {
			if !containsWord(lt, c.detailLC[i]) {
				continue
			}
			hat := d.Hat
			if h, ok := c.hats[hat]; !ok || h.Archived {
				hat = ""
			}
			out = append(out, learned{hat: hat, conf: ConfDetail, rank: 0, minutes: d.Minutes, act: d.Activity, detail: d.Detail,
				reason: Reason{Field: FieldHat, Source: SourceDetail, Ref: d.Detail, Hat: d.Hat, Minutes: d.Minutes}})
		}
	}
	ticket := func(project, key string) {
		u, ok := c.know.Projects[project]
		if !ok {
			return
		}
		if h, ok := c.hats[u.Hat]; !ok || h.Archived {
			return
		}
		out = append(out, learned{hat: u.Hat, conf: ConfTicket, rank: 1, minutes: u.Count, detail: key,
			reason: Reason{Field: FieldHat, Source: SourceTicket, Ref: key, Hat: u.Hat, Share: u.Share}})
	}
	for _, k := range c.tickets.BranchKeys(a[MatchBranch]) {
		ticket(keyProject(k), k)
	}
	if title != "" {
		for _, r := range c.tickets.Find(title) {
			ticket(r.Project, r.Key)
		}
	}
	parts := TitleParts(title, app)
	if r := strings.TrimSpace(a[MatchRepo]); r != "" {
		parts = append(parts, r)
	}
	for _, p := range parts {
		if lp, ok := c.parts[strings.ToLower(p)]; ok {
			out = append(out, learned{hat: lp.Hat, conf: ConfPart, rank: 2, minutes: lp.Minutes,
				reason: Reason{Field: FieldHat, Source: SourcePart, Ref: p, Hat: lp.Hat, Share: lp.Share, Minutes: lp.Minutes}})
		}
	}
	slices.SortStableFunc(out, func(x, y learned) int {
		if learnedBetter(x, y) {
			return -1
		}
		if learnedBetter(y, x) {
			return 1
		}
		return 0
	})
	return out
}

// WithHat fills in a hat for a candidate that has none (live: the worn
// hat), resolving the activity list against it. A candidate with a hat is
// returned as it is.
func (c Candidate) WithHat(cfg Config, hat string) Candidate {
	if c.Hat != "" || c.Ignore || hat == "" {
		return c
	}
	h, ok := cfg.Hat(hat)
	if !ok {
		return c
	}
	c.Hat = hat
	if len(c.Activities) > 0 {
		c.Activity = activityOf(h, c.Activities)
		c.Activities = nil
	}
	return c
}

// keyProject is the project of a ticket key: what comes before the last '-'.
func keyProject(key string) string {
	if i := strings.LastIndexByte(key, '-'); i > 0 {
		return key[:i]
	}
	return key
}
