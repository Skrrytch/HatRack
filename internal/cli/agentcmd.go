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
	"syscall"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
	"hatrack/internal/modules"
)

const unitName = "hatrack-agent.service"

func unitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", unitName)
}

func unitText(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=HatRack agent

[Service]
ExecStart=%q agent run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
}

func (a *App) agentCmd() *cobra.Command {
	c := &cobra.Command{Use: "agent", Short: i18n.T("cli.agent.short")}

	var name string
	login := &cobra.Command{
		Use: "login <url> <token>", Short: i18n.T("cli.login.short"), Args: cobra.ExactArgs(2),
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
			return nil
		},
	}
	login.Flags().StringVar(&name, "name", "", i18n.T("cli.login.name"))

	run := &cobra.Command{
		Use: "run", Short: i18n.T("cli.run.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runAgent(cmd.Context()) },
	}

	install := &cobra.Command{
		Use: "install", Short: i18n.T("cli.install.short"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			p := unitPath()
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(unitText(exe)), 0o644); err != nil {
				return err
			}
			for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", unitName}} {
				if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
					return fmt.Errorf("systemctl --user %v: %w\n%s", args, err, out)
				}
			}
			fmt.Fprintln(a.Out, i18n.T("cli.install.done", p))
			return nil
		},
	}

	uninstall := &cobra.Command{
		Use: "uninstall", Short: i18n.T("cli.uninstall.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
			os.Remove(unitPath())
			exec.Command("systemctl", "--user", "daemon-reload").Run()
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
	if err := ag.Load(); err != nil {
		db.Close()
		return nil, nil, err
	}
	return ag, db, nil
}

func (a *App) runAgent(parent context.Context) error {
	ag, db, err := a.newAgent()
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- ag.Serve(ctx, a.Socket) }()
	go ag.Run(ctx)
	log.Printf("hatrack agent started (socket %s)", a.Socket)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return nil
	}
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
