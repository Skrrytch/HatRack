package agent

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

func clearSession(t *testing.T) {
	for _, k := range sessionVars {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestImportSessionEnv(t *testing.T) {
	clearSession(t)
	if set := importSessionEnv([]string{"XAUTHORITY=/x", "LANG=de_DE.UTF-8"}); set != nil || os.Getenv("XAUTHORITY") != "" {
		t.Fatalf("no display: nothing is set, got %v", set)
	}
	t.Setenv("XDG_CURRENT_DESKTOP", "mine")
	set := importSessionEnv([]string{"DISPLAY=:0", "XAUTHORITY=/home/u/.Xauthority", "XDG_CURRENT_DESKTOP=X-Cinnamon", "LANG=C", "PATH=/evil", "BROKEN"})
	if !slices.Equal(set, []string{"DISPLAY=:0", "XAUTHORITY=/home/u/.Xauthority"}) {
		t.Fatalf("set: %v", set)
	}
	if os.Getenv("XDG_CURRENT_DESKTOP") != "mine" || os.Getenv("LANG") == "C" || os.Getenv("PATH") == "/evil" {
		t.Fatal("only missing session variables are set")
	}
}

func TestWatchSessionEnv(t *testing.T) {
	clearSession(t)
	defer func(d time.Duration) { sessionPollFast = d }(sessionPollFast)
	sessionPollFast = 10 * time.Millisecond
	calls := 0
	read := func(context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"LANG=C"}, nil // the session is not there yet
		}
		return []string{"WAYLAND_DISPLAY=wayland-0"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	WatchSessionEnv(ctx, read)
	if os.Getenv("WAYLAND_DISPLAY") != "wayland-0" || calls != 2 || time.Since(start) < sessionPollFast {
		t.Fatalf("calls %d, display %q", calls, os.Getenv("WAYLAND_DISPLAY"))
	}
	calls = 0
	WatchSessionEnv(ctx, read) // has a display: returns at once
	if calls != 0 {
		t.Fatal("a display is there: no read")
	}
}
