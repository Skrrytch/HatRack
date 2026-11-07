package sensors

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// busHasName reports whether name has an owner on the bus.
func busHasName(ctx context.Context, conn *dbus.Conn, name string) (bool, error) {
	var has bool
	err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has)
	return has, err
}

// screenSaver is a D-Bus screensaver service that tells when the screen is
// locked: the interface has the same name as the service.
type screenSaver struct {
	name string
	path dbus.ObjectPath
}

// dbusLock watches ActiveChanged of the first existing screensaver service.
type dbusLock struct {
	conn    *dbus.Conn
	obj     dbus.BusObject
	iface   string
	sigs    chan *dbus.Signal
	out     chan bool
	stop    chan struct{}
	done    chan struct{}
	closeMu sync.Once
}

// newDBusLock picks the first of services that is on the session bus and
// starts watching it. It owns conn from here on, also on error.
func newDBusLock(ctx context.Context, conn *dbus.Conn, services []screenSaver) (*dbusLock, error) {
	for _, s := range services {
		has, err := busHasName(ctx, conn, s.name)
		if err != nil || !has {
			continue
		}
		d := &dbusLock{
			conn:  conn,
			obj:   conn.Object(s.name, s.path),
			iface: s.name,
			sigs:  make(chan *dbus.Signal, 16),
			out:   make(chan bool, 16),
			stop:  make(chan struct{}),
			done:  make(chan struct{}),
		}
		conn.Signal(d.sigs)
		if err := conn.AddMatchSignalContext(ctx,
			dbus.WithMatchSender(s.name),
			dbus.WithMatchInterface(s.name),
			dbus.WithMatchMember("ActiveChanged")); err != nil {
			conn.RemoveSignal(d.sigs)
			conn.Close()
			return nil, err
		}
		go d.loop()
		return d, nil
	}
	conn.Close()
	names := make([]string, len(services))
	for i, s := range services {
		names[i] = s.name
	}
	return nil, fmt.Errorf("no screensaver service on the session bus (%s)", strings.Join(names, ", "))
}

func (d *dbusLock) loop() {
	defer close(d.done)
	defer close(d.out)
	for {
		select {
		case <-d.stop:
			return
		case <-d.conn.Context().Done():
			return
		case s := <-d.sigs:
			if s == nil || s.Name != d.iface+".ActiveChanged" || len(s.Body) != 1 {
				continue
			}
			v, ok := s.Body[0].(bool)
			if !ok {
				continue
			}
			select {
			case d.out <- v:
			case <-d.stop:
				return
			case <-d.conn.Context().Done():
				return
			}
		}
	}
}

func (d *dbusLock) Locked(ctx context.Context) (bool, error) {
	var v bool
	err := d.obj.CallWithContext(ctx, d.iface+".GetActive", 0).Store(&v)
	return v, err
}

func (d *dbusLock) Changes() <-chan bool { return d.out }

func (d *dbusLock) Close() error {
	d.closeMu.Do(func() {
		close(d.stop)
		d.conn.RemoveSignal(d.sigs)
		d.conn.Close()
		<-d.done
	})
	return nil
}

// openLock opens the lock source for a backend. Without a screensaver
// service the sensor still works, it just never reports "locked".
func openLock(ctx context.Context, services []screenSaver) lockSource {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil
	}
	d, err := newDBusLock(ctx, conn, services)
	if err != nil {
		return nil
	}
	return d
}

// dbusIdle polls an idle time method of a session bus service.
type dbusIdle struct {
	conn  *dbus.Conn
	obj   dbus.BusObject
	call  string
	scale time.Duration // unit of the returned number
	u64   bool          // the method returns uint64 (Mutter), else uint32 (KDE)
}

func (d *dbusIdle) IdleTime(ctx context.Context) (time.Duration, error) {
	c := d.obj.CallWithContext(ctx, d.call, 0)
	if c.Err != nil {
		return 0, c.Err
	}
	if d.u64 {
		var v uint64
		if err := c.Store(&v); err != nil {
			return 0, err
		}
		return time.Duration(v) * d.scale, nil
	}
	var v uint32
	if err := c.Store(&v); err != nil {
		return 0, err
	}
	return time.Duration(v) * d.scale, nil
}

func (d *dbusIdle) Close() error { return d.conn.Close() }
