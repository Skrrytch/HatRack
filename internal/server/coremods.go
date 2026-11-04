package server

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
)

// Modules that run where the core runs (runs_in: core) are driven by the
// server: they get the defaults' settings when the configuration changes and
// the state after every change. So far that is the mqtt module.

// statePublisher is a core module that follows the state.
type statePublisher interface {
	Configure(raw json.RawMessage) error
	Publish(st modules.MQTTState)
	Close()
}

func (s *Server) publishers() map[string]statePublisher {
	out := map[string]statePublisher{}
	for _, m := range s.reg.All() {
		if p, ok := m.(statePublisher); ok && m.RunsIn() == modules.RunsInCore {
			out[m.ID()] = p
		}
	}
	return out
}

// Start configures the core modules from the stored configuration and
// publishes the current state. The command line calls it once at startup.
func (s *Server) Start(ctx context.Context) {
	for _, m := range s.reg.All() {
		if v, ok := m.(interface{ SetVersion(string) }); ok {
			v.SetVersion(Version)
		}
	}
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		log.Printf("core modules: %v", err)
		return
	}
	s.configureCore(cfg)
	s.publishCore(ctx)
}

// Close stops the core modules (they say goodbye where they can).
func (s *Server) Close() {
	for _, p := range s.publishers() {
		p.Close()
	}
}

// configureCore hands each core module the defaults' settings, with
// ${env:NAME} expanded; a module without settings, or disabled, is off.
func (s *Server) configureCore(cfg core.Config) {
	for id, p := range s.publishers() {
		var raw json.RawMessage
		if m, ok := cfg.Modules[id]; ok {
			raw, _ = json.Marshal(core.ExpandEnvPrefix(m, "HATRACK_"))
		}
		if err := p.Configure(raw); err != nil {
			log.Printf("module %s: %v", id, err)
		}
	}
}

// publishCore sends the current state to the core modules.
func (s *Server) publishCore(ctx context.Context) {
	pubs := s.publishers()
	if len(pubs) == 0 {
		return
	}
	h, err := s.history(ctx)
	if err != nil {
		return
	}
	st := h.State
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		return
	}
	out := mqttState(st, cfg, s.Loc)
	for _, p := range pubs {
		p.Publish(out)
	}
}

// mqttState is the published form of a state: names instead of keys only,
// no detail.
func mqttState(st core.State, cfg core.Config, loc *time.Location) modules.MQTTState {
	// "working": a hat is on and its time is working time (not e.g. Private)
	out := modules.MQTTState{Mode: string(st.Mode), Working: st.Mode == core.ModeWearing && cfg.IsWork(st.Hat)}
	if out.Mode == "" {
		out.Mode = string(core.ModeNone)
	}
	if st.Mode != core.ModeNone && st.Hat != "" {
		out.Hat, out.HatName, out.Activity = st.Hat, st.Hat, st.Activity
		if h, ok := cfg.Hat(st.Hat); ok {
			out.HatName = h.DisplayName()
		}
		out.Color = newPalette(cfg).color(st.Hat)
	}
	if !st.Since.IsZero() {
		if loc == nil {
			loc = time.Local
		}
		out.Since = st.Since.In(loc).Format(time.RFC3339)
	}
	return out
}
