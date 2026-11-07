package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
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

func TestCorrections(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// a database from before corrections existed
	old, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE events (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, ts INTEGER NOT NULL,
	  device TEXT NOT NULL DEFAULT '', type TEXT NOT NULL, hat TEXT NOT NULL DEFAULT '', activity TEXT NOT NULL DEFAULT '',
	  neutral INTEGER NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(filepath.Join(dir, "old.db"))
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	defer s.Close()

	t0 := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	mk := func(min int, typ core.EventType, hat, act string) core.Event {
		e := core.NewEvent("pc", typ, t0.Add(time.Duration(min)*time.Minute))
		e.Hat, e.Activity = hat, act
		return e
	}
	a, b := mk(0, core.EvSwitch, "venom", "dev"), mk(60, core.EvSwitch, "tag", "support")
	if _, err := s.AppendEvents(ctx, []core.Event{a, b}); err != nil {
		t.Fatal(err)
	}
	// it was private, not tag
	c, err := core.PlanChange([]core.Event{a, b}, b.ID, core.Edit{Start: b.TS, Hat: "private", Activity: "garden"},
		core.Input{Device: "web", Now: t0.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendEvents(ctx, []core.Event{*c}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.CurrentState(ctx)
	if st.Hat != "private" || st.Activity != "garden" {
		t.Fatalf("state after the correction: %+v", st)
	}
	last, _ := s.LastActivities(ctx)
	if last["private"] != "garden" || last["tag"] != "" || last["venom"] != "dev" {
		t.Fatalf("last activities: %v", last)
	}
	// a correction made now still changes the past: Effective before 10:30
	eff, _ := s.Effective(ctx, t0.Add(90*time.Minute))
	if len(eff) != 2 || eff[1].Hat != "private" {
		t.Fatalf("effective: %+v", eff)
	}
	// the raw log keeps everything, the correction with its details
	raw, _ := s.EventsBetween(ctx, time.Time{}, time.Time{})
	if len(raw) != 3 || raw[2].Type != core.EvCorrection || len(raw[2].Supersedes) != 1 || len(raw[2].Replacement) != 1 {
		t.Fatalf("raw log: %+v", raw)
	}
}

func TestDetailsAndTicketLinks(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	e := ev(0, core.EvSwitch, "venom", "review")
	e.Detail = "https://jira.example.org/browse/TAG-1366"
	d := ev(5, core.EvDetail, "venom", "review")
	d.Detail = "TAG-2 from https://other.example/browse/TAG-2"
	if _, err := s.AppendEvents(ctx, []core.Event{e, d}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.CurrentState(ctx)
	if st.Detail != d.Detail {
		t.Fatalf("state detail: %+v", st)
	}
	raw, _ := s.EventsBetween(ctx, time.Time{}, time.Time{})
	if raw[0].Detail != e.Detail || raw[1].Type != core.EvDetail {
		t.Fatalf("stored: %+v", raw)
	}
	links, _ := s.TicketLinks(ctx)
	if links["TAG"] != "https://jira.example.org/browse/" || len(links) != 1 {
		t.Fatalf("learned, first wins: %v", links)
	}
	s.ResetTicketLink(ctx, "TAG")
	if links, _ := s.TicketLinks(ctx); len(links) != 0 {
		t.Fatal("reset")
	}
}

func ob(id, sensor string, startMin, endMin int, attrs core.Attrs) core.Observation {
	return core.Observation{ID: id, Sensor: sensor, Start: base.Add(time.Duration(startMin) * time.Minute),
		End: base.Add(time.Duration(endMin) * time.Minute), Attrs: attrs}
}

func allObs(t *testing.T, s *SQLite) []core.Observation {
	t.Helper()
	got, err := s.ObservationsBetween(context.Background(), time.Time{}, time.Time{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestObservationUpsert(t *testing.T) {
	s, ctx := open(t), context.Background()
	st, sk, err := s.PutObservations(ctx, "pc", []core.Observation{ob("a", "window", 0, 5, core.Attrs{"app": "vim"}), ob("b", "idle", 5, 6, nil)})
	if err != nil || st != 2 || sk != 0 {
		t.Fatalf("insert: %d %d %v", st, sk, err)
	}
	// the end grows; start and attrs stay
	st, sk, err = s.PutObservations(ctx, "pc", []core.Observation{ob("a", "window", 3, 9, core.Attrs{"app": "emacs"})})
	if err != nil || st != 1 || sk != 0 {
		t.Fatalf("extend: %d %d %v", st, sk, err)
	}
	got := allObs(t, s)
	if len(got) != 2 || !got[0].End.Equal(base.Add(9*time.Minute)) || !got[0].Start.Equal(base) || got[0].Attrs["app"] != "vim" || got[0].Device != "pc" {
		t.Fatalf("after extend: %+v", got)
	}
	if got[1].Attrs == nil || len(got[1].Attrs) != 0 {
		t.Fatalf("empty attrs: %#v", got[1].Attrs)
	}
	// same or earlier end: neither stored nor skipped
	st, sk, err = s.PutObservations(ctx, "pc", []core.Observation{ob("a", "window", 0, 9, nil), ob("a", "window", 0, 2, nil)})
	if err != nil || st != 0 || sk != 0 {
		t.Fatalf("no growth: %d %d %v", st, sk, err)
	}
	// an ID of another device is skipped and unchanged; the input device is ignored
	o := ob("a", "window", 0, 60, nil)
	o.Device = "pc"
	st, sk, err = s.PutObservations(ctx, "other", []core.Observation{o})
	if err != nil || st != 0 || sk != 1 {
		t.Fatalf("foreign id: %d %d %v", st, sk, err)
	}
	if got := allObs(t, s); !got[0].End.Equal(base.Add(9 * time.Minute)) {
		t.Fatalf("foreign id changed the span: %+v", got[0])
	}
	// invalid input stores nothing
	for _, bad := range []core.Observation{ob("", "idle", 0, 1, nil), ob("x", "", 0, 1, nil), ob("x", "idle", 5, 1, nil)} {
		if _, _, err := s.PutObservations(ctx, "pc", []core.Observation{ob("fine", "idle", 0, 1, nil), bad}); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if len(allObs(t, s)) != 2 {
		t.Fatal("invalid batch stored something")
	}
}

func TestObservationsBetween(t *testing.T) {
	s, ctx := open(t), context.Background()
	s.PutObservations(ctx, "pc", []core.Observation{
		ob("before", "idle", 0, 10, nil), // touches from only
		ob("left", "idle", 5, 15, nil),   // overlaps the left edge
		ob("inside", "window", 12, 14, nil),
		ob("right", "idle", 18, 30, nil), // overlaps the right edge
		ob("after", "idle", 20, 25, nil), // touches to only
	})
	s.PutObservations(ctx, "nb", []core.Observation{ob("nb1", "idle", 11, 13, nil)})
	from, to := base.Add(10*time.Minute), base.Add(20*time.Minute)
	ids := func(device, sensor string) string {
		got, err := s.ObservationsBetween(ctx, from, to, device, sensor)
		if err != nil {
			t.Fatal(err)
		}
		r := ""
		for _, o := range got {
			r += o.ID + " "
		}
		return r
	}
	if g := ids("", ""); g != "left nb1 inside right " {
		t.Fatalf("all: %q", g)
	}
	if g := ids("pc", "idle"); g != "left right " {
		t.Fatalf("pc idle: %q", g)
	}
	if g := ids("nb", ""); g != "nb1 " {
		t.Fatalf("nb: %q", g)
	}
	// open sides
	if got, _ := s.ObservationsBetween(ctx, time.Time{}, base.Add(10*time.Minute), "", ""); len(got) != 2 {
		t.Fatalf("open from: %+v", got)
	}

	stats, err := s.ObservationStats(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// pc idle: left clipped to 5 min, right clipped to 2 min
	if x := stats["pc"]["idle"]; x.Count != 2 || x.Duration != 7*time.Minute {
		t.Fatalf("pc idle: %+v", x)
	}
	if x := stats["pc"]["window"]; x.Count != 1 || x.Duration != 2*time.Minute {
		t.Fatalf("pc window: %+v", x)
	}
	if x := stats["nb"]["idle"]; x.Count != 1 || x.Duration != 2*time.Minute {
		t.Fatalf("nb idle: %+v", x)
	}
}

func TestObservationDeleteAndEvents(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.AppendEvents(ctx, []core.Event{ev(0, core.EvSwitch, "venom", "dev"), ev(60, core.EvSwitch, "tag", "support")}); err != nil {
		t.Fatal(err)
	}
	effBefore, _ := s.Effective(ctx, time.Time{})
	betBefore, _ := s.EventsBetween(ctx, time.Time{}, time.Time{})

	dev, _, err := s.AddDevice(ctx, "pc")
	if err != nil {
		t.Fatal(err)
	}
	s.PutObservations(ctx, "pc", []core.Observation{ob("p1", "idle", 0, 10, nil), ob("p2", "idle", 20, 30, nil)})
	s.PutObservations(ctx, "nb", []core.Observation{ob("n1", "idle", 0, 10, nil)})

	// only spans that ended before t go; a span ending exactly at t stays
	n, err := s.DeleteObservationsBefore(ctx, base.Add(10*time.Minute))
	if err != nil || n != 0 {
		t.Fatalf("boundary: %d %v", n, err)
	}
	n, err = s.DeleteObservationsBefore(ctx, base.Add(11*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("delete before: %d %v", n, err)
	}
	if got := allObs(t, s); len(got) != 1 || got[0].ID != "p2" {
		t.Fatalf("remaining: %+v", got)
	}
	s.PutObservations(ctx, "nb", []core.Observation{ob("n2", "idle", 40, 50, nil)})

	// deleting a device removes its spans by name; others and events stay
	if err := s.DeleteDevice(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	if got := allObs(t, s); len(got) != 1 || got[0].ID != "n2" {
		t.Fatalf("after DeleteDevice: %+v", got)
	}
	// test 22: observations never influence the event log
	n, _ = s.DeleteObservationsBefore(ctx, base.Add(24*time.Hour))
	if n != 1 || len(allObs(t, s)) != 0 {
		t.Fatalf("delete all: %d", n)
	}
	effAfter, _ := s.Effective(ctx, time.Time{})
	betAfter, _ := s.EventsBetween(ctx, time.Time{}, time.Time{})
	if len(effAfter) != 2 || !reflect.DeepEqual(effBefore, effAfter) || !reflect.DeepEqual(betBefore, betAfter) {
		t.Fatalf("events changed: %+v / %+v", effAfter, betAfter)
	}
}

func TestOpenOldDatabaseGetsObservations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st, _, err := s.PutObservations(context.Background(), "pc", []core.Observation{ob("a", "idle", 0, 1, nil)}); err != nil || st != 1 {
		t.Fatalf("%d %v", st, err)
	}
}
