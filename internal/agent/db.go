package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"

	"hatrack/internal/core"
)

// DB is the agent's local database: offline queue and cached state.
type DB struct {
	db *sql.DB

	obsMu      sync.Mutex // guards the observation queue and its count
	obsN       int        // rows in obs_queue, -1 until counted
	obsSeq     int64      // highest seq handed out
	obsTrimmed bool       // the bound was hit and logged
}

// MaxObsQueue bounds the observation queue: a server that never answers
// must not fill the disk.
const MaxObsQueue = 20000

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
	  CREATE TABLE IF NOT EXISTS obs_queue (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, json TEXT NOT NULL);
	  CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init %s: %w", path, err)
	}
	return &DB{db: db, obsN: -1}, nil
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

// countObs returns the number of queued observations. Caller holds obsMu.
func (d *DB) countObs() int {
	if d.obsN < 0 {
		var n int
		if d.db.QueryRow(`SELECT COUNT(*) FROM obs_queue`).Scan(&n) != nil {
			return 0
		}
		d.obsN = n
		d.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM obs_queue`).Scan(&d.obsSeq)
	}
	return d.obsN
}

// EnqueueObs stores an observation until the server has it. An observation
// that is queued already (same ID) is replaced by the newer version and keeps
// its place. Beyond MaxObsQueue the oldest rows are dropped.
func (d *DB) EnqueueObs(o core.Observation) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	d.obsMu.Lock()
	defer d.obsMu.Unlock()
	n := d.countObs()
	// one statement: a known ID keeps its place, a new row gets a higher seq
	var seq int64
	err = d.db.QueryRow(`INSERT INTO obs_queue (id, json) VALUES (?, ?)
	  ON CONFLICT(id) DO UPDATE SET json = excluded.json RETURNING seq`, o.ID, string(b)).Scan(&seq)
	if err != nil {
		return err
	}
	if seq <= d.obsSeq {
		return nil // replaced
	}
	d.obsSeq = seq
	n++
	d.obsN = n
	if n > MaxObsQueue {
		res, err := d.db.Exec(`DELETE FROM obs_queue WHERE seq IN (SELECT seq FROM obs_queue ORDER BY seq LIMIT ?)`, n-MaxObsQueue)
		if err != nil {
			return err
		}
		k, _ := res.RowsAffected()
		d.obsN -= int(k)
		if !d.obsTrimmed {
			d.obsTrimmed = true
			log.Printf("observation queue is full (%d), dropping the oldest", MaxObsQueue)
		}
	}
	return nil
}

// ObsQueue returns up to limit queued observations, oldest first.
func (d *DB) ObsQueue(limit int) ([]core.Observation, error) {
	rows, err := d.db.Query(`SELECT json FROM obs_queue ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Observation
	for rows.Next() {
		var s string
		var o core.Observation
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(s), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DequeueObs removes observations that were delivered. Only the version that
// was sent goes: a newer version of the same span stays queued.
func (d *DB) DequeueObs(sent []core.Observation) error {
	d.obsMu.Lock()
	defer d.obsMu.Unlock()
	d.countObs()
	for _, o := range sent {
		b, err := json.Marshal(o)
		if err != nil {
			return err
		}
		res, err := d.db.Exec(`DELETE FROM obs_queue WHERE id = ? AND json = ?`, o.ID, string(b))
		if err != nil {
			return err
		}
		k, _ := res.RowsAffected()
		d.obsN -= int(k)
	}
	return nil
}

// ObsQueueLen returns the number of queued observations.
func (d *DB) ObsQueueLen() int {
	d.obsMu.Lock()
	defer d.obsMu.Unlock()
	return d.countObs()
}
