package test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"hatrack/internal/store"
)

// TestObservationsEndToEnd runs server and agent as processes, feeds the
// agent's fake sensors through a file and checks what arrives at the server.
func TestObservationsEndToEnd(t *testing.T) {
	t.Run("window on", func(t *testing.T) { runObservations(t, true) })
	t.Run("window off", func(t *testing.T) { runObservations(t, false) })
}

// fakeState appends one state line to the fake sensor file.
func fakeState(t *testing.T, path, sensor, app string) {
	t.Helper()
	attrs := map[string]string{"state": "idle"}
	if sensor == "window" {
		attrs = map[string]string{"app": app, "title": app + " title"}
	}
	line, _ := json.Marshal(map[string]any{"sensor": sensor, "attrs": attrs})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func runObservations(t *testing.T, windowOn bool) {
	w := newWorld(t)
	addr := freePort(t)
	w.url = "http://" + addr
	fake := filepath.Join(w.dir, "sensor.jsonl")

	srv := w.background("server", "server", "--listen", addr)
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
	w.env = append(append([]string{}, w.env...), "HATRACK_FAKE_SENSOR="+fake)
	agent := w.background("agent", "agent", "run")
	w.env = saved
	waitFor(t, "agent", func() bool { _, _, code := w.hat("status"); return code == 0 })

	eventCount := func() int {
		req, _ := http.NewRequest("GET", w.url+"/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var ev []json.RawMessage
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Fatalf("events: %v: %s", err, body)
		}
		return len(ev)
	}
	// switch the window sensor on (the import goes through the agent)
	if windowOn {
		cfgFile := filepath.Join(w.dir, "observe.yaml")
		os.WriteFile(cfgFile, []byte("version: 1\nobserve:\n  devices:\n    testbox:\n      window: true\nhats:\n  venom:\n    name: VENOM\n"), 0o644)
		w.must("config", "import", cfgFile)
	}

	eventsBefore := eventCount()

	// "warm" absorbs a sensor that starts late and reads the file in one go
	step := 1300 * time.Millisecond
	fakeState(t, fake, "window", "warm")
	time.Sleep(step)
	for _, app := range []string{"code", "firefox", "terminal"} {
		fakeState(t, fake, "window", app)
		time.Sleep(step)
	}
	fakeState(t, fake, "idle", "")
	time.Sleep(step)

	// a graceful stop closes the spans and flushes the queue
	if err := agent.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("agent did not stop")
	}

	if got := eventCount(); got != eventsBefore {
		t.Errorf("observations changed the event log: %d -> %d", eventsBefore, got)
	}

	// stop the server, then read what it stored
	srv.Process.Signal(syscall.SIGTERM)
	srvDone := make(chan struct{})
	go func() { srv.Wait(); close(srvDone) }()
	select {
	case <-srvDone:
	case <-time.After(20 * time.Second):
		t.Fatal("server did not stop")
	}
	db, err := store.Open(filepath.Join(w.dir, "data", "hatrack-server", "hatrack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	obs, err := db.ObservationsBetween(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "testbox", "")
	if err != nil {
		t.Fatal(err)
	}
	apps := map[string]bool{}
	idle := 0
	for _, o := range obs {
		switch o.Sensor {
		case "window":
			apps[o.Attrs["app"]] = true
			if !o.End.After(o.Start) {
				t.Errorf("empty span: %+v", o)
			}
		case "idle":
			idle++
		}
	}
	if idle != 1 {
		t.Errorf("idle spans: %d, want 1 (%+v)", idle, obs)
	}
	if windowOn {
		for _, app := range []string{"code", "firefox", "terminal"} {
			if !apps[app] {
				t.Errorf("window span for %s missing: %+v", app, obs)
			}
		}
	} else if len(apps) != 0 {
		t.Errorf("window spans stored although the sensor is off: %+v", obs)
	}

	// the device reported the fake backend for both sensors
	dev, err := db.DeviceByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	var caps map[string]struct {
		Backend   string `json:"backend"`
		Available bool   `json:"available"`
	}
	if err := json.Unmarshal(dev.Capabilities, &caps); err != nil {
		t.Fatalf("caps %q: %v", dev.Capabilities, err)
	}
	for _, id := range []string{"sensor:window", "sensor:idle"} {
		if c := caps[id]; c.Backend != "fake" || !c.Available {
			t.Errorf("%s capability: %+v", id, c)
		}
	}
}
