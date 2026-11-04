package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// The mqtt module publishes the current state to an MQTT broker, so other
// systems can show it or react to it (Home Assistant, Node-RED, a lamp at
// the door). It runs where the core runs, so there is one publisher no
// matter how many devices there are. It only publishes; it never subscribes,
// so nothing outside can change the time log through it.
//
// Topics, all retained:
//
//	<topic>/state         {"mode":"wearing","working":true,"hat":"…","hat_name":"…","color":"…","activity":"…","since":"…"}
//	<topic>/availability  online | offline (offline is also the last will)
//
// With Home Assistant discovery it also announces four entities of a device
// "HatRack": hat, activity, working and since. Details are never sent.

const mqttSchema = `{"type":"object","additionalProperties":false,"required":["broker"],"properties":{` + commonProps + `,
  "broker":{"type":"string","minLength":1},
  "username":{"type":"string"},
  "password":{"type":"string"},
  "topic":{"type":"string","pattern":"^[^#+]+$"},
  "discovery":{"type":"boolean"},
  "discovery_prefix":{"type":"string","pattern":"^[^#+]+$"}}}`

// MQTTConfig is the module's configuration.
type MQTTConfig struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	Broker          string `json:"broker"`
	Username        string `json:"username,omitempty"`
	Password        string `json:"password,omitempty"`
	Topic           string `json:"topic,omitempty"`
	Discovery       bool   `json:"discovery,omitempty"`
	DiscoveryPrefix string `json:"discovery_prefix,omitempty"`
}

func (c MQTTConfig) topic() string {
	if t := strings.Trim(c.Topic, "/"); t != "" {
		return t
	}
	return "hatrack"
}

func (c MQTTConfig) prefix() string {
	if p := strings.Trim(c.DiscoveryPrefix, "/"); p != "" {
		return p
	}
	return "homeassistant"
}

// brokerURL accepts mqtt:// and mqtts:// as well as the tcp:// and ssl://
// the client library knows; a bare host gets mqtt:// and port 1883.
func brokerURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "mqtt://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("mqtt: bad broker address %q", s)
	}
	switch u.Scheme {
	case "mqtt", "tcp":
		u.Scheme = "tcp"
		if u.Port() == "" {
			u.Host += ":1883"
		}
	case "mqtts", "ssl", "tls":
		u.Scheme = "ssl"
		if u.Port() == "" {
			u.Host += ":8883"
		}
	default:
		return "", fmt.Errorf("mqtt: unknown scheme %q (use mqtt:// or mqtts://)", u.Scheme)
	}
	return u.Scheme + "://" + u.Host, nil
}

// MQTTState is what the module publishes.
type MQTTState struct {
	Mode     string `json:"mode"`
	Working  bool   `json:"working"`
	Hat      string `json:"hat,omitempty"`
	HatName  string `json:"hat_name,omitempty"`
	Color    string `json:"color,omitempty"`
	Activity string `json:"activity,omitempty"`
	Since    string `json:"since,omitempty"` // RFC 3339
}

// NewMQTT returns the mqtt module.
func NewMQTT() *MQTTModule { return &MQTTModule{} }

// MQTTModule keeps one connection to the broker. Configure (re)connects when
// the settings change, Publish sends the state.
type MQTTModule struct {
	mu      sync.Mutex
	cfg     MQTTConfig
	on      bool
	client  paho.Client
	last    *MQTTState
	version string
}

func (m *MQTTModule) ID() string           { return "mqtt" }
func (m *MQTTModule) RunsIn() RunsIn       { return RunsInCore }
func (m *MQTTModule) ConfigSchema() []byte { return []byte(mqttSchema) }
func (m *MQTTModule) Backends() []Backend  { return nil }

// PropertyOrder is the order of the fields in Setup: where, who, what.
func (m *MQTTModule) PropertyOrder() []string {
	return []string{"broker", "username", "password", "topic", "discovery", "discovery_prefix"}
}

// Enter and Leave do nothing: the module follows the state, not the hooks.
func (m *MQTTModule) Enter(context.Context, Switch, json.RawMessage) error { return nil }
func (m *MQTTModule) Leave(context.Context, Switch, json.RawMessage) error { return nil }

// SetVersion names the software version in the discovery messages.
func (m *MQTTModule) SetVersion(v string) {
	m.mu.Lock()
	m.version = v
	m.mu.Unlock()
}

// Configure applies new settings (raw nil or disabled turns the module off).
// It connects in the background; a broker that cannot be reached is retried
// by the client and does not hold up the caller.
func (m *MQTTModule) Configure(raw json.RawMessage) error {
	var cfg MQTTConfig
	on := len(raw) > 0
	if on {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		on = cfg.Enabled == nil || *cfg.Enabled
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if on == m.on && cfg == m.cfg {
		return nil
	}
	// entities announced under the old settings go away unless they stay
	keep := on && cfg.Discovery && cfg.topic() == m.cfg.topic() && cfg.prefix() == m.cfg.prefix()
	if m.on && m.cfg.Discovery && !keep && m.client != nil && m.client.IsConnectionOpen() {
		m.announce(m.client, m.cfg, m.version, false)
	}
	m.disconnect()
	m.cfg, m.on = cfg, on
	if !on {
		return nil
	}
	broker, err := brokerURL(cfg.Broker)
	if err != nil {
		m.on = false
		return err
	}
	avail := cfg.topic() + "/availability"
	opts := paho.NewClientOptions().AddBroker(broker).
		SetClientID("hatrack-"+sanitizeID(cfg.topic())).
		SetUsername(cfg.Username).SetPassword(cfg.Password).
		SetCleanSession(true).SetAutoReconnect(true).SetConnectRetry(true).
		SetConnectRetryInterval(10*time.Second).SetMaxReconnectInterval(time.Minute).
		SetWill(avail, "offline", 1, true)
	opts.SetOnConnectHandler(func(c paho.Client) {
		// after every (re)connect: availability, discovery, the last state
		m.mu.Lock()
		cfg, last, version := m.cfg, m.last, m.version
		m.mu.Unlock()
		m.send(c, avail, "online")
		if cfg.Discovery {
			m.announce(c, cfg, version, true)
		}
		if last != nil {
			m.sendState(c, cfg, *last)
		}
	})
	opts.SetConnectionLostHandler(func(_ paho.Client, err error) { log.Printf("mqtt: connection lost: %v", err) })
	m.client = paho.NewClient(opts)
	m.client.Connect() // retries in the background (SetConnectRetry)
	return nil
}

// Publish sends the state if the module is on; it never blocks for long.
func (m *MQTTModule) Publish(st MQTTState) {
	m.mu.Lock()
	m.last = &st
	c, cfg, on := m.client, m.cfg, m.on
	m.mu.Unlock()
	if on && c != nil && c.IsConnectionOpen() {
		m.sendState(c, cfg, st)
	}
}

// Close says offline and disconnects.
func (m *MQTTModule) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnect()
	m.on = false
}

// disconnect ends the connection cleanly: offline first, so the retained
// availability is right. Caller holds m.mu.
func (m *MQTTModule) disconnect() {
	if m.client == nil {
		return
	}
	if m.client.IsConnectionOpen() {
		m.send(m.client, m.cfg.topic()+"/availability", "offline")
	}
	m.client.Disconnect(250)
	m.client = nil
}

func (m *MQTTModule) sendState(c paho.Client, cfg MQTTConfig, st MQTTState) {
	b, _ := json.Marshal(st)
	m.send(c, cfg.topic()+"/state", string(b))
}

func (m *MQTTModule) send(c paho.Client, topic, payload string) {
	t := c.Publish(topic, 1, true, payload)
	if !t.WaitTimeout(5*time.Second) || t.Error() != nil {
		log.Printf("mqtt: publish %s: %v", topic, errOrTimeout(t.Error()))
	}
}

func errOrTimeout(err error) error {
	if err == nil {
		return errors.New("timeout")
	}
	return err
}

var idRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeID(s string) string { return strings.Trim(idRe.ReplaceAllString(s, "_"), "_") }

// discoveryEntity is one Home Assistant entity.
type discoveryEntity struct {
	component, key string
	extra          map[string]any
}

func discoveryEntities() []discoveryEntity {
	return []discoveryEntity{
		{"sensor", "hat", map[string]any{
			"value_template":           "{{ value_json.hat_name if value_json.hat_name else 'none' }}",
			"json_attributes_template": "{{ {'key': value_json.hat, 'color': value_json.color, 'mode': value_json.mode} | tojson }}",
			"icon":                     "mdi:hat-fedora"}},
		{"sensor", "activity", map[string]any{
			"value_template": "{{ value_json.activity if value_json.activity else 'none' }}",
			"icon":           "mdi:format-list-checks"}},
		{"binary_sensor", "working", map[string]any{
			"value_template": "{{ 'ON' if value_json.working else 'OFF' }}",
			"icon":           "mdi:briefcase-clock"}},
		{"sensor", "since", map[string]any{
			"value_template": "{{ value_json.since if value_json.since else 'None' }}",
			"device_class":   "timestamp"}},
	}
}

// discoveryTopic is where Home Assistant expects an entity's configuration.
func discoveryTopic(cfg MQTTConfig, e discoveryEntity) string {
	return fmt.Sprintf("%s/%s/%s/%s/config", cfg.prefix(), e.component, sanitizeID(cfg.topic()), e.key)
}

// DiscoveryMessages returns topic -> payload of the Home Assistant discovery
// messages for these settings.
func DiscoveryMessages(cfg MQTTConfig, version string) map[string]string {
	node := sanitizeID(cfg.topic())
	device := map[string]any{"identifiers": []string{node}, "name": "HatRack", "manufacturer": "HatRack", "model": "HatRack Server"}
	if version != "" {
		device["sw_version"] = version
	}
	out := map[string]string{}
	for _, e := range discoveryEntities() {
		msg := map[string]any{
			"name":                  e.key,
			"unique_id":             node + "_" + e.key,
			"state_topic":           cfg.topic() + "/state",
			"availability_topic":    cfg.topic() + "/availability",
			"payload_available":     "online",
			"payload_not_available": "offline",
			"device":                device,
		}
		if e.key == "hat" {
			msg["json_attributes_topic"] = cfg.topic() + "/state"
		}
		for k, v := range e.extra {
			msg[k] = v
		}
		b, _ := json.Marshal(msg)
		out[discoveryTopic(cfg, e)] = string(b)
	}
	return out
}

// announce publishes the discovery messages, or with on=false removes them
// (an empty retained message deletes the entity in Home Assistant).
func (m *MQTTModule) announce(c paho.Client, cfg MQTTConfig, version string, on bool) {
	for topic, payload := range DiscoveryMessages(cfg, version) {
		if !on {
			payload = ""
		}
		m.send(c, topic, payload)
	}
}
