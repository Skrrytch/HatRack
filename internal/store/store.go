// Package store defines the storage interface of the core and its SQLite
// implementation. The server and, later, the standalone agent share it.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"hatrack/internal/core"
)

// ErrNotFound is returned when a device or record does not exist.
var ErrNotFound = errors.New("not found")

// MCPToken lets an AI assistant use /mcp. Scopes: "read" (always) and
// "observe" (may read observations: app and window title); set at creation.
type MCPToken struct {
	ID       string
	Name     string
	Scopes   []string
	Created  time.Time
	LastUsed time.Time
}

// Device is a machine that runs an agent.
type Device struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	LastSeen     time.Time       `json:"last_seen,omitzero"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	// CanConfig lets the device change the configuration (hat config
	// import). Off by default: whoever writes the configuration decides
	// what every agent runs (scripts, programs).
	CanConfig bool `json:"can_config,omitempty"`
}

// ObsStat is the number and total length of one device's spans of one sensor.
type ObsStat struct {
	Count    int
	Duration time.Duration
}

// GrantChange is one change of a grant on a device; Old and New are levels,
// "" for off. Deleted tells whether stored data was removed with it.
type GrantChange struct {
	At       time.Time
	Device   string
	Grant    string
	Old, New string
	Deleted  bool
}

// Store is the storage interface of the core.
type Store interface {
	// AppendEvents inserts events; events whose ID exists are skipped.
	// It returns the number of newly inserted events.
	AppendEvents(ctx context.Context, ev []core.Event) (int, error)
	// EventsBetween returns events with from <= ts < to ordered by time.
	// A zero from or to leaves that side open.
	EventsBetween(ctx context.Context, from, to time.Time) ([]core.Event, error)
	// Effective returns the events before to (zero: all) that count, with
	// corrections applied.
	Effective(ctx context.Context, to time.Time) ([]core.Event, error)
	// CurrentState derives the current state from the log.
	CurrentState(ctx context.Context) (core.State, error)
	// LastActivities returns, per hat key, the last activity used.
	LastActivities(ctx context.Context) (map[string]string, error)
	// EventRev grows with every stored event. Clients use it to tell a
	// fresh state from one sent before their own events arrived.
	EventRev(ctx context.Context) (int64, error)

	GetConfig(ctx context.Context) (core.Config, int64, error)
	PutConfig(ctx context.Context, c core.Config) (int64, error)

	// AddDevice registers a device and returns its token (shown only once).
	AddDevice(ctx context.Context, name string) (Device, string, error)
	DeviceByToken(ctx context.Context, token string) (Device, error)
	// SetDeviceConfig allows or forbids a device to change the configuration.
	SetDeviceConfig(ctx context.Context, id string, allowed bool) error
	Devices(ctx context.Context) ([]Device, error)
	// MCP tokens: created with their scopes, the token is returned once.
	AddMCPToken(ctx context.Context, name string, scopes []string) (MCPToken, string, error)
	MCPTokenByToken(ctx context.Context, token string) (MCPToken, error)
	MCPTokens(ctx context.Context) ([]MCPToken, error)
	TouchMCPToken(ctx context.Context, id string) error
	DeleteMCPToken(ctx context.Context, id string) error
	// TicketLinks are the projects learned from issue links in details.
	TicketLinks(ctx context.Context) (core.TicketLinks, error)
	// ResetTicketLink forgets a project; the next issue link teaches it again.
	ResetTicketLink(ctx context.Context, project string) error
	// DeleteDevice removes a device; its token stops working at once.
	DeleteDevice(ctx context.Context, id string) error
	// PutObservations stores spans of one device (the argument is the truth,
	// Observation.Device is ignored). A known ID only moves its end forward
	// (end = max(end, new end)); start, sensor and attrs stay. stored counts
	// inserted spans and spans whose end grew; skipped counts IDs that belong
	// to another device. A known ID of this device with an end not later is
	// neither. Invalid input (empty ID or sensor, end before start) is an
	// error and stores nothing.
	PutObservations(ctx context.Context, device string, obs []core.Observation) (stored, skipped int, err error)
	// ObservationsBetween returns spans overlapping [from, to), ordered by
	// start; "" for device or sensor means all. A zero from or to leaves that
	// side open.
	ObservationsBetween(ctx context.Context, from, to time.Time, device, sensor string) ([]core.Observation, error)
	// ObservationStats sums, per device and sensor, the spans overlapping
	// [from, to), each clipped to the period.
	ObservationStats(ctx context.Context, from, to time.Time) (map[string]map[string]ObsStat, error)
	// DeleteObservationsBefore removes spans that ended before t.
	DeleteObservationsBefore(ctx context.Context, t time.Time) (int, error)
	// ClearObservations deletes everything observed up to t: spans that end
	// by t go, spans running past t start at t. t is remembered, so spans a
	// device sends later are cut the same way. It returns how many spans
	// were deleted.
	ClearObservations(ctx context.Context, t time.Time) (int, error)
	// ObservationSummary counts the stored spans and gives the earliest start
	// (zero when there are none).
	ObservationSummary(ctx context.Context) (n int, first time.Time, err error)
	// FilterObservations runs f over every span of a device and stores what
	// it returns: changed attributes are written, false deletes the span.
	// It returns how many spans changed and how many were deleted.
	FilterObservations(ctx context.Context, device string, f func(core.Observation) (core.Attrs, bool)) (changed, deleted int, err error)
	// LastObservationWith returns the end of the newest span of a device that
	// has one of the attributes, or belongs to the sensor (if not "").
	LastObservationWith(ctx context.Context, device string, attrs []string, sensor string) (time.Time, error)
	// AddGrantChange records a change of a grant; GrantLog returns the
	// changes since a time, newest first; DeleteGrantLogBefore prunes.
	AddGrantChange(ctx context.Context, c GrantChange) error
	GrantLog(ctx context.Context, since time.Time) ([]GrantChange, error)
	DeleteGrantLogBefore(ctx context.Context, t time.Time) (int, error)
	// HatUsed reports whether any event (or correction) refers to the hat.
	HatUsed(ctx context.Context, key string) (bool, error)
	TouchDevice(ctx context.Context, id string, caps json.RawMessage) error

	// SetPasswordHash stores the web UI password hash and ends all sessions.
	SetPasswordHash(ctx context.Context, hash string) error
	// PasswordHash returns the stored hash, or "" if no password is set.
	PasswordHash(ctx context.Context) (string, error)
	// CreateSession stores the hash of a session token until it expires.
	CreateSession(ctx context.Context, tokenHash string, expires time.Time) error
	SessionValid(ctx context.Context, tokenHash string, now time.Time) (bool, error)
	DeleteSession(ctx context.Context, tokenHash string) error

	// Backup writes a consistent copy of the database to path.
	Backup(ctx context.Context, path string) error
	Close() error
}
