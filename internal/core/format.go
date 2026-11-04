package core

import (
	"fmt"
	"time"
)

// FormatDuration renders 83 minutes as "1h 23m" and 5 minutes as "5m".
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %02dm", m/60, m%60)
}
