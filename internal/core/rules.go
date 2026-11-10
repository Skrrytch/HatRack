package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// Rules say which observed windows belong to which hat, activity and detail.
// A rule in a hat names that hat; a rule in defaults names none and gives
// only an activity, a detail, or marks noise. See docs/auto-detection.md.

// Rule modes for the live behaviour (#9).
const (
	ModeSuggest = "suggest" // the default
	ModeAuto    = "auto"
	ModeOff     = "off" // only afterwards
)

// Match fields of a rule.
const (
	MatchApp          = "app"
	MatchTitle        = "title"
	MatchNotification = "notification"
	MatchMedia        = "media"
	MatchBranch       = AttrBranch // the checked-out branch (sensor workdir)
	MatchRepo         = AttrRepo   // the work tree's folder name
)

// matchFields are the fields in the order their groups are looked up for
// numbered references in a detail: the first one present wins.
var matchFields = []string{MatchTitle, MatchBranch, MatchRepo, MatchNotification, MatchApp}

// Rule maps observed attributes to hat, activity and detail.
type Rule struct {
	ID       string            `yaml:"id,omitempty" json:"id,omitempty"`
	Match    map[string]string `yaml:"match" json:"match"`
	Activity Names             `yaml:"activity,omitempty" json:"activity,omitempty"`
	Detail   string            `yaml:"detail,omitempty" json:"detail,omitempty"`
	Ignore   bool              `yaml:"ignore,omitempty" json:"ignore,omitempty"`
	Mode     string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	Priority int               `yaml:"priority,omitempty" json:"priority,omitempty"`
}

// Names is one name or a list of names; written as a plain string when it
// holds one.
type Names []string

// UnmarshalYAML reads a string or a list of strings.
func (n *Names) UnmarshalYAML(b []byte) error {
	var one string
	if err := yaml.Unmarshal(b, &one); err == nil {
		*n = Names{one}
		return nil
	}
	var list []string
	if err := yaml.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("want a name or a list of names")
	}
	*n = list
	return nil
}

// MarshalYAML writes one name as a string.
func (n Names) MarshalYAML() (any, error) {
	if len(n) == 1 {
		return n[0], nil
	}
	return []string(n), nil
}

// UnmarshalJSON reads a string or a list of strings.
func (n *Names) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*n = Names{one}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("want a name or a list of names")
	}
	*n = list
	return nil
}

// MarshalJSON writes one name as a string.
func (n Names) MarshalJSON() ([]byte, error) {
	if len(n) == 1 {
		return json.Marshal(n[0])
	}
	return json.Marshal([]string(n))
}

// RulesScopeDefaults is the scope of the rules in defaults.
const RulesScopeDefaults = "defaults"

// RuleID returns the rule's own id, or one derived from its content and
// scope (hat key or RulesScopeDefaults). A changed rule gets a new id.
func RuleID(scope string, r Rule) string {
	if r.ID != "" {
		return r.ID
	}
	b, _ := json.Marshal(struct {
		M map[string]string `json:"m"`
		A Names             `json:"a,omitempty"`
		D string            `json:"d,omitempty"`
		I bool              `json:"i,omitempty"`
	}{r.Match, r.Activity, r.Detail, r.Ignore})
	sum := sha256.Sum256(b)
	return scope + ":" + hex.EncodeToString(sum[:4])
}

// ruleIDChars make the short ids HatRack gives rules: digits and lower
// case letters without o, l and i, which look like 0 and 1.
const ruleIDChars = "0123456789abcdefghjkmnpqrstuvwxyz"

// RuleIDs returns the ids the rules of cfg have.
func RuleIDs(cfg Config) map[string]bool {
	used := map[string]bool{}
	for _, h := range cfg.Hats {
		for _, r := range h.Rules {
			if r.ID != "" {
				used[r.ID] = true
			}
		}
	}
	for _, r := range cfg.Defaults.Rules {
		if r.ID != "" {
			used[r.ID] = true
		}
	}
	return used
}

// NewRuleID returns a short id of three characters that used does not
// hold, and adds it to used. The id only labels the rule for the eye; when
// three characters run out, it gets longer.
func NewRuleID(used map[string]bool) string {
	for n := 3; ; n++ {
		for try := 0; try < 200; try++ {
			b := make([]byte, n)
			rand.Read(b)
			for i := range b {
				b[i] = ruleIDChars[int(b[i])%len(ruleIDChars)]
			}
			if id := string(b); !used[id] {
				used[id] = true
				return id
			}
		}
	}
}

// AssignRuleIDs gives every rule of cfg without an id a new short one, so
// that a rule keeps its label when it changes. It reports whether it gave
// any; cfg gets its own copies of the rule lists it changes.
func AssignRuleIDs(cfg *Config) bool {
	used := RuleIDs(*cfg)
	fill := func(rules []Rule) ([]Rule, bool) {
		if !slices.ContainsFunc(rules, func(r Rule) bool { return r.ID == "" }) {
			return rules, false
		}
		rules = slices.Clone(rules)
		for i := range rules {
			if rules[i].ID == "" {
				rules[i].ID = NewRuleID(used)
			}
		}
		return rules, true
	}
	changed := false
	for i := range cfg.Hats {
		if rules, ok := fill(cfg.Hats[i].Rules); ok {
			if !changed {
				cfg.Hats = slices.Clone(cfg.Hats)
			}
			cfg.Hats[i].Rules, changed = rules, true
		}
	}
	if rules, ok := fill(cfg.Defaults.Rules); ok {
		cfg.Defaults.Rules, changed = rules, true
	}
	return changed
}

// compileMatch compiles a match pattern. Matching ignores case unless the
// pattern switches it off with (?-i).
func compileMatch(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("(?i)" + pattern)
}

// MatchSpan reports where a match pattern first matches value, as a rule
// does: start and end of the match, or ok false. An empty value never
// matches. err is a pattern that does not compile.
func MatchSpan(pattern, value string) (start, end int, ok bool, err error) {
	re, err := compileMatch(pattern)
	if err != nil || value == "" {
		return 0, 0, false, err
	}
	loc := re.FindStringIndex(value)
	if loc == nil {
		return 0, 0, false, nil
	}
	return loc[0], loc[1], true, nil
}

var (
	ruleIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	detailRefs = regexp.MustCompile(`\$(\d+)|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// MaxRulePriority bounds a rule's priority in both directions.
const MaxRulePriority = 100

// validateRules checks the rules of one scope. hat is nil for defaults.
func validateRules(path string, hat *Hat, rules []Rule, ids map[string]string) error {
	for i, r := range rules {
		p := fmt.Sprintf("%s[%d]", path, i)
		if err := validateRule(p, hat, r); err != nil {
			return err
		}
		if r.ID != "" {
			if prev, ok := ids[r.ID]; ok {
				return fmt.Errorf("config: %s.id %q: already used by %s", p, r.ID, prev)
			}
			ids[r.ID] = p
		}
	}
	return nil
}

// ValidateRule checks one rule for a hat (nil: a rule for all hats). path
// names the rule in the error, e.g. "hats.venom.rules[2]".
func ValidateRule(path string, hat *Hat, r Rule) error { return validateRule(path, hat, r) }

func validateRule(p string, hat *Hat, r Rule) error {
	if len(r.Match) == 0 {
		return fmt.Errorf("config: %s.match: give at least one of app, title", p)
	}
	res := map[string]*regexp.Regexp{}
	for _, f := range slices.Sorted(maps.Keys(r.Match)) {
		v := r.Match[f]
		switch f {
		case MatchApp, MatchTitle, MatchNotification, MatchBranch, MatchRepo:
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("config: %s.match.%s: empty pattern", p, f)
			}
			re, err := compileMatch(v)
			if err != nil {
				return fmt.Errorf("config: %s.match.%s: %v", p, f, regexErr(err))
			}
			res[f] = re
		case MatchMedia:
			if v != "mic" && v != "cam" {
				return fmt.Errorf("config: %s.match.media %q: want mic or cam", p, v)
			}
		default:
			return fmt.Errorf("config: %s.match: unknown field %q (use app, title, branch, repo, notification, media)", p, f)
		}
	}
	if r.ID != "" && !ruleIDRe.MatchString(r.ID) {
		return fmt.Errorf("config: %s.id %q: use lower case letters, digits, '-' and '_'", p, r.ID)
	}
	switch r.Mode {
	case "", ModeSuggest, ModeAuto, ModeOff:
	default:
		return fmt.Errorf("config: %s.mode %q: want suggest, auto or off", p, r.Mode)
	}
	if r.Priority < -MaxRulePriority || r.Priority > MaxRulePriority {
		return fmt.Errorf("config: %s.priority %d: want -%d to %d", p, r.Priority, MaxRulePriority, MaxRulePriority)
	}
	if r.Ignore {
		if len(r.Activity) > 0 || r.Detail != "" || r.Mode != "" {
			return fmt.Errorf("config: %s: an ignore rule has no activity, detail or mode", p)
		}
		return nil
	}
	if hat == nil && len(r.Activity) == 0 && r.Detail == "" {
		return fmt.Errorf("config: %s: a rule for all hats needs an activity, a detail or ignore", p)
	}
	for _, a := range r.Activity {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("config: %s.activity: empty name", p)
		}
		if hat != nil && !hasActivity(*hat, a, true) {
			return fmt.Errorf("config: %s.activity %q: hat %q has no such activity", p, a, hat.Key)
		}
	}
	return validateDetail(p, r.Detail, res)
}

// validateDetail checks that a detail template refers only to groups that
// exist: numbers in the first field of matchFields given, names in any.
func validateDetail(p, detail string, res map[string]*regexp.Regexp) error {
	var numbered *regexp.Regexp
	for _, f := range matchFields {
		if re, ok := res[f]; ok {
			numbered = re
			break
		}
	}
	for _, m := range detailRefs.FindAllStringSubmatch(detail, -1) {
		if m[1] != "" {
			n := 0
			fmt.Sscan(m[1], &n)
			if numbered == nil || n > numbered.NumSubexp() {
				return fmt.Errorf("config: %s.detail: $%s: the pattern has no such group", p, m[1])
			}
			continue
		}
		found := false
		for _, re := range res {
			found = found || re.SubexpIndex(m[2]) >= 0
		}
		if !found {
			return fmt.Errorf("config: %s.detail: ${%s}: no pattern has a group with that name", p, m[2])
		}
	}
	return nil
}

// regexErr shortens Go's regexp error to what helps a user.
func regexErr(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "error parsing regexp: ")
	return strings.Replace(s, "(?i)", "", 1)
}

// hasActivity reports whether a hat has the activity (ignoring case); with
// archived, archived activities count too.
func hasActivity(h Hat, name string, archived bool) bool {
	for _, a := range h.Activities {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	if archived {
		for _, a := range h.ArchivedActivity {
			if strings.EqualFold(a, name) {
				return true
			}
		}
	}
	return false
}

// activityOf returns the first name of the list the hat has (not archived),
// in the hat's spelling, or "".
func activityOf(h Hat, names []string) string {
	for _, n := range names {
		for _, a := range h.Activities {
			if strings.EqualFold(a, n) {
				return a
			}
		}
	}
	return ""
}
