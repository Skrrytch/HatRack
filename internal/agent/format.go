package agent

import (
	"fmt"
	"strings"
	"time"

	"hatrack/internal/core"
	"hatrack/internal/i18n"
)

func clock(t time.Time) string { return t.Local().Format("15:04") }

// StatusLine renders the one-line status.
func StatusLine(d StatusData, now time.Time) string {
	s := d.State
	switch s.Mode {
	case core.ModeWearing:
		parts := []string{d.HatName}
		if s.Activity != "" {
			parts = append(parts, s.Activity)
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", i18n.T("cli.since", clock(s.Since)), core.FormatDuration(now.Sub(s.Since))))
		return strings.Join(parts, " · ")
	case core.ModePaused:
		return i18n.T("cli.paused", d.HatName, clock(s.Since))
	}
	return i18n.T("cli.nohat")
}
