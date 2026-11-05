package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/server"
)

// ErrUnauthorized means the server rejected the device token.
var ErrUnauthorized = errors.New("the server rejected the device token")

// Client talks to the server's REST API.
type Client struct {
	base  string
	token string
	http  *http.Client // short requests
}

// NewClient creates a client for a server URL and device token.
func NewClient(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) req(ctx context.Context, method, path string, body []byte, ctype string) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	return r, nil
}

// APIError is an error answer of the server.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("server: %s (%d)", e.Message, e.Status) }

func (c *Client) do(ctx context.Context, method, path string, body []byte, ctype string, out any) ([]byte, error) {
	r, err := c.req(ctx, method, path, body, ctype)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return nil, &APIError{resp.StatusCode, e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// PostEvents sends events; the server skips ones it already has.
// PostEvents sends events and returns the server's event revision after
// storing them (0 from servers that do not report it).
func (c *Client) PostEvents(ctx context.Context, ev []core.Event) (int64, error) {
	b, _ := json.Marshal(ev)
	var out struct {
		EventRev int64 `json:"event_rev"`
	}
	_, err := c.do(ctx, "POST", "/api/events", b, "application/json", &out)
	return out.EventRev, err
}

// State fetches the current state.
func (c *Client) State(ctx context.Context) (server.StateResponse, error) {
	var s server.StateResponse
	_, err := c.do(ctx, "GET", "/api/state", nil, "", &s)
	return s, err
}

// Config fetches the configuration.
func (c *Client) Config(ctx context.Context) (core.Config, []byte, error) {
	b, err := c.do(ctx, "GET", "/api/config", nil, "", nil)
	if err != nil {
		return core.Config{}, nil, err
	}
	cfg, err := core.ParseConfig(b)
	return cfg, b, err
}

// PutConfig imports a configuration.
func (c *Client) PutConfig(ctx context.Context, yamlData []byte, dry, replace bool) (server.ImportResult, error) {
	q := url.Values{}
	if dry {
		q.Set("dry_run", "1")
	}
	if replace {
		q.Set("replace", "1")
	}
	var res server.ImportResult
	_, err := c.do(ctx, "PUT", "/api/config?"+q.Encode(), yamlData, "application/yaml", &res)
	return res, err
}

// Intervals fetches intervals and totals of a period.
func (c *Client) Intervals(ctx context.Context, from, to time.Time) (server.IntervalsResponse, error) {
	q := url.Values{}
	q.Set("from", from.UTC().Format(time.RFC3339))
	q.Set("to", to.UTC().Format(time.RFC3339))
	var r server.IntervalsResponse
	_, err := c.do(ctx, "GET", "/api/intervals?"+q.Encode(), nil, "", &r)
	return r, err
}

// Hello registers the device's capabilities and tells the server which
// version this agent is; the answer names the server's version.
func (c *Client) Hello(ctx context.Context, caps any) (server.HelloResponse, error) {
	b, _ := json.Marshal(map[string]any{"capabilities": caps, "version": Version, "protocol": server.Protocol})
	var r server.HelloResponse
	_, err := c.do(ctx, "POST", "/api/devices/hello", b, "application/json", &r)
	return r, err
}

// Stream reads the SSE stream until it ends or ctx is cancelled.
func (c *Client) Stream(ctx context.Context, onOpen func(), handle func(event, data string)) error {
	r, err := c.req(ctx, "GET", "/api/stream", nil, "")
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(r) // no timeout: the stream is long-lived
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream: %s", resp.Status)
	}
	if onOpen != nil {
		onOpen()
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			handle(event, strings.TrimPrefix(line, "data: "))
			event = ""
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}
