package server

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The Day view's band "Observed": which app had the focus and when the
// computer was idle. It is for orientation only and never changes the log.

// obsColors are the app colours: muted, with white text on them.
var obsColors = []string{"#5b8db8", "#c9894b", "#6aa57a", "#a87cb8", "#5fa7a3", "#b8a04f", "#c26d6d", "#7f8c9a"}

const (
	obsMergeGap   = time.Minute // spans of one app closer than this form one block
	obsLegendApps = 8           // apps named in the legend, the rest are "others"
	obsTitles     = 3           // titles listed in a block's tooltip
	obsOthersCol  = "#c3c6ca"
	obsPrivateCol = "#9aa0a6"
)

type obsBlock struct {
	Left, Width float64 // within its hour, in percent
	Color       string
	Label       string // the app; shown inside the block if there is room
	Title       string // info lines, shown on hover or tap
	What        string // hat|activity booked then (or just before), preselected in the add form
	Private     bool
	Away        bool
	Start, End  string // HH:MM of the whole block for the add form; empty: not clickable
	app         string // lower-case app, for the colour
	start, end  time.Time
}

// obsRow is what one device did during the day, as whole blocks.
type obsRow struct {
	Device string
	Blocks []obsBlock // windows first, then absences on top
}

// obsLine is one device's part of an hour.
type obsLine struct {
	Device string
	Blocks []obsBlock
}

// obsBooked is a booked entry's part of an hour, drawn as a thin line.
type obsBooked struct {
	Left, Width float64
	Color       string
	Title       string
}

// obsHour is one row of the band: an hour with something in it.
type obsHour struct {
	Label  string // "09"
	Many   bool   // more than one device in this hour: lines get their device name
	Skip   string // hours left out before this one, "12–13"; "" if none
	Lines  []obsLine
	Booked []obsBooked
}

type obsKey struct {
	Name, Color, Duration string
}

type obsView struct {
	Hours      []obsHour
	ShowDevice bool
	Legend     []obsKey // apps, then others
	Private    *obsKey
	Away       *obsKey
	rows       []obsRow // per device, whole blocks (Left/Width unset)
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

// obsClip keeps the window and idle spans of [day, next), clipped to it.
func obsClip(day, next time.Time, obs []core.Observation) []core.Observation {
	var out []core.Observation
	for _, o := range obs {
		if o.Sensor != "window" && o.Sensor != "idle" {
			continue
		}
		o.Start, o.End = later(o.Start, day), earlier(o.End, next)
		if o.Start.Before(o.End) {
			out = append(out, o)
		}
	}
	return out
}

// obsColorMap gives each app its colour: the slot of its name's hash, or the
// next free one if a busier app of the day has it, so up to len(obsColors)
// apps never share a colour. keys are lower-case, busiest first.
func obsColorMap(keys []string) map[string]string {
	out := map[string]string{}
	used := make([]bool, len(obsColors))
	for n, k := range keys {
		h := fnv.New32a()
		h.Write([]byte(k))
		i := int(h.Sum32() % uint32(len(obsColors)))
		if n < len(obsColors) {
			for used[i] {
				i = (i + 1) % len(obsColors)
			}
			used[i] = true
		}
		out[k] = obsColors[i]
	}
	return out
}

// obsMinutes is a duration for a tooltip, at least a minute, written like
// the rest of the page.
func obsMinutes(d time.Duration) string { return core.FormatDuration(max(d, time.Minute)) }

// obsClock turns a block into the HH:MM values of the add form: the start
// rounded down, the end up, the end not later than 23:59 and after the start.
func obsClock(day, start, end time.Time) (string, string) {
	loc := day.Location()
	s := start.In(loc)
	sm := s.Hour()*60 + s.Minute()
	em := 24 * 60
	if e := end.In(loc); e.Before(day.AddDate(0, 0, 1)) {
		em = e.Hour()*60 + e.Minute()
		if e.Second() > 0 || e.Nanosecond() > 0 {
			em++
		}
	}
	em = min(em, 24*60-1)
	if em <= sm {
		sm, em = min(sm, 24*60-2), min(sm, 24*60-2)+1
	}
	return fmt.Sprintf("%02d:%02d", sm/60, sm%60), fmt.Sprintf("%02d:%02d", em/60, em%60)
}

// obsSpan is a plain time range.
type obsSpan struct{ start, end time.Time }

// obsUnion merges overlapping spans; the input is sorted by start.
func obsUnion(in []obsSpan) []obsSpan {
	var out []obsSpan
	for _, s := range in {
		if n := len(out); n > 0 && !s.start.After(out[n-1].end) {
			out[n-1].end = later(out[n-1].end, s.end)
			continue
		}
		out = append(out, s)
	}
	return out
}

// obsOverlap is the time of [a, b) covered by the union.
func obsOverlap(u []obsSpan, a, b time.Time) time.Duration {
	var d time.Duration
	for _, s := range u {
		if lo, hi := later(a, s.start), earlier(b, s.end); lo.Before(hi) {
			d += hi.Sub(lo)
		}
	}
	return d
}

// obsWhat is the hat and activity booked at t, or else the last one that
// ended before it (a gap is most often filled with what ran before), or "".
func obsWhat(iv []core.Interval, t time.Time) string {
	prev := ""
	for _, i := range iv {
		if fleeting(i) {
			continue
		}
		if !i.Start.After(t) && (i.Open || i.End.After(t)) {
			return i.Hat + "|" + i.Activity
		}
		if !i.End.After(t) {
			prev = i.Hat + "|" + i.Activity
		}
	}
	return prev
}

// buildObs lays out the clipped observations of one day as one row per hour
// in which a computer was in use; iv are the booked intervals of the day,
// shown as a thin line under each hour. It returns nil if there is nothing
// to show, also on a day of only absence.
func buildObs(day time.Time, obs []core.Observation, iv []core.Interval, pal palette, nm map[string]string, lang string, canWrite bool) *obsView {
	if len(obs) == 0 {
		return nil
	}
	loc := day.Location()
	hm := func(t time.Time) string { return t.In(loc).Format("15:04") }
	timeLine := func(a, b time.Time) string {
		return fmt.Sprintf("%s–%s · %s", hm(a), hm(b), obsMinutes(b.Sub(a)))
	}

	byDev := map[string][]core.Observation{}
	for _, o := range obs {
		byDev[o.Device] = append(byDev[o.Device], o)
	}
	devs := make([]string, 0, len(byDev))
	for d := range byDev {
		devs = append(devs, d)
	}
	sort.Strings(devs)

	v := &obsView{ShowDevice: len(devs) > 1}
	appTime := map[string]time.Duration{} // by lower-case app
	appName := map[string]string{}
	var privTime, awayTime time.Duration
	var anyPriv bool
	var used [24]bool // hours in which some computer was in use

	hourOf := func(h int) (time.Time, time.Time) {
		return time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, loc),
			time.Date(day.Year(), day.Month(), day.Day(), h+1, 0, 0, 0, loc)
	}

	for _, dev := range devs {
		list := byDev[dev]
		sort.SliceStable(list, func(a, b int) bool { return list[a].Start.Before(list[b].Start) })
		var idle []obsSpan
		for _, o := range list {
			if o.Sensor == "idle" {
				idle = append(idle, obsSpan{o.Start, o.End})
			}
		}
		away := obsUnion(idle) // overlapping absences count once
		for _, a := range away {
			awayTime += a.end.Sub(a.start)
		}
		row := obsRow{Device: dev}

		type block struct {
			start, end time.Time
			key, app   string
			private    bool
			titles     map[string]time.Duration
			order      []string // titles by first appearance
		}
		var blocks []*block
		for _, o := range list {
			if o.Sensor != "window" {
				continue
			}
			private := o.Attrs[core.AttrPrivate] == "1"
			app := o.Attrs["app"]
			if !private && app == "" {
				continue // nothing to name it by
			}
			key := strings.ToLower(app)
			if private {
				key, app = "\x00private", ""
			}
			rest := o.End.Sub(o.Start) - obsOverlap(away, o.Start, o.End)
			for h := 0; h < 24; h++ {
				hs, he := hourOf(h)
				if lo, hi := later(o.Start, hs), earlier(o.End, he); lo.Before(hi) && hi.Sub(lo) > obsOverlap(away, lo, hi) {
					used[h] = true
				}
			}
			if private {
				privTime += rest
				anyPriv = true
			} else {
				appTime[key] += rest
				if appName[key] == "" {
					appName[key] = app
				}
			}
			var b *block
			if n := len(blocks); n > 0 && blocks[n-1].key == key && o.Start.Sub(blocks[n-1].end) < obsMergeGap {
				b = blocks[n-1]
				b.end = later(b.end, o.End)
			} else {
				b = &block{start: o.Start, end: o.End, key: key, app: app, private: private, titles: map[string]time.Duration{}}
				blocks = append(blocks, b)
			}
			if t := core.NormalizeTitle(o.Attrs["title"]); t != "" && !private {
				if _, ok := b.titles[t]; !ok {
					b.order = append(b.order, t)
				}
				b.titles[t] += o.End.Sub(o.Start)
			}
		}
		for _, b := range blocks {
			ob := obsBlock{start: b.start, end: b.end, Private: b.private}
			lines := []string{timeLine(b.start, b.end)}
			if b.private {
				lines = append(lines, i18n.In(lang, "web.obs.private"))
				ob.Color = obsPrivateCol
			} else {
				ob.Label, ob.app = b.app, b.key
				lines = append(lines, b.app)
				ts := append([]string(nil), b.order...)
				sort.SliceStable(ts, func(x, y int) bool { return b.titles[ts[x]] > b.titles[ts[y]] })
				for _, t := range ts[:min(len(ts), obsTitles)] {
					lines = append(lines, fmt.Sprintf("– %s (%s)", t, obsMinutes(b.titles[t])))
				}
			}
			if canWrite {
				ob.Start, ob.End = obsClock(day, b.start, b.end)
				ob.What = obsWhat(iv, b.start.Add(b.end.Sub(b.start)/2))
			}
			if len(devs) > 1 {
				lines[0] += " · " + dev
			}
			ob.Title = strings.Join(lines, "\n")
			row.Blocks = append(row.Blocks, ob)
		}

		// absences go last, so they are drawn over the windows
		devNote := ""
		if len(devs) > 1 {
			devNote = " · " + dev
		}
		for _, o := range list {
			if o.Sensor != "idle" {
				continue
			}
			kind := "web.obs.inactive"
			if o.Attrs["kind"] == "locked" {
				kind = "web.obs.locked"
			}
			row.Blocks = append(row.Blocks, obsBlock{start: o.Start, end: o.End, Away: true,
				Title: timeLine(o.Start, o.End) + devNote + "\n" + i18n.In(lang, "web.obs.away") + " (" + i18n.In(lang, kind) + ")"})
		}
		v.rows = append(v.rows, row)
	}
	keys := make([]string, 0, len(appTime))
	for k, d := range appTime {
		if d > 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(a, b int) bool {
		if appTime[keys[a]] != appTime[keys[b]] {
			return appTime[keys[a]] > appTime[keys[b]]
		}
		return keys[a] < keys[b]
	})
	color := obsColorMap(keys)
	for _, r := range v.rows {
		for i := range r.Blocks {
			if b := &r.Blocks[i]; b.app != "" {
				b.Color = color[b.app]
				if b.Color == "" { // only seen while away: no time of its own
					b.Color = obsColorMap([]string{b.app})[b.app]
				}
			}
		}
	}
	v.Hours = obsHours(day, v.rows, used, iv, pal, nm)
	if len(v.Hours) == 0 {
		return nil // only absence: no computer was in use
	}
	var others time.Duration
	for i, k := range keys {
		if i < obsLegendApps {
			v.Legend = append(v.Legend, obsKey{Name: appName[k], Color: color[k], Duration: core.FormatDuration(appTime[k])})
		} else {
			others += appTime[k]
		}
	}
	if others > 0 {
		v.Legend = append(v.Legend, obsKey{Name: i18n.In(lang, "web.obs.others"), Color: obsOthersCol, Duration: core.FormatDuration(others)})
	}
	if anyPriv {
		v.Private = &obsKey{Name: i18n.In(lang, "web.obs.private"), Color: obsPrivateCol, Duration: core.FormatDuration(privTime)}
	}
	if awayTime > 0 {
		v.Away = &obsKey{Name: i18n.In(lang, "web.obs.away"), Duration: core.FormatDuration(awayTime)}
	}
	return v
}

// obsHours cuts the blocks of every device into the hours marked as used,
// with the booked entries of each hour. A block keeps its whole time, info
// and add-form values in every hour it reaches into.
func obsHours(day time.Time, rows []obsRow, used [24]bool, iv []core.Interval, pal palette, nm map[string]string) []obsHour {
	loc := day.Location()
	var out []obsHour
	last := -1
	for h := 0; h < 24; h++ {
		if !used[h] {
			continue
		}
		hs := time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, loc)
		he := time.Date(day.Year(), day.Month(), day.Day(), h+1, 0, 0, 0, loc)
		span := float64(he.Sub(hs))
		place := func(a, b time.Time) (float64, float64, bool) {
			lo, hi := later(a, hs), earlier(b, he)
			if !lo.Before(hi) {
				return 0, 0, false
			}
			return float64(lo.Sub(hs)) / span * 100, float64(hi.Sub(lo)) / span * 100, true
		}
		hr := obsHour{Label: fmt.Sprintf("%02d", h)}
		if last >= 0 && h > last+1 {
			hr.Skip = fmt.Sprintf("%02d–%02d", last+1, h)
		}
		last = h
		for _, r := range rows {
			line := obsLine{Device: r.Device}
			for _, b := range r.Blocks {
				if l, w, ok := place(b.start, b.end); ok {
					b.Left, b.Width = l, w
					line.Blocks = append(line.Blocks, b)
				}
			}
			if len(line.Blocks) > 0 {
				hr.Lines = append(hr.Lines, line)
			}
		}
		hr.Many = len(hr.Lines) > 1
		for _, i := range iv {
			if fleeting(i) {
				continue
			}
			if l, w, ok := place(i.Start, i.End); ok {
				t := nameOf(nm, i.Hat)
				if i.Activity != "" {
					t += " · " + i.Activity
				}
				t += " " + i.Start.In(loc).Format("15:04") + "–" + i.End.In(loc).Format("15:04")
				hr.Booked = append(hr.Booked, obsBooked{Left: l, Width: w, Color: pal.color(i.Hat), Title: t})
			}
		}
		out = append(out, hr)
	}
	return out
}
