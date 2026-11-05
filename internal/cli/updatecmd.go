package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"hatrack/internal/i18n"
	"hatrack/internal/release"
)

// Seams for tests.
var (
	updateExe = func() (string, error) {
		p, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(p)
	}
	updateRestart = restartRunning
	updateClient  = &http.Client{Timeout: 2 * time.Minute}
)

func (a *App) updateCmd() *cobra.Command {
	var check, force bool
	c := &cobra.Command{
		Use: "update", Short: i18n.T("cli.update.short"), Args: cobra.NoArgs,
		Long: i18n.T("cli.update.long"),
		RunE: func(cmd *cobra.Command, _ []string) error { return a.runUpdate(cmd.Context(), check, force) },
	}
	c.Flags().BoolVar(&check, "check", false, i18n.T("cli.update.check"))
	c.Flags().BoolVar(&force, "force", false, i18n.T("cli.update.force"))
	return c
}

func (a *App) runUpdate(ctx context.Context, check, force bool) error {
	exe, err := updateExe()
	if err != nil {
		return err
	}
	arch, err := release.Arch()
	if err != nil {
		return err
	}
	release.UserAgent = "hatrack/" + Version
	rel, err := release.Latest(ctx, updateClient)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("err.update_latest"), err)
	}
	latest := rel.Version()

	if !release.Newer(latest, Version) {
		fmt.Fprintln(a.Out, i18n.T("cli.update.current", Version))
		return nil
	}
	// a package manager owns files below /usr; replacing them would break it
	if strings.HasPrefix(exe, "/usr/") {
		fmt.Fprintln(a.Out, i18n.T("cli.update.package", latest, release.DownloadURL(rel.Tag, release.DebName(arch))))
		return nil
	}
	if check {
		fmt.Fprintln(a.Out, i18n.T("cli.update.available", latest, Version))
		return nil
	}
	if !release.IsRelease(Version) && !force {
		return errors.New(i18n.T("err.update_source", Version))
	}

	fmt.Fprintln(a.Out, i18n.T("cli.update.fetching", latest))
	// the new file goes next to the old one, so the rename below is atomic
	tmp := filepath.Join(filepath.Dir(exe), ".hat-update")
	defer os.Remove(tmp)
	if err := release.Fetch(ctx, updateClient, rel, arch, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, i18n.T("cli.update.done", latest, exe))
	updateRestart(exe)
	return nil
}

// restartRunning restarts the installed services and the tray, so they run
// the new binary at once. (They also notice it on their own within a few
// seconds; this just saves the wait.)
func restartRunning(exe string) {
	var units []string
	for _, u := range []string{"hatrack-server.service", unitName} {
		if exec.Command("systemctl", "--user", "cat", u).Run() == nil {
			units = append(units, u)
		}
	}
	if len(units) > 0 {
		exec.Command("systemctl", append([]string{"--user", "restart"}, units...)...).Run()
	}
	exec.Command(exe, "tray", "restart").Run()
}
