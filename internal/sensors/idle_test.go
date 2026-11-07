package sensors

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"hatrack/internal/core"
)

var idleT0 = time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)

func TestIdleLogic(t *testing.T) {
	type step struct {
		name    string
		do      func(l *idleLogic) (time.Time, core.Attrs, bool)
		changed bool
		kind    string // "" = nil attrs
		at      time.Time
	}
	idle := func(now time.Duration, d time.Duration) func(*idleLogic) (time.Time, core.Attrs, bool) {
		return func(l *idleLogic) (time.Time, core.Attrs, bool) { return l.Idle(idleT0.Add(now), d) }
	}
	lock := func(now time.Duration, v bool) func(*idleLogic) (time.Time, core.Attrs, bool) {
		return func(l *idleLogic) (time.Time, core.Attrs, bool) { return l.Lock(idleT0.Add(now), v) }
	}
	m := time.Minute
	tests := []struct {
		name  string
		steps []step
	}{
		{"active stays quiet", []step{
			{"a", idle(0, 3*time.Second), false, "", time.Time{}},
			{"b", idle(5*time.Second, 59*time.Second), false, "", time.Time{}},
		}},
		{"inactive is backdated and reported once", []step{
			{"below", idle(59*time.Second, 59*time.Second), false, "", time.Time{}},
			{"at threshold", idle(60*time.Second, 60*time.Second), true, "inactive", idleT0},
			{"later", idle(65*time.Second, 65*time.Second), false, "", time.Time{}},
			{"input", idle(70*time.Second, time.Second), true, "", idleT0.Add(70 * time.Second)},
		}},
		{"new idle span after input in between", []step{
			{"first", idle(2*m, 2*m), true, "inactive", idleT0},
			{"second", idle(5*m, 90*time.Second), true, "inactive", idleT0.Add(5*m - 90*time.Second)},
		}},
		{"lock wins and is backdated by its time", []step{
			{"idle", idle(2*m, 2*m), true, "inactive", idleT0},
			{"lock", lock(3*m, true), true, "locked", idleT0.Add(3 * m)},
			{"poll while locked", idle(4*m, 4*m), false, "", time.Time{}},
			{"lock again", lock(4*m+time.Second, true), false, "", time.Time{}},
		}},
		{"unlock with input", []step{
			{"lock", lock(0, true), true, "locked", idleT0},
			{"fresh reading", idle(10*m, time.Second), false, "", time.Time{}},
			{"unlock", lock(10*m, false), true, "", idleT0.Add(10 * m)},
		}},
		{"unlock while still idle falls back to inactive", []step{
			{"lock", lock(0, true), true, "locked", idleT0},
			{"reading", idle(10*m, 10*m), false, "", time.Time{}},
			{"unlock", lock(10*m, false), true, "inactive", idleT0},
		}},
		{"lock from active, unlock quickly", []step{
			{"lock", lock(0, true), true, "locked", idleT0},
			{"unlock", lock(5*time.Second, false), true, "", idleT0.Add(5 * time.Second)},
		}},
		{"negative idle time counts as active", []step{
			{"a", idle(0, -time.Second), false, "", time.Time{}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var l idleLogic
			for _, s := range tt.steps {
				at, a, changed := s.do(&l)
				if changed != s.changed {
					t.Fatalf("%s: changed = %v, want %v", s.name, changed, s.changed)
				}
				if !changed {
					continue
				}
				if !at.Equal(s.at) {
					t.Fatalf("%s: at = %v, want %v", s.name, at, s.at)
				}
				if s.kind == "" {
					if a != nil {
						t.Fatalf("%s: attrs = %v, want nil", s.name, a)
					}
				} else if len(a) != 1 || a["kind"] != s.kind {
					t.Fatalf("%s: attrs = %v, want kind %s", s.name, a, s.kind)
				}
			}
		})
	}
}

// ---- fakes ----

type fakeIdle struct {
	mu     sync.Mutex
	d      time.Duration
	err    error
	closed bool
}

func (f *fakeIdle) set(d time.Duration, err error) { f.mu.Lock(); f.d, f.err = d, err; f.mu.Unlock() }
func (f *fakeIdle) IdleTime(context.Context) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.d, f.err
}
func (f *fakeIdle) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }

type fakeLock struct {
	locked bool
	ch     chan bool
}

func (f *fakeLock) Locked(context.Context) (bool, error) { return f.locked, nil }
func (f *fakeLock) Changes() <-chan bool                 { return f.ch }
func (f *fakeLock) Close() error                         { return nil }

type event struct {
	at   time.Time
	kind string
}

type idleCollector struct {
	mu sync.Mutex
	ev []event
}

func (c *idleCollector) emit(at time.Time, a core.Attrs) {
	c.mu.Lock()
	c.ev = append(c.ev, event{at, a["kind"]})
	c.mu.Unlock()
}

func (c *idleCollector) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.ev {
		k := e.kind
		if k == "" {
			k = "active"
		}
		out = append(out, k)
	}
	return out
}

func waitFor(t *testing.T, c *idleCollector, want ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := c.kinds()
		if len(got) >= len(want) {
			for i, w := range want {
				if got[i] != w {
					t.Fatalf("events = %v, want %v", got, want)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runAsync(ctx context.Context, f func(context.Context) error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- f(ctx) }()
	return done
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestIdleRunner(t *testing.T) {
	idle := &fakeIdle{}
	lock := &fakeLock{ch: make(chan bool, 4)}
	c := &idleCollector{}
	r := idleRunner{idle: idle, lock: lock, poll: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, func(ctx context.Context) error { return r.run(ctx, c.emit) })

	idle.set(2*time.Minute, nil)
	waitFor(t, c, "inactive")
	lock.ch <- true
	waitFor(t, c, "inactive", "locked")
	idle.set(0, nil)
	time.Sleep(30 * time.Millisecond) // still locked: nothing new
	if got := c.kinds(); len(got) != 2 {
		t.Fatalf("events while locked = %v", got)
	}
	lock.ch <- false
	waitFor(t, c, "inactive", "locked", "active")

	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestIdleRunnerStartsLocked(t *testing.T) {
	idle := &fakeIdle{d: 3 * time.Minute}
	lock := &fakeLock{locked: true, ch: make(chan bool)}
	c := &idleCollector{}
	r := idleRunner{idle: idle, lock: lock, poll: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, func(ctx context.Context) error { return r.run(ctx, c.emit) })
	waitFor(t, c, "locked")
	time.Sleep(20 * time.Millisecond)
	if got := c.kinds(); len(got) != 1 {
		t.Fatalf("events = %v, want only locked", got)
	}
	cancel()
	waitDone(t, done)
}

func TestIdleRunnerErrors(t *testing.T) {
	t.Run("read error", func(t *testing.T) {
		idle := &fakeIdle{}
		r := idleRunner{idle: idle, poll: 5 * time.Millisecond}
		done := runAsync(context.Background(), func(ctx context.Context) error { return r.run(ctx, func(time.Time, core.Attrs) {}) })
		time.Sleep(15 * time.Millisecond)
		idle.set(0, errors.New("connection closed"))
		if err := waitDone(t, done); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("lock connection lost", func(t *testing.T) {
		lock := &fakeLock{ch: make(chan bool)}
		r := idleRunner{idle: &fakeIdle{}, lock: lock, poll: time.Hour}
		done := runAsync(context.Background(), func(ctx context.Context) error { return r.run(ctx, func(time.Time, core.Attrs) {}) })
		close(lock.ch)
		if err := waitDone(t, done); err == nil {
			t.Fatal("want an error")
		}
	})
}

// Each backend's Run with fake sources.
func TestBackendsRun(t *testing.T) {
	mk := func() (*fakeIdle, *fakeLock, func(context.Context) (idleSource, error), func(context.Context) lockSource) {
		idle := &fakeIdle{d: 90 * time.Second}
		lock := &fakeLock{ch: make(chan bool, 1)}
		return idle, lock,
			func(context.Context) (idleSource, error) { return idle, nil },
			func(context.Context) lockSource { return lock }
	}
	idleB, idleL, idleO, lockO := mk()
	_ = idleL
	backends := map[string]func() Backend{}
	backends["x11"] = func() Backend {
		return &x11Backend{poll: 5 * time.Millisecond, openIdle: idleO, openLock: lockO}
	}
	backends["mutter"] = func() Backend {
		return &mutterBackend{poll: 5 * time.Millisecond, openIdle: idleO, openLock: lockO}
	}
	backends["kde"] = func() Backend {
		return &kdeBackend{poll: 5 * time.Millisecond, openIdle: idleO, openLock: lockO}
	}
	for name, mkb := range backends {
		t.Run(name, func(t *testing.T) {
			idleB.set(90*time.Second, nil)
			idleB.mu.Lock()
			idleB.closed = false
			idleB.mu.Unlock()
			b := mkb()
			if b.Name() != name {
				t.Fatalf("name %q", b.Name())
			}
			c := &idleCollector{}
			ctx, cancel := context.WithCancel(context.Background())
			done := runAsync(ctx, func(ctx context.Context) error { return b.Run(ctx, c.emit) })
			waitFor(t, c, "inactive")
			idleB.set(0, nil)
			waitFor(t, c, "inactive", "active")
			idleL.ch <- true
			waitFor(t, c, "inactive", "active", "locked")
			cancel()
			if err := waitDone(t, done); err != nil {
				t.Fatal(err)
			}
			idleB.mu.Lock()
			defer idleB.mu.Unlock()
			if !idleB.closed {
				t.Fatal("idle source not closed")
			}
		})
	}
}

func TestBackendOpenError(t *testing.T) {
	want := errors.New("no bus")
	b := &mutterBackend{openIdle: func(context.Context) (idleSource, error) { return nil, want }}
	if err := b.Run(context.Background(), func(time.Time, core.Attrs) {}); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

type fakeHints struct {
	mu  sync.Mutex
	h   sessionHints
	err error
	ch  chan struct{}
}

func (f *fakeHints) set(h sessionHints) { f.mu.Lock(); f.h = h; f.mu.Unlock() }
func (f *fakeHints) Read(context.Context) (sessionHints, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.h, f.err
}
func (f *fakeHints) Changes() <-chan struct{} { return f.ch }
func (f *fakeHints) Close() error             { return nil }

func TestLogindRun(t *testing.T) {
	src := &fakeHints{ch: make(chan struct{}, 1)}
	b := &logindBackend{open: func(context.Context) (hintSource, error) { return src, nil }}
	c := &idleCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, func(ctx context.Context) error { return b.Run(ctx, c.emit) })

	time.Sleep(20 * time.Millisecond)
	if got := c.kinds(); len(got) != 0 {
		t.Fatalf("events at start = %v", got)
	}
	since := time.Now().Add(-5 * time.Minute)
	src.set(sessionHints{Idle: true, IdleSince: since})
	src.ch <- struct{}{}
	waitFor(t, c, "inactive")
	c.mu.Lock()
	if got := c.ev[0].at; !got.Round(time.Millisecond).Equal(since.Round(time.Millisecond)) && got.Sub(since) > time.Millisecond {
		t.Fatalf("at = %v, want ~%v", got, since)
	}
	c.mu.Unlock()
	src.set(sessionHints{Idle: true, IdleSince: since, Locked: true})
	src.ch <- struct{}{}
	waitFor(t, c, "inactive", "locked")
	src.set(sessionHints{})
	src.ch <- struct{}{}
	waitFor(t, c, "inactive", "locked", "active")

	close(src.ch)
	if err := waitDone(t, done); err == nil {
		t.Fatal("want an error when the connection is lost")
	}
	cancel()
}

func TestLogindStartsLockedAndIdle(t *testing.T) {
	src := &fakeHints{ch: make(chan struct{}), h: sessionHints{Idle: true, Locked: true}}
	c := &idleCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, func(ctx context.Context) error { return runLogind(ctx, src, time.Now, c.emit) })
	waitFor(t, c, "locked")
	time.Sleep(10 * time.Millisecond)
	if got := c.kinds(); len(got) != 1 {
		t.Fatalf("events = %v", got)
	}
	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestDetectReasons(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	if ok, why := (&x11Backend{}).Detect(context.Background()); ok || why != "wayland session" {
		t.Fatalf("wayland: %v %q", ok, why)
	}
	t.Setenv("XDG_SESSION_TYPE", "x11")
	t.Setenv("DISPLAY", "")
	if ok, why := (&x11Backend{}).Detect(context.Background()); ok || why != "no X display" {
		t.Fatalf("no display: %v %q", ok, why)
	}
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	if ok, why := (&kdeBackend{}).Detect(context.Background()); ok || why == "" {
		t.Fatalf("kde on gnome: %v %q", ok, why)
	}
}
