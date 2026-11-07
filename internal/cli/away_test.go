package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// fakeAgent answers every request with a status and keeps the requests.
func fakeAgent(t *testing.T, d agent.StatusData) (sock string, reqs func() []agent.Request) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "a.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got := make(chan agent.Request, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadBytes('\n')
				var req agent.Request
				json.Unmarshal(line, &req)
				got <- req
				data, _ := json.Marshal(d)
				json.NewEncoder(c).Encode(agent.Response{OK: true, Data: data})
			}()
		}
	}()
	return sock, func() []agent.Request {
		var out []agent.Request
		for {
			select {
			case r := <-got:
				out = append(out, r)
			default:
				return out
			}
		}
	}
}

func TestAwayCommandParsing(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	i18n.Set("en")
	tests := []struct {
		args []string
		want agent.Request
		bad  bool
	}{
		{[]string{"keep"}, agent.Request{Cmd: "away", Answer: "keep"}, false},
		{[]string{"PAUSE"}, agent.Request{Cmd: "away", Answer: "pause"}, false},
		{[]string{"work"}, agent.Request{Cmd: "away", Answer: "work"}, false},
		{[]string{"venom"}, agent.Request{Cmd: "away", Answer: "other", Hat: "venom"}, false},
		{[]string{"venom", "do", "review"}, agent.Request{Cmd: "away", Answer: "other", Hat: "venom", Activity: "review"}, false},
		{[]string{"venom", "do", "review", "PR", "482"}, agent.Request{Cmd: "away", Answer: "other", Hat: "venom", Activity: "review", Detail: "PR 482"}, false},
		{[]string{"venom", "do", "review:", "PR 482"}, agent.Request{Cmd: "away", Answer: "other", Hat: "venom", Activity: "review", Detail: "PR 482"}, false},
		{[]string{"venom", "TAG-1366"}, agent.Request{Cmd: "away", Answer: "other", Hat: "venom", Detail: "TAG-1366"}, false},
		{[]string{"keep", "more"}, agent.Request{}, true},
		{[]string{"venom", "do"}, agent.Request{}, true},
		{nil, agent.Request{}, true},
	}
	for _, tc := range tests {
		sock, reqs := fakeAgent(t, agent.StatusData{HatName: "VENOM"})
		var out, errb bytes.Buffer
		app := &App{Out: &out, Err: &errb, Socket: sock, IsTTY: func() bool { return false }}
		code := app.Execute(append([]string{"away"}, tc.args...))
		got := reqs()
		if tc.bad {
			if code == 0 || len(got) != 0 {
				t.Errorf("%v: code %d, requests %+v", tc.args, code, got)
			}
			continue
		}
		if code != 0 || len(got) != 1 || got[0] != tc.want {
			t.Errorf("%v: code %d, requests %+v, want %+v (%s)", tc.args, code, got, tc.want, errb.String())
		}
	}
}

func TestAwayStatusLine(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	i18n.Set("en")
	defer i18n.Set("en")
	from := time.Date(2026, 11, 4, 14, 2, 0, 0, time.Local)
	d := agent.StatusData{HatName: "VENOM", Connected: true,
		State: core.State{Mode: core.ModeWearing, Hat: "venom", Since: from.Add(45 * time.Minute)},
		Away:  &agent.AwayQuestion{ID: "x", From: from, To: from.Add(45 * time.Minute), Booked: "pause"}}
	sock, _ := fakeAgent(t, d)
	var out, errb bytes.Buffer
	app := &App{Out: &out, Err: &errb, Socket: sock, IsTTY: func() bool { return false }}
	if code := app.Execute([]string{"status"}); code != 0 {
		t.Fatal(errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || lines[1] != "Away 14:02–14:47 – booked as a break; hat away work|pause|keep" {
		t.Fatalf("output:\n%s", out.String())
	}
	d.Away.Booked = "work"
	if got := agent.AwayLine(d.Away); !strings.Contains(got, "counted as work") {
		t.Fatalf("work line: %q", got)
	}
	i18n.Set("de")
	if got := agent.AwayLine(d.Away); got != "Weg 14:02–14:47 – als Arbeit gezählt; hat away work|pause|keep" {
		t.Fatalf("german: %q", got)
	}
	if agent.AwayLine(nil) != "" {
		t.Fatal("line without question")
	}
}
