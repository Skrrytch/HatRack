// Package cli implements the hat command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
)

// Version is set at build time.
var Version = "dev"

// App holds what commands need; tests replace the fields.
type App struct {
	Out, Err io.Writer
	In       io.Reader
	Socket   string
	IsTTY    func() bool
}

// NewApp returns an app wired to the real terminal and socket.
func NewApp() *App {
	return &App{Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Socket: agent.SocketPath(), IsTTY: stdinIsTTY}
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// silentError is an error that was already shown to the user.
type silentError struct{ error }

// Root builds the command tree.
func (a *App) Root() *cobra.Command {
	lang := ""
	if c, err := agent.LoadConfig(); err == nil {
		lang = c.Language
	}
	i18n.Set(i18n.Resolve(lang))

	root := &cobra.Command{
		Use:           "hat",
		Short:         i18n.T("cli.short"),
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return a.status() },
	}
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(a.statusCmd(), a.onCmd(), a.doCmd(), a.offCmd(), a.logCmd(), a.configCmd(), a.agentCmd(), a.serverCmd())
	return root
}

// Execute runs the command line and returns the exit code.
func (a *App) Execute(args []string) int {
	root := a.Root()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var s silentError
		if !errors.As(err, &s) {
			fmt.Fprintln(a.Err, "hat: "+err.Error())
		}
		return 1
	}
	return 0
}

// call sends a request to the agent and turns errors into messages.
func (a *App) call(req agent.Request) (agent.Response, error) {
	resp, err := agent.Call(a.Socket, req)
	if errors.Is(err, agent.ErrNotRunning) {
		return resp, errors.New(i18n.T("err.agent_down"))
	}
	if err != nil {
		return resp, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
