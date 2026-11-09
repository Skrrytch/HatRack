package server

import (
	"context"
	"log"
	"time"
)

// retentionEvery is how often old observations are deleted.
const retentionEvery = time.Hour

// RunRetention deletes expired observations at once and then every hour, until
// ctx ends.
func (s *Server) RunRetention(ctx context.Context) {
	s.pruneObservations(ctx)
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pruneObservations(ctx)
		}
	}
}

// pruneObservations does one pass: it deletes the observations that ended
// before now minus the configured retention.
func (s *Server) pruneObservations(ctx context.Context) {
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		log.Printf("observation retention: %v", err)
		return
	}
	n, err := s.st.DeleteObservationsBefore(ctx, s.Now().Add(-cfg.Observe.Retention()))
	if err != nil {
		log.Printf("observation retention: %v", err)
		return
	}
	if n > 0 {
		log.Printf("observation retention: deleted %d spans", n)
	}
	// the history of grants is kept a year, independent of observations
	if _, err := s.st.DeleteGrantLogBefore(ctx, s.Now().AddDate(-1, 0, 0)); err != nil {
		log.Printf("grant history retention: %v", err)
	}
}
