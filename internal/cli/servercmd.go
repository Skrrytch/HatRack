package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
	var listen, data string
	c := &cobra.Command{
		Use: "server", Short: i18n.T("cli.server.short"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := openStore(data)
			if err != nil {
				return err
			}
			defer st.Close()
			srv := &http.Server{
				Addr: listen, Handler: server.New(st, modules.Builtin(nil)).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
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
	c.AddCommand(device, backup)
	return c
}
