package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
	"hatrack/internal/server"
)

func TestParseAt(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	now := time.Date(2026, 11, 4, 14, 30, 0, 0, loc)
	tests := map[string]string{
		"09:15":                     "2026-11-04T09:15:00+01:00",
		"2026-11-03 17:00":          "2026-11-03T17:00:00+01:00",
		"2026-11-03T17:00:00+01:00": "2026-11-03T17:00:00+01:00",
		"":                          "",
	}
	for in, want := range tests {
		got, err := parseAt(in, now)
		if err != nil || got != want {
			t.Errorf("parseAt(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseAt("breakfast", now); err == nil {
		t.Error("expected an error")
	}
}

func TestPeriod(t *testing.T) {
	loc := time.FixedZone("x", 0)
	now := time.Date(2026, 11, 4, 14, 30, 0, 0, loc) // a Wednesday
	from, to, _ := period("week", now)
	if from.Format("2006-01-02") != "2026-11-02" || to.Format("2006-01-02") != "2026-11-09" {
		t.Fatalf("week: %v %v", from, to)
	}
	from, to, _ = period("yesterday", now)
	if from.Format("01-02") != "11-03" || to.Format("01-02") != "11-04" {
		t.Fatalf("yesterday: %v %v", from, to)
	}
	from, _, err := period("2026-10-31", now)
	if err != nil || from.Day() != 31 {
		t.Fatalf("date: %v %v", from, err)
	}
	if _, _, err := period("last month", now); err == nil {
		t.Fatal("expected an error")
	}
}

func TestStatusLineAndLog(t *testing.T) {
	i18n.Set("en")
	since := time.Now().Add(-83 * time.Minute)
	line := StatusLine(agent.StatusData{HatName: "VENOM", State: core.State{Mode: core.ModeWearing, Hat: "venom", Activity: "review", Since: since}}, time.Now())
	if !strings.HasPrefix(line, "VENOM · review · since ") || !strings.HasSuffix(line, "(1h 23m)") {
		t.Fatalf("line: %q", line)
	}
	if got := StatusLine(agent.StatusData{HatName: "VENOM", State: core.State{Mode: core.ModePaused, Since: since}}, time.Now()); !strings.HasPrefix(got, "paused (VENOM) since ") {
		t.Fatalf("paused: %q", got)
	}
	if got := StatusLine(agent.StatusData{}, time.Now()); got != "No hat on" {
		t.Fatalf("none: %q", got)
	}

	t0 := time.Now().Truncate(time.Hour).Add(-3 * time.Hour)
	iv := []core.Interval{
		{Hat: "venom", Activity: "review", Start: t0, End: t0.Add(time.Hour)},
		{Hat: "tag", Start: t0.Add(time.Hour), End: t0.Add(90 * time.Minute), Open: true},
	}
	var buf bytes.Buffer
	RenderLog(&buf, agent.LogData{
		Names:             map[string]string{"venom": "VENOM", "tag": "TAG"},
		IntervalsResponse: serverIntervals(iv),
	}, t0, t0.Add(24*time.Hour))
	out := buf.String()
	for _, want := range []string{"VENOM · review", "1h 00m", "TAG", "30m", "Totals", "Sum", "1h 30m", "now"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output lacks %q:\n%s", want, out)
		}
	}
}

func serverIntervals(iv []core.Interval) server.IntervalsResponse {
	return server.IntervalsResponse{Intervals: iv, Totals: core.Totals(iv)}
}

func TestWrongArgumentsShowUsage(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer i18n.Set("en")
	var out, errb bytes.Buffer
	app := &App{Out: &out, Err: &errb, Socket: "/nonexistent/sock", IsTTY: func() bool { return false }}
	if code := app.Execute([]string{"agent", "login", "nur-ein-token"}); code == 0 {
		t.Fatal("expected a failure")
	}
	msg := errb.String()
	for _, want := range []string{"Falsche Argumente", "hat agent login <url> <token>", "Beispiel:", "hat agent login --help"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	errb.Reset()
	app.Execute([]string{"off", "--bogus"})
	if !strings.Contains(errb.String(), "Falsche Option") || !strings.Contains(errb.String(), "hat off") {
		t.Errorf("flag error:\n%s", errb.String())
	}
}
