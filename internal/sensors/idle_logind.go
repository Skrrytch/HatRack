package sensors

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"hatrack/internal/core"
)

const (
	loginName    = "org.freedesktop.login1"
	loginManager = "/org/freedesktop/login1"
	loginSession = "org.freedesktop.login1.Session"
)

// sessionHints are the session properties the logind backend uses.
type sessionHints struct {
	Idle      bool
	IdleSince time.Time // zero if unknown
	Locked    bool
}

// hintSource reads the hints of the user's session and tells when they change.
type hintSource interface {
	Read(ctx context.Context) (sessionHints, error)
	// Changes signals every property change; closed when the connection is lost.
	Changes() <-chan struct{}
	Close() error
}

// logindBackend uses IdleHint and LockedHint of the logind session. These
// are only as good as the desktop that sets them.
type logindBackend struct {
	open func(ctx context.Context) (hintSource, error)
}

func (*logindBackend) Name() string { return "logind" }

func (b *logindBackend) Detect(ctx context.Context) (bool, string) {
	if b.open != nil {
		return true, ""
	}
	src, err := openLogind(ctx)
	if err != nil {
		return false, err.Error()
	}
	src.Close()
	return true, ""
}

func (b *logindBackend) Run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	open := b.open
	if open == nil {
		open = openLogind
	}
	src, err := open(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	return runLogind(ctx, src, time.Now, emit)
}

// runLogind feeds the hints into the idle logic on every change.
func runLogind(ctx context.Context, src hintSource, now func() time.Time, emit func(time.Time, core.Attrs)) error {
	var l idleLogic
	send := func(at time.Time, a core.Attrs, changed bool) {
		if changed {
			emit(at, a)
		}
	}
	changes := src.Changes()
	apply := func() error {
		h, err := src.Read(ctx)
		if err != nil {
			return ctxErr(ctx, err)
		}
		t := now()
		// Idle first, so that unlocking reports by the current idle state.
		idleFor := time.Duration(0)
		if h.Idle {
			idleFor = IdleReportAfter
			if !h.IdleSince.IsZero() && t.Sub(h.IdleSince) > idleFor {
				idleFor = t.Sub(h.IdleSince)
			}
		}
		if h.Locked { // lock first, so that no "inactive" shows in between
			send(l.Lock(t, true))
			send(l.Idle(t, idleFor))
		} else {
			send(l.Idle(t, idleFor))
			send(l.Lock(t, false))
		}
		return nil
	}
	if err := apply(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-changes:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("idle: logind connection lost")
			}
			if err := apply(); err != nil {
				return err
			}
		}
	}
}

// logindSession is the D-Bus implementation of hintSource.
type logindSession struct {
	conn *dbus.Conn
	obj  dbus.BusObject
	sigs chan *dbus.Signal
	out  chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func openLogind(ctx context.Context) (hintSource, error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, errors.New("system bus not reachable")
	}
	mgr := conn.Object(loginName, loginManager)
	var path dbus.ObjectPath
	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		err = mgr.CallWithContext(ctx, loginName+".Manager.GetSession", 0, id).Store(&path)
	} else {
		err = mgr.CallWithContext(ctx, loginName+".Manager.GetSessionByPID", 0, uint32(os.Getpid())).Store(&path)
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("logind session not found: %v", err)
	}
	s := &logindSession{
		conn: conn,
		obj:  conn.Object(loginName, path),
		sigs: make(chan *dbus.Signal, 16),
		out:  make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	conn.Signal(s.sigs)
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchSender(loginName),
		dbus.WithMatchObjectPath(path),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged")); err != nil {
		conn.RemoveSignal(s.sigs)
		conn.Close()
		return nil, err
	}
	go s.loop(path)
	return s, nil
}

func (s *logindSession) loop(path dbus.ObjectPath) {
	defer close(s.done)
	defer close(s.out)
	for {
		select {
		case <-s.stop:
			return
		case <-s.conn.Context().Done():
			return
		case sig := <-s.sigs:
			if sig == nil || sig.Path != path || sig.Name != "org.freedesktop.DBus.Properties.PropertiesChanged" {
				continue
			}
			select { // coalesce: one pending wake-up is enough
			case s.out <- struct{}{}:
			default:
			}
		}
	}
}

func (s *logindSession) Read(ctx context.Context) (sessionHints, error) {
	var all map[string]dbus.Variant
	if err := s.obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, loginSession).Store(&all); err != nil {
		return sessionHints{}, err
	}
	var h sessionHints
	if v, ok := all["IdleHint"].Value().(bool); ok {
		h.Idle = v
	}
	if v, ok := all["LockedHint"].Value().(bool); ok {
		h.Locked = v
	}
	if v, ok := all["IdleSinceHint"].Value().(uint64); ok && v > 0 {
		h.IdleSince = time.UnixMicro(int64(v))
	}
	return h, nil
}

func (s *logindSession) Changes() <-chan struct{} { return s.out }

func (s *logindSession) Close() error {
	s.once.Do(func() {
		close(s.stop)
		s.conn.RemoveSignal(s.sigs)
		s.conn.Close()
		<-s.done
	})
	return nil
}
