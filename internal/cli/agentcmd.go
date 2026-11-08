package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
	"hatrack/internal/modules"
	"hatrack/internal/reexec"
	"hatrack/internal/sensors"
)

const unitName = "hatrack-agent.service"

func configHome() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func unitPath(name string) string { return filepath.Join(configHome(), "systemd", "user", name) }

func unitText(desc, exe string, args ...string) string {
	cmd := fmt.Sprintf("%q", exe)
	for _, a := range args {
		cmd += " " + a
	}
	return fmt.Sprintf(`[Unit]
Description=%s
# stopped before the session bus, so the desktop can still be reached when
# the agent takes the hat off at shutdown
After=dbus.socket

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, desc, cmd)
}

// installUnit writes a systemd user unit for this binary and starts it.
func installUnit(name, desc string, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	p := unitPath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(unitText(desc, exe, args...)), 0o644); err != nil {
		return "", err
	}
	// restart, not just "enable --now": a running agent keeps its old server
	// and token in memory, so a new setup would not take effect
	for _, args := range [][]string{{"daemon-reload"}, {"enable", name}, {"restart", name}} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl --user %v: %w\n%s", args, err, out)
		}
	}
	return p, nil
}

// restartAgentUnit restarts the agent service if it runs; tests replace it.
var restartAgentUnit = func() bool {
	if exec.Command("systemctl", "--user", "is-active", "--quiet", unitName).Run() != nil {
		return false
	}
	return exec.Command("systemctl", "--user", "restart", unitName).Run() == nil
}

// removeUnit stops and removes a systemd user unit.
func removeUnit(name string) {
	exec.Command("systemctl", "--user", "disable", "--now", name).Run()
	os.Remove(unitPath(name))
	exec.Command("systemctl", "--user", "daemon-reload").Run()
}

func (a *App) agentCmd() *cobra.Command {
	c := &cobra.Command{Use: "agent", Short: i18n.T("cli.agent.short")}

	var name string
	login := &cobra.Command{
		Use: "login <url> <token>", Short: i18n.T("cli.login.short"), Args: cobra.ExactArgs(2),
		Example: "  hat agent login http://127.0.0.1:8421 hat_AbC123...",
		RunE: func(cmd *cobra.Command, args []string) error {
			url, token := args[0], args[1]
			if _, err := agent.NewClient(url, token).State(cmd.Context()); err != nil {
				if errors.Is(err, agent.ErrUnauthorized) {
					return errors.New(i18n.T("err.unauthorized"))
				}
				return fmt.Errorf("%s: %w", i18n.T("err.unreachable", url), err)
			}
			cfg, _ := agent.LoadConfig()
			if name == "" {
				name, _ = os.Hostname()
			}
			cfg.Server, cfg.Token, cfg.Device = url, token, name
			if err := agent.SaveConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.login.done", agent.ConfigPath()))
			// a running agent service picks up the new login only on restart
			if restartAgentUnit() {
				fmt.Fprintln(a.Out, i18n.T("cli.login.restarted"))
			}
			return nil
		},
	}
	login.Flags().StringVar(&name, "name", "", i18n.T("cli.login.name"))

	run := &cobra.Command{
		Use: "run", Short: i18n.T("cli.run.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return reexec.Run(cmd.Context(), "agent", a.runAgent) },
	}

	install := &cobra.Command{
		Use: "install", Short: i18n.T("cli.install.short"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := installUnit(unitName, "HatRack agent", "agent", "run")
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.install.done", p))
			return nil
		},
	}

	uninstall := &cobra.Command{
		Use: "uninstall", Short: i18n.T("cli.uninstall.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			removeUnit(unitName)
			if err := a.restoreNeutral(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.uninstall.done"))
			return nil
		},
	}
	c.AddCommand(login, run, install, uninstall)
	return c
}

func (a *App) newAgent() (*agent.Agent, *agent.DB, error) {
	cfg, err := agent.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	if cfg.Server == "" || cfg.Token == "" {
		return nil, nil, errors.New(i18n.T("err.not_logged_in"))
	}
	db, err := agent.OpenDB(agent.DBPath())
	if err != nil {
		return nil, nil, err
	}
	reg := modules.Builtin(nil)
	mods, errs := modules.LoadExternal(modules.ExternalDir())
	for _, m := range mods {
		reg.Register(m)
	}
	for _, e := range errs {
		log.Printf("external module skipped: %v", e)
	}
	ag := agent.New(cfg, db, reg)
	ag.SetSensors(sensors.Builtin())
	if err := ag.Load(); err != nil {
		db.Close()
		return nil, nil, err
	}
	return ag, db, nil
}

func (a *App) runAgent(parent context.Context) error {
	agent.Version = Version
	ag, db, err := a.newAgent()
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// started at boot: the desktop's DISPLAY comes later (sensors, launch)
	go agent.WatchSessionEnv(ctx, agent.UserManagerEnv)
	errc := make(chan error, 1)
	go func() { errc <- ag.Serve(ctx, a.Socket) }()
	runDone := make(chan struct{})
	go func() { ag.Run(ctx); close(runDone) }()
	log.Printf("hatrack agent started (socket %s)", a.Socket)
	var runErr error
	select {
	case runErr = <-errc:
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		// let Run stop the sensors and send the last observations
		select {
		case <-runDone:
		case <-time.After(6 * time.Second):
		}
	}
	// A stop signal during the shutdown of the user's session takes the hat
	// off. A plain restart (update, `hat agent install`) must not.
	if ctx.Err() != nil && sessionEnding() {
		tctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := ag.TakeOff(tctx); err != nil {
			log.Printf("taking the hat off at shutdown: %v", err)
		}
	}
	return runErr
}

// shutdownGrace is how long the agent may take to take the hat off while the
// session ends.
const shutdownGrace = 8 * time.Second

// sessionEnding reports whether the user's systemd instance is shutting
// down (shutdown, reboot or the last logout). Tests replace it.
var sessionEnding = func() bool {
	out, _ := exec.Command("systemctl", "--user", "is-system-running").Output()
	return strings.TrimSpace(string(out)) == "stopping"
}

func (a *App) restoreNeutral(ctx context.Context) error {
	ag, db, err := a.newAgent()
	if err != nil {
		return nil // nothing was ever set up
	}
	defer db.Close()
	for _, f := range ag.RestoreAll(ctx) {
		fmt.Fprintln(a.Err, i18n.T("cli.warning", f.Error()))
	}
	return nil
}
