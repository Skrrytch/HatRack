package panel

import (
	"image"
	"image/color"
	"strings"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// The return mode: after an absence the panel opens with a question band
// instead of the status header. The hat cards below answer "it was something
// else"; the band's two chips answer pause / work, the booked one is
// preselected and takes no action beyond closing the question.

var cAccent = color.RGBA{0x2a, 0x9d, 0x9a, 255} // the preselected answer

// awayRequest builds the socket request that answers a question.
func awayRequest(q agent.AwayQuestion, answer string) *agent.Request {
	return &agent.Request{Cmd: "away", Away: q.ID, Answer: answer}
}

// awayOther answers "it was something else": the hat, an activity if one was
// picked, and a detail.
func awayOther(q agent.AwayQuestion, hat, activity, detail string) Action {
	r := awayRequest(q, agent.AwayOther)
	r.Hat, r.Activity, r.Detail = hat, activity, detail
	return Action{Request: r}
}

// AwayKeep is the action of closing the question without a change.
func AwayKeep(q agent.AwayQuestion) Action { return Action{Request: awayRequest(q, agent.AwayKeep)} }

// awayWhat names what ran before the absence: "VENOM · development · VEN-1".
func awayWhat(q agent.AwayQuestion) string {
	what := q.HatName
	if what == "" {
		what = q.Hat
	}
	for _, p := range []string{q.Activity, core.ShortDetail(q.Detail)} {
		if p != "" {
			what += " · " + p
		}
	}
	return what
}

// returnBand lays out the question band. It returns the height of the band
// (its last line is the border) and the y where the cards' area starts.
func returnBand(l *Layout, m metrics, q agent.AwayQuestion, W int) (bandH, y int) {
	l.Default = i18n.T("away.panel.hint")
	what := awayWhat(q)
	title := i18n.T("away.panel.title", q.From.Local().Format("15:04"), q.To.Local().Format("15:04"), core.FormatDuration(q.To.Sub(q.From)))
	sub := i18n.T("away.panel.booked_pause", what, q.To.Local().Format("15:04"))
	if q.Booked == agent.AwayWork {
		sub = i18n.T("away.panel.booked_work", what)
	}
	y = m.pad
	l.Texts = append(l.Texts,
		Text{X: m.pad, Y: y, S: fit(title, true, m.titlePx, W-2*m.pad), Bold: true, Px: m.titlePx, Color: cFG})
	y += m.titlePx + m.gap/2 + 2
	l.Texts = append(l.Texts,
		Text{X: m.pad, Y: y, S: fit(sub, false, m.txPx, W-2*m.pad), Px: m.txPx, Color: cDim})
	y += m.txPx + m.gap

	// the two answers: the booked one first, preselected, then the other
	pause, work := i18n.T("away.panel.pause"), i18n.T("away.panel.work")
	first, second := Hit{Label: pause}, Hit{Label: work, Sub: what}
	firstAns, secondAns := agent.AwayKeep, agent.AwayWork
	if q.Booked == agent.AwayWork {
		first, second = Hit{Label: work}, Hit{Label: pause}
		secondAns = agent.AwayPause
	}
	first.Current, first.Icon = true, "check"
	chH := m.chipH + int(8*m.s+.5)
	x := m.pad
	for _, c := range []struct {
		h   Hit
		ans string
	}{{first, firstAns}, {second, secondAns}} {
		h := c.h
		w := textWidth(h.Label, true, m.chipPx) + 2*m.chipPad
		if h.Icon != "" {
			w += m.chipPx + m.chipPad/2
		}
		if h.Sub != "" {
			sub := fit(h.Sub, false, m.chipPx, W-2*m.pad-w-m.chipPad/2)
			w += m.chipPad/2 + textWidth(sub, false, m.chipPx)
			h.Sub = sub
		}
		h.R, h.Kind, h.Color = Rect{x, y, w, chH}, KindChip, cAccent
		h.Action = Action{Request: awayRequest(q, c.ans)}
		if c.ans == agent.AwayKeep {
			h.Action = AwayKeep(q)
		}
		l.Hits = append(l.Hits, h)
		x += w + m.chipGap + m.chipPad/2
	}
	y += chH + m.pad
	l.Boxes = append(l.Boxes, Box{R: Rect{0, y - 1, W, 1}, Color: cBorder})
	bandH = y
	y += m.gap
	l.Texts = append(l.Texts, Text{X: m.pad, Y: y, S: i18n.T("away.panel.or"), Px: m.chipPx, Color: cDim})
	y += m.chipPx + m.gap/2
	return bandH, y
}

// chip returns the index of the k-th answer chip, or -1.
func (l Layout) chip(k int) int {
	for i, h := range l.Hits {
		if h.Kind == KindChip {
			if k == 0 {
				return i
			}
			k--
		}
	}
	return -1
}

// awayKey handles the keys of the return mode; ok reports it did.
func (s *screen) awayKey(name string) (act Action, done, ok bool) {
	switch name {
	case "escape":
		return AwayKeep(*s.away), true, true
	case "left", "right":
		k := 0
		if name == "right" {
			k = 1
		}
		if i := s.l.chip(k); i >= 0 && s.focus >= 0 && s.focus < len(s.l.Hits) && s.l.Hits[s.focus].Kind == KindChip {
			s.focus = i
			return Action{}, false, true
		}
	}
	return Action{}, false, false
}

// drawChip draws an answer chip: label in bold, then what it refers to.
func drawChip(img *image.RGBA, h Hit, hover, focus bool, m metrics) {
	bg, fg := cChip, cFG
	switch {
	case h.Current:
		bg, fg = h.Color, color.RGBA{0xff, 0xff, 0xff, 255}
		if hover {
			bg = mixc(bg, color.RGBA{0xff, 0xff, 0xff, 255}, .12)
		}
	case hover:
		bg = mixc(cChip, cFG, .15)
	}
	fillRound(img, h.R, h.R.H/2, bg)
	if focus {
		frame(img, h.R, h.R.H/2, 2, cFG, bg)
	}
	x := h.R.X + m.chipPad
	ty := h.R.Y + (h.R.H-m.chipPx)/2 - 1
	if h.Icon != "" {
		drawIcon(img, h.Icon, Rect{x, h.R.Y + (h.R.H-m.chipPx)/2, m.chipPx, m.chipPx}, fg)
		x += m.chipPx + m.chipPad/2
	}
	drawText(img, Text{X: x, Y: ty, S: h.Label, Bold: true, Px: m.chipPx, Color: fg})
	if h.Sub != "" {
		x += textWidth(h.Label, true, m.chipPx) + m.chipPad/2
		drawText(img, Text{X: x, Y: ty, S: strings.TrimSpace(h.Sub), Px: m.chipPx, Color: fg})
	}
}
