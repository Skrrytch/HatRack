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

// Device is a machine that runs an agent.
type Device struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	LastSeen     time.Time       `json:"last_seen,omitzero"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

// Store is the storage interface of the core.
type Store interface {
	// AppendEvents inserts events; events whose ID exists are skipped.
	// It returns the number of newly inserted events.
	AppendEvents(ctx context.Context, ev []core.Event) (int, error)
	// EventsBetween returns events with from <= ts < to ordered by time.
	// A zero from or to leaves that side open.
	EventsBetween(ctx context.Context, from, to time.Time) ([]core.Event, error)
	// CurrentState derives the current state from the log.
	CurrentState(ctx context.Context) (core.State, error)
	// LastActivities returns, per hat key, the last activity used.
	LastActivities(ctx context.Context) (map[string]string, error)

	GetConfig(ctx context.Context) (core.Config, int64, error)
	PutConfig(ctx context.Context, c core.Config) (int64, error)

	// AddDevice registers a device and returns its token (shown only once).
	AddDevice(ctx context.Context, name string) (Device, string, error)
	DeviceByToken(ctx context.Context, token string) (Device, error)
	Devices(ctx context.Context) ([]Device, error)
	TouchDevice(ctx context.Context, id string, caps json.RawMessage) error

	// Backup writes a consistent copy of the database to path.
	Backup(ctx context.Context, path string) error
	Close() error
}
