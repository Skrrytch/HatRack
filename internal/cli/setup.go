package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"hatrack/internal/agent"
	"hatrack/internal/i18n"
	"hatrack/internal/tray"
)

// Seams for tests: the real ones touch systemd and the desktop.
var (
	setupInstallUnit = installUnit
	setupTrayInstall = installTrayAutostart
	setupTrayShow    = func() error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return tray.Show(exe)
	}
	setupHasWatcher = tray.HasWatcher
	setupWait       = 15 * time.Second
)

// starterConfig is offered when the rack has no hats yet; it needs no
// desktop settings, so it works everywhere and can be edited later.
const starterConfig = `version: 1
hats:
  work:
    name: Work
    color: "#4e79a7"
    activities: [focus, meeting]
  private:
    name: Private
    color: "#59a14f"
    activities: []
`

type setupOpts struct {
	server, token, name string
	local, noServices   bool
	noTray, yes         bool
}

// setupCmd walks through the first-time setup as a dialog.
func (a *App) setupCmd() *cobra.Command {
	var o setupOpts
	c := &cobra.Command{
		Use: "setup", Short: i18n.T("cli.setup.short"), Args: cobra.NoArgs,
		Long: i18n.T("cli.setup.long"),
		Example: "  hat setup\n" +
			"  hat setup --server https://hat.example.org --token hat_AbC123...",
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runSetup(cmd.Context(), o) },
	}
	c.Flags().StringVar(&o.server, "server", "", i18n.T("cli.setup.server"))
	c.Flags().StringVar(&o.token, "token", "", i18n.T("cli.setup.token"))
	c.Flags().StringVar(&o.name, "name", "", i18n.T("cli.login.name"))
	c.Flags().BoolVar(&o.local, "local", false, i18n.T("cli.setup.local"))
	c.Flags().BoolVar(&o.noServices, "no-services", false, i18n.T("cli.setup.noservices"))
	c.Flags().BoolVar(&o.noTray, "no-tray", false, i18n.T("cli.setup.notray"))
	c.Flags().BoolVarP(&o.yes, "yes", "y", false, i18n.T("cli.setup.yes"))
	return c
}

// dialog reads answers line by line, so a script can pipe them in.
type dialog struct {
	a   *App
	r   *bufio.Reader
	yes bool
}

func (d *dialog) line() string {
	s, _ := d.r.ReadString('\n')
	return strings.TrimSpace(s)
}

// ask shows a question with a default; Enter takes the default.
func (d *dialog) ask(q, def string) string {
	if d.yes {
		return def
	}
	if def != "" {
		fmt.Fprintf(d.a.Out, "%s [%s] ", q, def)
	} else {
		fmt.Fprintf(d.a.Out, "%s ", q)
	}
	if s := d.line(); s != "" {
		return s
	}
	return def
}

// confirm asks a yes/no question; the default is yes.
func (d *dialog) confirm(q string) bool {
	if d.yes {
		return true
	}
	yes, no := i18n.T("cli.setup.yes_key"), i18n.T("cli.setup.no_key")
	fmt.Fprintf(d.a.Out, "%s [%s/%s] ", q, strings.ToUpper(yes), no)
	s := strings.ToLower(d.line())
	return s == "" || strings.HasPrefix(s, yes)
}

// password asks twice on a terminal (no echo); from a pipe it reads a line.
// An empty answer means "skip".
func (d *dialog) password(q string) (string, error) {
	if f, ok := d.a.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		read := func(p string) (string, error) {
			fmt.Fprint(d.a.Out, p)
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(d.a.Out)
			return string(b), err
		}
		p1, err := read(q + " ")
		if err != nil || p1 == "" {
			return "", err
		}
		p2, err := read(i18n.T("cli.password.again"))
		if err != nil {
			return "", err
		}
		if p1 != p2 {
			return "", errors.New(i18n.T("cli.password.mismatch"))
		}
		return p1, nil
	}
	fmt.Fprint(d.a.Out, q+" ")
	return d.line(), nil
}

func (a *App) runSetup(ctx context.Context, o setupOpts) error {
	d := &dialog{a: a, r: bufio.NewReader(a.In), yes: o.yes}
	say := func(key string, args ...any) { fmt.Fprintln(a.Out, i18n.T(key, args...)) }
	step := func(key string, args ...any) { fmt.Fprintln(a.Out, "\n▸ "+i18n.T(key, args...)) }

	say("cli.setup.intro")

	cfg, _ := agent.LoadConfig()
	asked := o.server != "" || o.local
	connected := false
	if cfg.Server != "" && cfg.Token != "" && !asked {
		if _, err := agent.NewClient(cfg.Server, cfg.Token).State(ctx); err == nil {
			connected = true
			step("cli.setup.already", cfg.Server, cfg.Device)
			if !d.confirm(i18n.T("cli.setup.keep")) {
				connected = false
			}
		}
	}

	if !connected {
		remote := o.server != ""
		if !asked {
			step("cli.setup.where")
			fmt.Fprintln(a.Out, "  1) "+i18n.T("cli.setup.where_local"))
			fmt.Fprintln(a.Out, "  2) "+i18n.T("cli.setup.where_remote"))
			remote = d.ask(i18n.T("cli.setup.choose"), "1") == "2"
		}
		var err error
		if remote {
			cfg, err = a.setupRemote(ctx, d, o)
		} else {
			cfg, err = a.setupLocal(ctx, d, o, step)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, i18n.T("cli.login.done", agent.ConfigPath()))
	}

	// starter hats on an empty rack
	cl := agent.NewClient(cfg.Server, cfg.Token)
	if hc, _, err := cl.Config(ctx); err == nil && len(hc.Hats) == 0 {
		step("cli.setup.starter_title")
		if d.confirm(i18n.T("cli.setup.starter")) {
			if _, err := cl.PutConfig(ctx, []byte(starterConfig), false, false); err != nil {
				fmt.Fprintln(a.Err, i18n.T("cli.setup.starter_failed", err.Error()))
			} else {
				say("cli.setup.starter_done")
			}
		}
	}

	// the agent
	step("cli.setup.agent")
	if o.noServices {
		say("cli.setup.noservices_hint")
	} else {
		if _, err := setupInstallUnit(unitName, "HatRack agent", "agent", "run"); err != nil {
			return fmt.Errorf("%s: %w", i18n.T("err.setup_systemd"), err)
		}
		if err := waitFor(ctx, setupWait, func() bool {
			_, err := agent.Call(a.Socket, agent.Request{Cmd: "ping"})
			return err == nil
		}); err != nil {
			fmt.Fprintln(a.Err, i18n.T("cli.setup.agent_slow"))
		} else {
			say("cli.setup.agent_ok")
		}
	}

	// the tray
	if !o.noTray {
		step("cli.setup.tray")
		switch {
		case !setupHasWatcher():
			say("cli.setup.tray_nowatcher")
		case d.confirm(i18n.T("cli.setup.tray_ask")):
			if _, err := setupTrayInstall(); err != nil {
				return err
			}
			if err := setupTrayShow(); err != nil {
				fmt.Fprintln(a.Err, err)
			}
			say("cli.setup.tray_ok")
		}
	}

	step("cli.setup.done")
	say("cli.setup.next_web", cfg.Server)
	say("cli.setup.next_panel")
	return nil
}

// setupRemote logs in to an existing server (another computer, Docker).
func (a *App) setupRemote(ctx context.Context, d *dialog, o setupOpts) (agent.Config, error) {
	server, token := o.server, o.token
	if server == "" {
		server = d.ask(i18n.T("cli.setup.ask_url"), "")
	}
	if u, err := url.Parse(server); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return agent.Config{}, errors.New(i18n.T("err.setup_url", server))
	}
	if token == "" {
		fmt.Fprintln(a.Out, i18n.T("cli.setup.token_help"))
		token = d.ask(i18n.T("cli.setup.ask_token"), "")
	}
	return a.setupLogin(ctx, server, token, o.name)
}

// setupLogin checks the token and stores the connection.
func (a *App) setupLogin(ctx context.Context, server, token, name string) (agent.Config, error) {
	if _, err := agent.NewClient(server, token).State(ctx); err != nil {
		if errors.Is(err, agent.ErrUnauthorized) {
			return agent.Config{}, errors.New(i18n.T("err.unauthorized"))
		}
		return agent.Config{}, fmt.Errorf("%s: %w", i18n.T("err.unreachable", server), err)
	}
	cfg, _ := agent.LoadConfig()
	if name == "" {
		name, _ = os.Hostname()
	}
	cfg.Server, cfg.Token, cfg.Device = server, token, name
	return cfg, agent.SaveConfig(cfg)
}

// setupLocal runs the server on this computer as a service, adds this
// computer as a device and logs the agent in: no token to copy around.
func (a *App) setupLocal(ctx context.Context, d *dialog, o setupOpts, step func(string, ...any)) (agent.Config, error) {
	listen := defaultListen()
	base := "http://" + strings.Replace(listen, "0.0.0.0", "127.0.0.1", 1)

	step("cli.setup.server_step", base)
	if o.noServices {
		fmt.Fprintln(a.Out, i18n.T("cli.setup.noservices_server"))
	} else {
		if _, err := setupInstallUnit("hatrack-server.service", "HatRack Server", "server"); err != nil {
			return agent.Config{}, fmt.Errorf("%s: %w", i18n.T("err.setup_systemd"), err)
		}
	}
	if err := waitFor(ctx, setupWait, func() bool {
		_, err := agent.NewClient(base, "x").State(ctx)
		return err == nil || errors.Is(err, agent.ErrUnauthorized)
	}); err != nil {
		return agent.Config{}, errors.New(i18n.T("err.setup_noserver", base))
	}
	fmt.Fprintln(a.Out, i18n.T("cli.setup.server_ok"))

	// this computer as a device; the store is the server's data directory
	st, err := openStore(defaultData())
	if err != nil {
		return agent.Config{}, err
	}
	defer st.Close()
	name := o.name
	if name == "" {
		name, _ = os.Hostname()
	}
	var token string
	for i := 1; i <= 9; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s-%d", name, i)
		}
		if _, token, err = st.AddDevice(ctx, n); err == nil {
			name = n
			break
		}
	}
	if token == "" {
		return agent.Config{}, err
	}
	cfg, err := a.setupLogin(ctx, base, token, name)
	if err != nil {
		return cfg, err
	}

	// password for the web interface
	step("cli.setup.password_step")
	if d.yes {
		fmt.Fprintln(a.Out, i18n.T("cli.setup.password_skip"))
		return cfg, nil
	}
	pw, err := d.password(i18n.T("cli.setup.password_ask"))
	if err != nil {
		return cfg, err
	}
	if pw == "" {
		fmt.Fprintln(a.Out, i18n.T("cli.setup.password_skip"))
		return cfg, nil
	}
	if err := a.storePassword(ctx, pw); err != nil {
		return cfg, err
	}
	fmt.Fprintln(a.Out, i18n.T("cli.setup.password_ok"))
	return cfg, nil
}

func waitFor(ctx context.Context, max time.Duration, ok func() bool) error {
	deadline := time.Now().Add(max)
	for !ok() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("timeout")
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	return nil
}
