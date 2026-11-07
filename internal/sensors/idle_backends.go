package sensors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/screensaver"
	"github.com/jezek/xgb/xproto"

	"hatrack/internal/core"
)

// The screensaver services that report the lock state, with their paths.
var (
	cinnamonSaver = screenSaver{"org.cinnamon.ScreenSaver", "/org/cinnamon/ScreenSaver"}
	gnomeSaver    = screenSaver{"org.gnome.ScreenSaver", "/org/gnome/ScreenSaver"}
	freedeskSaver = screenSaver{"org.freedesktop.ScreenSaver", "/org/freedesktop/ScreenSaver"}
)

// ---- x11 ----------------------------------------------------------------

// x11Backend reads the idle time from the MIT-SCREEN-SAVER extension and the
// lock state from a screensaver service on the session bus.
type x11Backend struct {
	poll     time.Duration // idlePoll
	openIdle func(ctx context.Context) (idleSource, error)
	openLock func(ctx context.Context) lockSource
}

func (*x11Backend) Name() string { return "x11" }

func (b *x11Backend) Detect(ctx context.Context) (bool, string) {
	if b.openIdle != nil {
		return true, ""
	}
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		return false, "wayland session"
	}
	c, err := x11Connect()
	if err != nil {
		return false, err.Error()
	}
	c.Close()
	return true, ""
}

func (b *x11Backend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	open := b.openIdle
	if open == nil {
		open = openX11Idle
	}
	idle, err := open(ctx)
	if err != nil {
		return err
	}
	defer idle.Close()
	var lock lockSource
	if b.openLock != nil {
		lock = b.openLock(ctx)
	} else {
		lock = openLock(ctx, []screenSaver{cinnamonSaver, gnomeSaver, freedeskSaver})
	}
	if lock != nil {
		defer lock.Close()
	}
	r := idleRunner{idle: idle, lock: lock, poll: b.poll}
	return r.run(ctx, emit)
}

// x11Connect opens the display and checks the screensaver extension.
func x11Connect() (*xgb.Conn, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, errors.New("no X display")
	}
	c, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("no X display: %v", err)
	}
	if err := screensaver.Init(c); err != nil {
		c.Close()
		return nil, errors.New("no screensaver extension")
	}
	return c, nil
}

type x11Idle struct {
	c    *xgb.Conn
	root xproto.Window
}

func openX11Idle(ctx context.Context) (idleSource, error) {
	c, err := x11Connect()
	if err != nil {
		return nil, err
	}
	return &x11Idle{c: c, root: xproto.Setup(c).DefaultScreen(c).Root}, nil
}

func (x *x11Idle) IdleTime(context.Context) (time.Duration, error) {
	r, err := screensaver.QueryInfo(x.c, xproto.Drawable(x.root)).Reply()
	if err != nil {
		return 0, err
	}
	return time.Duration(r.MsSinceUserInput) * time.Millisecond, nil
}

func (x *x11Idle) Close() error { x.c.Close(); return nil }

// ---- mutter -------------------------------------------------------------

const (
	mutterName = "org.gnome.Mutter.IdleMonitor"
	mutterPath = "/org/gnome/Mutter/IdleMonitor/Core"
)

// mutterBackend reads the idle time from GNOME's IdleMonitor.
type mutterBackend struct {
	poll     time.Duration
	openIdle func(ctx context.Context) (idleSource, error)
	openLock func(ctx context.Context) lockSource
}

func (*mutterBackend) Name() string { return "mutter" }

func (b *mutterBackend) Detect(ctx context.Context) (bool, string) {
	if b.openIdle != nil {
		return true, ""
	}
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return false, "no session bus"
	}
	defer conn.Close()
	if has, err := busHasName(ctx, conn, mutterName); err != nil || !has {
		return false, mutterName + " not on the session bus"
	}
	return true, ""
}

func (b *mutterBackend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	open := b.openIdle
	if open == nil {
		open = func(ctx context.Context) (idleSource, error) {
			conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
			if err != nil {
				return nil, err
			}
			return &dbusIdle{conn: conn, obj: conn.Object(mutterName, mutterPath),
				call: mutterName + ".GetIdletime", scale: time.Millisecond, u64: true}, nil
		}
	}
	return runDBusIdle(ctx, emit, open, b.openLock, []screenSaver{gnomeSaver}, b.poll)
}

// ---- kde ----------------------------------------------------------------

const kdeCall = "org.freedesktop.ScreenSaver.GetSessionIdleTime"

// kdeBackend reads the idle time from KDE's screensaver service.
type kdeBackend struct {
	poll     time.Duration
	openIdle func(ctx context.Context) (idleSource, error)
	openLock func(ctx context.Context) lockSource
}

func (*kdeBackend) Name() string { return "kde" }

func (b *kdeBackend) Detect(ctx context.Context) (bool, string) {
	if b.openIdle != nil {
		return true, ""
	}
	if !strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		return false, "not a KDE session"
	}
	src, err := openKDEIdle(ctx)
	if err != nil {
		return false, "GetSessionIdleTime failed: " + err.Error()
	}
	src.Close()
	return true, ""
}

func (b *kdeBackend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	open := b.openIdle
	if open == nil {
		open = openKDEIdle
	}
	return runDBusIdle(ctx, emit, open, b.openLock, []screenSaver{freedeskSaver}, b.poll)
}

// openKDEIdle connects and checks that the call works.
func openKDEIdle(ctx context.Context) (idleSource, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	src := &dbusIdle{conn: conn, obj: conn.Object("org.freedesktop.ScreenSaver", "/ScreenSaver"),
		call: kdeCall, scale: time.Second}
	if _, err := src.IdleTime(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return src, nil
}

// runDBusIdle is the Run of the backends with a polled D-Bus idle time.
func runDBusIdle(ctx context.Context, emit func(time.Time, core.Attrs),
	openIdle func(context.Context) (idleSource, error),
	openLockFn func(context.Context) lockSource, savers []screenSaver, poll time.Duration) error {
	idle, err := openIdle(ctx)
	if err != nil {
		return err
	}
	defer idle.Close()
	var lock lockSource
	if openLockFn != nil {
		lock = openLockFn(ctx)
	} else {
		lock = openLock(ctx, savers)
	}
	if lock != nil {
		defer lock.Close()
	}
	r := idleRunner{idle: idle, lock: lock, poll: poll}
	return r.run(ctx, emit)
}
