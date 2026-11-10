package core

import "time"

// Absorbing: some window spans say nothing of their own and plainly belong
// to the work before them, so a day reads as fewer, longer pieces. Stored
// observations never change; this happens when a day is read, so a changed
// setting changes past days at once. A span is absorbed only if nothing
// recognises it (kind unknown), so a rule always keeps its span visible.

// Defaults and limits of absorbing.
const (
	DefaultGlanceSeconds = 5
	MaxGlanceSeconds     = 60
	// PassingSpan: a window that had the focus for less than this was only
	// passed on the way to another one (Alt-Tab).
	PassingSpan = 2 * time.Second
	// absorbGap: a span is absorbed only if it follows the one before
	// without a gap (an absence cuts spans apart).
	absorbGap = 10 * time.Second
)

// DefaultHelpers are the helper apps used while observe.helpers is unset:
// password prompts and managers, file and permission dialogs, screenshots.
var DefaultHelpers = []string{
	"SshAskpass", "ssh-askpass", "Ksshaskpass",
	"KeePassXC", "KeePass2", "1Password", "Bitwarden", "proton-pass",
	"Xdg-desktop-portal-gtk", "xdg-desktop-portal-gnome", "xdg-desktop-portal-kde",
	"Polkit-gnome-authentication-agent-1", "polkit-kde-authentication-agent-1",
	"flameshot", "Gnome-screenshot", "spectacle",
}

// HelperApps is the effective helper list.
func (o Observe) HelperApps() []string {
	if o.Helpers == nil {
		return DefaultHelpers
	}
	return *o.Helpers
}

// Helper reports whether the app is a helper.
func (o Observe) Helper(app string) bool { return listHas(o.HelperApps(), app) }

// Glance is the longest side trip that counts for the window around it.
func (o Observe) Glance() time.Duration {
	s := o.GlanceSeconds
	if s == 0 {
		s = DefaultGlanceSeconds
	}
	return time.Duration(s) * time.Second
}

// sameWindow reports spans of one window: same device, app and title up to
// status marks.
func sameWindow(a, b Observation) bool {
	return a.Device == b.Device && a.Attrs["app"] == b.Attrs["app"] &&
		NormalizeTitle(a.Attrs["title"]) == NormalizeTitle(b.Attrs["title"])
}

// absorb hands spans that belong to the work before them to that span: a
// helper app, a window only passed (under PassingSpan), and side trips
// shorter than the glance between two spans of the same window. The span
// before grows to the end of what it absorbed and lists the pieces in
// Absorbed. rs is sorted by start, as ReadDay returns it.
func absorb(rs []ReadSpan, o Observe) []ReadSpan {
	glance := o.Glance()
	out := make([]ReadSpan, 0, len(rs))
	gone := make([]bool, len(rs)) // absorbed ahead of their turn
	last := map[string]int{}      // device -> index in out of its latest span
	// can reports whether rs[i] may be absorbed into out[p], after the span
	// that ended at end.
	can := func(i, p int, end time.Time) bool {
		r := rs[i]
		return r.Cand.Kind() == KindUnknown && r.Attrs[AttrPrivate] != "1" &&
			r.Start.Sub(end) <= absorbGap
	}
	// next is the index of the next span on the device after i, or -1.
	next := func(i int) int {
		for j := i + 1; j < len(rs); j++ {
			if rs[j].Device == rs[i].Device {
				return j
			}
		}
		return -1
	}
	for i, r := range rs {
		if gone[i] {
			continue
		}
		p, ok := last[r.Device]
		if ok && can(i, p, out[p].End) {
			// one piece: a helper, or a window only passed
			if o.Helper(r.Attrs["app"]) || r.End.Sub(r.Start) < PassingSpan {
				out[p].take(r)
				continue
			}
			// a side trip: short pieces, then back to the same window
			if trip := sideTrip(rs, i, out[p].Observation, glance, next, func(j int, end time.Time) bool { return can(j, p, end) }); trip != nil {
				for _, k := range trip {
					out[p].take(rs[k])
					gone[k] = true
				}
				continue
			}
		}
		out = append(out, r)
		last[r.Device] = len(out) - 1
	}
	return out
}

// sideTrip returns the spans from i on, all of them allowed by can and
// together shorter than glance, that are followed by a span of the window
// home; nil if there is no such trip.
func sideTrip(rs []ReadSpan, i int, home Observation, glance time.Duration, next func(int) int, can func(int, time.Time) bool) []int {
	trip := []int{i}
	d := rs[i].End.Sub(rs[i].Start)
	for j := next(i); j >= 0 && d < glance; j = next(j) {
		prev := rs[trip[len(trip)-1]].End
		if rs[j].Start.Sub(prev) > absorbGap {
			return nil
		}
		if sameWindow(rs[j].Observation, home) {
			return trip
		}
		if !can(j, prev) {
			return nil
		}
		trip = append(trip, j)
		d += rs[j].End.Sub(rs[j].Start)
	}
	return nil
}

// take absorbs r: the span grows to r's end and lists r's pieces.
func (s *ReadSpan) take(r ReadSpan) {
	if r.End.After(s.End) {
		s.End = r.End
	}
	s.Absorbed = append(append(s.Absorbed, r.Observation), r.Absorbed...)
}
