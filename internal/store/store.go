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

// MCPToken lets an AI assistant use /mcp. Scopes: "read" for now.
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
