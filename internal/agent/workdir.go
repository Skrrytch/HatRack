package agent

import (
	"context"
	"maps"
	"strconv"
	"sync"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/sensors"
)

// wdPolicy is what the workdir reader needs from the settings.
type wdPolicy struct {
	obs    core.Observe
	device string
}

// workdirRefresh is how often repository and branch are looked up again
// while the focused window stays the same (a checkout, another session).
const workdirRefresh = 30 * time.Second

// workdirEmit wraps the window sensor's emit: it removes the window's process
// id and, with the grant workdir, adds repository and branch. A new window
// state is always passed on; between them the lookup is repeated and a change
// passed on. The recorder filters by the grants again.
func (a *Agent) workdirEmit(ctx context.Context, next func(time.Time, core.Attrs)) func(time.Time, core.Attrs) {
	var mu sync.Mutex
	var raw, last core.Attrs
	push := func(at time.Time, always bool) { // caller holds mu
		out := a.enrichWindow(raw)
		if !always && maps.Equal(out, last) {
			return
		}
		last = out
		next(at, out)
	}
	go func() {
		t := time.NewTicker(workdirRefresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mu.Lock()
				if raw != nil {
					push(a.Now(), false)
				}
				mu.Unlock()
			}
		}
	}()
	return func(at time.Time, attrs core.Attrs) {
		mu.Lock()
		defer mu.Unlock()
		raw = attrs
		push(at, true)
	}
}

// enrichWindow returns the window attributes without the process id, plus
// repository and branch where the grant allows and the reader is sure.
func (a *Agent) enrichWindow(raw core.Attrs) core.Attrs {
	if raw == nil {
		return nil
	}
	out := maps.Clone(raw)
	pid, _ := strconv.Atoi(out[sensors.AttrPID])
	delete(out, sensors.AttrPID)
	p := a.wdPolicy.Load()
	if p == nil || p.obs.Granted(p.device, core.GrantWorkdir) == "" {
		return out
	}
	app := out["app"]
	if p.obs.Ignored(app) || p.obs.Redacted(app) {
		return out
	}
	for k, v := range a.workdir.Lookup(app, pid, p.obs.IgnorePaths) {
		out[k] = v
	}
	return out
}
