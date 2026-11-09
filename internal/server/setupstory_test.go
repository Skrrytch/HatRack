package server

import (
	"context"
	"strings"
	"testing"

	"hatrack/internal/core"
)

func TestSetupStory(t *testing.T) {
	e, b := setupBrowser(t)
	addDevice(t, e, "desk")
	_, out := b.do("GET", "/setup", nil)
	// four steps in order, one heading each
	last := -1
	for _, want := range []string{"What you do", "How HatRack tells what you work on", "Where HatRack runs and who reads along", "Back up and move"} {
		i := strings.Index(out, want)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order", want)
		}
		last = i
	}
	// the defaults are a line in the hats card, not a section of their own
	if !strings.Contains(out, "What all hats inherit:") || !strings.Contains(out, "working hours Mon–Fri 08:00–18:00") || strings.Contains(out, "<h1>Defaults</h1>") {
		t.Errorf("inherit line:\n%s", out)
	}
	// tiles link the pages and show their state
	for _, want := range []string{`class="tile" href="/setup/grants"`, `class="tile" href="/assign"`, `class="tile" href="/assign?tab=tickets"`, `class="tile" href="/setup/detection"`,
		`class="tile" href="/setup/devices"`, `class="tile" href="/setup/ai"`, "0 rules · 0 learned clues", "away after 5 min", "no access yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %s", want)
		}
	}
	// what needs the user: no window sensor anywhere, a device never seen
	for _, want := range []string{"Active window is off on every computer", "desk, laptop have never connected"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks the hint %q", want)
		}
	}
	// with the window sensor on, its hint goes
	enableWindow(t, e, b)
	if _, out := b.do("GET", "/setup", nil); strings.Contains(out, "Active window is off on every computer") || !strings.Contains(out, "laptop: Away, Active window") {
		t.Error("window on: hint or state wrong")
	}
	// an AI access shows what it reads
	e.st.AddMCPToken(context.Background(), "Claude Code", []string{"read", "observe"})
	if _, out := b.do("GET", "/setup", nil); !strings.Contains(out, "Claude Code: reads time log and observations · never used yet") {
		t.Error("AI tile")
	}
}

func TestDaysText(t *testing.T) {
	c := core.NewConfig()
	if got := inheritLine("de", c); !strings.HasPrefix(got, "Arbeitszeit Mo–Fr 08:00–18:00") {
		t.Errorf("default: %s", got)
	}
	c.Defaults.WorkHours = &core.WorkHours{Start: "09:00", End: "17:00", Days: []string{"mon", "wed"}}
	if got := inheritLine("de", c); !strings.HasPrefix(got, "Arbeitszeit Mo, Mi 09:00–17:00") {
		t.Errorf("two days: %s", got)
	}
}
