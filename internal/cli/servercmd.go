package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"hatrack/internal/i18n"
	"hatrack/internal/modules"
	"hatrack/internal/server"
	"hatrack/internal/store"
)

func defaultData() string {
	if v := os.Getenv("HATRACK_DATA"); v != "" {
		return v
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "hatrack-server")
}

func defaultListen() string {
	if v := os.Getenv("HATRACK_LISTEN"); v != "" {
		return v
	}
	return "127.0.0.1:8421"
}

func openStore(dir string) (*store.SQLite, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dir, "hatrack.db"))
}

func (a *App) serverCmd() *cobra.Command {
	var listen, data, tz string
	c := &cobra.Command{
		Use: "server", Short: i18n.T("cli.server.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(data)
			if err != nil {
				return err
			}
			defer st.Close()
			web := server.New(st, modules.Builtin(nil))
			if tz != "" {
				loc, err := time.LoadLocation(tz)
				if err != nil {
					return err
				}
				web.Loc = loc
			}
			srv := &http.Server{Addr: listen, Handler: web.Handler(), ReadHeaderTimeout: 10 * time.Second}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(sctx)
			}()
			log.Printf("hatrack server listening on http://%s (data: %s)", listen, data)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	c.PersistentFlags().StringVar(&data, "data", defaultData(), i18n.T("cli.server.data"))
	c.Flags().StringVar(&tz, "timezone", os.Getenv("HATRACK_TZ"), i18n.T("cli.server.tz"))
	c.Flags().StringVar(&listen, "listen", defaultListen(), i18n.T("cli.server.listen"))

	device := &cobra.Command{Use: "device", Short: i18n.T("cli.device.short")}
	add := &cobra.Command{
		Use: "add <name>", Short: i18n.T("cli.device.add"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(data)
			if err != nil {
				return err
			}
			defer st.Close()
			_, token, err := st.AddDevice(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Err, i18n.T("cli.device.token", args[0]))
			fmt.Fprintln(a.Out, token)
			return nil
		},
	}
	list := &cobra.Command{
		Use: "list", Short: i18n.T("cli.device.list"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(data)
			if err != nil {
				return err
			}
			defer st.Close()
			devs, err := st.Devices(cmd.Context())
			if err != nil {
				return err
			}
			for _, d := range devs {
				seen := "-"
				if !d.LastSeen.IsZero() {
					seen = d.LastSeen.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(a.Out, "%-24s %s\n", d.Name, seen)
			}
			return nil
		},
	}
	device.AddCommand(add, list)

	backup := &cobra.Command{
		Use: "backup <file>", Short: i18n.T("cli.backup.short"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(data)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.Backup(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(a.Err, i18n.T("cli.backup.done", args[0]))
			return nil
		},
	}
	user := &cobra.Command{Use: "user", Short: i18n.T("cli.user.short")}
	user.AddCommand(&cobra.Command{
		Use: "set-password", Short: i18n.T("cli.user.setpassword"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			pw, err := a.readPassword()
			if err != nil {
				return err
			}
			hash, err := server.HashPassword(pw)
			if err != nil {
				return err
			}
			st, err := openStore(data)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.SetPasswordHash(cmd.Context(), hash); err != nil {
				return err
			}
			fmt.Fprintln(a.Err, i18n.T("cli.password.done"))
			return nil
		},
	})
	install := &cobra.Command{
		Use: "install", Short: i18n.T("cli.server.install"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := installUnit("hatrack-server.service", "HatRack Server", "server")
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, i18n.T("cli.install.done", p))
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: i18n.T("cli.server.uninstall"), Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			removeUnit("hatrack-server.service")
			fmt.Fprintln(a.Out, i18n.T("cli.server.removed"))
			return nil
		},
	}
	c.AddCommand(device, backup, user, install, uninstall)
	return c
}

// readPassword asks twice on a terminal (without echo); from a pipe it reads
// one line, so it can be scripted.
func (a *App) readPassword() (string, error) {
	if f, ok := a.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		read := func(prompt string) (string, error) {
			fmt.Fprint(a.Err, prompt)
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(a.Err)
			return string(b), err
		}
		p1, err := read(i18n.T("cli.password.prompt"))
		if err != nil {
			return "", err
		}
		p2, err := read(i18n.T("cli.password.again"))
		if err != nil {
			return "", err
		}
		if p1 != p2 {
			return "", errors.New(i18n.T("cli.password.mismatch"))
		}
		if p1 == "" {
			return "", errors.New(i18n.T("cli.password.empty"))
		}
		return p1, nil
	}
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New(i18n.T("cli.password.empty"))
	}
	return line, nil
}
