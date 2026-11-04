// Package agent is the background service of a device: it holds the SSE
// connection, applies hats through the modules, queues events while offline
// and serves the local socket for the CLI.
package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// Config is ~/.config/hatrack/agent.yaml.
type Config struct {
	Server   string `yaml:"server"`
	Device   string `yaml:"device"`
	Token    string `yaml:"token"`
	Language string `yaml:"language,omitempty"` // auto | en | de
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

// ConfigPath is the location of agent.yaml.
func ConfigPath() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "hatrack", "agent.yaml")
}

// DBPath is the location of the local state database.
func DBPath() string {
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "hatrack", "agent.db")
}

// SocketPath is the location of the agent socket.
func SocketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "hatrack", "agent.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("hatrack-%d", os.Getuid()), "agent.sock")
}

// LoadConfig reads agent.yaml. A missing file yields an empty config.
func LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigPath(), err)
	}
	return c, nil
}

// SaveConfig writes agent.yaml readable only by the user (it holds the token).
func SaveConfig(c Config) error {
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), b, 0o600)
}
