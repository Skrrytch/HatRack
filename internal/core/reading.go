package core

import (
	"sort"
	"time"
)

// Reading a day: every active window span with what HatRack makes of it and
// what was booked then. The Day view, the rule test, the rule statistics and
// later the condensing into blocks (#4) and MCP all start from this, so they
// agree on what a span was.

// Levels of how sure a candidate's hat is, for people.
const (
	LevelSure   = "sure"
	LevelLikely = "likely"
	LevelMaybe  = "maybe"
)

// Lowest confidences of the levels.
const (
	SureConf   = ConfRule
	LikelyConf = ConfDetail
)

// Level says how sure the hat is: LevelSure, LevelLikely, LevelMaybe, or ""
// without a hat.
func (c Candidate) Level() string {
	switch {
	case c.Hat == "":
		return ""
	case c.Conf >= SureConf:
		return LevelSure
	case c.Conf >= LikelyConf:
		return LevelLikely
	}
	return LevelMaybe
}

// Kinds of a candidate, for the share of a day that HatRack can assign.
const (
	KindHat     = "hat"     // a hat was found
	KindPart    = "part"    // only an activity or a detail
	KindUnknown = "unknown" // no clue at all
	KindNoise   = "noise"   // hidden by a rule
)

// Kind sorts a candidate into one of the kinds.
func (c Candidate) Kind() string {
	switch {
	case c.Ignore:
		return KindNoise
	case c.Hat != "":
		return KindHat
	case c.IsZero():
		return KindUnknown
	}
	return KindPart
}

// ReadSpan is an active window span with its candidate and the hat booked in
// its middle.
type ReadSpan struct {
	Observation
	Cand   Candidate
	Booked string // "" when nothing was booked
}

// Differs reports a span whose hat was found but another one was booked.
func (r ReadSpan) Differs() bool {
	return r.Cand.Hat != "" && r.Booked != "" && r.Booked != r.Cand.Hat
}

// ReadDay reads observations (window and idle spans, any order) against the
// booked intervals: the window spans without the absences, sorted by start,
// each classified. Fleeting entries do not count as booked.
func ReadDay(obs []Observation, iv []Interval, cls *Classifier) []ReadSpan {
	spans := ActiveSpans(obs, obs)
	out := make([]ReadSpan, 0, len(spans))
	for _, o := range spans {
		out = append(out, ReadSpan{Observation: o, Cand: cls.Classify(o.Attrs),
			Booked: BookedAt(iv, o.Start.Add(o.End.Sub(o.Start)/2))})
	}
	return out
}

// BookedAt is the hat of the entry that runs at t, or "". Fleeting entries
// are skipped.
func BookedAt(iv []Interval, t time.Time) string {
	for _, i := range iv {
		if i.Fleeting() {
			continue
		}
		if !i.Start.After(t) && (i.Open || i.End.After(t)) {
			return i.Hat
		}
	}
	return ""
}

// Coverage is the time of a reading per kind.
type Coverage map[string]time.Duration

// CoverageOf adds up the time of the spans per kind.
func CoverageOf(rs []ReadSpan) Coverage {
	c := Coverage{}
	for _, r := range rs {
		c[r.Cand.Kind()] += r.End.Sub(r.Start)
	}
	return c
}

// Percent gives the shares of the kinds in the given order, rounded so that
// they add up to 100 (largest remainder); nil when the reading is empty.
func (c Coverage) Percent(kinds ...string) []int {
	var total time.Duration
	for _, k := range kinds {
		total += max(c[k], 0)
	}
	if total == 0 {
		return nil
	}
	out := make([]int, len(kinds))
	rest := make([]time.Duration, len(kinds)) // remainder of d*100/total, in units of 1/total
	left := 100
	for i, k := range kinds {
		x := max(c[k], 0) * 100
		out[i], rest[i] = int(x/total), x%total
		left -= out[i]
	}
	order := make([]int, len(kinds))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rest[order[a]] > rest[order[b]] })
	for k := 0; left > 0; k++ {
		out[order[k%len(order)]]++
		left--
	}
	return out
}
