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
	"strings"
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
CREATE TABLE IF NOT EXISTS ticket_links (
  project TEXT PRIMARY KEY,
  base    TEXT NOT NULL,
  learned INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_tokens (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL UNIQUE,
  scopes     TEXT NOT NULL DEFAULT 'read',
  created    INTEGER NOT NULL,
  last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS devices (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL UNIQUE,
  caps       TEXT NOT NULL DEFAULT '',
  last_seen  INTEGER NOT NULL DEFAULT 0,
  created    INTEGER NOT NULL,
  can_config INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS observations (
  id     TEXT PRIMARY KEY,
  device TEXT NOT NULL,
  sensor TEXT NOT NULL,
  start  INTEGER NOT NULL,
  end    INTEGER NOT NULL,
  attrs  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS observations_start ON observations(start);
CREATE TABLE IF NOT EXISTS grant_log (
  ts      INTEGER NOT NULL,
  device  TEXT NOT NULL,
  grant_id TEXT NOT NULL,
  old     TEXT NOT NULL DEFAULT '',
  new     TEXT NOT NULL DEFAULT '',
  deleted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS grant_log_ts ON grant_log(ts);
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
	// MVP 2: details below the activity
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'detail'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE events ADD COLUMN detail TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	// MVP 5: devices may change the configuration only when allowed
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('devices') WHERE name = 'can_config'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(`ALTER TABLE devices ADD COLUMN can_config INTEGER NOT NULL DEFAULT 0`); err != nil {
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
		  (id, ts, device, type, hat, activity, neutral, note, data, detail) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.TS.UnixNano(), e.Device, string(e.Type), e.Hat, e.Activity, b2i(e.Neutral), e.Note, eventData(e), e.Detail)
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
		// ticket links in details teach where their projects live; the
		// first link per project wins
		texts := []string{e.Detail}
		for _, r := range e.Replacement {
			texts = append(texts, r.Detail)
		}
		for _, t := range texts {
			for project, base := range core.LearnTicketLinks(t, nil) {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ticket_links (project, base, learned) VALUES (?,?,?)`,
					project, base, time.Now().UnixNano()); err != nil {
					return 0, err
				}
			}
		}
	}
	return n, tx.Commit()
}

const eventCols = `id, ts, device, type, hat, activity, neutral, note, data, detail`

func scanEvents(rows *sql.Rows) ([]core.Event, error) {
	defer rows.Close()
	var out []core.Event
	for rows.Next() {
		var e core.Event
		var ts int64
		var typ string
		var neutral int
		var data string
		if err := rows.Scan(&e.ID, &ts, &e.Device, &typ, &e.Hat, &e.Activity, &neutral, &e.Note, &data, &e.Detail); err != nil {
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
	// an activity carries its hat and stands on its own like a switch
	// (core.State.Apply), so folding can start at the newest of either
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(ts) FROM events WHERE type IN ('switch', 'activity')`).Scan(&last); err != nil {
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

func (s *SQLite) SetDeviceConfig(ctx context.Context, id string, allowed bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET can_config = ? WHERE id = ?`, allowed, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) DeviceByToken(ctx context.Context, token string) (Device, error) {
	var d Device
	var caps string
	var seen int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, caps, last_seen, can_config FROM devices WHERE token_hash = ?`,
		hashToken(token)).Scan(&d.ID, &d.Name, &caps, &seen, &d.CanConfig)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, caps, last_seen, can_config FROM devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var caps string
		var seen int64
		if err := rows.Scan(&d.ID, &d.Name, &caps, &seen, &d.CanConfig); err != nil {
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

// MCP tokens give an AI assistant access to /mcp, and nothing else. They
// look different from device tokens ("hrm_" instead of "hat_").

func (s *SQLite) AddMCPToken(ctx context.Context, name string, scopes []string) (MCPToken, string, error) {
	if name == "" {
		return MCPToken{}, "", errors.New("token name is empty")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return MCPToken{}, "", err
	}
	token := "hrm_" + base64.RawURLEncoding.EncodeToString(raw[:])
	t := MCPToken{ID: core.NewID(), Name: name, Scopes: scopes, Created: time.Now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_tokens (id, name, token_hash, scopes, created) VALUES (?,?,?,?,?)`,
		t.ID, t.Name, hashToken(token), strings.Join(scopes, ","), t.Created.UnixNano())
	if err != nil {
		return MCPToken{}, "", fmt.Errorf("add MCP token %q: %w", name, err)
	}
	return t, token, nil
}

func scanMCP(sc interface{ Scan(...any) error }) (MCPToken, error) {
	var t MCPToken
	var scopes string
	var created, used int64
	if err := sc.Scan(&t.ID, &t.Name, &scopes, &created, &used); err != nil {
		return t, err
	}
	t.Scopes = strings.Split(scopes, ",")
	t.Created = time.Unix(0, created)
	if used != 0 {
		t.LastUsed = time.Unix(0, used)
	}
	return t, nil
}

func (s *SQLite) MCPTokenByToken(ctx context.Context, token string) (MCPToken, error) {
	t, err := scanMCP(s.db.QueryRowContext(ctx, `SELECT id, name, scopes, created, last_used FROM mcp_tokens WHERE token_hash = ?`, hashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *SQLite) MCPTokens(ctx context.Context) ([]MCPToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scopes, created, last_used FROM mcp_tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPToken
	for rows.Next() {
		t, err := scanMCP(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLite) TouchMCPToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_tokens SET last_used = ? WHERE id = ?`, time.Now().UnixNano(), id)
	return err
}

func (s *SQLite) DeleteMCPToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_tokens WHERE id = ?`, id)
	return err
}

func (s *SQLite) TicketLinks(ctx context.Context) (core.TicketLinks, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project, base FROM ticket_links ORDER BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := core.TicketLinks{}
	for rows.Next() {
		var p, b string
		if err := rows.Scan(&p, &b); err != nil {
			return nil, err
		}
		out[p] = b
	}
	return out, rows.Err()
}

func (s *SQLite) ResetTicketLink(ctx context.Context, project string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ticket_links WHERE project = ?`, project)
	return err
}

func (s *SQLite) DeleteDevice(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// observations carry the device name; events stay
	if _, err := tx.ExecContext(ctx, `DELETE FROM observations WHERE device = (SELECT name FROM devices WHERE id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) PutObservations(ctx context.Context, device string, obs []core.Observation) (stored, skipped int, err error) {
	for _, o := range obs {
		if o.ID == "" || o.Sensor == "" || o.End.Before(o.Start) {
			return 0, 0, fmt.Errorf("invalid observation %q", o.ID)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	for _, o := range obs {
		var dev string
		var end int64
		err := tx.QueryRowContext(ctx, `SELECT device, end FROM observations WHERE id = ?`, o.ID).Scan(&dev, &end)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			attrs := o.Attrs
			if attrs == nil {
				attrs = core.Attrs{}
			}
			js, err := json.Marshal(attrs)
			if err != nil {
				return 0, 0, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO observations (id, device, sensor, start, end, attrs) VALUES (?, ?, ?, ?, ?, ?)`,
				o.ID, device, o.Sensor, o.Start.UnixNano(), o.End.UnixNano(), string(js)); err != nil {
				return 0, 0, err
			}
			stored++
		case err != nil:
			return 0, 0, err
		case dev != device:
			skipped++
		case o.End.UnixNano() > end:
			if _, err := tx.ExecContext(ctx, `UPDATE observations SET end = ? WHERE id = ?`, o.End.UnixNano(), o.ID); err != nil {
				return 0, 0, err
			}
			stored++
		}
	}
	return stored, skipped, tx.Commit()
}

func (s *SQLite) ObservationsBetween(ctx context.Context, from, to time.Time, device, sensor string) ([]core.Observation, error) {
	q := `SELECT id, device, sensor, start, end, attrs FROM observations WHERE 1=1`
	var args []any
	if !to.IsZero() {
		q += ` AND start < ?`
		args = append(args, to.UnixNano())
	}
	if !from.IsZero() {
		q += ` AND end > ?`
		args = append(args, from.UnixNano())
	}
	if device != "" {
		q += ` AND device = ?`
		args = append(args, device)
	}
	if sensor != "" {
		q += ` AND sensor = ?`
		args = append(args, sensor)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY start, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Observation
	for rows.Next() {
		var o core.Observation
		var st, en int64
		var js string
		if err := rows.Scan(&o.ID, &o.Device, &o.Sensor, &st, &en, &js); err != nil {
			return nil, err
		}
		o.Start, o.End = time.Unix(0, st).UTC(), time.Unix(0, en).UTC()
		if err := json.Unmarshal([]byte(js), &o.Attrs); err != nil {
			return nil, fmt.Errorf("observation %s: %w", o.ID, err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQLite) ObservationStats(ctx context.Context, from, to time.Time) (map[string]map[string]ObsStat, error) {
	obs, err := s.ObservationsBetween(ctx, from, to, "", "")
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]ObsStat{}
	for _, o := range obs {
		st, en := o.Start, o.End
		if !from.IsZero() && st.Before(from) {
			st = from
		}
		if !to.IsZero() && en.After(to) {
			en = to
		}
		if out[o.Device] == nil {
			out[o.Device] = map[string]ObsStat{}
		}
		x := out[o.Device][o.Sensor]
		x.Count++
		x.Duration += en.Sub(st)
		out[o.Device][o.Sensor] = x
	}
	return out, nil
}

func (s *SQLite) DeleteObservationsBefore(ctx context.Context, t time.Time) (int, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM observations WHERE end < ?`, t.UnixNano())
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	return int(n), err
}

func (s *SQLite) HatUsed(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE hat = ?
	  OR (type = 'correction' AND data LIKE ?))`, key, `%"hat":"`+key+`"%`).Scan(&n)
	return n == 1, err
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
