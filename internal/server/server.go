// Package server is HatRack Server: the REST API, the SSE stream and the
// read-only "Now" page around the core and a Store.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
	"hatrack/internal/store"
)

// Server serves the API for agents and the "Now" page.
// Version is reported to MCP clients; the command line sets it.
var Version = "dev"

type Server struct {
	st  store.Store
	reg *modules.Registry
	hub *hub
	// Now returns the current time; tests replace it.
	Now func() time.Time
	// Loc is the time zone for pages when the browser does not tell its own.
	Loc     *time.Location
	limiter *loginLimiter
	mux     *http.ServeMux
}

// New creates a server. The registry is used to validate module settings of
// imported configurations.
func New(st store.Store, reg *modules.Registry) *Server {
	s := &Server{st: st, reg: reg, hub: newHub(), Now: time.Now, Loc: time.Local, mux: http.NewServeMux(),
		limiter: &loginLimiter{Delay: defaultDelay}}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	s.mux.HandleFunc("POST /api/events", s.api(s.postEvents))
	s.mux.HandleFunc("GET /api/events", s.api(s.getEvents))
	s.mux.HandleFunc("GET /api/state", s.api(s.getState))
	s.mux.HandleFunc("GET /api/intervals", s.api(s.getIntervals))
	s.mux.HandleFunc("GET /api/config", s.api(s.getConfig))
	s.mux.HandleFunc("PUT /api/config", s.api(s.putConfig))
	s.mux.HandleFunc("GET /api/stream", s.api(s.stream))
	s.mux.HandleFunc("POST /api/devices/hello", s.api(s.hello))
	s.mux.HandleFunc("GET /{$}", s.page(false, s.nowPage))
	s.mux.HandleFunc("GET /day", s.page(false, s.dayPage))
	s.mux.HandleFunc("GET /week", s.page(false, s.weekPage))
	s.mux.HandleFunc("GET /week.csv", s.page(false, s.weekCSV))
	s.mux.HandleFunc("POST /action", s.page(true, s.action))
	s.mux.HandleFunc("GET /ui/stream", s.page(false, s.uiStream))
	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.loginPage)
	s.mux.HandleFunc("POST /logout", s.page(true, s.logout))
	s.mux.HandleFunc("GET /static/{file}", s.static)
	s.mux.Handle("/mcp", s.mcpHandler(Version))
	s.mux.HandleFunc("POST /day/edit", s.page(true, s.dayEdit))
	s.mux.HandleFunc("GET /setup", s.page(true, s.setupPage))
	s.mux.HandleFunc("POST /setup/hats", s.page(true, s.hatsAction))
	s.mux.HandleFunc("GET /setup/hat", s.page(true, s.hatPage))
	s.mux.HandleFunc("POST /setup/hat", s.page(true, s.hatSave))
	s.mux.HandleFunc("POST /setup/tickets", s.page(true, s.ticketsAction))
	s.mux.HandleFunc("GET /setup/ai", s.page(true, s.aiPage))
	s.mux.HandleFunc("POST /setup/ai", s.page(true, s.aiAction))
	s.mux.HandleFunc("GET /setup/devices", s.page(true, s.devicesPage))
	s.mux.HandleFunc("POST /setup/devices", s.page(true, s.devicesAction))
	s.mux.HandleFunc("GET /setup/defaults", s.page(true, s.defaultsPage))
	s.mux.HandleFunc("POST /setup/defaults", s.page(true, s.defaultsSave))
	s.mux.HandleFunc("POST /setup/import", s.page(true, s.importAction))
	s.mux.HandleFunc("GET /setup/export.yaml", s.page(true, s.exportConfig))
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

type ctxKey struct{}

// api wraps a handler with bearer token authentication.
func (s *Server) api(h func(http.ResponseWriter, *http.Request, store.Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			httpError(w, http.StatusUnauthorized, "missing token")
			return
		}
		dev, err := s.st.DeviceByToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, http.StatusUnauthorized, "unknown token")
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		h(w, r, dev)
	}
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	httpError(w, http.StatusInternalServerError, "internal error")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// StateResponse is the current state plus the data agents need to plan.
type StateResponse struct {
	State          core.State        `json:"state"`
	LastActivities map[string]string `json:"last_activities"`
	// Recent: hat -> the details of the last weeks (short form, newest
	// first), for suggestions in the panel
	Recent     map[string][]core.RecentDetail `json:"recent,omitempty"`
	ConfigRev  int64                          `json:"config_rev"`
	EventRev   int64                          `json:"event_rev"` // grows with every stored event
	ServerTime time.Time                      `json:"server_time"`
}

// recentPerHat caps the detail suggestions sent per hat.
const recentPerHat = 50

func (s *Server) stateResponse(ctx context.Context) (StateResponse, error) {
	st, err := s.st.CurrentState(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	last, err := s.st.LastActivities(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	_, rev, err := s.st.GetConfig(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	erev, err := s.st.EventRev(ctx)
	if err != nil {
		return StateResponse{}, err
	}
	resp := StateResponse{State: st, LastActivities: last, ConfigRev: rev, EventRev: erev, ServerTime: s.Now().UTC()}
	since := s.Now().Add(-core.RecentWindow)
	if ev, err := s.st.Effective(ctx, time.Time{}); err == nil {
		resp.Recent = core.RecentByHat(ev, since, recentPerHat)
	}
	return resp, nil
}

func (s *Server) broadcastState(ctx context.Context) {
	resp, err := s.stateResponse(ctx)
	if err != nil {
		log.Printf("broadcast: %v", err)
		return
	}
	b, _ := json.Marshal(resp)
	s.hub.broadcast(message{Event: "state", Data: string(b)})
	s.publishCore(ctx)
}

func (s *Server) postEvents(w http.ResponseWriter, r *http.Request, dev store.Device) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	var evs []core.Event
	if t := strings.TrimSpace(string(body)); strings.HasPrefix(t, "{") {
		var e core.Event
		if err := json.Unmarshal(body, &e); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		evs = []core.Event{e}
	} else if err := json.Unmarshal(body, &evs); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	for i := range evs {
		if evs[i].Device == "" {
			evs[i].Device = dev.Name
		}
		if err := evs[i].Validate(); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		if evs[i].Type == core.EvCorrection {
			// corrections rewrite history; they come from the web UI only
			httpError(w, http.StatusForbidden, "devices may not send corrections")
			return
		}
	}
	n, err := s.st.AppendEvents(r.Context(), evs)
	if err != nil {
		serverError(w, err)
		return
	}
	resp, err := s.stateResponse(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	if n > 0 {
		s.broadcastState(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{"inserted": n, "state": resp.State, "event_rev": resp.EventRev})
}

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}

func (s *Server) period(r *http.Request) (from, to time.Time, err error) {
	if from, err = parseTime(r.URL.Query().Get("from")); err != nil {
		return from, to, fmt.Errorf("from: %w", err)
	}
	if to, err = parseTime(r.URL.Query().Get("to")); err != nil {
		return from, to, fmt.Errorf("to: %w", err)
	}
	return from, to, nil
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request, _ store.Device) {
	from, to, err := s.period(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	ev, err := s.st.EventsBetween(r.Context(), from, to)
	if err != nil {
		serverError(w, err)
		return
	}
	if ev == nil {
		ev = []core.Event{}
	}
	writeJSON(w, http.StatusOK, ev)
}

// IntervalsResponse is the answer of GET /api/intervals.
type IntervalsResponse struct {
	From      time.Time       `json:"from"`
	To        time.Time       `json:"to"`
	Intervals []core.Interval `json:"intervals"`
	Totals    []core.HatTotal `json:"totals"`
}

func (s *Server) getIntervals(w http.ResponseWriter, r *http.Request, _ store.Device) {
	from, to, err := s.period(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := s.Now().UTC()
	if to.IsZero() {
		to = now
	}
	// Intervals need the events before the period, so load everything up to `to`.
	ev, err := s.st.Effective(r.Context(), to)
	if err != nil {
		serverError(w, err)
		return
	}
	iv := core.Intervals(ev, now)
	if !from.IsZero() {
		iv = core.Clip(iv, from, to)
	} else {
		iv = core.Clip(iv, time.Time{}, to)
	}
	if iv == nil {
		iv = []core.Interval{}
	}
	tot := core.Totals(iv)
	if tot == nil {
		tot = []core.HatTotal{}
	}
	writeJSON(w, http.StatusOK, IntervalsResponse{From: from, To: to, Intervals: iv, Totals: tot})
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request, _ store.Device) {
	resp, err := s.stateResponse(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request, _ store.Device) {
	cfg, rev, err := s.st.GetConfig(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	b, err := cfg.Export()
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("X-Config-Rev", fmt.Sprint(rev))
	w.Write(b)
}

// ImportResult is the answer of PUT /api/config.
type ImportResult struct {
	DryRun   bool     `json:"dry_run"`
	Changes  []string `json:"changes"`
	Warnings []string `json:"warnings"`
	Rev      int64    `json:"rev"`
}

var putMu sync.Mutex

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request, dev store.Device) {
	if !dev.CanConfig {
		// the configuration says what every agent runs (scripts, programs);
		// a device token alone must not be enough to change it
		httpError(w, http.StatusForbidden, "device "+dev.Name+" may not change the configuration; allow it in the web UI under Setup → Devices")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	q := r.URL.Query()
	dry, replace := q.Get("dry_run") != "" && q.Get("dry_run") != "0", q.Get("replace") != "" && q.Get("replace") != "0"
	res, err := s.importConfig(r.Context(), body, dry, replace)
	var bad badConfig
	switch {
	case errors.As(err, &bad):
		httpError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		serverError(w, err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// badConfig is a configuration the user has to fix (as opposed to a failure
// of the server).
type badConfig struct{ error }

// importConfig merges a YAML configuration into the stored one (see
// core.Merge). With dry it only reports what would change.
func (s *Server) importConfig(ctx context.Context, data []byte, dry, replace bool) (ImportResult, error) {
	in, err := core.ParseConfig(data)
	if err != nil {
		return ImportResult{}, badConfig{err}
	}
	if err := modules.Validate(s.reg, in); err != nil {
		return ImportResult{}, badConfig{err}
	}
	var res ImportResult
	_, err = s.editConfig(ctx, dry, func(cur *core.Config) error {
		merged, changes := core.Merge(*cur, in, replace)
		*cur = merged
		res = ImportResult{DryRun: dry, Changes: changes, Warnings: merged.Warnings(false)}
		if len(changes) == 0 {
			return errUnchanged
		}
		return nil
	}, &res.Rev)
	if res.Changes == nil {
		res.Changes = []string{}
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	return res, err
}

// errUnchanged tells editConfig that there is nothing to store.
var errUnchanged = errors.New("unchanged")

// editConfig loads the configuration, lets fn change it, checks the result
// and stores it, telling the agents. Unless dry, of course. rev, if given,
// receives the revision of the stored (or current) configuration.
func (s *Server) editConfig(ctx context.Context, dry bool, fn func(*core.Config) error, rev ...*int64) (core.Config, error) {
	putMu.Lock()
	defer putMu.Unlock()
	cfg, cur, err := s.st.GetConfig(ctx)
	if err != nil {
		return cfg, err
	}
	if cfg.Version == 0 {
		cfg.Version = core.ConfigVersion
	}
	setRev := func(v int64) {
		if len(rev) > 0 && rev[0] != nil {
			*rev[0] = v
		}
	}
	setRev(cur)
	if err := fn(&cfg); err != nil {
		if errors.Is(err, errUnchanged) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, badConfig{err}
	}
	if err := modules.Validate(s.reg, cfg); err != nil {
		return cfg, badConfig{err}
	}
	if dry {
		return cfg, nil
	}
	n, err := s.st.PutConfig(ctx, cfg)
	if err != nil {
		return cfg, err
	}
	setRev(n)
	b, _ := json.Marshal(map[string]int64{"rev": n})
	s.hub.broadcast(message{Event: "config", Data: string(b)})
	s.configureCore(cfg)
	s.publishCore(ctx) // names and colours may have changed
	return cfg, nil
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request, dev store.Device) {
	var in struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if r.ContentLength != 0 {
		// up to 8 MiB: the capabilities carry wallpaper previews
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil && err != io.EOF {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := s.st.TouchDevice(r.Context(), dev.ID, in.Capabilities); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": dev.Name, "server_time": s.Now().UTC()})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, dev store.Device) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The first message is the current state, so an agent that connects
	// always starts from the truth.
	resp, err := s.stateResponse(r.Context())
	if err != nil {
		return
	}
	b, _ := json.Marshal(resp)
	writeSSE(w, message{Event: "state", Data: string(b)})
	fl.Flush()
	s.st.TouchDevice(r.Context(), dev.ID, nil)

	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return // too slow; the client reconnects and gets the state again
			}
			writeSSE(w, m)
			fl.Flush()
		case <-tick.C:
			io.WriteString(w, ": keepalive\n\n")
			fl.Flush()
			s.st.TouchDevice(r.Context(), dev.ID, nil)
		}
	}
}

func writeSSE(w io.Writer, m message) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Event, m.Data)
}

type message struct{ Event, Data string }

type hub struct {
	mu   sync.Mutex
	subs map[chan message]struct{}
}

func newHub() *hub { return &hub{subs: map[chan message]struct{}{}} }

func (h *hub) subscribe() chan message {
	ch := make(chan message, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan message) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(m message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}
