package sensors

import (
	"context"
	"errors"
	"time"

	"hatrack/internal/core"
)

// idlePoll is how often the idle time is read.
const idlePoll = 5 * time.Second

// idleSource reads the time since the last user input.
type idleSource interface {
	IdleTime(ctx context.Context) (time.Duration, error)
	Close() error
}

// lockSource reports the lock state of the screen.
type lockSource interface {
	// Locked is the state right now.
	Locked(ctx context.Context) (bool, error)
	// Changes delivers every change; it is closed when the connection is lost.
	Changes() <-chan bool
	Close() error
}

// idleRunner is the polling loop shared by the x11, mutter and kde backends.
type idleRunner struct {
	idle idleSource
	lock lockSource // nil: no lock detection
	poll time.Duration
	now  func() time.Time
}

func (r *idleRunner) run(ctx context.Context, emit func(time.Time, core.Attrs)) error {
	poll := r.poll
	if poll <= 0 {
		poll = idlePoll
	}
	now := r.now
	if now == nil {
		now = time.Now
	}
	var l idleLogic
	send := func(at time.Time, a core.Attrs, changed bool) {
		if changed {
			emit(at, a)
		}
	}

	var changes <-chan bool
	if r.lock != nil {
		changes = r.lock.Changes()
		if locked, err := r.lock.Locked(ctx); err == nil && locked {
			send(l.Lock(now(), true))
		}
	}
	read := func() error {
		d, err := r.idle.IdleTime(ctx)
		if err != nil {
			return err
		}
		send(l.Idle(now(), d))
		return nil
	}
	if err := read(); err != nil {
		return ctxErr(ctx, err)
	}

	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := read(); err != nil {
				return ctxErr(ctx, err)
			}
		case locked, ok := <-changes:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("idle: lock connection lost")
			}
			if !locked {
				// Feed a fresh reading first: unlocking reports by idle time.
				if err := read(); err != nil {
					return ctxErr(ctx, err)
				}
			}
			send(l.Lock(now(), locked))
		}
	}
}

// ctxErr hides errors caused by a cancelled context.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}
