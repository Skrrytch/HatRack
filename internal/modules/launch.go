package modules

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"hatrack/internal/core"
)

// launchModule opens what a hat needs when it is put on: programs (by their
// .desktop id) and addresses, folders or files (with xdg-open). It does not
// close anything when the hat comes off; that is left to the user.
type launchModule struct {
	b *xdgLaunch
}

// Starter starts a program without waiting for it. Tests replace it.
type Starter func(name string, args ...string) error

// StartDetached starts a program in its own session, so it outlives the agent.
func StartDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it when it ends
	return nil
}

// NewLaunch returns the launch module. A nil starter starts real programs.
func NewLaunch(start Starter) Module {
	if start == nil {
		start = StartDetached
	}
	return &launchModule{b: &xdgLaunch{start: start}}
}

func (m *launchModule) ID() string          { return "launch" }
func (m *launchModule) RunsIn() RunsIn      { return RunsInAgent }
func (m *launchModule) Backends() []Backend { return nil }
func (m *launchModule) ConfigSchema() []byte {
	return []byte(`{"type":"object","additionalProperties":false,"properties":{` + commonProps + `,
	  "apps":{"type":"array","items":{"type":"string"}},
	  "open":{"type":"array","items":{"type":"string"}}}}`)
}

// Active reports the backend for the capabilities; launch needs xdg-open.
func (m *launchModule) Active(ctx context.Context) Backend {
	if m.b.Detect(ctx) {
		return m.b
	}
	return nil
}

type launchCfg struct {
	Apps []string `json:"apps"`
	Open []string `json:"open"`
}

func (m *launchModule) Enter(ctx context.Context, sw Switch, raw json.RawMessage) error {
	var cfg launchCfg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	var errs []error
	for _, app := range cfg.Apps {
		if app = strings.TrimSpace(app); app == "" {
			continue
		}
		id := strings.TrimSuffix(app, ".desktop")
		if desktopFile(id) == "" {
			errs = append(errs, fmt.Errorf("launch: no program %q (no %s.desktop)", app, id))
			continue
		}
		if err := m.b.start("gtk-launch", id); err != nil {
			errs = append(errs, fmt.Errorf("launch %s: %w", app, err))
		}
	}
	for _, target := range cfg.Open {
		if target = strings.TrimSpace(target); target == "" {
			continue
		}
		if !strings.Contains(target, "://") {
			target = core.ExpandPath(target)
			if _, err := os.Stat(target); err != nil {
				errs = append(errs, fmt.Errorf("launch: %s does not exist", target))
				continue
			}
		}
		if err := m.b.start("xdg-open", target); err != nil {
			errs = append(errs, fmt.Errorf("open %s: %w", target, err))
		}
	}
	return errors.Join(errs...)
}

// Leave does nothing: closing windows on someone's behalf would lose work.
func (m *launchModule) Leave(ctx context.Context, sw Switch, raw json.RawMessage) error { return nil }

// xdgLaunch is the freedesktop backend: gtk-launch and xdg-open.
type xdgLaunch struct{ start Starter }

func (b *xdgLaunch) Name() string { return "xdg" }

func (b *xdgLaunch) Detect(ctx context.Context) bool {
	_, err := exec.LookPath("xdg-open")
	return err == nil
}

func (b *xdgLaunch) Apply(ctx context.Context, cfg json.RawMessage) error { return nil }

func (b *xdgLaunch) Snapshot(ctx context.Context) (json.RawMessage, error) { return nil, nil }

// Choices lists the installed programs for the pick list in Setup.
func (b *xdgLaunch) Choices(ctx context.Context) map[string][]Choice {
	return map[string][]Choice{"apps": installedApps()}
}

func appDirs() []string {
	var dirs []string
	for _, d := range dataDirs() {
		dirs = append(dirs, filepath.Join(d, "applications"))
	}
	return dirs
}

// desktopFile finds the .desktop file of a program id, or "".
func desktopFile(id string) string {
	for _, d := range appDirs() {
		p := filepath.Join(d, id+".desktop")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// installedApps reads the visible programs: id and display name.
func installedApps() []Choice {
	seen := map[string]bool{}
	var out []Choice
	for _, d := range appDirs() {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".desktop")
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			if name, show := desktopName(filepath.Join(d, e.Name())); show {
				out = append(out, Choice{Value: id, Label: name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// desktopName reads Name= of the [Desktop Entry] section, and whether the
// program is meant to be shown (not NoDisplay or Hidden, an application).
func desktopName(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	name, show, inEntry, isApp := "", true, false, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "Name":
			name = v
		case "NoDisplay", "Hidden":
			show = show && v != "true"
		case "Type":
			isApp = v == "Application"
		}
	}
	return name, show && isApp && name != ""
}
