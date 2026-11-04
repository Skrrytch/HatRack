package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
	"hatrack/internal/panel"
	"hatrack/internal/tray"
)

// panelCmd opens the quick switcher. A running tray shows it instantly; without
// one the command draws the panel itself. Bind it to a keyboard shortcut.
func (a *App) panelCmd() *cobra.Command {
	var pointer bool
	c := &cobra.Command{
		Use: "panel", Short: i18n.T("cli.panel.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tray.PanelRemote(pointer) {
				return nil
			}
			resp, err := agent.Call(a.Socket, agent.Request{Cmd: "hats"})
			if err != nil {
				return errors.New(i18n.T("err.agent_down"))
			}
			var snap agent.Snapshot
			if err := json.Unmarshal(resp.Data, &snap); err != nil {
				return err
			}
			where := panel.Centered
			if pointer {
				where = panel.AtPointer
			}
			act, err := panel.Run(context.Background(), panel.Options{
				Snapshot: func() agent.Snapshot { return snap }, Where: where,
				Version: Version, ProjectURL: tray.ProjectURL,
			})
			if errors.Is(err, panel.ErrNoDisplay) && snap.Server != "" {
				return exec.Command("xdg-open", snap.Server).Start()
			}
			if err != nil {
				return err
			}
			switch {
			case act.Request != nil:
				r, err := agent.Call(a.Socket, *act.Request)
				if err != nil {
					return err
				}
				if !r.OK {
					return fmt.Errorf("%s", r.Error)
				}
			case act.Open:
				return exec.Command("xdg-open", snap.Server).Start()
			case act.Link != "":
				return exec.Command("xdg-open", act.Link).Start()
			}
			return nil
		},
	}
	c.Flags().BoolVar(&pointer, "pointer", false, i18n.T("cli.panel.pointer"))
	return c
}
