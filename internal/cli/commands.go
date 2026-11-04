package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

func (a *App) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: i18n.T("cli.status.short"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return a.status() },
	}
}

func (a *App) status() error {
	resp, err := a.call(agent.Request{Cmd: "status"})
	if err != nil {
		return err
	}
	a.printStatus(resp, false)
	return nil
}

func decode[T any](r agent.Response) T {
	var v T
	json.Unmarshal(r.Data, &v)
	return v
}

func (a *App) printStatus(resp agent.Response, afterChange bool) {
	d := decode[agent.StatusData](resp)
	fmt.Fprintln(a.Out, agent.StatusLine(d, time.Now()))
	if !d.Connected {
		fmt.Fprintln(a.Err, i18n.T("cli.offline", d.Queued))
	}
}

// parseAt reads HH:MM (today), "YYYY-MM-DD HH:MM" or RFC 3339 and returns
// RFC 3339. An empty string stays empty.
func parseAt(s string, now time.Time) (string, error) {
	if s == "" {
		return "", nil
	}
	loc := now.Location()
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		return t.Format(time.RFC3339), nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t.Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format(time.RFC3339), nil
	}
	return "", errors.New(i18n.T("err.badat", s))
}

func (a *App) onCmd() *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "on [hat] [do activity]",
		Short: i18n.T("cli.on.short"),
		Long:  i18n.T("cli.on.long"),
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := agent.Request{Cmd: "on"}
			switch len(args) {
			case 0:
			case 1:
				req.Hat = args[0]
			case 3:
				if !strings.EqualFold(args[1], "do") {
					return errors.New(i18n.T("err.on_usage"))
				}
				req.Hat, req.Activity, req.HasActivity = args[0], args[2], true
			default:
				return errors.New(i18n.T("err.on_usage"))
			}
			var err error
			if req.At, err = parseAt(at, time.Now()); err != nil {
				return err
			}
			if req.Hat != "" && !req.HasActivity {
				// resume_activity: ask needs an answer from a person
				info, err := a.call(agent.Request{Cmd: "info", Hat: req.Hat})
				if err != nil {
					return err
				}
				i := decode[agent.InfoData](info)
				if i.Mode == core.ResumeAsk && i.Last != "" && a.IsTTY() {
					req.Answer = a.askResume(i.Last)
				}
			}
			resp, err := a.call(req)
			if err != nil {
				return err
			}
			a.printStatus(resp, true)
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "", i18n.T("cli.at"))
	return cmd
}

func (a *App) askResume(last string) string {
	fmt.Fprintf(a.Err, "%s [Y/n] ", i18n.T("cli.ask_resume", last))
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes", "j", "ja":
		return core.AnswerYes
	}
	return core.AnswerNo
}

func (a *App) doCmd() *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use: "do <activity>", Short: i18n.T("cli.do.short"), Long: i18n.T("cli.do.long"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := parseAt(at, time.Now())
			if err != nil {
				return err
			}
			resp, err := a.call(agent.Request{Cmd: "do", Activity: args[0], At: t})
			if err != nil {
				return err
			}
			a.printStatus(resp, true)
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "", i18n.T("cli.at"))
	return cmd
}

func (a *App) offCmd() *cobra.Command {
	var at string
	var neutral bool
	cmd := &cobra.Command{
		Use: "off", Short: i18n.T("cli.off.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := parseAt(at, time.Now())
			if err != nil {
				return err
			}
			resp, err := a.call(agent.Request{Cmd: "off", Neutral: neutral, At: t})
			if err != nil {
				return err
			}
			a.printStatus(resp, true)
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "", i18n.T("cli.at"))
	cmd.Flags().BoolVar(&neutral, "neutral", false, i18n.T("cli.off.neutral"))
	return cmd
}

func (a *App) configCmd() *cobra.Command {
	cfg := &cobra.Command{Use: "config", Short: i18n.T("cli.config.short")}
	var out string
	export := &cobra.Command{
		Use: "export", Short: i18n.T("cli.export.short"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			resp, err := a.call(agent.Request{Cmd: "config_export"})
			if err != nil {
				return err
			}
			text := decode[string](resp)
			if out == "" || out == "-" {
				fmt.Fprint(a.Out, text)
				return nil
			}
			if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
				return err
			}
			fmt.Fprintln(a.Err, i18n.T("cli.exported", out))
			return nil
		},
	}
	export.Flags().StringVarP(&out, "output", "o", "", i18n.T("cli.export.output"))

	var dry, replace bool
	imp := &cobra.Command{
		Use: "import <file>", Short: i18n.T("cli.import.short"), Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var data []byte
			var err error
			if args[0] == "-" {
				data, err = readAll(a)
			} else {
				data, err = os.ReadFile(args[0])
			}
			if err != nil {
				return err
			}
			resp, err := a.call(agent.Request{Cmd: "config_import", Data: string(data), DryRun: dry, Replace: replace})
			if err != nil {
				return err
			}
			res := decode[agent.ImportData](resp)
			for _, w := range res.Warnings {
				fmt.Fprintln(a.Err, i18n.T("cli.warning", w))
			}
			if len(res.Changes) == 0 {
				fmt.Fprintln(a.Out, i18n.T("cli.import.nochange"))
				return nil
			}
			head := "cli.import.done"
			if dry {
				head = "cli.import.dry"
			}
			fmt.Fprintln(a.Out, i18n.T(head, len(res.Changes)))
			for _, c := range res.Changes {
				fmt.Fprintln(a.Out, "  "+c)
			}
			return nil
		},
	}
	imp.Flags().BoolVar(&dry, "dry-run", false, i18n.T("cli.import.dryflag"))
	imp.Flags().BoolVar(&replace, "replace", false, i18n.T("cli.import.replace"))
	cfg.AddCommand(export, imp)
	return cfg
}

func readAll(a *App) ([]byte, error) {
	var sb strings.Builder
	sc := bufio.NewScanner(a.In)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		sb.WriteString(sc.Text() + "\n")
	}
	return []byte(sb.String()), sc.Err()
}
