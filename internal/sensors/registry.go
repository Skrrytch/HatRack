// Package sensors defines the observation sensors, their backends and the
// recorder that turns reported states into spans.
package sensors

import (
	"context"
	"os"
	"time"

	"hatrack/internal/core"
)

// Backend implements a sensor for one environment.
type Backend interface {
	Name() string
	// Detect reports whether the backend works here; reason says why not
	// (shown on the Devices page), e.g. "no X11 display".
	Detect(ctx context.Context) (ok bool, reason string)
	// Run watches until ctx ends and calls emit on every change of state.
	// nil attrs mean "nothing to report" (no focused window, not idle).
	Run(ctx context.Context, emit func(at time.Time, a core.Attrs)) error
}

// Sensor is one kind of observation with its backends in order of preference.
type Sensor struct {
	ID       string // "idle", "window"
	Backends []Backend
}

// Registry holds the sensors in registration order.
type Registry struct{ list []Sensor }

// Register adds a sensor; a sensor with the same ID is replaced in place.
func (r *Registry) Register(s Sensor) {
	for i := range r.list {
		if r.list[i].ID == s.ID {
			r.list[i] = s
			return
		}
	}
	r.list = append(r.list, s)
}

// Get returns the sensor with the given ID.
func (r *Registry) Get(id string) (Sensor, bool) {
	for _, s := range r.list {
		if s.ID == id {
			return s, true
		}
	}
	return Sensor{}, false
}

// All returns the sensors in registration order.
func (r *Registry) All() []Sensor { return append([]Sensor(nil), r.list...) }

// Builtin returns "idle" and "window". "idle" has the backends x11, mutter,
// kde and logind, "window" the backend x11. If FakeEnv is set, both get a fake
// backend first.
func Builtin() *Registry {
	r := &Registry{}
	for _, id := range []string{IdleSensor, "window"} {
		s := Sensor{ID: id}
		if path := os.Getenv(FakeEnv); path != "" {
			s.Backends = []Backend{&fakeBackend{sensor: id, path: path, poll: 100 * time.Millisecond}}
		}
		if id == IdleSensor {
			s.Backends = append(s.Backends, &x11Backend{poll: idlePoll}, &mutterBackend{poll: idlePoll},
				&kdeBackend{poll: idlePoll}, &logindBackend{})
		}
		if id == "window" {
			s.Backends = append(s.Backends, &x11WindowBackend{})
		}
		r.Register(s)
	}
	return r
}
