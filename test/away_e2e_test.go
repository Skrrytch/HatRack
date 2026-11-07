package test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// awayEvent is the part of an event log entry this test looks at.
type awayEvent struct {
	ID   string    `json:"id"`
	TS   time.Time `json:"ts"`
	Type string    `json:"type"`
	Hat  string    `json:"hat"`
}

// awayEvents reads the event log of the server with the device token.
func awayEvents(t *testing.T, url, token string) []awayEvent {
	t.Helper()
	req, _ := http.NewRequest("GET", url+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var ev []awayEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("events: %v: %s", err, body)
	}
	return ev
}

// writeIdle appends one idle state line to the fake sensor file.
func writeIdle(t *testing.T, path, attrs string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"sensor":"idle","attrs":` + attrs + "}\n"); err != nil {
		t.Fatal(err)
	}
}

// waitUntil polls cond for up to timeout; waitFor in e2e_test.go gives up
// after 5 s, which is too short for the 15 s stop delay of the tracker.
func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// near reports whether a and b differ by less than 3 s.
func near(a, b time.Time) bool {
	d := a.Sub(b)
	return d < 3*time.Second && d > -3*time.Second
}

// TestAwayEndToEnd runs server and agent as processes with a 60x time scale
// (5 min become 5 s): a locked screen stops the clock, the return starts it
// again with a question, and the answer moves the booking.
func TestAwayEndToEnd(t *testing.T) {
	w := newWorld(t)
	addr := freePort(t)
	w.url = "http://" + addr
	fake := filepath.Join(w.dir, "sensor.jsonl")

	w.background("server", "server", "--listen", addr)
	waitFor(t, "server", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	token := strings.TrimSpace(w.must("server", "device", "add", "testbox", "--config"))
	w.must("agent", "login", w.url, token, "--name", "testbox")

	saved := w.env
	w.env = append(append([]string{}, w.env...), "HATRACK_FAKE_SENSOR="+fake, "HATRACK_AWAY_SCALE=60")
	w.background("agent", "agent", "run")
	w.env = saved
	waitFor(t, "agent", func() bool { _, _, code := w.hat("status"); return code == 0 })

	cfgFile := filepath.Join(w.dir, "away.yaml")
	os.WriteFile(cfgFile, []byte("version: 1\nhats:\n  venom:\n    name: VENOM\n"), 0o644)
	w.must("config", "import", cfgFile)
	w.must("on", "venom")
	if out := w.must(); !strings.HasPrefix(out, "VENOM") {
		t.Fatalf("status after on: %q", out)
	}
	// the idle sensor starts with the agent; the fake file is read from the top
	time.Sleep(1500 * time.Millisecond)

	// the screen locks: the clock stops at the lock time
	t0 := time.Now()
	writeIdle(t, fake, `{"kind":"locked"}`)
	var off awayEvent
	waitUntil(t, 30*time.Second, "off event at the lock time", func() bool {
		for _, e := range awayEvents(t, w.url, token) {
			if e.Type == "off" && near(e.TS, t0) {
				off = e
				return true
			}
		}
		return false
	})
	if off.Hat != "venom" {
		t.Errorf("off event hat: %q, want venom", off.Hat)
	}

	// the user is back: the hat runs again, the status asks a question
	t1 := time.Now()
	writeIdle(t, fake, "null")
	waitUntil(t, 10*time.Second, "switch event at the return time", func() bool {
		for _, e := range awayEvents(t, w.url, token) {
			if e.Type == "switch" && e.Hat == "venom" && near(e.TS, t1) {
				return true
			}
		}
		return false
	})
	waitUntil(t, 10*time.Second, "question in the status", func() bool {
		out, _, code := w.hat("status")
		return code == 0 && len(strings.Split(strings.TrimSpace(out), "\n")) > 1
	})
	switchesBefore := 0
	for _, e := range awayEvents(t, w.url, token) {
		if e.Type == "switch" {
			switchesBefore++
		}
	}

	// the answer "work" books the absence as work: one switch just after the lock
	w.must("away", "work")
	var answer awayEvent
	waitUntil(t, 10*time.Second, "switch after the lock time", func() bool {
		for _, e := range awayEvents(t, w.url, token) {
			if e.Type == "switch" && e.Hat == "venom" && e.TS.After(off.TS) && near(e.TS, t0) {
				answer = e
				return true
			}
		}
		return false
	})
	if d := answer.TS.Sub(off.TS); d != time.Millisecond {
		t.Errorf("answer switch %v after the off, want 1ms", d)
	}

	// the question is gone from the status
	waitUntil(t, 10*time.Second, "status back to one line", func() bool {
		out, _, code := w.hat("status")
		return code == 0 && len(strings.Split(strings.TrimSpace(out), "\n")) == 1
	})
	switchesAfter := 0
	for _, e := range awayEvents(t, w.url, token) {
		if e.Type == "switch" {
			switchesAfter++
		}
	}
	if switchesAfter != switchesBefore+1 {
		t.Errorf("answer sent %d switch events, want exactly one", switchesAfter-switchesBefore)
	}
}
