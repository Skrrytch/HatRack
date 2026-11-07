package server

import (
	"context"
	"testing"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/modules"
)

func TestPruneObservations(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	srv := New(e.st, modules.Builtin(nil))
	srv.Now = func() time.Time { return e.now }
	mk := func(id string, daysAgo int) core.Observation {
		end := e.now.AddDate(0, 0, -daysAgo)
		return core.Observation{ID: id, Sensor: "idle", Start: end.Add(-time.Hour), End: end, Attrs: core.Attrs{}}
	}
	// default retention is 28 days
	if _, _, err := e.st.PutObservations(ctx, "laptop", []core.Observation{mk("old", 29), mk("new", 27)}); err != nil {
		t.Fatal(err)
	}
	srv.pruneObservations(ctx)
	got, err := e.st.ObservationsBetween(ctx, time.Time{}, time.Time{}, "", "")
	if err != nil || len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("after prune: %+v %v", got, err)
	}
}
