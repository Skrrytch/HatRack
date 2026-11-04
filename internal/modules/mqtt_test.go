package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// testBroker is an MQTT broker in the test process that records the last
// message per topic.
type testBroker struct {
	addr string
	mu   sync.Mutex
	last map[string]string
	seen map[string]int
}

func newTestBroker(t *testing.T) *testBroker {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	addr := l.Addr().String()
	l.Close()
	srv := mqtt.New(&mqtt.Options{InlineClient: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	srv.AddHook(new(auth.AllowHook), nil)
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "t", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	b := &testBroker{addr: addr, last: map[string]string{}, seen: map[string]int{}}
	srv.Subscribe("#", 1, func(_ *mqtt.Client, _ packets.Subscription, pk packets.Packet) {
		b.mu.Lock()
		b.last[pk.TopicName] = string(pk.Payload)
		b.seen[pk.TopicName]++
		b.mu.Unlock()
	})
	return b
}

func (b *testBroker) get(topic string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.last[topic]
	return v, ok
}

func (b *testBroker) wait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestBrokerURL(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.5":                "tcp://192.168.1.5:1883",
		"mqtt://broker:1884":         "tcp://broker:1884",
		"mqtts://broker":             "ssl://broker:8883",
		"tcp://h:1883":               "tcp://h:1883",
		"mqtt://user@h/ignored/path": "tcp://h:1883",
	} {
		if got, err := brokerURL(in); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if _, err := brokerURL("http://h"); err == nil {
		t.Error("http is not a broker")
	}
}

func TestMQTTPublish(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	b := newTestBroker(t)
	m := NewMQTT()
	m.SetVersion("t1")
	// the state of before the connection is sent once connected
	m.Publish(MQTTState{Mode: "wearing", Working: true, Hat: "tickets", HatName: "Tickets", Color: "#8c4ea3", Activity: "Testen", Since: "2026-11-04T10:00:00+01:00"})
	cfg, _ := json.Marshal(map[string]any{"broker": "mqtt://" + b.addr, "discovery": true})
	if err := m.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	b.wait(t, "online and state", func() bool {
		a, _ := b.get("hatrack/availability")
		s, _ := b.get("hatrack/state")
		return a == "online" && strings.Contains(s, `"activity":"Testen"`)
	})
	// Home Assistant discovery: four entities of one device, no detail
	for _, topic := range []string{"homeassistant/sensor/hatrack/hat/config", "homeassistant/sensor/hatrack/activity/config",
		"homeassistant/binary_sensor/hatrack/working/config", "homeassistant/sensor/hatrack/since/config"} {
		b.wait(t, topic, func() bool { v, _ := b.get(topic); return v != "" })
		v, _ := b.get(topic)
		var msg map[string]any
		if err := json.Unmarshal([]byte(v), &msg); err != nil || msg["state_topic"] != "hatrack/state" ||
			msg["availability_topic"] != "hatrack/availability" || !strings.HasPrefix(msg["unique_id"].(string), "hatrack_") {
			t.Fatalf("%s: %s", topic, v)
		}
	}
	since, _ := b.get("homeassistant/sensor/hatrack/since/config")
	if !strings.Contains(since, `"device_class":"timestamp"`) || !strings.Contains(since, `"sw_version":"t1"`) {
		t.Fatalf("since: %s", since)
	}

	// a new state
	m.Publish(MQTTState{Mode: "paused", Hat: "tickets", HatName: "Tickets"})
	b.wait(t, "paused", func() bool { s, _ := b.get("hatrack/state"); return strings.Contains(s, `"mode":"paused"`) })
	if s, _ := b.get("hatrack/state"); strings.Contains(s, "working\":true") || strings.Contains(s, "detail") {
		t.Fatalf("paused state: %s", s)
	}

	// discovery off removes the entities; the same settings again change nothing
	off, _ := json.Marshal(map[string]any{"broker": "mqtt://" + b.addr})
	m.Configure(off)
	b.wait(t, "entities removed", func() bool { v, ok := b.get("homeassistant/sensor/hatrack/hat/config"); return ok && v == "" })
	b.wait(t, "online again", func() bool { a, _ := b.get("hatrack/availability"); return a == "online" })
	b.mu.Lock()
	n := b.seen["hatrack/availability"]
	b.mu.Unlock()
	m.Configure(off)
	time.Sleep(200 * time.Millisecond)
	b.mu.Lock()
	if b.seen["hatrack/availability"] != n {
		t.Fatal("unchanged settings do not reconnect")
	}
	b.mu.Unlock()

	// disabled: goodbye
	dis, _ := json.Marshal(map[string]any{"broker": "mqtt://" + b.addr, "enabled": false})
	m.Configure(dis)
	b.wait(t, "offline", func() bool { a, _ := b.get("hatrack/availability"); return a == "offline" })
	m.Close()
}

func TestMQTTIsACoreModule(t *testing.T) {
	m := NewMQTT()
	if m.RunsIn() != RunsInCore {
		t.Fatal("runs in the core")
	}
	if err := validateAgainst(m.ConfigSchema(), map[string]any{"broker": "h", "topic": "a/#"}, "x"); err == nil {
		t.Fatal("wildcards are not topics")
	}
	// the agent neither runs it nor reports it
	reg := &Registry{}
	reg.Register(m)
	h := NewHost(reg)
	if _, ok := h.Capabilities(t.Context())["mqtt"]; ok {
		t.Fatal("not a device capability")
	}
	if f := h.Run(t.Context(), "enter", Switch{}, map[string]json.RawMessage{"mqtt": []byte(`{"broker":"x"}`)}); len(f) != 0 {
		t.Fatal(f)
	}
}

func TestMQTTSameSettingsKeepTheConnection(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	b := newTestBroker(t)
	m := NewMQTT()
	defer m.Close()
	cfg, _ := json.Marshal(map[string]any{"broker": "mqtt://" + b.addr, "enabled": true})
	m.Configure(cfg)
	b.wait(t, "online", func() bool { a, _ := b.get("hatrack/availability"); return a == "online" })
	m.mu.Lock()
	first := m.client
	m.mu.Unlock()
	// the same settings decoded again (a new *bool) change nothing
	m.Configure(cfg)
	m.mu.Lock()
	same := m.client == first
	m.mu.Unlock()
	if !same {
		t.Fatal("unchanged settings reconnected")
	}
	// many quick states: the newest one is what stays retained
	for i := 0; i < 20; i++ {
		m.Publish(MQTTState{Mode: "wearing", Hat: "h", Activity: fmt.Sprint(i)})
	}
	b.wait(t, "newest state", func() bool { s, _ := b.get("hatrack/state"); return strings.Contains(s, `"activity":"19"`) })
	time.Sleep(200 * time.Millisecond)
	if s, _ := b.get("hatrack/state"); !strings.Contains(s, `"activity":"19"`) {
		t.Fatalf("an older state arrived last: %s", s)
	}
}
