package server

import (
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
	skipped := 0
	keep := make([]core.Observation, 0, len(obs))
	for _, o := range obs {
		if !cfg.Observe.Enabled(dev.Name, o.Sensor) || o.End.Before(oldest) {
			skipped++
			continue
		}
		keep = append(keep, o)
	}
	stored, storeSkipped, err := s.st.PutObservations(r.Context(), dev.Name, keep)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"stored": stored, "skipped": skipped + storeSkipped})
}
