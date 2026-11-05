package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
	"hatrack/internal/tray"
)

// Seams for tests: the real ones touch systemd and the desktop.
var (
	removeUnitFn = removeUnit
	removeTray   = func() { tray.Hide() }
)

// dataDirs are the directories HatRack writes on this computer. The
// configuration directory also holds external modules the user wrote.
func dataDirs() []string {
	xdg := func(env, def string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, def)
	}
	return []string{
		filepath.Dir(agent.ConfigPath()), // agent.yaml, modules/
		filepath.Dir(agent.DBPath()),     // the agent's state and queue
		defaultData(),                    // the server's database
		filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "hatrack"), // wallpapers
		filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "hatrack"),      // previews
	}
}

// runRemove undoes `hat setup`: services, tray autostart, the look. The data
// stays unless purge is set, so a later setup continues where it left off.
func (a *App) runRemove(ctx context.Context, o setupOpts) error {
	d := &dialog{a: a, r: bufio.NewReader(a.In), yes: o.yes}
	say := func(key string, args ...any) { fmt.Fprintln(a.Out, i18n.T(key, args...)) }

	removeUnitFn(unitName)
	if err := a.restoreNeutral(ctx); err != nil {
		fmt.Fprintln(a.Err, i18n.T("cli.warning", err.Error()))
	}
	removeUnitFn("hatrack-server.service")
	os.Remove(autostartPath())
	removeTray()
	say("cli.remove.done")

	var existing []string
	for _, dir := range dataDirs() {
		if _, err := os.Stat(dir); err == nil {
			existing = append(existing, dir)
		}
	}
	if !o.purge {
		if len(existing) > 0 {
			say("cli.remove.kept")
			for _, dir := range existing {
				fmt.Fprintln(a.Out, "  "+dir)
			}
			say("cli.remove.purge_hint")
		}
	} else if len(existing) > 0 {
		say("cli.remove.purge_list")
		for _, dir := range existing {
			fmt.Fprintln(a.Out, "  "+dir)
		}
		if !d.confirmNo(i18n.T("cli.remove.purge_ask")) {
			say("cli.remove.purge_skipped")
		} else {
			for _, dir := range existing {
				if err := os.RemoveAll(dir); err != nil {
					fmt.Fprintln(a.Err, i18n.T("cli.warning", err.Error()))
				}
			}
			say("cli.remove.purged")
		}
	}

	// the binary itself: by the package manager, or by hand
	if exe, err := updateExe(); err == nil {
		if strings.HasPrefix(exe, "/usr/") {
			say("cli.remove.binary_pkg")
		} else {
			say("cli.remove.binary", exe)
		}
	}
	return nil
}

// confirmNo asks a yes/no question whose default is no; --yes answers yes.
func (d *dialog) confirmNo(q string) bool {
	if d.yes {
		return true
	}
	yes, no := i18n.T("cli.setup.yes_key"), i18n.T("cli.setup.no_key")
	fmt.Fprintf(d.a.Out, "%s [%s/%s] ", q, yes, strings.ToUpper(no))
	s := strings.ToLower(d.line())
	return s != "" && strings.HasPrefix(s, yes)
}
