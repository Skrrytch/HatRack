package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"hatrack/internal/agent"
)

// Tab completion asks the running agent for hats and activities; without an
// agent it offers nothing (and never fails).

func (a *App) snapshot() (agent.Snapshot, bool) {
	resp, err := agent.Call(a.Socket, agent.Request{Cmd: "hats"})
	if err != nil || !resp.OK {
		return agent.Snapshot{}, false
	}
	return decode[agent.Snapshot](resp), true
}

// completeOn offers hat keys, then "do", then the hat's activities.
func (a *App) completeOn(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	snap, ok := a.snapshot()
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	switch len(args) {
	case 0:
		out := []string{"do"}
		for _, h := range snap.Hats {
			out = append(out, h.Key)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	case 1:
		return []string{"do"}, cobra.ShellCompDirectiveNoFileComp
	case 2:
		if strings.EqualFold(args[1], "do") {
			for _, h := range snap.Hats {
				if strings.EqualFold(h.Key, args[0]) {
					return h.Activities, cobra.ShellCompDirectiveNoFileComp
				}
			}
		}
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeDo offers the activities of the hat that is on.
func (a *App) completeDo(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	snap, ok := a.snapshot()
	if !ok || len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	for _, h := range snap.Hats {
		if h.Key == snap.Status.State.Hat {
			return h.Activities, cobra.ShellCompDirectiveNoFileComp
		}
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeAway offers the answers and hat keys, then "do", then the hat's
// activities.
func (a *App) completeAway(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	snap, ok := a.snapshot()
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	switch len(args) {
	case 0:
		out := append([]string{}, awayAnswers...)
		for _, h := range snap.Hats {
			out = append(out, h.Key)
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	case 1:
		for _, h := range snap.Hats {
			if strings.EqualFold(h.Key, args[0]) {
				return []string{"do"}, cobra.ShellCompDirectiveNoFileComp
			}
		}
	case 2:
		if strings.EqualFold(args[1], "do") {
			for _, h := range snap.Hats {
				if strings.EqualFold(h.Key, args[0]) {
					return h.Activities, cobra.ShellCompDirectiveNoFileComp
				}
			}
		}
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
