package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"hatrack/internal/core"
)

func open(t *testing.T) *SQLite {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var base = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

func ev(min int, typ core.EventType, hat, act string) core.Event {
	e := core.NewEvent("d", typ, base.Add(time.Duration(min)*time.Minute))
	e.Hat, e.Activity = hat, act
	return e
}

func TestEventsAndState(t *testing.T) {
	s, ctx := open(t), context.Background()
	st, err := s.CurrentState(ctx)
	if err != nil || st.Mode != core.ModeNone {
		t.Fatalf("empty state: %+v %v", st, err)
	}
	a, b, c := ev(0, core.EvSwitch, "venom", "review"), ev(30, core.EvActivity, "venom", "meeting"), ev(60, core.EvOff, "venom", "")
	n, err := s.AppendEvents(ctx, []core.Event{c, a, b})
	if err != nil || n != 3 {
		t.Fatalf("append: %d %v", n, err)
	}
	if n, _ = s.AppendEvents(ctx, []core.Event{a}); n != 0 {
		t.Fatalf("duplicate event inserted: %d", n)
	}
	st, _ = s.CurrentState(ctx)
	if st.Mode != core.ModePaused || st.Hat != "venom" || st.Activity != "meeting" {
		t.Fatalf("state: %+v", st)
	}
	got, _ := s.EventsBetween(ctx, base.Add(10*time.Minute), base.Add(60*time.Minute))
	if len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("between: %+v", got)
	}
	// a backdated switch does not change the state
	if _, err := s.AppendEvents(ctx, []core.Event{ev(-30, core.EvSwitch, "tag", "")}); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.CurrentState(ctx); st.Hat != "venom" {
		t.Fatalf("backdated event changed state: %+v", st)
	}
	last, _ := s.LastActivities(ctx)
	if last["venom"] != "meeting" || len(last) != 1 {
		t.Fatalf("last activities: %v", last)
	}
	if _, err := s.AppendEvents(ctx, []core.Event{{ID: "x"}}); err == nil {
		t.Fatal("invalid event accepted")
	}
}

func TestConfigAndDevices(t *testing.T) {
	s, ctx := open(t), context.Background()
	cfg, rev, err := s.GetConfig(ctx)
	if err != nil || rev != 0 || len(cfg.Hats) != 0 {
		t.Fatalf("empty config: %+v %d %v", cfg, rev, err)
	}
	cfg.Hats = core.HatList{{Key: "venom", Name: "VENOM", Activities: []string{"a"}}}
	if rev, err = s.PutConfig(ctx, cfg); err != nil || rev != 1 {
		t.Fatalf("put: %d %v", rev, err)
	}
	got, rev, _ := s.GetConfig(ctx)
	if rev != 1 || got.Hats[0].Key != "venom" {
		t.Fatalf("get: %+v %d", got, rev)
	}

	d, token, err := s.AddDevice(ctx, "laptop")
	if err != nil || token == "" {
		t.Fatal(err)
	}
	if _, _, err := s.AddDevice(ctx, "laptop"); err == nil {
		t.Fatal("duplicate device name accepted")
	}
	d2, err := s.DeviceByToken(ctx, token)
	if err != nil || d2.ID != d.ID {
		t.Fatalf("token lookup: %+v %v", d2, err)
	}
	if _, err := s.DeviceByToken(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("bad token: %v", err)
	}
	if err := s.TouchDevice(ctx, d.ID, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Devices(ctx)
	if len(list) != 1 || list[0].LastSeen.IsZero() {
		t.Fatalf("devices: %+v", list)
	}
}

func TestBackup(t *testing.T) {
	s, ctx := open(t), context.Background()
	s.AppendEvents(ctx, []core.Event{ev(0, core.EvSwitch, "venom", "")})
	out := filepath.Join(t.TempDir(), "b.db")
	if err := s.Backup(ctx, out); err != nil {
		t.Fatal(err)
	}
	b, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if st, _ := b.CurrentState(ctx); st.Hat != "venom" {
		t.Fatalf("backup state: %+v", st)
	}
	if err := s.Backup(ctx, out); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
}

func TestPasswordAndSessions(t *testing.T) {
	s, ctx := open(t), context.Background()
	if h, err := s.PasswordHash(ctx); err != nil || h != "" {
		t.Fatalf("no password yet: %q %v", h, err)
	}
	if err := s.SetPasswordHash(ctx, "hash1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.CreateSession(ctx, "tok", now.Add(time.Hour))
	s.CreateSession(ctx, "old", now.Add(-time.Hour))
	if ok, _ := s.SessionValid(ctx, "tok", now); !ok {
		t.Fatal("session should be valid")
	}
	if ok, _ := s.SessionValid(ctx, "old", now); ok {
		t.Fatal("expired session accepted")
	}
	if ok, _ := s.SessionValid(ctx, "nope", now); ok {
		t.Fatal("unknown session accepted")
	}
	s.DeleteSession(ctx, "tok")
	if ok, _ := s.SessionValid(ctx, "tok", now); ok {
		t.Fatal("deleted session accepted")
	}
	s.CreateSession(ctx, "tok2", now.Add(time.Hour))
	s.SetPasswordHash(ctx, "hash2") // a new password ends all sessions
	if ok, _ := s.SessionValid(ctx, "tok2", now); ok {
		t.Fatal("session survived a password change")
	}
	if h, _ := s.PasswordHash(ctx); h != "hash2" {
		t.Fatalf("hash: %q", h)
	}
}
