package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"hatrack/internal/core"
)

// DB is the agent's local database: offline queue and cached state.
type DB struct{ db *sql.DB }

// OpenDB opens (and creates) the database file.
func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
	  CREATE TABLE IF NOT EXISTS queue (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, json TEXT NOT NULL);
	  CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init %s: %w", path, err)
	}
	return &DB{db}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Enqueue stores an event until the server has it.
func (d *DB) Enqueue(ev core.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT OR IGNORE INTO queue (id, json) VALUES (?, ?)`, ev.ID, string(b))
	return err
}

// Queue returns up to limit queued events in the order they were created.
func (d *DB) Queue(limit int) ([]core.Event, error) {
	rows, err := d.db.Query(`SELECT json FROM queue ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Event
	for rows.Next() {
		var s string
		var ev core.Event
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(s), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Dequeue removes events that were delivered.
func (d *DB) Dequeue(ev []core.Event) error {
	for _, e := range ev {
		if _, err := d.db.Exec(`DELETE FROM queue WHERE id = ?`, e.ID); err != nil {
			return err
		}
	}
	return nil
}

// QueueLen returns the number of queued events.
func (d *DB) QueueLen() int {
	var n int
	d.db.QueryRow(`SELECT COUNT(*) FROM queue`).Scan(&n)
	return n
}

// Get reads a cached value and decodes it from JSON into v. It reports
// whether the key exists.
func (d *DB) Get(key string, v any) (bool, error) {
	var s string
	err := d.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&s)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(s), v)
}

// Put stores a value as JSON.
func (d *DB) Put(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT INTO kv (key, value) VALUES (?, ?)
	  ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}
