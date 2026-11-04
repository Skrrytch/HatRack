package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"hatrack/internal/core"
)

// SQLite is the SQLite implementation of Store.
type SQLite struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS events (
  seq      INTEGER PRIMARY KEY AUTOINCREMENT,
  id       TEXT NOT NULL UNIQUE,
  ts       INTEGER NOT NULL,
  device   TEXT NOT NULL DEFAULT '',
  type     TEXT NOT NULL,
  hat      TEXT NOT NULL DEFAULT '',
  activity TEXT NOT NULL DEFAULT '',
  neutral  INTEGER NOT NULL DEFAULT 0,
  note     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS config (
  id   INTEGER PRIMARY KEY CHECK (id = 1),
  json TEXT NOT NULL,
  rev  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  expires    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL UNIQUE,
  caps       TEXT NOT NULL DEFAULT '',
  last_seen  INTEGER NOT NULL DEFAULT 0,
  created    INTEGER NOT NULL
);
`

// Open opens (and creates) the database file. ":memory:" is not supported;
// tests use a temporary file.
func Open(path string) (*SQLite, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init database %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database %s: %w", path, err)
	}
	return &SQLite{db: db}, nil
}

// migrate brings databases of older versions up to date.
func migrate(db *sql.DB) error {
	// MVP 2: corrections keep their details as JSON in events.data
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'data'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE events ADD COLUMN data TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// correctionData is what events.data holds for a correction.
type correctionData struct {
	Supersedes  []string     `json:"supersedes,omitempty"`
	Replacement []core.Event `json:"replacement,omitempty"`
	Source      string       `json:"source,omitempty"`
	Status      string       `json:"status,omitempty"`
}

func eventData(e core.Event) string {
	if e.Type != core.EvCorrection {
		return ""
	}
	b, _ := json.Marshal(correctionData{e.Supersedes, e.Replacement, e.Source, e.Status})
	return string(b)
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQLite) AppendEvents(ctx context.Context, ev []core.Event) (int, error) {
	for _, e := range ev {
		if err := e.Validate(); err != nil {
			return 0, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, e := range ev {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO events
		  (id, ts, device, type, hat, activity, neutral, note, data) VALUES (?,?,?,?,?,?,?,?,?)`,
			e.ID, e.TS.UnixNano(), e.Device, string(e.Type), e.Hat, e.Activity, b2i(e.Neutral), e.Note, eventData(e))
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, tx.Commit()
}

const eventCols = `id, ts, device, type, hat, activity, neutral, note, data`

func scanEvents(rows *sql.Rows) ([]core.Event, error) {
	defer rows.Close()
	var out []core.Event
	for rows.Next() {
		var e core.Event
		var ts int64
		var typ string
		var neutral int
		var data string
		if err := rows.Scan(&e.ID, &ts, &e.Device, &typ, &e.Hat, &e.Activity, &neutral, &e.Note, &data); err != nil {
			return nil, err
		}
		e.TS, e.Type, e.Neutral = time.Unix(0, ts).UTC(), core.EventType(typ), neutral != 0
		if data != "" {
			var d correctionData
			if err := json.Unmarshal([]byte(data), &d); err == nil {
				e.Supersedes, e.Replacement, e.Source, e.Status = d.Supersedes, d.Replacement, d.Source, d.Status
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLite) EventsBetween(ctx context.Context, from, to time.Time) ([]core.Event, error) {
	q := `SELECT ` + eventCols + ` FROM events WHERE 1=1`
	var args []any
	if !from.IsZero() {
		q += ` AND ts >= ?`
		args = append(args, from.UnixNano())
	}
	if !to.IsZero() {
		q += ` AND ts < ?`
		args = append(args, to.UnixNano())
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY ts, seq`, args...)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// Effective returns the events before to that count, with corrections
// applied (see core.Effective). Corrections are read whatever their own
// time, because they may change any part of the log.
func (s *SQLite) Effective(ctx context.Context, to time.Time) ([]core.Event, error) {
	q := `SELECT ` + eventCols + ` FROM events WHERE type = 'correction'`
	var args []any
	if !to.IsZero() {
		q += ` OR ts < ?`
		args = append(args, to.UnixNano())
	} else {
		q += ` OR 1=1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY ts, seq`, args...)
	if err != nil {
		return nil, err
	}
	all, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	eff := core.Effective(all)
	if to.IsZero() {
		return eff, nil
	}
	out := eff[:0]
	for _, e := range eff {
		if e.TS.Before(to) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *SQLite) hasCorrections(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE type = 'correction')`).Scan(&n)
	return n == 1, err
}

func (s *SQLite) CurrentState(ctx context.Context) (core.State, error) {
	// With corrections, any part of the log may have changed: fold it all.
	if corr, err := s.hasCorrections(ctx); err != nil {
		return core.State{}, err
	} else if corr {
		ev, err := s.Effective(ctx, time.Time{})
		if err != nil {
			return core.State{}, err
		}
		return core.Fold(ev), nil
	}
	var last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(ts) FROM events WHERE type = 'switch'`).Scan(&last); err != nil {
		return core.State{}, err
	}
	if !last.Valid {
		return core.NewState(), nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE ts >= ? ORDER BY ts, seq`, last.Int64)
	if err != nil {
		return core.State{}, err
	}
	ev, err := scanEvents(rows)
	if err != nil {
		return core.State{}, err
	}
	return core.Fold(ev), nil
}

func (s *SQLite) EventRev(ctx context.Context) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&rev)
	return rev, err
}

func (s *SQLite) LastActivities(ctx context.Context) (map[string]string, error) {
	if corr, err := s.hasCorrections(ctx); err != nil {
		return nil, err
	} else if corr {
		ev, err := s.Effective(ctx, time.Time{})
		if err != nil {
			return nil, err
		}
		out := map[string]string{}
		for _, e := range ev { // sorted by time: the last one wins
			if (e.Type == core.EvSwitch || e.Type == core.EvActivity) && e.Hat != "" && e.Activity != "" {
				out[e.Hat] = e.Activity
			}
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
	  SELECT e.hat, e.activity FROM events e
	  WHERE e.activity != '' AND e.type IN ('switch','activity') AND e.hat != ''
	    AND e.seq = (SELECT e2.seq FROM events e2
	                 WHERE e2.hat = e.hat AND e2.activity != '' AND e2.type IN ('switch','activity')
	                 ORDER BY e2.ts DESC, e2.seq DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, a string
		if err := rows.Scan(&h, &a); err != nil {
			return nil, err
		}
		out[h] = a
	}
	return out, rows.Err()
}

func (s *SQLite) GetConfig(ctx context.Context) (core.Config, int64, error) {
	var js string
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT json, rev FROM config WHERE id = 1`).Scan(&js, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return core.NewConfig(), 0, nil
	}
	if err != nil {
		return core.Config{}, 0, err
	}
	var c core.Config
	if err := json.Unmarshal([]byte(js), &c); err != nil {
		return core.Config{}, 0, err
	}
	return c, rev, nil
}

func (s *SQLite) PutConfig(ctx context.Context, c core.Config) (int64, error) {
	js, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRowContext(ctx, `SELECT rev FROM config WHERE id = 1`).Scan(&rev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	rev++
	if _, err := tx.ExecContext(ctx, `INSERT INTO config (id, json, rev) VALUES (1, ?, ?)
	  ON CONFLICT(id) DO UPDATE SET json = excluded.json, rev = excluded.rev`, string(js), rev); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *SQLite) AddDevice(ctx context.Context, name string) (Device, string, error) {
	if name == "" {
		return Device{}, "", errors.New("device name is empty")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Device{}, "", err
	}
	token := "hat_" + base64.RawURLEncoding.EncodeToString(raw[:])
	d := Device{ID: core.NewID(), Name: name}
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices (id, name, token_hash, created) VALUES (?,?,?,?)`,
		d.ID, d.Name, hashToken(token), time.Now().UnixNano())
	if err != nil {
		return Device{}, "", fmt.Errorf("add device %q: %w", name, err)
	}
	return d, token, nil
}

func (s *SQLite) DeviceByToken(ctx context.Context, token string) (Device, error) {
	var d Device
	var caps string
	var seen int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, caps, last_seen FROM devices WHERE token_hash = ?`,
		hashToken(token)).Scan(&d.ID, &d.Name, &caps, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	if caps != "" {
		d.Capabilities = json.RawMessage(caps)
	}
	if seen > 0 {
		d.LastSeen = time.Unix(0, seen).UTC()
	}
	return d, nil
}

func (s *SQLite) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, caps, last_seen FROM devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var caps string
		var seen int64
		if err := rows.Scan(&d.ID, &d.Name, &caps, &seen); err != nil {
			return nil, err
		}
		if caps != "" {
			d.Capabilities = json.RawMessage(caps)
		}
		if seen > 0 {
			d.LastSeen = time.Unix(0, seen).UTC()
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *SQLite) TouchDevice(ctx context.Context, id string, caps json.RawMessage) error {
	if len(caps) > 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen = ?, caps = ? WHERE id = ?`,
			time.Now().UnixNano(), string(caps), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE id = ?`, time.Now().UnixNano(), id)
	return err
}

func (s *SQLite) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

func (s *SQLite) SetPasswordHash(ctx context.Context, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('password_hash', ?)
	  ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) PasswordHash(ctx context.Context) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'password_hash'`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func (s *SQLite) CreateSession(ctx context.Context, tokenHash string, expires time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires < ?`, time.Now().UnixNano()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO sessions (token_hash, expires) VALUES (?, ?)`,
		tokenHash, expires.UnixNano())
	return err
}

func (s *SQLite) SessionValid(ctx context.Context, tokenHash string, now time.Time) (bool, error) {
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT expires FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && exp > now.UnixNano(), err
}

func (s *SQLite) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}
