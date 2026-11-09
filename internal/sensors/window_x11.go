package sensors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"hatrack/internal/core"
)

// ---- window: x11 ---------------------------------------------------------

// x11WindowBackend reports the focused window from the window manager's
// _NET_ACTIVE_WINDOW property, driven by property change events.
type x11WindowBackend struct {
	now func() time.Time // time.Now if nil
}

func (*x11WindowBackend) Name() string { return "x11" }

func (b *x11WindowBackend) Detect(ctx context.Context) (bool, string) {
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		return false, "wayland session (XWayland sees only X11 windows)"
	}
	if os.Getenv("DISPLAY") == "" {
		return false, "no X display"
	}
	c, err := xgb.NewConn()
	if err != nil {
		return false, "no X display: " + err.Error()
	}
	defer c.Close()
	root := xproto.Setup(c).DefaultScreen(c).Root
	active, err := internAtom(c, "_NET_ACTIVE_WINDOW")
	if err != nil {
		return false, "no X display: " + err.Error()
	}
	supported, err := internAtom(c, "_NET_SUPPORTED")
	if err != nil {
		return false, "no X display: " + err.Error()
	}
	if p, err := xproto.GetProperty(c, false, root, supported, xproto.AtomAtom, 0, 1<<16).Reply(); err == nil && p != nil {
		for _, a := range uint32s(p.Value) {
			if xproto.Atom(a) == active {
				return true, ""
			}
		}
	}
	return false, "window manager does not report the active window"
}

func internAtom(c *xgb.Conn, name string) (xproto.Atom, error) {
	r, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return r.Atom, nil
}

// uint32s decodes a format-32 property value.
func uint32s(b []byte) []uint32 {
	out := make([]uint32, 0, len(b)/4)
	for ; len(b) >= 4; b = b[4:] {
		out = append(out, xgb.Get32(b))
	}
	return out
}

// windowWatch is the state of one Run.
type windowWatch struct {
	c    *xgb.Conn
	root xproto.Window
	// active is the window whose app and title are reported and watched:
	// the focused window, or the window it is transient for (see ownerOf).
	// 0 for none.
	active xproto.Window

	netActive, netWMName, utf8String, netWMPid xproto.Atom
}

// ownerOf returns the window that a focused window n is attributed to. A
// transient window (a progress or other dialog of an application) belongs to
// the window it is transient for, so its changing title does not split the
// time of that window. Without a parent n stays. Chains are followed up to a
// fixed depth; the root window and cycles end the walk.
func ownerOf(n, root xproto.Window, transientFor func(xproto.Window) xproto.Window) xproto.Window {
	for i := 0; i < 8 && n != 0 && n != root; i++ {
		p := transientFor(n)
		if p == 0 || p == root || p == n {
			return n
		}
		n = p
	}
	return n
}

// transientFor reads WM_TRANSIENT_FOR of a window; 0 if it has none.
func (w *windowWatch) transientFor(n xproto.Window) xproto.Window {
	p, err := xproto.GetProperty(w.c, false, n, xproto.AtomWmTransientFor, xproto.AtomWindow, 0, 1).Reply()
	if err != nil || p == nil || p.Format != 32 || len(p.Value) < 4 {
		return 0
	}
	return xproto.Window(xgb.Get32(p.Value))
}

func (b *x11WindowBackend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	c, err := xgb.NewConn()
	if err != nil {
		return fmt.Errorf("x11: %w", err)
	}
	// Closing the connection wakes up WaitForEvent.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	defer c.Close()

	w := &windowWatch{c: c, root: xproto.Setup(c).DefaultScreen(c).Root}
	for _, a := range []struct {
		dst  *xproto.Atom
		name string
	}{{&w.netActive, "_NET_ACTIVE_WINDOW"}, {&w.netWMName, "_NET_WM_NAME"}, {&w.utf8String, "UTF8_STRING"}, {&w.netWMPid, "_NET_WM_PID"}} {
		if *a.dst, err = internAtom(c, a.name); err != nil {
			return lostOr(ctx, err)
		}
	}
	now := b.now
	if now == nil {
		now = time.Now
	}

	if err := xproto.ChangeWindowAttributesChecked(c, w.root, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return lostOr(ctx, err)
	}
	var last core.Attrs
	first := true
	// refresh re-reads the state and emits it if it changed.
	refresh := func() {
		a := w.attrs()
		if !first && equalAttrs(a, last) {
			return
		}
		first, last = false, a
		emit(now(), a)
	}
	w.setActive(w.readActive())
	refresh()

	for {
		ev, err := c.WaitForEvent()
		if ev == nil && err == nil {
			return lostOr(ctx, errors.New("x11: connection lost"))
		}
		if ev == nil {
			continue // BadWindow and friends: the window vanished
		}
		pn, ok := ev.(xproto.PropertyNotifyEvent)
		if !ok {
			continue
		}
		switch {
		case pn.Window == w.root && pn.Atom == w.netActive:
			w.setActive(w.readActive())
			refresh()
		case pn.Window == w.active && w.active != 0 &&
			(pn.Atom == w.netWMName || pn.Atom == xproto.AtomWmName || pn.Atom == xproto.AtomWmClass):
			refresh()
		}
	}
}

// lostOr returns nil if ctx is done, else err.
func lostOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// readActive returns the active window, 0 for none.
func (w *windowWatch) readActive() xproto.Window {
	p, err := xproto.GetProperty(w.c, false, w.root, w.netActive, xproto.AtomWindow, 0, 1).Reply()
	if err != nil || p == nil || p.Format != 32 || len(p.Value) < 4 {
		return 0
	}
	return xproto.Window(xgb.Get32(p.Value))
}

// setActive moves the PropertyChange selection to the window the new active
// window is attributed to (see ownerOf).
func (w *windowWatch) setActive(n xproto.Window) {
	if n == w.root {
		n = 0 // unselecting on root would stop the _NET_ACTIVE_WINDOW events
	}
	n = ownerOf(n, w.root, w.transientFor)
	if n == w.active {
		return
	}
	if w.active != 0 {
		// May be gone already; errors are ignored.
		xproto.ChangeWindowAttributes(w.c, w.active, xproto.CwEventMask, []uint32{0})
	}
	w.active = n
	if n != 0 {
		xproto.ChangeWindowAttributes(w.c, n, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
	}
}

// attrs reads app and title of the active window; nil if there is nothing
// useful.
func (w *windowWatch) attrs() core.Attrs {
	if w.active == 0 {
		return nil
	}
	get := func(atom xproto.Atom) *xproto.GetPropertyReply {
		p, err := xproto.GetProperty(w.c, false, w.active, atom, xproto.GetPropertyTypeAny, 0, 1<<16).Reply()
		if err != nil || p == nil || p.ValueLen == 0 {
			return nil
		}
		return p
	}
	a := core.Attrs{}
	if p := get(xproto.AtomWmClass); p != nil {
		if app := parseWMClass(p.Value); app != "" {
			a["app"] = app
		}
	}
	title := ""
	if p := get(w.netWMName); p != nil {
		title = decodeTitle(false, p.Value)
	}
	if title == "" {
		if p := get(xproto.AtomWmName); p != nil {
			title = decodeTitle(p.Type == xproto.AtomString, p.Value)
		}
	}
	if title != "" {
		a["title"] = title
	}
	if len(a) == 0 {
		return nil
	}
	// the process, for the workdir reader; the agent removes it before
	// anything is recorded
	if p := get(w.netWMPid); p != nil && p.Format == 32 && len(p.Value) >= 4 {
		if pid := xgb.Get32(p.Value); pid > 0 {
			a[AttrPID] = strconv.FormatUint(uint64(pid), 10)
		}
	}
	return a
}

// parseWMClass returns the class part of a WM_CLASS value
// ("instance\0class\0"); with only one string, that one.
func parseWMClass(b []byte) string {
	parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	if len(parts) >= 2 {
		return parts[1]
	}
	return parts[0]
}

// decodeTitle turns a window name into UTF-8. latin1 is set for properties of
// type STRING; any other type is used as is if it is valid UTF-8, else dropped.
// Trailing NULs are trimmed.
func decodeTitle(latin1 bool, b []byte) string {
	b = []byte(strings.TrimRight(string(b), "\x00"))
	if latin1 {
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return string(r)
	}
	if !utf8.Valid(b) {
		return ""
	}
	return string(b)
}
