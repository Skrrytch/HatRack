// Package cli implements the hat command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 { // "hat bogus": name the unknown command
				msg := i18n.T("err.unknowncommand", args[0])
				if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
					msg += " " + i18n.T("err.didyoumean", strings.Join(s, ", "))
				}
				return silentUsage{errors.New(msg + "\n" + i18n.T("err.morehelp", "hat"))}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return a.status() },
	}
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.AddCommand(a.statusCmd(), a.onCmd(), a.doCmd(), a.detailCmd(), a.offCmd(), a.logCmd(), a.configCmd(), a.agentCmd(), a.serverCmd(), a.trayCmd(), a.panelCmd(), a.setupCmd())
	localize(root)
	friendlyUsage(root)
	return root
}

// useLine is cobra's UseLine with "[flags]" in the user's language.
func useLine(c *cobra.Command) string {
	return strings.Replace(c.UseLine(), "[flags]", i18n.T("cli.tpl.flagsword"), 1)
}

func init() { cobra.AddTemplateFunc("useLine", useLine) }

// usageTemplate is cobra's usage template with headings in the user's
// language.
func usageTemplate() string {
	return i18n.T("cli.tpl.usage") + `{{if .Runnable}}
  {{useLine .}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} ` + i18n.T("cli.tpl.command") + `{{end}}{{if gt (len .Aliases) 0}}

` + i18n.T("cli.tpl.aliases") + `
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

` + i18n.T("cli.tpl.examples") + `
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

` + i18n.T("cli.tpl.commands") + `{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

` + i18n.T("cli.tpl.flags") + `
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

` + i18n.T("cli.tpl.globalflags") + `
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

` + i18n.T("cli.tpl.more", "{{.CommandPath}}") + `{{end}}
`
}

// localize puts cobra's own texts (usage headings, the help flag and
// command, completion) into the user's language.
func localize(root *cobra.Command) {
	root.SetUsageTemplate(usageTemplate())
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	root.InitDefaultVersionFlag()
	if f := root.Flags().Lookup("version"); f != nil {
		f.Usage = i18n.T("cli.version_flag")
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		if f := c.Flags().Lookup("help"); f != nil {
			f.Usage = i18n.T("cli.help_flag", c.Name())
		}
		switch c.Name() {
		case "help":
			c.Short = i18n.T("cli.help_short")
		case "completion":
			c.Short = i18n.T("cli.completion_short")
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// usageError explains a wrong call: what was wrong, how to call the command,
// and where to read more. Cobra's own messages are English and bare.
func usageError(c *cobra.Command, problem string) error {
	var b strings.Builder
	b.WriteString(problem + "\n\n")
	b.WriteString(i18n.T("err.usage", useLine(c)) + "\n")
	if c.Short != "" {
		b.WriteString("  " + c.Short + "\n")
	}
	if c.Example != "" {
		b.WriteString("\n" + i18n.T("err.example") + "\n" + c.Example + "\n")
	}
	b.WriteString("\n" + i18n.T("err.morehelp", c.CommandPath()))
	return silentUsage{errors.New(b.String())}
}

// silentUsage marks errors whose text already is complete.
type silentUsage struct{ error }

// friendlyUsage replaces cobra's argument and flag errors in all commands.
func friendlyUsage(c *cobra.Command) {
	if orig := c.Args; orig != nil {
		c.Args = func(cmd *cobra.Command, args []string) error {
			if err := orig(cmd, args); err != nil {
				var s silentUsage
				if errors.As(err, &s) {
					return err // already explained
				}
				return usageError(cmd, i18n.T("err.args", len(args)))
			}
			return nil
		}
	}
	c.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError(cmd, i18n.T("err.flags", err.Error()))
	})
	for _, sub := range c.Commands() {
		friendlyUsage(sub)
	}
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
