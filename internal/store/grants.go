package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	"hatrack/internal/core"
)

func (s *SQLite) FilterObservations(ctx context.Context, device string, f func(core.Observation) (core.Attrs, bool)) (changed, deleted int, err error) {
	obs, err := s.ObservationsBetween(ctx, time.Time{}, time.Time{}, device, "")
	if err != nil {
		return 0, 0, err
	}
	// f runs before the transaction: it may read the store itself, and the
	// database has one connection
	type result struct {
		id    string
		attrs string // "" deletes
	}
	var todo []result
	for _, o := range obs {
		a, keep := f(o)
		switch {
		case !keep:
			todo = append(todo, result{o.ID, ""})
		case !maps.Equal(a, o.Attrs):
			if a == nil {
				a = core.Attrs{}
			}
			js, err := json.Marshal(a)
			if err != nil {
				return 0, 0, err
			}
			todo = append(todo, result{o.ID, string(js)})
		}
	}
	if len(todo) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	for _, r := range todo {
		if r.attrs == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM observations WHERE id = ?`, r.id); err != nil {
				return 0, 0, err
			}
			deleted++
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE observations SET attrs = ? WHERE id = ?`, r.attrs, r.id); err != nil {
			return 0, 0, err
		}
		changed++
	}
	return changed, deleted, tx.Commit()
}

var attrKeyRe = regexp.MustCompile(`^[a-z_]{1,32}$`)

func (s *SQLite) LastObservationWith(ctx context.Context, device string, attrs []string, sensor string) (time.Time, error) {
	var cond []string
	args := []any{device}
	for _, a := range attrs {
		if !attrKeyRe.MatchString(a) {
			return time.Time{}, fmt.Errorf("attribute %q", a)
		}
		cond = append(cond, `json_extract(attrs, '$.`+a+`') IS NOT NULL`)
	}
	if sensor != "" {
		cond = append(cond, `sensor = ?`)
		args = append(args, sensor)
	}
	if len(cond) == 0 {
		return time.Time{}, nil
	}
	var end *int64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(end) FROM observations WHERE device = ? AND (`+strings.Join(cond, " OR ")+`)`, args...).Scan(&end)
	if err != nil || end == nil {
		return time.Time{}, err
	}
	return time.Unix(0, *end).UTC(), nil
}

func (s *SQLite) AddGrantChange(ctx context.Context, c GrantChange) error {
	del := 0
	if c.Deleted {
		del = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO grant_log (ts, device, grant_id, old, new, deleted) VALUES (?, ?, ?, ?, ?, ?)`,
		c.At.UnixNano(), c.Device, c.Grant, c.Old, c.New, del)
	return err
}

func (s *SQLite) GrantLog(ctx context.Context, since time.Time) ([]GrantChange, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, device, grant_id, old, new, deleted FROM grant_log WHERE ts >= ? ORDER BY ts DESC, rowid DESC`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GrantChange
	for rows.Next() {
		var c GrantChange
		var ts int64
		var del int
		if err := rows.Scan(&ts, &c.Device, &c.Grant, &c.Old, &c.New, &del); err != nil {
			return nil, err
		}
		c.At, c.Deleted = time.Unix(0, ts).UTC(), del == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteGrantLogBefore(ctx context.Context, t time.Time) (int, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM grant_log WHERE ts < ?`, t.UnixNano())
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	return int(n), err
}
