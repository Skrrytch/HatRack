package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

// period resolves today, yesterday, week or a date into [from, to).
func period(arg string, now time.Time) (from, to time.Time, err error) {
	loc := now.Location()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch strings.ToLower(arg) {
	case "", "today":
		return midnight, midnight.AddDate(0, 0, 1), nil
	case "yesterday":
		return midnight.AddDate(0, 0, -1), midnight, nil
	case "week":
		back := (int(midnight.Weekday()) + 6) % 7 // Monday is the first day
		start := midnight.AddDate(0, 0, -back)
		return start, start.AddDate(0, 0, 7), nil
	}
	d, perr := time.ParseInLocation("2006-01-02", arg, loc)
	if perr != nil {
		return from, to, errors.New(i18n.T("err.badperiod", arg))
	}
	return d, d.AddDate(0, 0, 1), nil
}

func (a *App) logCmd() *cobra.Command {
	return &cobra.Command{
		Use: "log [today|yesterday|week|YYYY-MM-DD]", Short: i18n.T("cli.log.short"), Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			now := time.Now()
			from, to, err := period(arg, now)
			if err != nil {
				return err
			}
			resp, err := a.call(agent.Request{Cmd: "log", From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)})
			if err != nil {
				return err
			}
			RenderLog(a.Out, decode[agent.LogData](resp), from, to)
			return nil
		},
	}
}

func label(names map[string]string, hat string) string {
	if n, ok := names[hat]; ok {
		return n
	}
	return hat
}

func hm(d time.Duration) string { return core.FormatDuration(d) }

// RenderLog prints the timeline per day and the totals.
func RenderLog(w io.Writer, d agent.LogData, from, to time.Time) {
	if len(d.Intervals) == 0 {
		fmt.Fprintln(w, i18n.T("cli.log.empty"))
		return
	}
	multi := to.Sub(from) > 36*time.Hour
	day := ""
	for _, iv := range d.Intervals {
		start := iv.Start.Local()
		if multi {
			if k := start.Format("Mon 2006-01-02"); k != day {
				day = k
				fmt.Fprintf(w, "\n%s\n", day)
			}
		}
		end := clock(iv.End)
		if iv.Open {
			end = i18n.T("cli.log.now")
		}
		what := label(d.Names, iv.Hat)
		if iv.Activity != "" {
			what += " · " + iv.Activity
		}
		fmt.Fprintf(w, "  %s–%-5s  %-34s %8s\n", clock(iv.Start), end, what, hm(iv.Duration()))
	}
	fmt.Fprintf(w, "\n%s\n", i18n.T("cli.log.totals"))
	var sum time.Duration
	for _, t := range d.Totals {
		sum += t.Duration
		fmt.Fprintf(w, "  %-36s %8s\n", label(d.Names, t.Hat), hm(t.Duration))
		if len(t.Activities) == 1 && t.Activities[0].Activity == "" {
			continue
		}
		for _, a := range t.Activities {
			name := a.Activity
			if name == "" {
				name = i18n.T("cli.log.noactivity")
			}
			fmt.Fprintf(w, "    %-34s %8s\n", name, hm(a.Duration))
		}
	}
	fmt.Fprintf(w, "  %-36s %8s\n", i18n.T("cli.log.sum"), hm(sum))
}
