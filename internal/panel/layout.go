// Package panel is the quick switcher: a small window with all hats and their
// activities, opened from the tray or a keyboard shortcut. Layout and drawing
// are plain Go (no toolkit); only the window itself is a backend (X11 so far).
package panel

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// Action is what a click does.
type Action struct {
	Request *agent.Request // send to the agent
	Open    bool           // open the web UI
	About   bool           // show the about screen
	Link    string         // open this address
	Quit    bool           // end the tray (used by the tray menu)
	Close   bool           // just close the panel
}

type Rect struct{ X, Y, W, H int }

func (r Rect) Contains(x, y int) bool { return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H }

// Kind tells how a hit is drawn.
type Kind int

const (
	KindHat Kind = iota
	KindActivity
	KindTool
)

// Hit is a clickable area.
type Hit struct {
	R        Rect
	Kind     Kind
	Label    string
	Hint     string
	Color    color.RGBA
	Current  bool // the hat or activity that is worn now
	Match    bool // matches the typed filter
	Action   Action
	Disabled bool
	Icon     string // "pause", "play", "stop", "open" or ""
	Card     Rect   // hats: the whole card, for the frame
}

// Box is a filled rounded rectangle behind other things.
type Box struct {
	R      Rect
	Radius int
	Color  color.RGBA
	Border color.RGBA
}

// Text is a line of text; X, Y is its top-left corner.
type Text struct {
	X, Y  int
	S     string
	Bold  bool
	Px    int
	Color color.RGBA
	Right bool // X is the right edge
}

// Layout is everything needed to draw one screen and to hit-test it.
type Layout struct {
	W, H     int
	Scale    float64
	Boxes    []Box
	Texts    []Text
	Hits     []Hit
	IconRect *Rect  // the app icon (about screen)
	IconHex  string // its colour
	Filter   string
	Default  string // hint when nothing is hovered
}

// Palette of the panel (dark, like the icon).
var (
	cBG     = color.RGBA{0x23, 0x26, 0x2b, 255}
	cBorder = color.RGBA{0x3d, 0x42, 0x4a, 255}
	cCard   = color.RGBA{0x2c, 0x30, 0x36, 255}
	cChip   = color.RGBA{0x3a, 0x3f, 0x47, 255}
	cFG     = color.RGBA{0xf0, 0xe6, 0xdc, 255}
	cDim    = color.RGBA{0x9a, 0x9f, 0xa8, 255}
)

func rgba(hex string) color.RGBA { return parse(hex) }

func parse(hex string) color.RGBA {
	var r, g, b uint8
	if _, err := fmt.Sscanf(strings.ToLower(hex), "#%02x%02x%02x", &r, &g, &b); err != nil {
		return color.RGBA{0x8a, 0x8f, 0x98, 255}
	}
	return color.RGBA{r, g, b, 255}
}

type metrics struct {
	s                                   float64
	pad, gap, cardW, titleH, dot        int
	toolW, toolBtnH                     int
	chipH, chipPad, chipGap, headerH    int
	toolH, hintH, titlePx, chipPx, txPx int
}

func newMetrics(s float64) metrics {
	u := func(n float64) int { return int(n*s + .5) }
	return metrics{
		s: s, pad: u(14), gap: u(10), cardW: u(176), titleH: u(30), dot: u(10), toolW: u(48), toolBtnH: u(36),
		chipH: u(26), chipPad: u(10), chipGap: u(6), headerH: u(44),
		toolH: u(52), hintH: u(30), titlePx: u(16), chipPx: u(13), txPx: u(14),
	}
}

func prefix(s, q string) bool {
	return q == "" || strings.HasPrefix(strings.ToLower(s), strings.ToLower(q))
}

func hatMatches(h agent.HatInfo, q string) bool {
	if prefix(h.Name, q) {
		return true
	}
	for _, a := range h.Activities {
		if prefix(a, q) {
			return true
		}
	}
	return false
}

// hatHint explains what a click on the hat does.
func hatHint(h agent.HatInfo, st core.State) string {
	switch {
	case st.Mode == core.ModePaused && st.Hat == h.Key:
		return i18n.T("panel.hint.resume", h.Name)
	case st.Mode == core.ModeWearing && st.Hat == h.Key:
		return i18n.T("panel.hint.already", h.Name)
	case h.Resume == core.ResumeLast && h.Last != "":
		return i18n.T("panel.hint.hat_last", h.Name, h.Last)
	}
	return i18n.T("panel.hint.hat", h.Name)
}

// Build lays out the main screen: status, hat cards, toolbar. query is the
// text typed to filter hats.
func Build(snap agent.Snapshot, query string, scale float64, now time.Time) Layout {
	m := newMetrics(scale)
	st := snap.Status.State
	var hats []agent.HatInfo
	for _, h := range snap.Hats {
		if hatMatches(h, query) {
			hats = append(hats, h)
		}
	}
	cols := min(max(len(hats), 1), 3)
	W := max(m.pad*2+cols*m.cardW+(cols-1)*m.gap, int(340*scale+.5))
	l := Layout{W: W, Scale: scale, Filter: query, Default: i18n.T("panel.hint.default")}

	// header: colour dot, status line
	hdrColor := rgba("#8a8f98")
	if st.Mode == core.ModeWearing {
		for _, h := range snap.Hats {
			if h.Key == st.Hat && h.Color != "" {
				hdrColor = rgba(h.Color)
			}
		}
		if snap.Status.Color != "" {
			hdrColor = rgba(snap.Status.Color)
		}
	}
	dot := m.s6() * 5 / 3
	l.Boxes = append(l.Boxes, Box{R: Rect{m.pad, (m.headerH - dot) / 2, dot, dot}, Radius: dot / 2, Color: hdrColor})
	line := agent.StatusLine(snap.Status, now)
	l.Texts = append(l.Texts, Text{X: m.pad + dot + m.gap, Y: (m.headerH - m.txPx) / 2, S: fit(line, true, m.txPx, W-2*m.pad-dot-m.gap), Bold: true, Px: m.txPx, Color: cFG})
	y := m.headerH
	l.Boxes = append(l.Boxes, Box{R: Rect{0, y - 1, W, 1}, Color: cBorder})

	// hat cards in a grid
	y += m.pad
	if len(hats) == 0 {
		msg := i18n.T("err.nohats")
		if query != "" {
			msg = i18n.T("panel.nomatch", query)
		}
		l.Texts = append(l.Texts, Text{X: m.pad, Y: y, S: fit(msg, false, m.txPx, W-2*m.pad), Px: m.txPx, Color: cDim})
		y += m.txPx + m.pad
	}
	for row := 0; row*cols < len(hats); row++ {
		rowHats := hats[row*cols : min((row+1)*cols, len(hats))]
		// first pass: heights
		heights := make([]int, len(rowHats))
		chipRows := make([][][]string, len(rowHats))
		rowH := 0
		for i, h := range rowHats {
			chipRows[i] = flow(h.Activities, m, m.cardW-2*m.chipPad)
			heights[i] = m.titleH + len(chipRows[i])*(m.chipH+m.chipGap) + m.chipPad
			rowH = max(rowH, heights[i])
		}
		for i, h := range rowHats {
			x := m.pad + i*(m.cardW+m.gap)
			current := st.Mode != core.ModeNone && st.Hat == h.Key
			col := rgba(h.Color)
			if h.Color == "" {
				col = rgba("#4e79a7")
			}
			card := Rect{x, y, m.cardW, rowH}
			l.Boxes = append(l.Boxes, Box{R: card, Radius: m.s6(), Color: cCard})
			// title
			titleHit := Hit{R: Rect{x, y, m.cardW, m.titleH}, Kind: KindHat, Label: h.Name, Color: col, Card: card, Current: current,
				Match: prefix(h.Name, query), Hint: hatHint(h, st),
				Action: Action{Request: &agent.Request{Cmd: "on", Hat: h.Key}}}
			if st.Mode == core.ModePaused && st.Hat == h.Key {
				titleHit.Action = Action{Request: &agent.Request{Cmd: "on"}}
			}
			l.Hits = append(l.Hits, titleHit)
			// chips
			cy := y + m.titleH
			for _, rowActs := range chipRows[i] {
				cx := x + m.chipPad
				for _, a := range rowActs {
					cur := current && st.Mode == core.ModeWearing && st.Activity == a
					w := textWidth(a, cur, m.chipPx) + 2*m.chipPad // the worn chip is bold
					w = min(w, m.cardW-2*m.chipPad)
					hint := i18n.T("panel.hint.act", h.Name, a)
					if current && st.Mode == core.ModeWearing {
						hint = i18n.T("panel.hint.act_here", a)
					}
					l.Hits = append(l.Hits, Hit{R: Rect{cx, cy, w, m.chipH}, Kind: KindActivity, Label: a, Color: col, Current: cur,
						Match: query != "" && prefix(a, query) && !prefix(h.Name, query), Hint: hint,
						Action: Action{Request: &agent.Request{Cmd: "on", Hat: h.Key, Activity: a, HasActivity: true}}})
					cx += w + m.chipGap
				}
				cy += m.chipH + m.chipGap
			}
		}
		y += rowH + m.gap
	}
	y += m.pad - m.gap

	// toolbar
	l.Boxes = append(l.Boxes, Box{R: Rect{0, y, W, 1}, Color: cBorder})
	ty := y + (m.toolH-m.toolBtnH)/2
	tx := m.pad
	tool := func(label, icon, hint string, a Action) {
		l.Hits = append(l.Hits, Hit{R: Rect{tx, ty, m.toolW, m.toolBtnH}, Kind: KindTool, Label: label, Icon: icon, Hint: hint, Action: a, Color: cFG})
		tx += m.toolW + m.chipGap
	}
	switch st.Mode {
	case core.ModeWearing:
		tool(i18n.T("ui.pause"), "pause", i18n.T("panel.hint.pause"), Action{Request: &agent.Request{Cmd: "off"}})
		tool(i18n.T("panel.takeoff"), "stop", i18n.T("panel.hint.takeoff"), Action{Request: &agent.Request{Cmd: "off", Neutral: true}})
	case core.ModePaused:
		tool(i18n.T("ui.resume"), "play", i18n.T("panel.hint.resume_tool"), Action{Request: &agent.Request{Cmd: "on"}})
		if !st.Neutral {
			tool(i18n.T("panel.takeoff"), "stop", i18n.T("panel.hint.takeoff"), Action{Request: &agent.Request{Cmd: "off", Neutral: true}})
		}
	}
	// the web UI sits at the right edge; About lives in the tray's right-click menu
	if snap.Server != "" {
		l.Hits = append(l.Hits, Hit{R: Rect{W - m.pad - m.toolW, ty, m.toolW, m.toolBtnH}, Kind: KindTool, Label: i18n.T("tray.open"), Icon: "open",
			Hint: i18n.T("panel.hint.open"), Action: Action{Open: true}, Color: cFG})
	}
	y += m.toolH

	// hint line
	l.Boxes = append(l.Boxes, Box{R: Rect{0, y, W, 1}, Color: cBorder})
	y += m.hintH
	l.H = y
	return l
}

func (m metrics) s6() int { return int(6*m.s + .5) }

// flow breaks activities into rows of chips that fit within width.
func flow(acts []string, m metrics, width int) [][]string {
	var rows [][]string
	var cur []string
	x := 0
	for _, a := range acts {
		w := min(textWidth(a, true, m.chipPx)+2*m.chipPad, width) // room for bold
		if len(cur) > 0 && x+w > width {
			rows = append(rows, cur)
			cur, x = nil, 0
		}
		cur = append(cur, a)
		x += w + m.chipGap
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}
	return rows
}

// ---- interaction ---------------------------------------------------------------

// At returns the index of the hit under the point, or -1.
func (l Layout) At(x, y int) int {
	for i, h := range l.Hits {
		if !h.Disabled && h.R.Contains(x, y) {
			return i
		}
	}
	return -1
}

// FirstFocus picks the hit to focus first: with a filter the first match,
// otherwise the current hat, otherwise the first hat.
func (l Layout) FirstFocus() int {
	if l.Filter != "" {
		for i, h := range l.Hits {
			if h.Match && !h.Disabled {
				return i
			}
		}
	}
	for i, h := range l.Hits {
		if h.Kind == KindHat && h.Current {
			return i
		}
	}
	for i, h := range l.Hits {
		if h.Kind == KindHat {
			return i
		}
	}
	if len(l.Hits) > 0 {
		return 0
	}
	return -1
}

// Step moves the focus by dir (+1 or -1) over the enabled hits, wrapping.
func (l Layout) Step(cur, dir int) int {
	n := len(l.Hits)
	if n == 0 {
		return -1
	}
	i := cur
	for k := 0; k < n; k++ {
		i = (i + dir + n) % n
		if !l.Hits[i].Disabled {
			return i
		}
	}
	return cur
}

// HintFor returns the hint for the hovered or focused hit.
func (l Layout) HintFor(hover, focus int) string {
	for _, i := range []int{hover, focus} {
		if i >= 0 && i < len(l.Hits) && l.Hits[i].Hint != "" {
			return l.Hits[i].Hint
		}
	}
	return l.Default
}

// BuildAbout lays out the about screen.
func BuildAbout(version, url string, scale float64) Layout {
	m := newMetrics(scale)
	u := func(n float64) int { return int(n*scale + .5) }
	W := u(340)
	l := Layout{W: W, Scale: scale, Default: ""}
	iconSize := u(72)
	l.IconRect = &Rect{(W - iconSize) / 2, m.pad * 2, iconSize, iconSize}
	l.IconHex = "#2fa7e0"
	y := m.pad*2 + iconSize + m.gap*2
	center := func(s string, bold bool, px int, c color.RGBA) {
		s = fit(s, bold, px, W-2*m.pad)
		w := textWidth(s, bold, px)
		l.Texts = append(l.Texts, Text{X: (W - w) / 2, Y: y, S: s, Bold: bold, Px: px, Color: c})
		y += px + m.gap
	}
	center("HatRack", true, u(22), cFG)
	center(i18n.T("panel.about.version", version), false, m.txPx, cDim)
	y += m.gap / 2
	center(i18n.T("cli.short"), false, m.chipPx, cFG)
	center(i18n.T("panel.about.license"), false, m.chipPx, cDim)
	y += m.gap
	bw := textWidth(i18n.T("panel.about.project"), false, m.chipPx) + 4*m.chipPad
	l.Hits = append(l.Hits, Hit{R: Rect{(W - bw) / 2, y, bw, m.chipH + u(4)}, Kind: KindTool, Label: i18n.T("panel.about.project"),
		Hint: url, Action: Action{Link: url}, Color: cFG})
	y += m.chipH + u(4) + m.pad*2
	l.H = y
	return l
}
