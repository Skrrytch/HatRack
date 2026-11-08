package core

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Knowledge is what HatRack learned without any rule: details and ticket
// projects from the log (RecentWindow back), title parts from the
// observations (as far back as they are kept). The server builds it; agents
// get a copy. It holds no whole titles, only parts that point to a hat.
type Knowledge struct {
	Built    time.Time             `json:"built"`
	Details  []LearnedDetail       `json:"details,omitempty"`
	Projects map[string]ProjectUse `json:"projects,omitempty"`
	Parts    []LearnedPart         `json:"parts,omitempty"`
}

// LearnedDetail is a detail booked before, with the hat and activity it had
// most of its time.
type LearnedDetail struct {
	Detail   string `json:"detail"`
	Hat      string `json:"hat"`
	Activity string `json:"activity,omitempty"`
	Minutes  int    `json:"minutes"`
}

// ProjectUse is the hat a ticket project is booked on.
type ProjectUse struct {
	Hat   string  `json:"hat"`
	Share float64 `json:"share"`
	Count int     `json:"count"` // booked entries with keys of the project
}

// LearnedPart is a part of window titles seen mostly while one hat was
// booked. Part is in lower case.
type LearnedPart struct {
	Part    string  `json:"part"`
	Hat     string  `json:"hat"`
	Share   float64 `json:"share"`
	Minutes int     `json:"minutes"` // booked minutes with this part on screen
}

// Learning thresholds and limits.
const (
	LearnShare        = 0.8              // share of the time on one hat
	LearnPartMin      = 30 * time.Minute // booked time a title part needs
	LearnProjectCount = 3                // booked entries a project needs
	MaxLearnedDetails = 500
	MaxLearnedParts   = 2000
	minPartRunes      = 3
	maxPartRunes      = 80
)

var titleSep = regexp.MustCompile(`\s+[-–—·|]\s+`)

// appSuffixes are cut from an app name before parts are compared with it.
var appSuffixes = []string{"-stable", "-bin", "-beta", "-nightly", "-desktop"}

func appBase(app string) string {
	a := strings.ToLower(strings.TrimSpace(app))
	for _, s := range appSuffixes {
		a = strings.TrimSuffix(a, s)
	}
	return a
}

// TitleParts splits a window title at its separators (" - ", " – ", " — ",
// " · ", " | ") into the parts that may name a context: 3 to 80 characters,
// with a letter, not the app's own name ("Vivaldi", "Visual Studio Code"
// for app Code). Each part once, in order.
func TitleParts(title, app string) []string {
	base := appBase(app)
	var out []string
	seen := map[string]bool{}
	for _, p := range titleSep.Split(NormalizeTitle(title), -1) {
		p = strings.TrimSpace(p)
		n := utf8.RuneCountInString(p)
		if n < minPartRunes || n > maxPartRunes || !strings.ContainsFunc(p, unicode.IsLetter) {
			continue
		}
		lp := strings.ToLower(p)
		if utf8.RuneCountInString(base) >= minPartRunes && (strings.Contains(lp, base) || strings.Contains(base, lp)) {
			continue
		}
		if !seen[lp] {
			seen[lp] = true
			out = append(out, p)
		}
	}
	return out
}

// learnables are the parts of a detail that can be found in a title: the
// keys of known projects, or else the whole detail if it is one word of at
// least five characters with a digit or one of -_/. (a branch name).
// Free text never counts.
func learnables(detail string, t Tickets) []string {
	detail = strings.TrimSpace(ShortDetail(detail))
	if refs := t.Find(detail); len(refs) > 0 {
		out := make([]string, 0, len(refs))
		for _, r := range refs {
			out = append(out, r.Key)
		}
		return out
	}
	if utf8.RuneCountInString(detail) < 5 || strings.ContainsFunc(detail, unicode.IsSpace) {
		return nil
	}
	if strings.ContainsFunc(detail, func(r rune) bool { return unicode.IsDigit(r) || strings.ContainsRune("-_/.", r) }) {
		return []string{detail}
	}
	return nil
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/'
}

// containsWord reports whether word occurs in s as a whole, not as part of
// a longer word, key or path. Both are expected in lower case.
func containsWord(s, word string) bool {
	if word == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(word)
		before, _ := utf8.DecodeLastRuneInString(s[:a])
		after, _ := utf8.DecodeRuneInString(s[b:])
		if (a == 0 || !wordRune(before)) && (b == len(s) || !wordRune(after)) {
			return true
		}
		i = a + 1
	}
}

type hatTime map[string]time.Duration // "hat" or "hat|activity" -> time

func (m hatTime) best() (string, time.Duration, time.Duration) {
	var key string
	var top, total time.Duration
	for k, d := range m {
		total += d
		if d > top || (d == top && k < key) {
			key, top = k, d
		}
	}
	return key, top, total
}

// BuildKnowledge learns from the log and the observations. retention is how
// long observations are kept; title parts are learned from that period only.
func BuildKnowledge(events []Event, obs []Observation, t Tickets, retention time.Duration, now time.Time) Knowledge {
	k := Knowledge{Built: now, Projects: map[string]ProjectUse{}}
	all := Intervals(events, now)
	since := now.Add(-RecentWindow)
	var iv []Interval
	for _, i := range all {
		if i.End.After(since) && i.Hat != "" {
			iv = append(iv, i)
		}
	}

	// details and projects, from the log
	type detailStat struct {
		show string
		by   hatTime
	}
	details := map[string]*detailStat{}
	projTime := map[string]hatTime{}
	projCount := map[string]map[string]int{}
	for _, i := range iv {
		if i.Detail == "" {
			continue
		}
		d := i.Duration()
		for _, l := range learnables(i.Detail, t) {
			key := strings.ToLower(l)
			st := details[key]
			if st == nil {
				st = &detailStat{show: l, by: hatTime{}}
				details[key] = st
			}
			st.by[i.Hat+"|"+i.Activity] += d
		}
		for _, r := range t.Find(ShortDetail(i.Detail)) {
			if projTime[r.Project] == nil {
				projTime[r.Project], projCount[r.Project] = hatTime{}, map[string]int{}
			}
			projTime[r.Project][i.Hat] += d
			projCount[r.Project][i.Hat]++
		}
	}
	for _, st := range details {
		key, top, _ := st.by.best()
		hat, act, _ := strings.Cut(key, "|")
		k.Details = append(k.Details, LearnedDetail{Detail: st.show, Hat: hat, Activity: act, Minutes: Minutes(top)})
	}
	sort.Slice(k.Details, func(a, b int) bool {
		if k.Details[a].Minutes != k.Details[b].Minutes {
			return k.Details[a].Minutes > k.Details[b].Minutes
		}
		return k.Details[a].Detail < k.Details[b].Detail
	})
	if len(k.Details) > MaxLearnedDetails {
		k.Details = k.Details[:MaxLearnedDetails]
	}
	for p, by := range projTime {
		hat, top, total := by.best()
		if total > 0 && float64(top)/float64(total) >= LearnShare && projCount[p][hat] >= LearnProjectCount {
			k.Projects[p] = ProjectUse{Hat: hat, Share: float64(top) / float64(total), Count: projCount[p][hat]}
		}
	}

	// title parts, from the observations that are kept
	from := now.Add(-retention)
	var window, idle []Observation
	for _, o := range obs {
		if !o.End.After(from) {
			continue
		}
		switch o.Sensor {
		case "window":
			if o.Attrs["title"] != "" && o.Attrs[AttrPrivate] != "1" {
				window = append(window, o)
			}
		case "idle":
			idle = append(idle, o)
		}
	}
	sort.Slice(iv, func(a, b int) bool { return iv[a].Start.Before(iv[b].Start) })
	parts := map[string]hatTime{}
	for _, o := range ActiveSpans(window, idle) {
		ps := TitleParts(o.Attrs["title"], o.Attrs["app"])
		if len(ps) == 0 {
			continue
		}
		// the first interval that may overlap: iv is sorted by start and
		// does not overlap itself, so its ends are sorted too
		n := sort.Search(len(iv), func(x int) bool { return iv[x].End.After(o.Start) })
		for _, i := range iv[n:] {
			if !i.Start.Before(o.End) {
				break
			}
			lo, hi := later(i.Start, o.Start), earlier(i.End, o.End)
			if !lo.Before(hi) {
				continue
			}
			for _, p := range ps {
				lp := strings.ToLower(p)
				if parts[lp] == nil {
					parts[lp] = hatTime{}
				}
				parts[lp][i.Hat] += hi.Sub(lo)
			}
		}
	}
	for p, by := range parts {
		hat, top, total := by.best()
		if top >= LearnPartMin && float64(top)/float64(total) >= LearnShare {
			k.Parts = append(k.Parts, LearnedPart{Part: p, Hat: hat, Share: float64(top) / float64(total), Minutes: Minutes(top)})
		}
	}
	sort.Slice(k.Parts, func(a, b int) bool {
		if k.Parts[a].Minutes != k.Parts[b].Minutes {
			return k.Parts[a].Minutes > k.Parts[b].Minutes
		}
		return k.Parts[a].Part < k.Parts[b].Part
	})
	if len(k.Parts) > MaxLearnedParts {
		k.Parts = k.Parts[:MaxLearnedParts]
	}
	return k
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
