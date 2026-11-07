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
		if s.Detail != "" {
			parts = append(parts, core.ShortDetail(s.Detail))
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", i18n.T("cli.since", clock(s.Since)), core.FormatDuration(now.Sub(s.Since))))
		return strings.Join(parts, " · ")
	case core.ModePaused:
		return i18n.T("cli.paused", d.HatName, clock(s.Since))
	}
	return i18n.T("cli.nohat")
}

// AwayLine renders the open question of an absence as one line, or "" if
// there is none.
func AwayLine(q *AwayQuestion) string {
	if q == nil {
		return ""
	}
	key := "away.status_pause"
	if q.Booked == AwayWork {
		key = "away.status_work"
	}
	return i18n.T(key, clock(q.From), clock(q.To))
}
