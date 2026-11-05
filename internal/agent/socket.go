package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/server"
)

// Request is one line the CLI sends over the agent socket.
type Request struct {
	Cmd         string `json:"cmd"`
	Hat         string `json:"hat,omitempty"`
	Activity    string `json:"activity,omitempty"`
	HasActivity bool   `json:"has_activity,omitempty"`
	Answer      string `json:"answer,omitempty"` // for resume_activity: ask
	Detail      string `json:"detail,omitempty"` // free text below the activity
	At          string `json:"at,omitempty"`     // RFC3339
	Neutral     bool   `json:"neutral,omitempty"`
	From        string `json:"from,omitempty"` // RFC3339, for log
	To          string `json:"to,omitempty"`
	Data        string `json:"data,omitempty"` // YAML for config_import
	DryRun      bool   `json:"dry_run,omitempty"`
	Replace     bool   `json:"replace,omitempty"`
}

// Response is the answer: one line of JSON.
type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	State *core.State     `json:"state,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// StatusData is the data of a status answer.
type StatusData struct {
	State     core.State `json:"state"`
	HatName   string     `json:"hat_name,omitempty"`
	Color     string     `json:"color,omitempty"`
	Connected bool       `json:"connected"`
	Queued    int        `json:"queued"`
	Rejected  int        `json:"rejected,omitempty"` // events the server refused, set aside
	Changed   bool       `json:"changed"`            // false if the command was a no-op
	// Version is the agent's, ServerVersion the server's; Mismatch is set when
	// their protocols differ and one of them needs an update.
	Version       string `json:"version,omitempty"`
	ServerVersion string `json:"server_version,omitempty"`
	Mismatch      bool   `json:"mismatch,omitempty"`
}

// HatInfo describes a hat for clients that show a menu.
type HatInfo struct {
	Key        string   `json:"key"`
	Name       string   `json:"name"`
	Color      string   `json:"color,omitempty"`
	Activities []string `json:"activities"`
	Resume     string   `json:"resume"`
	Last       string   `json:"last,omitempty"` // last activity used, if still valid
	// Recent details of the last weeks, newest first, for suggestions
	Recent []core.RecentDetail `json:"recent,omitempty"`
}

// Snapshot is everything a menu client needs: the status, the hats and the
// server address (for "open the web UI").
type Snapshot struct {
	Status StatusData `json:"status"`
	Hats   []HatInfo  `json:"hats"`
	Server string     `json:"server,omitempty"`
}

// InfoData is the data of an info answer (resume settings of a hat).
type InfoData struct {
	Hat  string `json:"hat"`
	Name string `json:"name"`
	Mode string `json:"mode"`
	Last string `json:"last,omitempty"`
}

// LogData is the data of a log answer.
type LogData struct {
	server.IntervalsResponse
	Names map[string]string `json:"names"`
	// NotWork lists hats whose time is no working time (left out of the sum)
	NotWork map[string]bool `json:"not_work,omitempty"`
}

// ImportData is the data of a config_import answer.
type ImportData struct {
	server.ImportResult
}

func ok(data any) Response {
	r := Response{OK: true}
	if data != nil {
		r.Data, _ = json.Marshal(data)
	}
	return r
}

func fail(msg string) Response { return Response{Error: msg} }

// errText turns an error into a message in the user's language.
func errText(err error) string {
	var nf *core.NotFoundError
	var am *core.AmbiguousError
	switch {
	case errors.As(err, &nf):
		if len(nf.Available) == 0 {
			return i18n.T("err."+nf.Kind+".none", nf.Input)
		}
		return i18n.T("err."+nf.Kind+".notfound", nf.Input, strings.Join(nf.Available, ", "))
	case errors.As(err, &am):
		return i18n.T("err."+am.Kind+".ambiguous", am.Input, strings.Join(am.Candidates, ", "))
	case errors.Is(err, core.ErrNoHatToResume):
		return i18n.T("err.noresume")
	case errors.Is(err, core.ErrNoHat):
		return i18n.T("err.nohat")
	case errors.Is(err, core.ErrPaused):
		return i18n.T("err.paused")
	case errors.Is(err, core.ErrFuture):
		return i18n.T("err.future")
	case errors.Is(err, core.ErrBeforeEntry):
		return i18n.T("err.before_entry")
	case errors.Is(err, ErrUnauthorized):
		return i18n.T("err.unauthorized")
	}
	return err.Error()
}

// Handle executes one request.
func (a *Agent) Handle(ctx context.Context, req Request) Response {
	in := core.Input{Device: a.cfg.Device, Now: a.Now()}
	if req.At != "" {
		t, err := time.Parse(time.RFC3339, req.At)
		if err != nil {
			return fail(i18n.T("err.badtime", req.At))
		}
		in.At = t
	}
	switch req.Cmd {
	case "ping":
		return ok(nil)
	case "status":
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.status(false)
	case "hats":
		a.mu.Lock()
		defer a.mu.Unlock()
		return ok(a.snapshot())
	case "info":
		a.mu.Lock()
		defer a.mu.Unlock()
		ri, err := core.InfoOn(a.config, a.last, req.Hat)
		if err != nil {
			return fail(errText(err))
		}
		return ok(InfoData{Hat: ri.Hat.Key, Name: ri.Hat.DisplayName(), Mode: ri.Mode, Last: ri.Last})
	case "on", "do", "off", "detail":
		return a.change(ctx, req, in)
	case "log":
		return a.log(ctx, req)
	case "config_export":
		return a.configExport(ctx)
	case "config_import":
		return a.configImport(ctx, req)
	}
	return fail(i18n.T("err.unknowncmd", req.Cmd))
}

func (a *Agent) change(ctx context.Context, req Request, in core.Input) Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ev *core.Event
	var err error
	if len(a.config.ActiveHats()) == 0 && req.Cmd == "on" {
		return fail(i18n.T("err.nohats"))
	}
	switch req.Cmd {
	case "on":
		ev, err = core.PlanOn(a.config, a.state, a.last,
			core.OnArgs{Hat: req.Hat, Activity: req.Activity, HasActivity: req.HasActivity, Answer: req.Answer, Detail: req.Detail}, in)
	case "detail":
		ev, err = core.PlanDetail(a.state, req.Detail, in)
	case "do":
		ev, err = core.PlanDo(a.config, a.state, req.Activity, req.Detail, in)
	case "off":
		ev, err = core.PlanOff(a.state, req.Neutral, in)
	}
	if err != nil {
		return fail(errText(err))
	}
	if ev == nil {
		return a.status(false)
	}
	if err := a.submit(ctx, *ev); err != nil {
		return fail(err.Error())
	}
	return a.status(true)
}

// snapshot builds the menu data. Caller holds a.mu.
func (a *Agent) snapshot() Snapshot {
	r := a.status(false)
	snap := Snapshot{Server: a.cfg.Server, Hats: []HatInfo{}}
	json.Unmarshal(r.Data, &snap.Status)
	for _, h := range a.config.ActiveHats() {
		acts := h.Activities
		if acts == nil {
			acts = []string{}
		}
		info := HatInfo{Key: h.Key, Name: h.DisplayName(), Color: h.Color,
			Activities: acts, Resume: a.config.ResumeMode(h), Recent: a.recent[h.Key]}
		if l := a.last[h.Key]; l != "" {
			if act, err := core.ResolveActivity(h, l); err == nil {
				info.Last = act
			}
		}
		snap.Hats = append(snap.Hats, info)
	}
	return snap
}

// status builds the answer for status and for changes. Caller holds a.mu.
func (a *Agent) status(changed bool) Response {
	d := StatusData{State: a.state, Connected: a.connected, Queued: a.db.QueueLen(), Changed: changed,
		Version: Version, ServerVersion: a.serverVersion, Mismatch: a.mismatch}
	var kept []RejectedEvent
	if found, _ := a.db.Get("rejected", &kept); found {
		d.Rejected = len(kept)
	}
	if h, found := a.config.Hat(a.state.Hat); found {
		d.HatName, d.Color = h.DisplayName(), h.Color
	} else {
		d.HatName = a.state.Hat
	}
	r := ok(d)
	r.State = &a.state
	return r
}

func (a *Agent) log(ctx context.Context, req Request) Response {
	from, err1 := time.Parse(time.RFC3339, req.From)
	to, err2 := time.Parse(time.RFC3339, req.To)
	if err1 != nil || err2 != nil {
		return fail(i18n.T("err.badtime", req.From+" "+req.To))
	}
	res, err := a.client.Intervals(ctx, from, to)
	if err != nil {
		return fail(a.serverErr(err))
	}
	a.mu.Lock()
	names, notWork := map[string]string{}, map[string]bool{}
	for _, h := range a.config.Hats {
		names[h.Key] = h.DisplayName()
		if !h.CountsAsWork() {
			notWork[h.Key] = true
		}
	}
	a.mu.Unlock()
	return ok(LogData{IntervalsResponse: res, Names: names, NotWork: notWork})
}

func (a *Agent) serverErr(err error) string {
	var api *APIError
	if errors.As(err, &api) || errors.Is(err, ErrUnauthorized) {
		return errText(err)
	}
	return i18n.T("err.unreachable", a.cfg.Server)
}

// configExport answers from the server, or from the cache while offline.
func (a *Agent) configExport(ctx context.Context) Response {
	_, b, err := a.client.Config(ctx)
	if err != nil {
		a.mu.Lock()
		cfg := a.config
		a.mu.Unlock()
		if b, err = cfg.Export(); err != nil {
			return fail(err.Error())
		}
	}
	return ok(string(b))
}

func (a *Agent) configImport(ctx context.Context, req Request) Response {
	cfg, err := core.ParseConfig([]byte(req.Data))
	if err != nil {
		return fail(err.Error())
	}
	res, err := a.client.PutConfig(ctx, []byte(req.Data), req.DryRun, req.Replace)
	if err != nil {
		return fail(a.serverErr(err))
	}
	// Files are checked here, on the machine where the import happens.
	res.Warnings = append(cfg.Warnings(true), res.Warnings...)
	res.Warnings = dedupe(res.Warnings)
	if !req.DryRun {
		a.refreshConfig(ctx)
	}
	return ok(ImportData{res})
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Serve listens on the agent socket until ctx is cancelled.
func (a *Agent) Serve(ctx context.Context, path string) error {
	if len(path) > 100 {
		return fmt.Errorf("socket path too long for a Unix socket (%d characters): %s; set a shorter XDG_RUNTIME_DIR", len(path), path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return fmt.Errorf("another agent is already running (%s)", path)
	}
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	go func() { <-ctx.Done(); l.Close(); os.Remove(path) }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go a.serveConn(ctx, c)
	}
}

func (a *Agent) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	enc := json.NewEncoder(c)
	for {
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		line, _ := r.ReadBytes('\n')
		if len(line) == 0 {
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(fail("bad request: " + err.Error()))
			continue
		}
		if req.Cmd == "watch" {
			c.SetReadDeadline(time.Time{})
			a.watch(ctx, c, enc)
			return
		}
		c.SetWriteDeadline(time.Now().Add(60 * time.Second))
		if err := enc.Encode(a.Handle(ctx, req)); err != nil {
			return
		}
	}
}

// Call sends one request to the agent at path and returns its answer. If no
// agent runs, it returns ErrNotRunning.
func Call(path string, req Request) (Response, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return Response{}, ErrNotRunning
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(90 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// ErrNotRunning means the agent socket is not there.
var ErrNotRunning = errors.New("agent not running")

// watch keeps the connection open and sends a snapshot now and after every
// change of state, configuration or connection.
func (a *Agent) watch(ctx context.Context, c net.Conn, enc *json.Encoder) {
	ch := a.subscribe()
	defer a.unsubscribe(ch)
	send := func() bool {
		a.mu.Lock()
		resp := ok(a.snapshot())
		a.mu.Unlock()
		c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return enc.Encode(resp) == nil
	}
	if !send() {
		return
	}
	// A heartbeat finds clients that went away without a change in between.
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-tick.C:
		}
		if !send() {
			return
		}
	}
}

// Watch connects to the agent and calls fn for every snapshot until the
// connection ends or ctx is cancelled.
func Watch(ctx context.Context, path string, fn func(Snapshot)) error {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return ErrNotRunning
	}
	defer c.Close()
	go func() { <-ctx.Done(); c.Close() }()
	if err := json.NewEncoder(c).Encode(Request{Cmd: "watch"}); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(c))
	for {
		var resp Response
		if err := dec.Decode(&resp); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var snap Snapshot
		if err := json.Unmarshal(resp.Data, &snap); err == nil {
			fn(snap)
		}
	}
}
