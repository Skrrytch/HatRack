package panel

import (
	"context"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
)

// x11 is the X11 window backend of the panel: one override-redirect window
// that grabs keyboard and pointer, so a click outside or Escape closes it.

type xwin struct {
	c         *xgb.Conn
	root      xproto.Window
	win       xproto.Window
	gc        xproto.Gcontext
	depth     byte
	maxReq    int
	keymap    []xproto.Keysym
	perKey    int
	minKey    int
	w, h      int
	x, y      int
	monitor   [4]int // x, y, w, h of the monitor with the pointer
	pasteProp xproto.Atom
}

func runX11(ctx context.Context, o Options) (Action, error) {
	if os.Getenv("DISPLAY") == "" {
		return Action{}, ErrNoDisplay
	}
	c, err := xgb.NewConn()
	if err != nil {
		return Action{}, fmt.Errorf("%w: %v", ErrNoDisplay, err)
	}
	defer c.Close()
	xw := &xwin{c: c}
	if err := xw.setup(); err != nil {
		return Action{}, err
	}

	s := &screen{o: o, snap: o.Snapshot(), about: o.About, scale: xw.scale()}
	s.rebuild()

	px, py := xw.pointer()
	xw.findMonitor(px, py)
	xw.place(s.l.W, s.l.H, o.Where, px, py)
	if err := xw.create(s.l.W, s.l.H); err != nil {
		return Action{}, err
	}
	defer xproto.DestroyWindow(c, xw.win)
	if err := xw.grab(); err != nil {
		return Action{}, err
	}
	defer xproto.UngrabKeyboard(c, xproto.TimeCurrentTime)
	defer xproto.UngrabPointer(c, xproto.TimeCurrentTime)

	events := make(chan xgb.Event)
	done := make(chan struct{})
	defer close(done) // the reader stops even if an event comes after the loop
	go func() {
		defer close(events)
		for {
			ev, xerr := c.WaitForEvent()
			if ev == nil && xerr == nil {
				return
			}
			if ev != nil {
				select {
				case events <- ev:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()

	paint := func() { xw.paint(Render(s.l, s.view())) }
	paint()
	for {
		select {
		case <-ctx.Done():
			return Action{}, nil
		case ev, ok := <-events:
			if !ok {
				return Action{}, nil
			}
			relayout := false
			switch e := ev.(type) {
			case xproto.ExposeEvent:
				paint()
				continue
			case xproto.MotionNotifyEvent:
				i := s.l.At(int(e.EventX), int(e.EventY))
				if e.Event != xw.win {
					i = -1
				}
				if i != s.hover {
					s.hover = i
					paint()
				}
				continue
			case xproto.ButtonPressEvent:
				inside := e.Event == xw.win && int(e.EventX) >= 0 && int(e.EventY) >= 0 &&
					int(e.EventX) < xw.w && int(e.EventY) < xw.h
				if !inside {
					return Action{}, nil
				}
				if e.Detail != 1 {
					continue
				}
				wasW, wasH := s.l.W, s.l.H
				act, done := s.activate(s.l.At(int(e.EventX), int(e.EventY)))
				if done {
					return act, nil
				}
				relayout = s.l.W != wasW || s.l.H != wasH
			case xproto.SelectionNotifyEvent:
				wasW, wasH := s.l.W, s.l.H
				s.insert(xw.paste(e))
				relayout = s.l.W != wasW || s.l.H != wasH
			case xproto.KeyPressEvent:
				name, r := xw.decode(e.Detail, e.State)
				if name == "paste" {
					xw.requestPaste()
					continue
				}
				wasW, wasH := s.l.W, s.l.H
				act, done := s.key(name, r)
				if done {
					return act, nil
				}
				relayout = s.l.W != wasW || s.l.H != wasH
			default:
				continue
			}
			if relayout {
				xw.resize(s.l.W, s.l.H, o.Where, px, py)
			}
			paint()
		}
	}
}

func (x *xwin) setup() error {
	setup := xproto.Setup(x.c)
	scr := setup.DefaultScreen(x.c)
	x.root, x.depth = scr.Root, scr.RootDepth
	x.maxReq = int(setup.MaximumRequestLength) * 4
	x.monitor = [4]int{0, 0, int(scr.WidthInPixels), int(scr.HeightInPixels)}
	x.minKey = int(setup.MinKeycode)
	n := int(setup.MaxKeycode) - x.minKey + 1
	km, err := xproto.GetKeyboardMapping(x.c, setup.MinKeycode, byte(n)).Reply()
	if err != nil {
		return err
	}
	x.keymap, x.perKey = km.Keysyms, int(km.KeysymsPerKeycode)
	return nil
}

// scale reads Xft.dpi from the root window, as most toolkits do.
func (x *xwin) scale() float64 {
	r, err := xproto.GetProperty(x.c, false, x.root, xproto.AtomResourceManager, xproto.AtomString, 0, 16384).Reply()
	if err != nil || r == nil {
		return 1
	}
	for _, line := range strings.Split(string(r.Value), "\n") {
		if v, ok := strings.CutPrefix(line, "Xft.dpi:"); ok {
			if dpi, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && dpi >= 96 {
				return min(dpi/96, 3)
			}
		}
	}
	return 1
}

func (x *xwin) pointer() (int, int) {
	r, err := xproto.QueryPointer(x.c, x.root).Reply()
	if err != nil {
		return 0, 0
	}
	return int(r.RootX), int(r.RootY)
}

func (x *xwin) findMonitor(px, py int) {
	if err := randr.Init(x.c); err != nil {
		return
	}
	r, err := randr.GetMonitors(x.c, x.root, true).Reply()
	if err != nil {
		return
	}
	for _, m := range r.Monitors {
		mx, my, mw, mh := int(m.X), int(m.Y), int(m.Width), int(m.Height)
		if px >= mx && px < mx+mw && py >= my && py < my+mh {
			x.monitor = [4]int{mx, my, mw, mh}
			return
		}
	}
}

func (x *xwin) place(w, h int, where Where, px, py int) {
	mx, my, mw, mh := x.monitor[0], x.monitor[1], x.monitor[2], x.monitor[3]
	x.w, x.h = w, h
	if where == Centered {
		x.x, x.y = mx+(mw-w)/2, my+(mh-h)/2-mh/10
	} else {
		x.x = px - w/2
		if py < my+mh/2 {
			x.y = py + 10
		} else {
			x.y = py - h - 10
		}
	}
	x.x = max(mx, min(x.x, mx+mw-w))
	x.y = max(my, min(x.y, my+mh-h))
}

func (x *xwin) create(w, h int) error {
	wid, err := xproto.NewWindowId(x.c)
	if err != nil {
		return err
	}
	x.win = wid
	mask := uint32(xproto.CwBackPixel | xproto.CwOverrideRedirect | xproto.CwEventMask)
	vals := []uint32{0x17191c, 1, xproto.EventMaskExposure | xproto.EventMaskPointerMotion |
		xproto.EventMaskButtonPress | xproto.EventMaskKeyPress}
	scr := xproto.Setup(x.c).DefaultScreen(x.c)
	if err := xproto.CreateWindowChecked(x.c, x.depth, wid, x.root, int16(x.x), int16(x.y), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, scr.RootVisual, mask, vals).Check(); err != nil {
		return err
	}
	x.gc, err = xproto.NewGcontextId(x.c)
	if err != nil {
		return err
	}
	if err := xproto.CreateGCChecked(x.c, x.gc, xproto.Drawable(wid), 0, nil).Check(); err != nil {
		return err
	}
	return xproto.MapWindowChecked(x.c, wid).Check()
}

// grab takes keyboard and pointer. A click on a tray icon is often still
// being handled by the desktop's panel, which holds a grab for a moment, so
// a refused grab is retried for about a second.
func (x *xwin) grab() error {
	var err error
	for i := 0; i < 50; i++ {
		if err = x.tryGrab(); err == nil {
			return nil
		}
		xproto.UngrabKeyboard(x.c, xproto.TimeCurrentTime)
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

func (x *xwin) tryGrab() error {
	kr, err := xproto.GrabKeyboard(x.c, true, x.win, xproto.TimeCurrentTime, xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
	if err != nil {
		return err
	}
	if kr.Status != xproto.GrabStatusSuccess {
		return fmt.Errorf("cannot grab the keyboard (status %d)", kr.Status)
	}
	em := uint16(xproto.EventMaskButtonPress | xproto.EventMaskPointerMotion)
	pr, err := xproto.GrabPointer(x.c, true, x.win, em, xproto.GrabModeAsync, xproto.GrabModeAsync, xproto.WindowNone, xproto.CursorNone, xproto.TimeCurrentTime).Reply()
	if err != nil {
		return err
	}
	if pr.Status != xproto.GrabStatusSuccess {
		return fmt.Errorf("cannot grab the pointer (status %d)", pr.Status)
	}
	return nil
}

func (x *xwin) resize(w, h int, where Where, px, py int) {
	x.place(w, h, where, px, py)
	xproto.ConfigureWindow(x.c, x.win, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{uint32(int32(x.x)), uint32(int32(x.y)), uint32(w), uint32(h)})
}

// paint sends the image in strips that fit into one request.
func (x *xwin) paint(img *image.RGBA) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rows := max((x.maxReq-64)/(w*4), 1)
	for y0 := 0; y0 < h; y0 += rows {
		n := min(rows, h-y0)
		buf := make([]byte, w*n*4)
		for r := 0; r < n; r++ {
			src := img.Pix[(y0+r)*img.Stride : (y0+r)*img.Stride+w*4]
			dst := buf[r*w*4:]
			for i := 0; i < w; i++ {
				dst[i*4], dst[i*4+1], dst[i*4+2], dst[i*4+3] = src[i*4+2], src[i*4+1], src[i*4], 0
			}
		}
		xproto.PutImage(x.c, xproto.ImageFormatZPixmap, xproto.Drawable(x.win), x.gc,
			uint16(w), uint16(n), 0, int16(y0), 0, x.depth, buf)
	}
	x.c.Sync()
}

func isLetter(sym xproto.Keysym) bool {
	return sym >= 'a' && sym <= 'z' || sym >= 'A' && sym <= 'Z' || sym >= 0xc0 && sym <= 0xfe && sym != 0xd7 && sym != 0xf7
}

// requestPaste asks the owner of the clipboard for its text; it arrives as
// a SelectionNotify event (see paste).
func (x *xwin) requestPaste() {
	clip, err1 := xproto.InternAtom(x.c, false, uint16(len("CLIPBOARD")), "CLIPBOARD").Reply()
	utf8, err2 := xproto.InternAtom(x.c, false, uint16(len("UTF8_STRING")), "UTF8_STRING").Reply()
	prop, err3 := xproto.InternAtom(x.c, false, uint16(len("HATRACK_PASTE")), "HATRACK_PASTE").Reply()
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	x.pasteProp = prop.Atom
	xproto.ConvertSelection(x.c, x.win, clip.Atom, utf8.Atom, prop.Atom, xproto.TimeCurrentTime)
}

// paste reads the text the clipboard owner put on our window.
func (x *xwin) paste(e xproto.SelectionNotifyEvent) string {
	if e.Property == xproto.AtomNone || e.Property != x.pasteProp {
		return ""
	}
	r, err := xproto.GetProperty(x.c, true, x.win, e.Property, xproto.GetPropertyTypeAny, 0, 4096).Reply()
	if err != nil || r.Format != 8 {
		return ""
	}
	return string(r.Value)
}

// decode turns a key press into a name for special keys or a rune.
func (x *xwin) decode(code xproto.Keycode, state uint16) (string, rune) {
	idx := (int(code) - x.minKey) * x.perKey
	if idx < 0 || idx+1 >= len(x.keymap) || x.perKey < 1 {
		return "", 0
	}
	shift := state&xproto.ModMaskShift != 0
	caps := state&xproto.ModMaskLock != 0
	ctrl := state&xproto.ModMaskControl != 0
	alt := state&xproto.ModMask1 != 0
	altgr := state&xproto.ModMask5 != 0 // ISO_Level3_Shift on most layouts
	sym := x.keymap[idx]
	base := sym
	level := 0
	if altgr && x.perKey > 4 && x.keymap[idx+4] != 0 {
		level = 4 // group 1, level 3 (and 4 with shift) in the core keymap
	}
	if shift && x.perKey > level+1 && x.keymap[idx+level+1] != 0 {
		sym = x.keymap[idx+level+1]
	} else {
		sym = x.keymap[idx+level]
	}
	// Caps Lock only changes letters
	if caps && !altgr && x.perKey > 1 && isLetter(base) {
		if shift {
			sym = base
		} else if x.keymap[idx+1] != 0 {
			sym = x.keymap[idx+1]
		}
	}
	if ctrl || alt {
		switch {
		case ctrl && (base == 'v' || base == 'V'):
			return "paste", 0
		case ctrl && sym == 0xff08:
			return "delword", 0
		}
		return "", 0 // shortcuts type nothing
	}
	if sym >= 0xffb0 && sym <= 0xffb9 { // keypad digits
		return "", rune('0' + sym - 0xffb0)
	}
	switch sym {
	case 0xffae:
		return "", '.'
	case 0xffac:
		return "", ','
	}
	switch sym {
	case 0xff1b:
		return "escape", 0
	case 0xff0d, 0xff8d:
		return "enter", 0
	case 0xff09:
		if shift {
			return "prev", 0
		}
		return "next", 0
	case 0xfe20:
		return "prev", 0
	case 0xff08:
		return "backspace", 0
	case 0xff51:
		return "left", 0
	case 0xff52:
		return "up", 0
	case 0xff53:
		return "right", 0
	case 0xff54:
		return "down", 0
	}
	switch {
	case sym >= 0x20 && sym <= 0x7e, sym >= 0xa0 && sym <= 0xff:
		return "", rune(sym)
	case sym >= 0x01000100 && sym <= 0x0110ffff:
		return "", rune(sym - 0x01000000)
	}
	return "", 0
}
