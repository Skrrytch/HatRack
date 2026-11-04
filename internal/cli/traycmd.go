package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"hatrack/internal/i18n"
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
			err := tray.Run(ctx, a.Socket)
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
	c.AddCommand(run, show, hide, toggle)
	return c
}
