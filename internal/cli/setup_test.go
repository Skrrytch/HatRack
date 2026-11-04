package cli

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hatrack/internal/agent"
	"hatrack/internal/modules"
	"hatrack/internal/server"
	"hatrack/internal/store"
)

type setupRig struct {
	srv   *httptest.Server
	st    *store.SQLite
	units []string
	tray  bool
}

func newSetupRig(t *testing.T) *setupRig {
	for _, v := range []string{"LANGUAGE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(v, "en_US.UTF-8")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HATRACK_DATA", filepath.Join(dir, "server"))
	st, err := openStore(filepath.Join(dir, "server"))
	if err != nil {
		t.Fatal(err)
	}
	r := &setupRig{st: st}
	r.srv = httptest.NewServer(server.New(st, modules.Builtin(nil)).Handler())
	t.Setenv("HATRACK_LISTEN", strings.TrimPrefix(r.srv.URL, "http://"))
	t.Cleanup(func() { r.srv.Close(); st.Close() })

	old := []any{setupInstallUnit, setupTrayInstall, setupTrayShow, setupHasWatcher, setupWait}
	setupInstallUnit = func(name, _ string, _ ...string) (string, error) { r.units = append(r.units, name); return "", nil }
	setupTrayInstall = func() (string, error) { r.tray = true; return "", nil }
	setupTrayShow = func() error { return nil }
	setupHasWatcher = func() bool { return true }
	setupWait = 300 * time.Millisecond
	t.Cleanup(func() {
		setupInstallUnit = old[0].(func(string, string, ...string) (string, error))
		setupTrayInstall = old[1].(func() (string, error))
		setupTrayShow = old[2].(func() error)
		setupHasWatcher = old[3].(func() bool)
		setupWait = old[4].(time.Duration)
	})
	return r
}

func runSetupWith(t *testing.T, input string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	a := &App{Out: &out, Err: &errb, In: strings.NewReader(input), Socket: filepath.Join(t.TempDir(), "none.sock"), IsTTY: func() bool { return false }}
	root := a.Root()
	root.SetArgs(append([]string{"setup"}, args...))
	err := root.Execute()
	return out.String() + errb.String(), err
}

func TestSetupLocal(t *testing.T) {
	r := newSetupRig(t)
	// 1 = this computer, password, starter hats yes, tray yes
	out, err := runSetupWith(t, "1\nsecret\n\n\n", "--name", "laptop")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Join(r.units, ",") != "hatrack-server.service,hatrack-agent.service" || !r.tray {
		t.Fatalf("units %v tray %v", r.units, r.tray)
	}
	cfg, _ := agent.LoadConfig()
	if cfg.Server != r.srv.URL || cfg.Device != "laptop" || !strings.HasPrefix(cfg.Token, "hat_") {
		t.Fatalf("agent config: %+v", cfg)
	}
	if h, _ := r.st.PasswordHash(context.Background()); h == "" {
		t.Fatal("password not stored")
	}
	hc, _, err := agent.NewClient(cfg.Server, cfg.Token).Config(context.Background())
	if err != nil || len(hc.Hats) != 2 {
		t.Fatalf("starter hats: %v %+v", err, hc.Hats)
	}

	// running it again keeps the connection and asks nothing about the server
	r.units = nil
	out, err = runSetupWith(t, "\n\n", "--no-tray")
	if err != nil || !strings.Contains(out, "connected to") || strings.Join(r.units, ",") != "hatrack-agent.service" {
		t.Fatalf("second run: %v %v\n%s", err, r.units, out)
	}
	if strings.Contains(out, "no hats yet") {
		t.Fatal("starter hats offered again")
	}
}

func TestSetupRemote(t *testing.T) {
	r := newSetupRig(t)
	_, token, _ := r.st.AddDevice(context.Background(), "desk")
	out, err := runSetupWith(t, "n\n", "--server", r.srv.URL, "--token", token, "--no-tray")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Join(r.units, ",") != "hatrack-agent.service" {
		t.Fatalf("only the agent is installed: %v", r.units)
	}
	if cfg, _ := agent.LoadConfig(); cfg.Token != token {
		t.Fatalf("token not saved: %+v", cfg)
	}
	if _, err := runSetupWith(t, "", "--server", r.srv.URL, "--token", "hat_wrong", "--no-tray"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := runSetupWith(t, "n\n2\nnot-a-url\n"); /* n: drop the connection */ err == nil || !strings.Contains(err.Error(), "not a server address") {
		t.Fatalf("bad url: %v", err)
	}
}
