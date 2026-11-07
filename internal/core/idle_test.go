package core

import (
	"strings"
	"testing"
	"time"
)

func TestIdleDefaults(t *testing.T) {
	var i Idle
	if i.AfterD() != 5*time.Minute || i.AskAfterD() != 10*time.Minute || i.StopAfterD() != 15*time.Minute || !i.Asks() {
		t.Fatalf("defaults: %v %v %v %v", i.AfterD(), i.AskAfterD(), i.StopAfterD(), i.Asks())
	}
	no := false
	i = Idle{After: 2, AskAfter: 20, StopAfter: 30, Ask: &no}
	if i.AfterD() != 2*time.Minute || i.AskAfterD() != 20*time.Minute || i.StopAfterD() != 30*time.Minute || i.Asks() {
		t.Fatalf("set: %+v", i)
	}
}

func TestIdleValidate(t *testing.T) {
	tests := []struct {
		name string
		in   Idle
		bad  string
	}{
		{"zero", Idle{}, ""},
		{"all set", Idle{After: 5, AskAfter: 10, StopAfter: 15}, ""},
		{"max", Idle{After: 1, AskAfter: 1440, StopAfter: 1440}, ""},
		{"too big", Idle{StopAfter: 1441}, "stop_after"},
		{"negative", Idle{After: -1}, "after"},
		{"after later than ask", Idle{After: 11}, "ask_after"},
		{"after later than stop", Idle{After: 8, AskAfter: 9, StopAfter: 7}, "stop_after"},
		{"after = ask = stop", Idle{After: 6, AskAfter: 6, StopAfter: 6}, ""},
	}
	for _, tc := range tests {
		err := Observe{Idle: tc.in}.Validate()
		switch {
		case tc.bad == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.bad)
		}
	}
}

func TestIdleParseExportMerge(t *testing.T) {
	cfg, err := ParseConfig([]byte("version: 1\nobserve:\n  idle:\n    after: 3\n    ask: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Observe.Idle.After != 3 || cfg.Observe.Idle.Asks() {
		t.Fatalf("parsed: %+v", cfg.Observe.Idle)
	}
	if _, err := ParseConfig([]byte("version: 1\nobserve:\n  idle:\n    after: 20\n")); err == nil {
		t.Fatal("after later than ask_after accepted")
	}
	// an unset block is not exported; a set one is, and is replaced as a whole
	if b, _ := NewConfig().Export(); strings.Contains(string(b), "idle") {
		t.Fatalf("empty idle exported: %s", b)
	}
	b, err := cfg.Export()
	if err != nil || !strings.Contains(string(b), "after: 3") {
		t.Fatalf("export: %s %v", b, err)
	}
	cur := NewConfig()
	cur.Observe.Idle = Idle{StopAfter: 30}
	m, _ := Merge(cur, NewConfig(), false)
	if m.Observe.Idle.StopAfter != 30 {
		t.Fatal("empty import changed idle")
	}
	m, _ = Merge(cur, cfg, false)
	if m.Observe.Idle.StopAfter != 0 || m.Observe.Idle.After != 3 {
		t.Fatalf("import did not replace the block: %+v", m.Observe.Idle)
	}
}
