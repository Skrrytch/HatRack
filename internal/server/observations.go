package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"hatrack/internal/core"
	"hatrack/internal/store"
)

// postObservations stores spans of the device's sensors. A bad entry rejects
// the whole batch. Observations are never events: nothing is appended to the
// log and nothing is broadcast.
func (s *Server) postObservations(w http.ResponseWriter, r *http.Request, dev store.Device) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	var obs []core.Observation
	if err := json.Unmarshal(body, &obs); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(obs) > core.MaxObservationBatch {
		httpError(w, http.StatusBadRequest, "too many observations in one batch")
		return
	}
	now := s.Now()
	for _, o := range obs {
		if err := o.Validate(now); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	cfg, _, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	oldest := now.Add(-cfg.Observe.Retention())
	isKey := s.isTicketKey(r.Context(), cfg)
	skipped, stripped := 0, 0
	keep := make([]core.Observation, 0, len(obs))
	for _, o := range obs {
		if !cfg.Observe.Enabled(dev.Name, o.Sensor) || o.End.Before(oldest) {
			skipped++
			continue
		}
		// what the device has no grant for is never stored, whatever the
		// agent sends (an old or broken one)
		a, ok := cfg.Observe.FilterGranted(dev.Name, o.Sensor, o.Attrs, isKey)
		if !ok {
			skipped++
			continue
		}
		if len(a) != len(o.Attrs) {
			stripped++
		}
		o.Attrs = a
		keep = append(keep, o)
	}
	stored, storeSkipped, err := s.st.PutObservations(r.Context(), dev.Name, keep)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"stored": stored, "skipped": skipped + storeSkipped, "stripped": stripped})
}

// isTicketKey returns a test whether a branch name holds a key of a known
// ticket project. The projects are looked up on first use only.
func (s *Server) isTicketKey(ctx context.Context, cfg core.Config) func(string) bool {
	var t *core.Tickets
	return func(v string) bool {
		if t == nil {
			tk := s.tickets(ctx, cfg)
			t = &tk
		}
		return len(t.BranchKeys(v)) > 0
	}
}
