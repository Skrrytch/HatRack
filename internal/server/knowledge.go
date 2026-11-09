package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/store"
)

// knowledgeTTL is how long learned knowledge is reused; it changes slowly.
const knowledgeTTL = 10 * time.Minute

// ruleStatsDays is how far back the hits of a rule are counted.
const ruleStatsDays = 7

// ruleStats is the active time per rule id in the last days in which the
// rule decided a field, or hid noise.
type ruleStats map[string]time.Duration

// knowledgeEntry is one computation: valid for one configuration revision
// and one set of learned ticket projects, for knowledgeTTL.
type knowledgeEntry struct {
	cfgRev int64
	links  int
	at     time.Time
	k      core.Knowledge
	stats  ruleStats
}

type knowledgeCache struct {
	mu  sync.Mutex // also keeps two computations from running at once
	cur *knowledgeEntry
}

// knowledge returns what was learned and the hits of the rules of the last
// days. The result is shared: callers read it and never change it.
func (s *Server) knowledge(r *http.Request) (core.Knowledge, ruleStats, error) {
	ctx := r.Context()
	cfg, rev, err := s.st.GetConfig(ctx)
	if err != nil {
		return core.Knowledge{}, nil, err
	}
	learned, err := s.st.TicketLinks(ctx)
	if err != nil {
		return core.Knowledge{}, nil, err
	}
	now := s.Now()
	s.know.mu.Lock()
	defer s.know.mu.Unlock()
	if e := s.know.cur; e != nil && e.cfgRev == rev && e.links == len(learned) && now.Sub(e.at) < knowledgeTTL && !now.Before(e.at) {
		return e.k, e.stats, nil
	}
	ev, err := s.st.Effective(ctx, time.Time{})
	if err != nil {
		return core.Knowledge{}, nil, err
	}
	retention := cfg.Observe.Retention()
	obs, err := s.st.ObservationsBetween(ctx, now.Add(-retention), now, "", "")
	if err != nil {
		return core.Knowledge{}, nil, err
	}
	tk := core.NewTickets(learned, cfg.Tickets)
	k := core.BuildKnowledge(ev, obs, tk, retention, now)
	e := &knowledgeEntry{cfgRev: rev, links: len(learned), at: now, k: k, stats: statsOf(cfg, k, tk, obs, now)}
	s.know.cur = e
	return e.k, e.stats, nil
}

// statsOf classifies the active time of the last days and adds it up per
// rule that is named in a reason.
func statsOf(cfg core.Config, k core.Knowledge, tk core.Tickets, obs []core.Observation, now time.Time) ruleStats {
	from := now.Add(-ruleStatsDays * 24 * time.Hour)
	var recent []core.Observation
	for _, o := range obs {
		if o.End.After(from) {
			recent = append(recent, o)
		}
	}
	out := ruleStats{}
	for _, o := range core.ReadDay(recent, nil, core.NewClassifier(cfg, k, tk)) {
		start := o.Start
		if start.Before(from) {
			start = from
		}
		if !start.Before(o.End) {
			continue
		}
		seen := map[string]bool{}
		for _, why := range o.Cand.Why {
			if why.Source == core.SourceRule && !seen[why.Ref] {
				seen[why.Ref] = true
				out[why.Ref] += o.End.Sub(start)
			}
		}
	}
	return out
}

// getKnowledge hands the learned knowledge to the agents, so that they
// classify like the server. ETag and If-None-Match save the transfer while
// nothing changed.
func (s *Server) getKnowledge(w http.ResponseWriter, r *http.Request, _ store.Device) {
	k, _, err := s.knowledge(r)
	if err != nil {
		serverError(w, err)
		return
	}
	b, err := json.Marshal(k)
	if err != nil {
		serverError(w, err)
		return
	}
	// "built" changes with every computation; the tag follows the content
	k.Built = time.Time{}
	c, _ := json.Marshal(k)
	sum := sha256.Sum256(c)
	etag := `"` + hex.EncodeToString(sum[:])[:16] + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// drop forgets the cached knowledge, e.g. after ticket links changed.
func (c *knowledgeCache) drop() {
	c.mu.Lock()
	c.cur = nil
	c.mu.Unlock()
}
