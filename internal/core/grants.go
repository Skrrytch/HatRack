package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Grants: everything a sensor may read beyond app, window title and absence
// needs a grant, per device, off by default (docs/auto-detection.md, 1b).
// Agent and server both apply FilterGranted, so a span never holds more than
// the grants of its device allow.

// Grant ids, levels and the attributes they govern.
const (
	GrantWorkdir    = "workdir"
	GrantAISessions = "ai_sessions"

	LevelKnownKeys = "known_keys" // only values with a key of a known ticket project
	LevelAll       = "all"
	LevelOn        = "on"

	AttrRepo      = "repo"       // folder name of the work tree, never a path
	AttrBranch    = "branch"     // checked-out branch
	AttrBranchSrc = "branch_src" // where repo and branch came from
	// BranchFromSession marks repo and branch read from an AI tool's session
	// file; they need the grant ai_sessions besides workdir.
	BranchFromSession = "session"
)

// Grant describes one grant.
type Grant struct {
	ID string
	// Levels are the allowed values, from narrow to wide; the first is the
	// default once granted.
	Levels []string
	// Attrs are the span attributes the grant governs; without the grant
	// they are removed. When, if set, limits that to matching spans.
	Attrs []string
	When  func(Attrs) bool
	// Sensor is a sensor whose spans need the grant as a whole.
	Sensor string
	// KeyAttr is kept at level known_keys only if it holds a key of a known
	// ticket project.
	KeyAttr string
	// Needs is a grant that must be given on the same device; NeedsSensor a
	// sensor that must be on there for the grant to bring anything.
	Needs, NeedsSensor string
	// Ready is set once a ticket ships the reader; only then does the grant
	// appear in Setup.
	Ready bool
}

// Grants is the catalog, sorted by benefit and intrusion. Its card texts are
// the messages grant.<id>.{title,lead,what,why,stores,risk,not_needed}.
var Grants = []Grant{
	{
		ID:          GrantWorkdir,
		Levels:      []string{LevelKnownKeys, LevelAll},
		Attrs:       []string{AttrRepo, AttrBranch, AttrBranchSrc},
		KeyAttr:     AttrBranch,
		NeedsSensor: "window",
	},
	{
		ID:     GrantAISessions,
		Levels: []string{LevelOn},
		Attrs:  []string{AttrRepo, AttrBranch, AttrBranchSrc},
		When:   func(a Attrs) bool { return a[AttrBranchSrc] == BranchFromSession },
		Needs:  GrantWorkdir,
	},
}

// GrantByID returns the grant with this id.
func GrantByID(id string) (Grant, bool) {
	for _, g := range Grants {
		if g.ID == id {
			return g, true
		}
	}
	return Grant{}, false
}

// Granted returns the level of a grant on a device, "" if it is not given
// or what it needs is not given.
func (o Observe) Granted(device, id string) string {
	lvl := o.Grants[device][id]
	if lvl == "" {
		return ""
	}
	g, ok := GrantByID(id)
	if !ok || !slices.Contains(g.Levels, lvl) {
		return ""
	}
	if g.Needs != "" && o.Granted(device, g.Needs) == "" {
		return ""
	}
	return lvl
}

// FilterGranted removes from a span what its device has no grant for. It
// returns the attributes (a copy if anything changed) and false if the whole
// span must go. isKey tells whether a text holds a key of a known ticket
// project; nil skips that check (the server makes it).
func (o Observe) FilterGranted(device, sensor string, a Attrs, isKey func(string) bool) (Attrs, bool) {
	out := a
	copied := false
	del := func(k string) {
		if _, ok := out[k]; !ok {
			return
		}
		if !copied {
			out, copied = maps.Clone(a), true
		}
		delete(out, k)
	}
	for _, g := range Grants {
		lvl := o.Granted(device, g.ID)
		if g.Sensor != "" && g.Sensor == sensor && lvl == "" {
			return nil, false
		}
		if len(g.Attrs) == 0 || (g.When != nil && !g.When(a)) {
			continue
		}
		switch {
		case lvl == "":
			for _, k := range g.Attrs {
				del(k)
			}
		case lvl == LevelKnownKeys && g.KeyAttr != "" && isKey != nil:
			if v, ok := a[g.KeyAttr]; ok && !isKey(v) {
				del(g.KeyAttr)
			}
		}
	}
	return out, true
}

// validateGrants checks observe.grants: known grants and levels.
func (o Observe) validateGrants() error {
	for _, dev := range slices.Sorted(maps.Keys(o.Grants)) {
		if strings.TrimSpace(dev) == "" {
			return fmt.Errorf("config: observe.grants: empty device name")
		}
		for _, id := range slices.Sorted(maps.Keys(o.Grants[dev])) {
			g, ok := GrantByID(id)
			if !ok {
				return fmt.Errorf("config: observe.grants.%s: unknown grant %q", dev, id)
			}
			if lvl := o.Grants[dev][id]; !slices.Contains(g.Levels, lvl) {
				return fmt.Errorf("config: observe.grants.%s.%s: level %q: want one of %s", dev, id, lvl, strings.Join(g.Levels, ", "))
			}
		}
	}
	return nil
}
