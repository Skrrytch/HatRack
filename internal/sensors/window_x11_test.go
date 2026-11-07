package sensors

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"hatrack/internal/core"
)

func TestParseWMClass(t *testing.T) {
	for in, want := range map[string]string{"code\x00Code\x00": "Code", "xterm\x00": "xterm", "": ""} {
		if got := parseWMClass([]byte(in)); got != want {
			t.Errorf("parseWMClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeTitle(t *testing.T) {
	for _, c := range []struct {
		latin1 bool
		in     string
		want   string
	}{
		{true, "M\xfcller", "Müller"},
		{false, "Müller", "Müller"},
		{false, "M\xfcller", ""},
		{false, "abc\x00\x00", "abc"},
		{true, "abc\x00", "abc"},
	} {
		if got := decodeTitle(c.latin1, []byte(c.in)); got != c.want {
			t.Errorf("decodeTitle(%v, %q) = %q, want %q", c.latin1, c.in, got, c.want)
		}
	}
}

func TestWindowDetectEnv(t *testing.T) {
	b := &x11WindowBackend{}
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	if ok, why := b.Detect(context.Background()); ok || why == "" {
		t.Fatalf("wayland: %v %q", ok, why)
	}
	t.Setenv("XDG_SESSION_TYPE", "x11")
	t.Setenv("DISPLAY", "")
	if ok, why := b.Detect(context.Background()); ok || why != "no X display" {
		t.Fatalf("no display: %v %q", ok, why)
	}
}

// startXvfb starts Xvfb on a free display and waits until it accepts
// connections. kill stops it.
func startXvfb(t *testing.T) (kill func()) {
	t.Helper()
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb not installed")
	}
	for n := 100; n < 200; n++ {
		if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n)); err == nil {
			continue
		}
		if _, err := os.Stat(fmt.Sprintf("/tmp/.X%d-lock", n)); err == nil {
			continue
		}
		disp := fmt.Sprintf(":%d", n)
		cmd := exec.Command("Xvfb", disp, "-screen", "0", "800x600x24", "-noreset")
		if err := cmd.Start(); err != nil {
			t.Skip(err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		kill = func() { cmd.Process.Signal(syscall.SIGTERM); <-exited }
		t.Cleanup(kill)
		for i := 0; i < 50; i++ {
			select {
			case <-exited:
				i = 50
				continue
			default:
			}
			if c, err := xgb.NewConnDisplay(disp); err == nil {
				c.Close()
				t.Setenv("DISPLAY", disp)
				return kill
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Skip("no Xvfb display")
	return nil
}

// fakeWM plays the window manager on its own connection.
type fakeWM struct {
	t    *testing.T
	c    *xgb.Conn
	root xproto.Window
	atom map[string]xproto.Atom
}

func newFakeWM(t *testing.T) *fakeWM {
	c, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return &fakeWM{t: t, c: c, root: xproto.Setup(c).DefaultScreen(c).Root, atom: map[string]xproto.Atom{}}
}

func (w *fakeWM) sync() { xproto.GetInputFocus(w.c).Reply() }

func (w *fakeWM) intern(name string) xproto.Atom {
	if a, ok := w.atom[name]; ok {
		return a
	}
	a, err := internAtom(w.c, name)
	if err != nil {
		w.t.Fatal(err)
	}
	w.atom[name] = a
	return a
}

func (w *fakeWM) set(win xproto.Window, prop string, typ xproto.Atom, format byte, data []byte) {
	if err := xproto.ChangePropertyChecked(w.c, xproto.PropModeReplace, win, w.intern(prop), typ, format,
		uint32(len(data))/uint32(format/8), data).Check(); err != nil {
		w.t.Fatal(err)
	}
	w.sync()
}

func (w *fakeWM) setStr(win xproto.Window, prop string, typ xproto.Atom, s string) {
	w.set(win, prop, typ, 8, []byte(s))
}

func (w *fakeWM) setActive(win xproto.Window) {
	b := make([]byte, 4)
	xgb.Put32(b, uint32(win))
	w.set(w.root, "_NET_ACTIVE_WINDOW", xproto.AtomWindow, 32, b)
}

func (w *fakeWM) newWindow() xproto.Window {
	id, err := xproto.NewWindowId(w.c)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := xproto.CreateWindowChecked(w.c, 0, id, w.root, 0, 0, 10, 10, 0, xproto.WindowClassInputOutput,
		0, 0, nil).Check(); err != nil {
		w.t.Fatal(err)
	}
	return id
}

type windowEmit struct {
	at time.Time
	a  core.Attrs
}

func TestWindowX11(t *testing.T) {
	kill := startXvfb(t)
	b := &x11WindowBackend{}
	ctx := context.Background()
	wm := newFakeWM(t)

	if ok, why := b.Detect(ctx); ok || why != "window manager does not report the active window" {
		t.Fatalf("without WM: %v %q", ok, why)
	}
	sup := make([]byte, 4)
	xgb.Put32(sup, uint32(wm.intern("_NET_ACTIVE_WINDOW")))
	wm.set(wm.root, "_NET_SUPPORTED", xproto.AtomAtom, 32, sup)
	if ok, why := b.Detect(ctx); !ok || why != "" {
		t.Fatalf("with WM: %v %q", ok, why)
	}

	a := wm.newWindow()
	wm.setStr(a, "WM_CLASS", xproto.AtomString, "code\x00Code\x00")
	wm.setStr(a, "_NET_WM_NAME", wm.intern("UTF8_STRING"), "main.go — HatRack")
	bw := wm.newWindow()
	wm.setStr(bw, "WM_CLASS", xproto.AtomString, "firefox\x00Firefox\x00")
	wm.setStr(bw, "WM_NAME", xproto.AtomString, "Inbox")

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	emits := make(chan windowEmit, 16)
	result := make(chan error, 1)
	go func() {
		result <- b.Run(runCtx, func(at time.Time, a core.Attrs) { emits <- windowEmit{at, a} })
	}()
	expect := func(step string, want core.Attrs) {
		t.Helper()
		select {
		case e := <-emits:
			if !reflect.DeepEqual(e.a, want) || e.at.IsZero() {
				t.Fatalf("%s: got %v, want %v", step, e.a, want)
			}
		case err := <-result:
			t.Fatalf("%s: Run ended: %v", step, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: no emit, want %v", step, want)
		}
	}
	quiet := func(step string) {
		t.Helper()
		select {
		case e := <-emits:
			t.Fatalf("%s: unexpected emit %v", step, e.a)
		case <-time.After(300 * time.Millisecond):
		}
	}
	utf8s := wm.intern("UTF8_STRING")

	expect("initial", nil)
	wm.setActive(a)
	expect("A", core.Attrs{"app": "Code", "title": "main.go — HatRack"})
	wm.setStr(a, "_NET_WM_NAME", utf8s, "other.go — HatRack")
	expect("A renamed", core.Attrs{"app": "Code", "title": "other.go — HatRack"})
	wm.setStr(a, "_NET_WM_NAME", utf8s, "other.go — HatRack")
	quiet("same title again")
	wm.setActive(bw)
	expect("B", core.Attrs{"app": "Firefox", "title": "Inbox"})
	wm.setStr(a, "_NET_WM_NAME", utf8s, "third.go")
	quiet("title of inactive window")
	wm.setStr(bw, "WM_NAME", xproto.AtomString, "Postf\xe4cher") // Latin-1
	expect("B renamed", core.Attrs{"app": "Firefox", "title": "Postfächer"})
	// a window manager naming the root window keeps the sensor listening
	wm.setActive(wm.root)
	expect("root", nil)
	wm.setActive(bw)
	expect("B after root", core.Attrs{"app": "Firefox", "title": "Postfächer"})
	wm.setActive(0)
	expect("none", nil)
	wm.setActive(a)
	expect("A again", core.Attrs{"app": "Code", "title": "third.go"})
	xproto.DestroyWindow(wm.c, a)
	wm.setActive(0)
	expect("A destroyed", nil)
	quiet("after destroy")

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}

	// A window active at start is reported at once; a lost connection is an error.
	wm.setActive(bw)
	go func() {
		result <- b.Run(ctx, func(at time.Time, a core.Attrs) { emits <- windowEmit{at, a} })
	}()
	expect("active at start", core.Attrs{"app": "Firefox", "title": "Postfächer"})
	kill()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Run returned nil after the server died")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the server died")
	}
}
