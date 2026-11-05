package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"hatrack/internal/i18n"
	"hatrack/internal/reexec"
	"hatrack/internal/tray"
)

func (a *App) trayCmd() *cobra.Command {
	c := &cobra.Command{Use: "tray", Short: i18n.T("cli.tray.short")}
	exe := func() (string, error) { return os.Executable() }

	run := &cobra.Command{
		Use: "run", Short: i18n.T("cli.tray.run"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			tray.Version = Version
			err := reexec.Run(ctx, "tray", func(ctx context.Context) error { return tray.Run(ctx, a.Socket) })
			if errors.Is(err, tray.ErrNoWatcher) {
				return errors.New(i18n.T("err.no_watcher"))
			}
			return err
		},
	}
	show := &cobra.Command{
		Use: "show", Short: i18n.T("cli.tray.show"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if !tray.HasWatcher() {
				return errors.New(i18n.T("err.no_watcher"))
			}
			p, err := exe()
			if err != nil {
				return err
			}
			if err := tray.Show(p); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.tray.shown"))
			return nil
		},
	}
	hide := &cobra.Command{
		Use: "hide", Short: i18n.T("cli.tray.hide"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := tray.Hide(); err != nil && tray.Running() {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.tray.hidden"))
			return nil
		},
	}
	toggle := &cobra.Command{
		Use: "toggle", Short: i18n.T("cli.tray.toggle"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := exe()
			if err != nil {
				return err
			}
			shown, err := tray.Toggle(p)
			if err != nil {
				return err
			}
			if shown {
				fmt.Fprintln(a.Out, i18n.T("cli.tray.shown"))
			} else {
				fmt.Fprintln(a.Out, i18n.T("cli.tray.hidden"))
			}
			return nil
		},
	}
	restart := &cobra.Command{
		Use: "restart", Short: i18n.T("cli.tray.restart"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if !tray.Running() {
				return nil // nothing to restart; the tray was not started
			}
			p, err := exe()
			if err != nil {
				return err
			}
			if err := tray.Hide(); err != nil {
				return err
			}
			for i := 0; i < 40 && tray.Running(); i++ {
				time.Sleep(50 * time.Millisecond)
			}
			if err := tray.Show(p); err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.tray.shown"))
			return nil
		},
	}
	install := &cobra.Command{
		Use: "install", Short: i18n.T("cli.tray.install"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			file, err := installTrayAutostart()
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.tray.installed", file))
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: i18n.T("cli.tray.uninstall"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			os.Remove(autostartPath())
			tray.Hide()
			fmt.Fprintln(a.Out, i18n.T("cli.tray.removed"))
			return nil
		},
	}
	c.AddCommand(run, show, hide, toggle, restart, install, uninstall)
	return c
}

// installTrayAutostart makes the tray start at login.
func installTrayAutostart() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return "", err
	}
	file := autostartPath()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	return file, os.WriteFile(file, []byte(autostartText(p)), 0o644)
}

func autostartPath() string { return filepath.Join(configHome(), "autostart", "hatrack-tray.desktop") }

func autostartText(exe string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=HatTray
Comment=HatRack tray icon
Exec=%q tray run
X-GNOME-Autostart-enabled=true
`, exe)
}
