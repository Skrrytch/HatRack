package tray

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
	"github.com/godbus/dbus/v5"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
	"hatrack/internal/icon"
	"hatrack/internal/panel"
)

// ControlSocket is where a running tray listens for show, hide and toggle.
func ControlSocket() string {
	return filepath.Join(filepath.Dir(agent.SocketPath()), "tray.sock")
}

// ErrNoWatcher means nothing on the session bus shows StatusNotifier icons.
var ErrNoWatcher = errors.New("no StatusNotifier watcher")

// HasWatcher reports whether a StatusNotifier host is running, which is what
// makes tray icons appear. Cinnamon uses xapp-sn-watcher, KDE has its own,
// GNOME needs the AppIndicator extension.
func HasWatcher() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

// Running tells whether a tray already answers on its control socket.
func Running() bool {
	_, err := control("ping")
	return err == nil
}

func control(cmd string) (string, error) {
	c, err := net.DialTimeout("unix", ControlSocket(), time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintln(c, cmd)
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line), nil
}

// Hide ends a running tray; the icon disappears with it.
func Hide() error {
	_, err := control("quit")
	return err
}

// Show starts a tray in the background if none runs.
func Show(exe string) error {
	if Running() {
		return nil
	}
	cmd := exec.Command(exe, "tray", "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Toggle hides the tray if it runs and shows it otherwise; it returns
// whether the tray is shown afterwards.
func Toggle(exe string) (bool, error) {
	if Running() {
		return false, Hide()
	}
	return true, Show(exe)
}

type app struct {
	sock string
	mu   sync.Mutex
	snap agent.Snapshot
	have bool
	menu []Item
	gen  chan struct{} // closed when the menu is rebuilt
	icon string
	tip  string

	panelOpen bool
	shownAway string // id of the last question shown in return mode
}

// Version and ProjectURL are shown in the about screen.
var (
	Version    = "dev"
	ProjectURL = "https://github.com/Skrrytch/HatRack"
)

// PanelRemote asks a running tray to show the panel, centred on the pointer's
// monitor or at the pointer (used by the keyboard shortcut). It reports whether a tray did.
func PanelRemote(atPointer bool) bool {
	cmd := "panel"
	if atPointer {
		cmd = "panel-pointer"
	}
	r, err := control(cmd)
	return err == nil && r == "ok"
}

// showPanel opens the panel unless one is open already. It runs in its own
// goroutine and performs the chosen action afterwards.
func (a *app) showPanel(where panel.Where, about bool) { a.openPanel(where, about, nil) }

// openPanel is showPanel with a question to answer (return mode) or nil.
func (a *app) openPanel(where panel.Where, about bool, away *agent.AwayQuestion) {
	a.mu.Lock()
	if a.panelOpen {
		a.mu.Unlock()
		return
	}
	a.panelOpen = true
	a.mu.Unlock()
	go func() {
		defer func() { a.mu.Lock(); a.panelOpen = false; a.mu.Unlock() }()
		act, err := panel.Run(context.Background(), panel.Options{
			Snapshot: func() agent.Snapshot { a.mu.Lock(); defer a.mu.Unlock(); return a.snap },
			Where:    where, About: about, Away: away, Version: Version, ProjectURL: ProjectURL,
		})
		if err != nil {
			log.Printf("panel: %v", err)
			if errors.Is(err, panel.ErrNoDisplay) {
				a.perform(&Action{Open: true})
			}
			return
		}
		if act != (Action{}) {
			a.perform(&act)
		}
	}()
}

// watcherWait is how long a starting tray waits for a StatusNotifier host.
// At login the autostart can run before the session's watcher is up.
const watcherWait = 30 * time.Second

// waitWatcher polls for a StatusNotifier host until one appears, the wait
// runs out (ErrNoWatcher) or ctx is done.
func waitWatcher(ctx context.Context, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for !HasWatcher() {
		if time.Now().After(deadline) {
			return ErrNoWatcher
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil
}

// Run shows the tray icon until it is told to quit. It blocks.
func Run(ctx context.Context, agentSocket string) error {
	if err := waitWatcher(ctx, watcherWait); err != nil {
		return err
	}
	if Running() {
		return fmt.Errorf("%s", i18n.T("err.tray_running"))
	}
	a := &app{sock: agentSocket}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ln, err := listenControl()
	if err != nil {
		return err
	}
	defer os.Remove(ControlSocket())
	systray.SetOnTapped(func() { a.showPanel(panel.AtPointer, false) })
	go a.serveControl(ln, cancel)
	go func() { <-ctx.Done(); systray.Quit() }()
	systray.Run(func() { a.ready(ctx) }, func() { ln.Close() })
	return nil
}

func listenControl() (net.Listener, error) {
	os.MkdirAll(filepath.Dir(ControlSocket()), 0o700)
	os.Remove(ControlSocket())
	ln, err := net.Listen("unix", ControlSocket())
	if err == nil {
		os.Chmod(ControlSocket(), 0o600)
	}
	return ln, err
}

func (a *app) serveControl(ln net.Listener, quit func()) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			line, _ := bufio.NewReader(c).ReadString('\n')
			switch strings.TrimSpace(line) {
			case "ping":
				fmt.Fprintln(c, "ok")
			case "quit":
				fmt.Fprintln(c, "ok")
				quit()
			case "panel":
				fmt.Fprintln(c, "ok")
				a.showPanel(panel.Centered, false)
			case "panel-pointer":
				fmt.Fprintln(c, "ok")
				a.showPanel(panel.AtPointer, false)
			case "about":
				fmt.Fprintln(c, "ok")
				a.showPanel(panel.Centered, true)
			}
		}()
	}
}

func (a *app) ready(ctx context.Context) {
	systray.SetTitle("HatRack")
	a.setIcon(Grey)
	systray.SetTooltip(i18n.T("tray.waiting"))
	go a.watchLoop(ctx)
	// the duration in the first line moves on, so refresh once a minute
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.mu.Lock()
				have := a.have
				a.mu.Unlock()
				if have {
					a.refresh(true)
				}
			}
		}
	}()
}

func (a *app) setIcon(color string) {
	if color == a.icon {
		return
	}
	a.icon = color
	systray.SetIcon(icon.Icon(color, 64))
}

// watchLoop follows the agent and reconnects when it restarts.
func (a *app) watchLoop(ctx context.Context) {
	for ctx.Err() == nil {
		err := agent.Watch(ctx, a.sock, func(s agent.Snapshot) {
			a.mu.Lock()
			a.snap, a.have = s, true
			id, open := returnToShow(a.shownAway, s, a.panelOpen)
			a.shownAway = id
			a.mu.Unlock()
			a.refresh(false)
			if open {
				a.openPanel(panel.Centered, false, s.Status.Away)
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.mu.Lock()
			a.have = false
			a.mu.Unlock()
			a.setIcon(Grey)
			systray.SetTooltip(i18n.T("err.agent_down"))
			a.rebuild([]Item{
				{Label: i18n.T("err.agent_down_short"), Disabled: true},
				{Separator: true},
				{Label: i18n.T("tray.quit"), Action: &Action{Quit: true}},
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// refresh updates icon, tooltip and menu from the latest snapshot. The menu
// is only rebuilt when it changed.
func (a *app) refresh(force bool) {
	a.mu.Lock()
	snap := a.snap
	a.mu.Unlock()
	a.setIcon(IconColor(snap))
	line := agent.StatusLine(snap.Status, time.Now())
	systray.SetTooltip(line)
	a.rebuild(BuildMenu(snap, time.Now()))
}

func sameMenu(a, b []Item) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (a *app) rebuild(items []Item) {
	a.mu.Lock()
	if a.menu != nil && sameMenu(a.menu, items) {
		a.mu.Unlock()
		return
	}
	a.menu = items
	if a.gen != nil {
		close(a.gen)
	}
	gen := make(chan struct{})
	a.gen = gen
	a.mu.Unlock()

	systray.ResetMenu()
	var add func(parent *systray.MenuItem, it Item)
	add = func(parent *systray.MenuItem, it Item) {
		if it.Separator {
			if parent == nil {
				systray.AddSeparator()
			} else {
				parent.AddSeparator()
			}
			return
		}
		var mi *systray.MenuItem
		switch {
		case parent == nil && it.Checked:
			mi = systray.AddMenuItemCheckbox(it.Label, "", true)
		case parent == nil:
			mi = systray.AddMenuItem(it.Label, "")
		case it.Checked:
			mi = parent.AddSubMenuItemCheckbox(it.Label, "", true)
		default:
			mi = parent.AddSubMenuItem(it.Label, "")
		}
		if it.Disabled {
			mi.Disable()
		}
		for _, c := range it.Children {
			add(mi, c)
		}
		if it.Action != nil {
			act := it.Action
			go func() {
				for {
					select {
					case <-gen:
						return
					case <-mi.ClickedCh:
						a.perform(act)
					}
				}
			}()
		}
	}
	for _, it := range items {
		add(nil, it)
	}
}

func (a *app) perform(act *Action) {
	switch {
	case act.Quit:
		systray.Quit()
	case act.About:
		a.showPanel(panel.Centered, true)
	case act.Link != "":
		if err := exec.Command("xdg-open", act.Link).Start(); err != nil {
			log.Printf("xdg-open: %v", err)
		}
	case act.Open:
		a.mu.Lock()
		url := a.snap.Server
		a.mu.Unlock()
		if err := exec.Command("xdg-open", url).Start(); err != nil {
			log.Printf("xdg-open: %v", err)
		}
	case act.Request != nil:
		resp, err := agent.Call(a.sock, *act.Request)
		if err != nil || !resp.OK {
			msg := resp.Error
			if err != nil {
				msg = err.Error()
			}
			log.Printf("%s: %s", act.Request.Cmd, msg)
			exec.Command("notify-send", "-a", "HatRack", "HatRack", msg).Start()
		}
	}
}
