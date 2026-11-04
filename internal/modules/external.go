package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/goccy/go-yaml"
)

// External modules live in <dir>/<id>/ with a module.yaml and an executable.
// The agent calls the executable with the hook name as argument and a JSON
// object on stdin: {"hook", "switch", "config"}. The answer is a JSON object
// on stdout: {"ok": true} or {"ok": false, "error": "..."}, and for the
// hook "snapshot" also {"snapshot": {...}}.
type external struct {
	id     string
	dir    string
	exe    string
	hooks  []string
	schema []byte
}

type externalManifest struct {
	ID         string         `yaml:"id"`
	Executable string         `yaml:"executable"`
	Hooks      []string       `yaml:"hooks"`
	Schema     map[string]any `yaml:"schema"`
}

// LoadExternal reads all external modules below dir. A missing directory is
// not an error. Broken modules are returned as errors next to the good ones.
func LoadExternal(dir string) ([]Module, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var mods []Module
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := loadExternal(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("module %s: %w", e.Name(), err))
			continue
		}
		mods = append(mods, m)
	}
	return mods, errs
}

func loadExternal(dir string) (Module, error) {
	b, err := os.ReadFile(filepath.Join(dir, "module.yaml"))
	if err != nil {
		return nil, err
	}
	var mf externalManifest
	if err := yaml.Unmarshal(b, &mf); err != nil {
		return nil, err
	}
	if mf.ID == "" {
		return nil, fmt.Errorf("module.yaml has no id")
	}
	if mf.Executable == "" {
		mf.Executable = "run"
	}
	exe := filepath.Join(dir, filepath.Base(mf.Executable))
	if fi, err := os.Stat(exe); err != nil || fi.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("%s is missing or not executable", mf.Executable)
	}
	var schema []byte
	if mf.Schema != nil {
		if schema, err = json.Marshal(mf.Schema); err != nil {
			return nil, err
		}
	}
	m := &external{id: mf.ID, dir: dir, exe: exe, hooks: mf.Hooks, schema: schema}
	if slices.Contains(mf.Hooks, "snapshot") && slices.Contains(mf.Hooks, "restore") {
		return &externalSnap{m}, nil
	}
	return m, nil
}

func (m *external) ID() string           { return m.id }
func (m *external) RunsIn() RunsIn       { return RunsInAgent }
func (m *external) ConfigSchema() []byte { return m.schema }
func (m *external) Backends() []Backend  { return nil }

func (m *external) Enter(ctx context.Context, sw Switch, cfg json.RawMessage) error {
	_, err := m.call(ctx, "enter", sw, cfg)
	return err
}

func (m *external) Leave(ctx context.Context, sw Switch, cfg json.RawMessage) error {
	_, err := m.call(ctx, "leave", sw, cfg)
	return err
}

func (m *external) call(ctx context.Context, hook string, sw Switch, cfg json.RawMessage) (json.RawMessage, error) {
	if !slices.Contains(m.hooks, hook) {
		return nil, nil
	}
	in, _ := json.Marshal(map[string]any{"hook": hook, "switch": sw, "config": cfg})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.exe, hook)
	cmd.Dir = m.dir
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", m.id, hook, err, bytes.TrimSpace(stderr.Bytes()))
	}
	var reply struct {
		OK       bool            `json:"ok"`
		Error    string          `json:"error"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("%s %s: bad answer: %w", m.id, hook, err)
	}
	if !reply.OK {
		return nil, fmt.Errorf("%s %s: %s", m.id, hook, reply.Error)
	}
	return reply.Snapshot, nil
}

// externalSnap adds snapshot and restore for modules that declare both hooks.
type externalSnap struct{ *external }

func (m *externalSnap) Snapshot(ctx context.Context) (json.RawMessage, error) {
	return m.call(ctx, "snapshot", Switch{}, nil)
}

func (m *externalSnap) Restore(ctx context.Context, snap json.RawMessage) error {
	_, err := m.call(ctx, "restore", Switch{}, snap)
	return err
}
