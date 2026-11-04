package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"
)

// Failure is a module error that did not stop the switch.
type Failure struct {
	Module string
	Hook   string
	Err    error
}

func (f Failure) Error() string { return fmt.Sprintf("%s %s: %v", f.Module, f.Hook, f.Err) }

// Capability says what a module can do on this device.
type Capability struct {
	Backend   string              `json:"backend,omitempty"`
	Available bool                `json:"available"`
	Choices   map[string][]Choice `json:"choices,omitempty"` // see Host.Choices
}

// Host runs modules for the agent. A failing module never stops the others.
type Host struct {
	Reg *Registry
	mu  sync.Mutex
}

// NewHost creates a host for a registry.
func NewHost(reg *Registry) *Host { return &Host{Reg: reg} }

// Capabilities reports, per module, which backend works on this device.
func (h *Host) Capabilities(ctx context.Context) map[string]Capability {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]Capability{}
	for _, m := range h.Reg.All() {
		c := Capability{}
		switch x := m.(type) {
		case interface{ Active(context.Context) Backend }:
			if b := x.Active(ctx); b != nil {
				c = Capability{Backend: b.Name(), Available: true}
			}
		case execModule, *external, *externalSnap:
			c = Capability{Backend: "builtin", Available: true}
		default:
			c.Available = true
		}
		out[m.ID()] = c
	}
	return out
}

// Snapshot asks every module with a look for its current state.
func (h *Host) Snapshot(ctx context.Context) (map[string]json.RawMessage, []Failure) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snaps := map[string]json.RawMessage{}
	var fails []Failure
	for _, m := range h.Reg.All() {
		s, ok := m.(Snapshotter)
		if !ok {
			continue
		}
		b, err := s.Snapshot(ctx)
		switch {
		case err == ErrNoBackend:
		case err != nil:
			fails = append(fails, Failure{m.ID(), "snapshot", err})
		case len(b) > 0 && string(b) != "null":
			snaps[m.ID()] = b
		}
	}
	return snaps, fails
}

// Restore applies the saved snapshots (the neutral look).
func (h *Host) Restore(ctx context.Context, snaps map[string]json.RawMessage) []Failure {
	h.mu.Lock()
	defer h.mu.Unlock()
	var fails []Failure
	for _, m := range h.Reg.All() {
		s, ok := m.(Snapshotter)
		snap := snaps[m.ID()]
		if !ok || len(snap) == 0 {
			continue
		}
		if err := s.Restore(ctx, snap); err != nil && err != ErrNoBackend {
			fails = append(fails, Failure{m.ID(), "restore", err})
		}
	}
	return fails
}

// Run calls the hook ("enter" or "leave") of every module that has settings
// in cfgs, in registry order.
func (h *Host) Run(ctx context.Context, hook string, sw Switch, cfgs map[string]json.RawMessage) []Failure {
	h.mu.Lock()
	defer h.mu.Unlock()
	var fails []Failure
	for _, m := range h.Reg.All() {
		cfg, ok := cfgs[m.ID()]
		if !ok {
			continue
		}
		var err error
		if hook == "enter" {
			err = m.Enter(ctx, sw, cfg)
		} else {
			err = m.Leave(ctx, sw, cfg)
		}
		if err != nil && err != ErrNoBackend {
			fails = append(fails, Failure{m.ID(), hook, err})
		}
	}
	return fails
}

// Notify shows a message through the notify module, if it works here.
func (h *Host) Notify(ctx context.Context, summary, body string) error {
	m, ok := h.Reg.Get("notify")
	if !ok {
		return ErrNoBackend
	}
	n, ok := m.(Notifier)
	if !ok {
		return ErrNoBackend
	}
	return n.Notify(ctx, summary, body)
}

// ExternalDir returns the directory for personal modules.
func ExternalDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = home + "/.config"
	}
	return base + "/hatrack/modules"
}

// IDs lists the module ids in run order.
func (r *Registry) IDs() []string {
	var out []string
	for _, m := range r.mods {
		out = append(out, m.ID())
	}
	return slices.Clone(out)
}
