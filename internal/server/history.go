package server

import (
	"context"
	"sort"
	"sync"
	"time"

	"hatrack/internal/core"
)

// The server derives everything (state, intervals, totals, suggestions)
// from the effective event log. Loading and resolving it from the database
// on every request grows with the years of log; so it is kept in memory and
// loaded again only when the event revision changes, i.e. after an event was
// stored.

// history is the effective log at one event revision. Its slices are
// shared: callers read them and never change them.
type history struct {
	Rev      int64
	Events   []core.Event                   // effective (corrections applied), by time
	State    core.State                     // the current state
	Last     map[string]string              // hat -> last activity used
	Recent   map[string][]core.RecentDetail // computed on first use
	recentAt time.Time
}

type historyCache struct {
	mu  sync.Mutex
	cur *history
}

// history returns the effective log, loading it only if events were stored
// since the last time. The revision is read first, so the data is never
// older than the revision it is labelled with.
func (s *Server) history(ctx context.Context) (*history, error) {
	rev, err := s.st.EventRev(ctx)
	if err != nil {
		return nil, err
	}
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	if h := s.hist.cur; h != nil && h.Rev == rev {
		return h, nil
	}
	ev, err := s.st.Effective(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	h := &history{Rev: rev, Events: ev, State: core.NewState(), Last: map[string]string{}}
	for _, e := range ev {
		h.State = h.State.Apply(e)
		if (e.Type == core.EvSwitch || e.Type == core.EvActivity) && e.Hat != "" && e.Activity != "" {
			h.Last[e.Hat] = e.Activity // sorted by time: the last one wins
		}
	}
	s.hist.cur = h
	return h, nil
}

// Before returns the effective events before to (all for a zero to), as the
// store's Effective(to) would.
func (h *history) Before(to time.Time) []core.Event {
	if to.IsZero() {
		return h.Events
	}
	i := sort.Search(len(h.Events), func(i int) bool { return !h.Events[i].TS.Before(to) })
	return h.Events[:i]
}

// RecentDetails returns the detail suggestions per hat, computed once per
// revision (and again after an hour, as the window moves on).
func (s *Server) recentDetails(h *history) map[string][]core.RecentDetail {
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	now := s.Now()
	if h.Recent == nil || now.Sub(h.recentAt) > time.Hour {
		h.Recent = core.RecentByHat(h.Events, now.Add(-core.RecentWindow), recentPerHat)
		h.recentAt = now
	}
	return h.Recent
}
