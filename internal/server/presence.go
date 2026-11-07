package server

import (
	"net/http"
	"sort"
	"time"

	"hatrack/internal/store"
)

// Presence: whether the user is at another computer. An agent that wants to
// stop the clock for an absence asks here first, so a computer that sits idle
// does not stop the clock while another one is in use.

// Presence timing: a device is online if it was seen this recently, and away
// if its idle span reaches back to the last checkpoints (spans are extended
// every minute).
const (
	presenceOnline   = 90 * time.Second
	presenceFresh    = 150 * time.Second
	presenceMaxChain = 24 * time.Hour
)

// PresenceDevice is what is known about another device.
type PresenceDevice struct {
	Device    string    `json:"device"`
	Online    bool      `json:"online"`
	Known     bool      `json:"known"` // idle detection is on and works there
	Away      bool      `json:"away"`
	AwaySince time.Time `json:"away_since,omitzero"`
}

// PresenceResponse is the answer of GET /api/presence.
type PresenceResponse struct {
	Devices []PresenceDevice `json:"devices"`
}

// getPresence answers for every device except the caller.
func (s *Server) getPresence(w http.ResponseWriter, r *http.Request, caller store.Device) {
	ctx := r.Context()
	devs, err := s.st.Devices(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	cfg, _, err := s.st.GetConfig(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	now := s.Now()
	out := PresenceResponse{Devices: []PresenceDevice{}}
	for _, d := range devs {
		if d.ID == caller.ID {
			continue
		}
		p := PresenceDevice{Device: d.Name}
		p.Online = !d.LastSeen.IsZero() && now.Sub(d.LastSeen) < presenceOnline
		idle, reported := capsOf(d)["sensor:idle"]
		p.Known = cfg.Observe.Enabled(d.Name, "idle") && reported && idle.Available
		if p.Known {
			obs, err := s.st.ObservationsBetween(ctx, now.Add(-presenceMaxChain), time.Time{}, d.Name, "idle")
			if err != nil {
				serverError(w, err)
				return
			}
			// the newest span, joined with the ones right before it (an
			// inactive span followed by a locked one is one absence)
			sort.Slice(obs, func(i, j int) bool { return obs[i].Start.Before(obs[j].Start) })
			if n := len(obs); n > 0 && !obs[n-1].End.Before(now.Add(-presenceFresh)) {
				p.Away, p.AwaySince = true, obs[n-1].Start
				for i := n - 2; i >= 0 && !obs[i].End.Before(p.AwaySince.Add(-presenceFresh)); i-- {
					if obs[i].Start.Before(p.AwaySince) {
						p.AwaySince = obs[i].Start
					}
				}
			}
		}
		out.Devices = append(out.Devices, p)
	}
	writeJSON(w, http.StatusOK, out)
}
