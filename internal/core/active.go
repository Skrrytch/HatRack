package core

import (
	"sort"
	"time"
)

// Span is a plain time range [Start, End).
type Span struct{ Start, End time.Time }

// Union merges overlapping or touching spans; the result is sorted.
func Union(in []Span) []Span {
	s := append([]Span(nil), in...)
	sort.Slice(s, func(a, b int) bool { return s[a].Start.Before(s[b].Start) })
	var out []Span
	for _, x := range s {
		if !x.Start.Before(x.End) {
			continue
		}
		if n := len(out); n > 0 && !x.Start.After(out[n-1].End) {
			if x.End.After(out[n-1].End) {
				out[n-1].End = x.End
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// Overlap is the time of [a, b) covered by u, which is a Union result.
func Overlap(u []Span, a, b time.Time) time.Duration {
	var d time.Duration
	for _, s := range u {
		lo, hi := s.Start, s.End
		if a.After(lo) {
			lo = a
		}
		if b.Before(hi) {
			hi = b
		}
		if lo.Before(hi) {
			d += hi.Sub(lo)
		}
	}
	return d
}

// ActiveSpans cuts the absences out of the window spans, per device: what
// remains is the time a window had the focus while someone was there. The
// pieces keep ID, device, sensor and attributes of their span; the result
// is sorted by start. Spans of other sensors in window are dropped.
func ActiveSpans(window, idle []Observation) []Observation {
	away := map[string][]Span{}
	for _, o := range idle {
		if o.Sensor == "idle" {
			away[o.Device] = append(away[o.Device], Span{o.Start, o.End})
		}
	}
	for d, s := range away {
		away[d] = Union(s)
	}
	var out []Observation
	for _, o := range window {
		if o.Sensor != "window" || !o.Start.Before(o.End) {
			continue
		}
		start := o.Start
		for _, a := range away[o.Device] {
			if !a.End.After(start) {
				continue
			}
			if !a.Start.Before(o.End) {
				break
			}
			if a.Start.After(start) {
				p := o
				p.Start, p.End = start, a.Start
				out = append(out, p)
			}
			if a.End.After(start) {
				start = a.End
			}
		}
		if start.Before(o.End) {
			p := o
			p.Start = start
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out
}
