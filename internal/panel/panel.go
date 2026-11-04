package panel

import (
	"context"
	"errors"
	"time"

	"hatrack/internal/agent"
)

// ErrNoDisplay means there is no usable window system; callers fall back to
// opening the web UI.
var ErrNoDisplay = errors.New("no display available for the panel")

// Where the panel appears.
type Where int

const (
	// AtPointer puts the panel next to the mouse pointer (tray click).
	AtPointer Where = iota
	// Centered puts it in the middle of the monitor with the pointer
	// (keyboard shortcut).
	Centered
)

// Options of one panel run.
type Options struct {
	Snapshot   func() agent.Snapshot // current data, called once per screen
	Where      Where
	About      bool // start with the about screen
	Version    string
	ProjectURL string
}

// Run shows the panel and blocks until it closes. It returns the action the
// user chose; the zero Action means it was dismissed. The caller performs the
// action after the window is gone.
func Run(ctx context.Context, o Options) (Action, error) {
	return runX11(ctx, o)
}

// screen holds what the event loop needs to rebuild a layout.
type screen struct {
	o      Options
	snap   agent.Snapshot
	about  bool
	filter string
	scale  float64
	l      Layout
	hover  int
	focus  int
	edit   Edit // the detail field being typed into
}

func (s *screen) rebuild() {
	if s.about {
		s.l = BuildAbout(s.o.Version, s.o.ProjectURL, s.scale)
		s.focus = -1
		if len(s.l.Hits) > 0 {
			s.focus = 0
		}
	} else {
		if s.edit.Hat == "" {
			s.edit.Sel = -1
		}
		s.l = BuildEdit(s.snap, s.filter, s.edit, s.scale, time.Now())
		s.focus = s.l.FirstFocus()
		if s.edit.Hat != "" {
			s.focus = s.l.Field(s.edit.Hat)
		}
	}
	s.hover = -1
}

func (s *screen) view() View { return View{Hover: s.hover, Focus: s.focus, Filter: s.filter} }

// activate returns done=true when the panel should close with act.
func (s *screen) activate(i int) (act Action, done bool) {
	if i < 0 || i >= len(s.l.Hits) {
		if s.edit.Hat != "" { // a click beside the field leaves it
			s.stopEdit()
		}
		return Action{}, false
	}
	a := s.l.Hits[i].Action
	if a.Edit != "" {
		if s.edit.Hat != a.Edit {
			s.startEdit(a.Edit, "")
		}
		return Action{}, false
	}
	if a.About {
		s.about = true
		s.rebuild()
		return Action{}, false
	}
	return a, true
}

func (s *screen) startEdit(hat, text string) {
	s.edit = Edit{Hat: hat, Text: text, Sel: -1}
	s.rebuild()
}

func (s *screen) stopEdit() {
	hat := s.edit.Hat
	s.edit = Edit{Sel: -1}
	s.rebuild()
	if i := s.l.Field(hat); i >= 0 {
		s.focus = i
	}
}

// editKey handles a key while the detail field has the focus.
func (s *screen) editKey(name string, r rune) (act Action, done bool) {
	switch name {
	case "escape", "next", "prev":
		s.stopEdit()
	case "enter":
		if s.edit.Sel >= 0 {
			if i := s.l.Suggest(s.edit.Sel); i >= 0 {
				return s.l.Hits[i].Action, true
			}
		}
		if s.l.Commit.Request != nil {
			return s.l.Commit, true
		}
		s.stopEdit()
	case "down":
		if s.edit.Sel+1 < s.l.SuggestCount() {
			s.edit.Sel++
			s.rebuild()
		}
	case "up":
		if s.edit.Sel >= 0 {
			s.edit.Sel--
			s.rebuild()
		}
	case "backspace":
		if rs := []rune(s.edit.Text); len(rs) > 0 {
			s.edit.Text, s.edit.Sel = string(rs[:len(rs)-1]), -1
			s.rebuild()
		}
	case "":
		if r >= ' ' {
			s.edit.Text, s.edit.Sel = s.edit.Text+string(r), -1
			s.rebuild()
		}
	}
	return Action{}, false
}

// key handles a non-printing key or printable rune. It returns done and the
// action when the panel should close, and relayout when the size may change.
func (s *screen) key(name string, r rune) (act Action, done bool) {
	if s.edit.Hat != "" && !s.about {
		return s.editKey(name, r)
	}
	var focused *Hit
	if s.focus >= 0 && s.focus < len(s.l.Hits) && !s.about {
		focused = &s.l.Hits[s.focus]
	}
	switch {
	case name == "next" && focused != nil && focused.Hat != "":
		// Tab goes to the detail field of the focused card
		s.startEdit(focused.Hat, "")
		return Action{}, false
	case name == "" && r > ' ' && focused != nil && focused.Kind == KindInput:
		s.startEdit(focused.Hat, string(r))
		return Action{}, false
	}
	switch name {
	case "escape":
		return Action{}, true
	case "enter":
		return s.activate(s.focus)
	case "next", "right", "down":
		s.focus = s.l.Step(s.focus, +1)
	case "prev", "left", "up":
		s.focus = s.l.Step(s.focus, -1)
	case "backspace":
		if !s.about && s.filter != "" {
			rs := []rune(s.filter)
			s.filter = string(rs[:len(rs)-1])
			s.rebuild()
		}
	default:
		if r >= ' ' && !s.about {
			s.filter += string(r)
			s.rebuild()
		}
	}
	return Action{}, false
}
