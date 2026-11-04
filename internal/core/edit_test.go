package core

import (
	"fmt"
	"strings"
	"testing"
)

// show renders intervals as "hat/act start-end" in minutes from t0.
func show(events []Event, now int) string {
	var parts []string
	for _, i := range Intervals(events, at(now)) {
		s := fmt.Sprintf("%s/%s %d-%d", i.Hat, i.Activity, int(i.Start.Sub(t0).Minutes()), int(i.End.Sub(t0).Minutes()))
		if i.Open {
			s += "+"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// day: venom/dev 0-60, tag/ 60-90, off 90, private/ 120-now(180)
func day() []Event {
	return []Event{
		ev(0, EvSwitch, "venom", "dev"),
		ev(60, EvSwitch, "tag", ""),
		ev(90, EvOff, "tag", ""),
		ev(120, EvSwitch, "private", ""),
	}
}

func apply(t *testing.T, events []Event, c *Event, err error) []Event {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return append(events, *c)
}

func in(now int) Input { return Input{Device: "web", Now: at(now)} }

func TestEditChange(t *testing.T) {
	evs := day()
	if got := show(evs, 180); got != "venom/dev 0-60, tag/ 60-90, private/ 120-180+" {
		t.Fatalf("start: %s", got)
	}
	// tag started at 50, not 60: venom ends earlier
	c, err := PlanChange(evs, evs[1].ID, Edit{Start: at(50), End: at(90), Hat: "tag"}, in(180))
	evs2 := apply(t, evs, c, err)
	if got := show(evs2, 180); got != "venom/dev 0-50, tag/ 50-90, private/ 120-180+" {
		t.Fatalf("move start: %s", got)
	}
	if len(c.Supersedes) != 1 || len(c.Replacement) != 1 {
		t.Fatalf("one superseded, one replacement: %+v", c)
	}
	// tag lasted until 100: the off moves
	c, err = PlanChange(evs2, Intervals(evs2, at(180))[1].StartID, Edit{Start: at(50), End: at(100), Hat: "tag"}, in(180))
	evs3 := apply(t, evs2, c, err)
	if got := show(evs3, 180); got != "venom/dev 0-50, tag/ 50-100, private/ 120-180+" {
		t.Fatalf("move end: %s", got)
	}
	// it was venom/review, not tag
	c, err = PlanChange(evs3, Intervals(evs3, at(180))[1].StartID, Edit{Start: at(50), End: at(100), Hat: "venom", Activity: "review"}, in(180))
	evs4 := apply(t, evs3, c, err)
	if got := show(evs4, 180); got != "venom/dev 0-50, venom/review 50-100, private/ 120-180+" {
		t.Fatalf("change hat: %s", got)
	}
	// an end past the next entry swallows it
	c, err = PlanChange(evs4, Intervals(evs4, at(180))[1].StartID, Edit{Start: at(50), End: at(150), Hat: "venom", Activity: "review"}, in(180))
	evs5 := apply(t, evs4, c, err)
	if got := show(evs5, 180); got != "venom/dev 0-50, venom/review 50-150, private/ 150-180+" {
		t.Fatalf("swallow: %s", got)
	}
	// the open entry gets an end
	open := Intervals(evs5, at(180))
	c, err = PlanChange(evs5, open[len(open)-1].StartID, Edit{Start: at(150), End: at(170), Hat: "private"}, in(180))
	evs6 := apply(t, evs5, c, err)
	if got := show(evs6, 180); !strings.HasSuffix(got, "private/ 150-170") {
		t.Fatalf("end the open entry: %s", got)
	}
	if st := Fold(evs6); st.Mode != ModePaused {
		t.Fatalf("after ending the open entry nothing runs: %+v", st)
	}
	// nothing changed, in the future, empty range
	if _, err := PlanChange(evs, evs[0].ID, Edit{Start: at(0), End: at(60), Hat: "venom", Activity: "dev"}, in(180)); err != ErrNoChange {
		t.Fatalf("no change: %v", err)
	}
	if _, err := PlanChange(evs, evs[0].ID, Edit{Start: at(0), End: at(300), Hat: "venom"}, in(180)); err != ErrFuture {
		t.Fatalf("future: %v", err)
	}
	if _, err := PlanChange(evs, evs[0].ID, Edit{Start: at(60), End: at(30), Hat: "venom"}, in(180)); err != ErrEmptyRange {
		t.Fatalf("empty: %v", err)
	}
}

func TestEditInsertDeleteUndo(t *testing.T) {
	evs := day()
	// fill the gap 90-120 with tag/support
	c, err := PlanInsert(evs, Edit{Start: at(90), End: at(120), Hat: "tag", Activity: "support"}, in(180))
	evs2 := apply(t, evs, c, err)
	if got := show(evs2, 180); got != "venom/dev 0-60, tag/ 60-90, tag/support 90-120, private/ 120-180+" {
		t.Fatalf("fill gap: %s", got)
	}
	// a forgotten switch in the middle of an entry splits it
	c, err = PlanInsert(evs, Edit{Start: at(20), End: at(30), Hat: "tag"}, in(180))
	evs3 := apply(t, evs, c, err)
	if got := show(evs3, 180); got != "venom/dev 0-20, tag/ 20-30, venom/dev 30-60, tag/ 60-90, private/ 120-180+" {
		t.Fatalf("split: %s", got)
	}
	// "since 150 I am at tag" (open)
	c, err = PlanInsert(evs, Edit{Start: at(150), Hat: "tag"}, in(180))
	evs4 := apply(t, evs, c, err)
	if got := show(evs4, 180); got != "venom/dev 0-60, tag/ 60-90, private/ 120-150, tag/ 150-180+" {
		t.Fatalf("since: %s", got)
	}
	if st := Fold(evs4); st.Hat != "tag" || st.Mode != ModeWearing {
		t.Fatalf("the current state follows the correction: %+v", st)
	}
	// delete tag 60-90: a gap
	c, err = PlanDelete(evs, evs[1].ID, in(180))
	evs5 := apply(t, evs, c, err)
	if got := show(evs5, 180); got != "venom/dev 0-60, private/ 120-180+" {
		t.Fatalf("delete: %s", got)
	}
	// undo brings it back
	u, err := PlanUndo(evs5, c.ID, in(181))
	evs6 := apply(t, evs5, u, err)
	if got := show(evs6, 180); got != "venom/dev 0-60, tag/ 60-90, private/ 120-180+" {
		t.Fatalf("undo: %s", got)
	}
	// and undoing the undo deletes it again
	u2, err := PlanUndo(evs6, u.ID, in(182))
	evs7 := apply(t, evs6, u2, err)
	if got := show(evs7, 180); got != "venom/dev 0-60, private/ 120-180+" {
		t.Fatalf("undo undo: %s", got)
	}
	// deleting the open entry: nothing runs from its start
	open := Intervals(evs, at(180))
	c, err = PlanDelete(evs, open[len(open)-1].StartID, in(180))
	evs8 := apply(t, evs, c, err)
	if st := Fold(evs8); st.Mode == ModeWearing {
		t.Fatalf("deleted open entry still runs: %+v %s", st, show(evs8, 180))
	}
	// deleting the first part of a hat leaves the later activity standing
	acts := []Event{ev(0, EvSwitch, "venom", "dev"), ev(30, EvActivity, "venom", "review"), ev(60, EvOff, "venom", "")}
	c, err = PlanDelete(acts, acts[0].ID, in(180))
	if got := show(apply(t, acts, c, err), 180); got != "venom/review 30-60" {
		t.Fatalf("delete before an activity change: %s", got)
	}
	// changing the hat of the first part keeps the second in its hat
	c, err = PlanChange(acts, acts[0].ID, Edit{Start: at(0), End: at(30), Hat: "tag"}, in(180))
	if got := show(apply(t, acts, c, err), 180); got != "tag/ 0-30, venom/review 30-60" {
		t.Fatalf("change before an activity change: %s", got)
	}
	// proposed corrections do not count
	p := *c
	p.ID, p.Status = NewID(), StatusProposed
	if got := show(append(evs, p), 180); got != show(evs, 180) {
		t.Fatalf("a proposed correction counted: %s", got)
	}
}
