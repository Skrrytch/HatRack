// Package modules contains the module runtime and the built-in modules.
// A module reacts to switches; a backend implements it for one desktop.
package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"hatrack/internal/core"
)

// RunsIn says where a module runs: on the device or wherever the core runs.
type RunsIn string

const (
	RunsInAgent RunsIn = "agent"
	RunsInCore  RunsIn = "core"
)

// ErrNoBackend means no backend of the module works on this device. It is
// not a failure: the feature is simply unavailable here.
var ErrNoBackend = errors.New("no backend available")

// Switch describes a hat change for the hooks.
type Switch struct {
	Hat      string `json:"hat"`
	HatName  string `json:"hat_name"`
	Color    string `json:"color,omitempty"`
	Activity string `json:"activity,omitempty"`
	Prev     string `json:"prev,omitempty"`
	PrevName string `json:"prev_name,omitempty"`
}

// Backend is the implementation of a module for one environment.
type Backend interface {
	Name() string
	Detect(ctx context.Context) bool
	Apply(ctx context.Context, cfg json.RawMessage) error
	Snapshot(ctx context.Context) (json.RawMessage, error)
}

// Module is one capability that reacts to switches.
type Module interface {
	ID() string
	RunsIn() RunsIn
	ConfigSchema() []byte // JSON Schema
	Backends() []Backend
	Enter(ctx context.Context, sw Switch, cfg json.RawMessage) error
	Leave(ctx context.Context, sw Switch, cfg json.RawMessage) error
}

// Snapshotter is implemented by modules that own a look which can be saved
// and restored. A snapshot has the same shape as the module configuration.
type Snapshotter interface {
	Snapshot(ctx context.Context) (json.RawMessage, error)
	Restore(ctx context.Context, snap json.RawMessage) error
}

// Runner runs an external command. It is replaced in tests.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// ExecRunner runs a real command with a time limit.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Registry holds the modules in the order they run.
type Registry struct {
	mods []Module
}

// Register adds a module; a module with the same id replaces the old one.
func (r *Registry) Register(m Module) {
	for i, x := range r.mods {
		if x.ID() == m.ID() {
			r.mods[i] = m
			return
		}
	}
	r.mods = append(r.mods, m)
}

// Get returns a module by id.
func (r *Registry) Get(id string) (Module, bool) {
	for _, m := range r.mods {
		if m.ID() == id {
			return m, true
		}
	}
	return nil, false
}

// All returns the modules in run order.
func (r *Registry) All() []Module { return append([]Module(nil), r.mods...) }

// Builtin returns a registry with the built-in modules.
func Builtin(run Runner) *Registry {
	if run == nil {
		run = ExecRunner
	}
	r := &Registry{}
	r.Register(NewWallpaper(run))
	r.Register(NewColorScheme(run))
	r.Register(NewExec())
	r.Register(NewNotify())
	return r
}

// backendModule is the common part of modules that apply a look through the
// first working backend.
type backendModule struct {
	id       string
	schema   string
	backends []Backend
	picked   Backend
	checked  bool
}

func (m *backendModule) ID() string           { return m.id }
func (m *backendModule) RunsIn() RunsIn       { return RunsInAgent }
func (m *backendModule) ConfigSchema() []byte { return []byte(m.schema) }
func (m *backendModule) Backends() []Backend  { return m.backends }

// Active returns the first backend whose Detect succeeds, or nil.
func (m *backendModule) Active(ctx context.Context) Backend {
	if !m.checked {
		for _, b := range m.backends {
			if b.Detect(ctx) {
				m.picked = b
				break
			}
		}
		m.checked = true
	}
	return m.picked
}

func (m *backendModule) Enter(ctx context.Context, _ Switch, cfg json.RawMessage) error {
	b := m.Active(ctx)
	if b == nil {
		return ErrNoBackend
	}
	return b.Apply(ctx, cfg)
}

func (m *backendModule) Leave(context.Context, Switch, json.RawMessage) error { return nil }

func (m *backendModule) Snapshot(ctx context.Context) (json.RawMessage, error) {
	b := m.Active(ctx)
	if b == nil {
		return nil, ErrNoBackend
	}
	return b.Snapshot(ctx)
}

func (m *backendModule) Restore(ctx context.Context, snap json.RawMessage) error {
	b := m.Active(ctx)
	if b == nil {
		return ErrNoBackend
	}
	return b.Apply(ctx, snap)
}

// Validate checks the module settings of a configuration against the schemas
// of the given registry. A hat's settings are checked together with the
// defaults they extend, so a hat may set only what differs; the defaults alone
// are checked for types only, because required values may come from the hats.
// Modules the registry does not know are skipped, because they may be
// external modules that exist only on devices.
func Validate(reg *Registry, cfg core.Config) error {
	for _, id := range sortedIDs(cfg.Modules) {
		mod, ok := reg.Get(id)
		if !ok {
			continue
		}
		if err := validateAgainst(withoutRequired(mod.ConfigSchema()), cfg.Modules[id], "modules."+id); err != nil {
			return err
		}
	}
	for _, h := range cfg.Hats {
		if h.Archived {
			continue
		}
		ids := map[string]core.ModuleCfg{}
		for id, m := range cfg.Modules {
			ids[id] = m
		}
		for id, m := range h.Modules {
			ids[id] = m
		}
		for _, id := range sortedIDs(ids) {
			mod, ok := reg.Get(id)
			if !ok {
				continue
			}
			merged, on := cfg.ModuleConfig(h, id)
			if !on {
				continue
			}
			if err := validateAgainst(mod.ConfigSchema(), merged, "hats."+h.Key+".modules."+id); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedIDs(m map[string]core.ModuleCfg) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// withoutRequired drops the top-level "required" list of a schema.
func withoutRequired(schema []byte) []byte {
	var m map[string]any
	if json.Unmarshal(schema, &m) != nil {
		return schema
	}
	delete(m, "required")
	b, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return b
}
