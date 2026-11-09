package agent

import (
	"maps"
	"testing"

	"hatrack/internal/core"
	"hatrack/internal/sensors"
)

func TestEnrichWindowWithoutGrant(t *testing.T) {
	a := &Agent{workdir: sensors.Workdir{Proc: t.TempDir(), Home: t.TempDir()}}
	raw := core.Attrs{"app": "Guake", "title": "zsh", sensors.AttrPID: "100"}
	want := core.Attrs{"app": "Guake", "title": "zsh"}
	// before any policy and without the grant: only the pid goes
	if got := a.enrichWindow(raw); !maps.Equal(got, want) {
		t.Errorf("no policy: %v", got)
	}
	a.wdPolicy.Store(&wdPolicy{obs: core.Observe{}, device: "laptop"})
	if got := a.enrichWindow(raw); !maps.Equal(got, want) {
		t.Errorf("no grant: %v", got)
	}
	// with the grant but no active session: nothing added
	a.wdPolicy.Store(&wdPolicy{device: "laptop", obs: core.Observe{Grants: map[string]map[string]string{"laptop": {core.GrantWorkdir: core.LevelKnownKeys}}}})
	if got := a.enrichWindow(raw); !maps.Equal(got, want) {
		t.Errorf("no session: %v", got)
	}
	if raw[sensors.AttrPID] != "100" {
		t.Error("input changed")
	}
	if a.enrichWindow(nil) != nil {
		t.Error("nil stays nil")
	}
}
