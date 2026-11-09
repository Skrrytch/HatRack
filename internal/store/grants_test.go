package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"hatrack/internal/core"
)

func TestFilterObservationsAndLast(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	obs := []core.Observation{
		{ID: "00000000-0000-4000-8000-000000000001", Sensor: "window", Start: t0, End: t0.Add(time.Minute), Attrs: core.Attrs{"app": "kitty", "repo": "shop", "branch": "ACME-1"}},
		{ID: "00000000-0000-4000-8000-000000000002", Sensor: "window", Start: t0.Add(time.Minute), End: t0.Add(2 * time.Minute), Attrs: core.Attrs{"app": "kitty"}},
		{ID: "00000000-0000-4000-8000-000000000003", Sensor: "media", Start: t0, End: t0.Add(3 * time.Minute), Attrs: core.Attrs{"mic": "on"}},
	}
	if _, _, err := st.PutObservations(ctx, "laptop", obs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutObservations(ctx, "desk", []core.Observation{{ID: "00000000-0000-4000-8000-000000000004", Sensor: "window", Start: t0, End: t0.Add(time.Hour), Attrs: core.Attrs{"repo": "x"}}}); err != nil {
		t.Fatal(err)
	}
	last, err := st.LastObservationWith(ctx, "laptop", []string{"repo", "branch"}, "")
	if err != nil || !last.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last with repo: %v %v", last, err)
	}
	if last, _ := st.LastObservationWith(ctx, "laptop", nil, "media"); !last.Equal(t0.Add(3 * time.Minute)) {
		t.Fatalf("last media: %v", last)
	}
	if last, _ := st.LastObservationWith(ctx, "other", []string{"repo"}, ""); !last.IsZero() {
		t.Fatalf("no spans: %v", last)
	}
	ch, del, err := st.FilterObservations(ctx, "laptop", func(o core.Observation) (core.Attrs, bool) {
		if o.Sensor == "media" {
			return nil, false
		}
		a := core.Attrs{}
		for k, v := range o.Attrs {
			if k != "repo" && k != "branch" {
				a[k] = v
			}
		}
		return a, true
	})
	if err != nil || ch != 1 || del != 1 {
		t.Fatalf("filter: changed %d deleted %d err %v", ch, del, err)
	}
	rest, _ := st.ObservationsBetween(ctx, time.Time{}, time.Time{}, "", "")
	if len(rest) != 3 {
		t.Fatalf("want 3 spans left, got %+v", rest)
	}
	for _, o := range rest {
		switch {
		case o.Device == "desk" && o.Attrs["repo"] != "x":
			t.Fatalf("other device touched: %+v", o)
		case o.Device == "laptop" && (o.Attrs["repo"] != "" || o.Attrs["branch"] != "" || o.Sensor == "media"):
			t.Fatalf("not filtered: %+v", o)
		}
	}
}

func TestGrantLog(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	for i, c := range []GrantChange{
		{At: t0.AddDate(-2, 0, 0), Device: "laptop", Grant: "workdir", New: "all"},
		{At: t0, Device: "laptop", Grant: "workdir", Old: "all", New: "known_keys"},
		{At: t0.Add(time.Hour), Device: "laptop", Grant: "workdir", Old: "known_keys", Deleted: true},
	} {
		if err := st.AddGrantChange(ctx, c); err != nil {
			t.Fatal(i, err)
		}
	}
	if n, err := st.DeleteGrantLogBefore(ctx, t0.AddDate(-1, 0, 0)); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	log, err := st.GrantLog(ctx, time.Time{})
	if err != nil || len(log) != 2 || !log[0].Deleted || log[0].New != "" || log[1].New != "known_keys" {
		t.Fatalf("log: %+v %v", log, err)
	}
}
