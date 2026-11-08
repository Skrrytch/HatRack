package agent

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// A user service starts at boot, before the desktop session exists, so the
// agent may run without DISPLAY. Then neither the X11 sensors nor programs
// started by the launch module can reach the desktop. Most desktops hand
// their variables to the systemd user manager at login; the agent takes
// them from there once they appear.

// sessionVars are the variables of a desktop session the agent takes over.
var sessionVars = []string{
	"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
	"XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "XDG_SESSION_DESKTOP", "DESKTOP_SESSION",
}

// hasDisplay reports whether the process can reach a display.
func hasDisplay() bool { return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" }

// importSessionEnv sets the session variables from env ("KEY=value") that
// are not set yet, but only if env has a display. It returns what it set.
func importSessionEnv(env []string) []string {
	vals := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			vals[k] = v
		}
	}
	if vals["DISPLAY"] == "" && vals["WAYLAND_DISPLAY"] == "" {
		return nil
	}
	var set []string
	for _, k := range sessionVars {
		if v := vals[k]; v != "" && os.Getenv(k) == "" {
			os.Setenv(k, v)
			set = append(set, k+"="+v)
		}
	}
	return set
}

// UserManagerEnv reads the environment of the systemd user manager.
func UserManagerEnv(ctx context.Context) ([]string, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	v, err := conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").
		GetProperty("org.freedesktop.systemd1.Manager.Environment")
	if err != nil {
		return nil, err
	}
	var env []string
	err = v.Store(&env)
	return env, err
}

// Session wait: often at first, rarely after a while (a machine without a
// desktop never gets one). Variables, so tests can shorten them.
var (
	sessionPollFast = 5 * time.Second
	sessionPollSlow = time.Minute
	sessionFastFor  = 5 * time.Minute
)

// WatchSessionEnv waits until read returns a display and takes over the
// session variables. It returns at once if the agent already has a display.
func WatchSessionEnv(ctx context.Context, read func(context.Context) ([]string, error)) {
	start := time.Now()
	for !hasDisplay() {
		if env, err := read(ctx); err == nil {
			if set := importSessionEnv(env); len(set) > 0 {
				log.Printf("desktop session found: %s", strings.Join(set, " "))
				return
			}
		}
		d := sessionPollFast
		if time.Since(start) > sessionFastFor {
			d = sessionPollSlow
		}
		if !wait(ctx, d) {
			return
		}
	}
}
