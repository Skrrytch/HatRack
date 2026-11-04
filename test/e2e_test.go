// Package test runs the real binary: server, agent and CLI as processes.
// gsettings is replaced by a script, the session bus is unreachable, so the
// test never touches the desktop it runs on.
package test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeGsettings = `#!/bin/sh
# usage: gsettings get|set SCHEMA KEY [VALUE]
dir="$FAKE_GS_DIR"
file="$dir/$2.$3"
echo "$@" >> "$dir/calls.log"
case "$1" in
  get) if [ -f "$file" ]; then cat "$file"; else echo "''"; fi ;;
  set) printf "'%s'\n" "$4" > "$file" ;;
esac
`

type world struct {
	t       *testing.T
	bin     string
	env     []string
	dir     string
	gsDir   string
	url     string
	agentUp bool
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "hat")
	build := exec.Command("go", "build", "-o", bin, "hatrack/cmd/hat")
	build.Dir = ".."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	w := &world{t: t, bin: bin, dir: dir, gsDir: filepath.Join(dir, "gs")}
	os.MkdirAll(w.gsDir, 0o755)
	fakeBin := filepath.Join(dir, "fakebin")
	os.MkdirAll(fakeBin, 0o755)
	os.WriteFile(filepath.Join(fakeBin, "gsettings"), []byte(fakeGsettings), 0o755)
	os.WriteFile(filepath.Join(dir, "neutral.jpg"), []byte("jpg"), 0o644)
	os.WriteFile(filepath.Join(w.gsDir, "org.cinnamon.desktop.background.picture-uri"), []byte("'file://"+dir+"/neutral.jpg'\n"), 0o644)
	os.WriteFile(filepath.Join(w.gsDir, "org.cinnamon.desktop.interface.gtk-theme"), []byte("'Mint-Y'\n"), 0o644)
	run := filepath.Join(dir, "run")
	os.MkdirAll(run, 0o700)
	w.env = []string{
		"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
		"HOME=" + dir, "XDG_CONFIG_HOME=" + dir + "/config", "XDG_STATE_HOME=" + dir + "/state",
		"XDG_DATA_HOME=" + dir + "/data", "XDG_RUNTIME_DIR=" + run,
		"XDG_CURRENT_DESKTOP=X-Cinnamon", "FAKE_GS_DIR=" + w.gsDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + dir + "/no-bus",
		"LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "HAT_E2E=1",
	}
	return w
}

func (w *world) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, w.bin, args...)
	c.Env = w.env
	return c
}

// hat runs the CLI and returns stdout, stderr and the exit code.
func (w *world) hat(args ...string) (string, string, int) {
	w.t.Helper()
	var out, errb bytes.Buffer
	c := w.cmd(context.Background(), args...)
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func (w *world) must(args ...string) string {
	w.t.Helper()
	out, errs, code := w.hat(args...)
	if code != 0 {
		w.t.Fatalf("hat %v failed (%d): %s%s", args, code, out, errs)
	}
	return out
}

func (w *world) background(name string, args ...string) *exec.Cmd {
	c := w.cmd(context.Background(), args...)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() {
		c.Process.Kill()
		c.Wait()
		if w.t.Failed() {
			w.t.Logf("--- %s output ---\n%s", name, buf.String())
		}
	})
	return c
}

func (w *world) gs(name string) string {
	b, _ := os.ReadFile(filepath.Join(w.gsDir, name))
	return strings.TrimSpace(string(b))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

const config = `version: 1
defaults:
  resume_activity: never
hats:
  venom:
    name: VENOM
    color: "#7a1fa2"
    resume_activity: last
    activities: [development, review, meeting]
    modules:
      wallpaper: {image: %s}
      color_scheme: {theme: Mint-Y-Dark-Purple}
      exec: {enter: "echo enter $HAT_KEY >> %s", leave: "echo leave $HAT_KEY >> %s"}
  private:
    name: Private
    activities: []
`

func TestEndToEnd(t *testing.T) {
	w := newWorld(t)
	addr := freePort(t)
	w.url = "http://" + addr
	img := filepath.Join(w.dir, "venom.jpg")
	os.WriteFile(img, []byte("jpg"), 0o644)
	hooks := filepath.Join(w.dir, "hooks.log")

	w.background("server", "server", "--listen", addr)
	waitFor(t, "server", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	// no agent yet: a clear message
	if _, errs, code := w.hat("status"); code == 0 || !strings.Contains(errs, "agent is not running") {
		t.Fatalf("status without agent: %d %q", code, errs)
	}
	// device and login
	token := strings.TrimSpace(w.must("server", "device", "add", "testbox", "--config"))
	if !strings.HasPrefix(token, "hat_") {
		t.Fatalf("token: %q", token)
	}
	if _, errs, code := w.hat("agent", "login", w.url, "hat_wrong"); code == 0 || !strings.Contains(errs, "rejected") {
		t.Fatalf("login with a wrong token: %d %q", code, errs)
	}
	w.must("agent", "login", w.url, token, "--name", "testbox")
	if fi, err := os.Stat(filepath.Join(w.dir, "config", "hatrack", "agent.yaml")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent.yaml: %v %v", fi, err)
	}

	w.background("agent", "agent", "run")
	waitFor(t, "agent", func() bool { _, _, code := w.hat("status"); return code == 0 })

	// configuration: import, dry run, export
	cfgFile := filepath.Join(w.dir, "hats.yaml")
	os.WriteFile(cfgFile, []byte(fmt.Sprintf(config, img, hooks, hooks)), 0o644)
	if out := w.must("config", "import", cfgFile, "--dry-run"); !strings.Contains(out, "Dry run") || !strings.Contains(out, "hat added: venom") {
		t.Fatalf("dry run: %s", out)
	}
	if out := w.must("config", "import", cfgFile); !strings.Contains(out, "Imported") {
		t.Fatalf("import: %s", out)
	}
	exported := w.must("config", "export")
	if !strings.Contains(exported, "venom:") || !strings.Contains(exported, "development") {
		t.Fatalf("export: %s", exported)
	}
	reimport := filepath.Join(w.dir, "again.yaml")
	os.WriteFile(reimport, []byte(exported), 0o644)
	if out := w.must("config", "import", reimport); !strings.Contains(out, "Nothing to change") {
		t.Fatalf("export → import should be lossless: %s", out)
	}
	if _, errs, code := w.hat("config", "import", filepath.Join(w.dir, "nope.yaml")); code == 0 {
		t.Fatalf("missing file accepted: %s", errs)
	}

	// put on a hat with an activity (case and prefix)
	waitFor(t, "config in agent", func() bool { _, _, code := w.hat("on", "VEN", "do", "rev"); return code == 0 })
	if out := w.must(); !strings.HasPrefix(out, "VENOM · review · since ") {
		t.Fatalf("status: %q", out)
	}
	if got := w.gs("org.cinnamon.desktop.background.picture-uri"); got != "'file://"+img+"'" {
		t.Fatalf("wallpaper: %s", got)
	}
	if got := w.gs("org.cinnamon.desktop.interface.gtk-theme"); got != "'Mint-Y-Dark-Purple'" {
		t.Fatalf("theme: %s", got)
	}
	waitFor(t, "enter hook", func() bool { b, _ := os.ReadFile(hooks); return strings.Contains(string(b), "enter venom") })

	// unknown activity: error, nothing changes
	if _, errs, code := w.hat("do", "nonsense"); code == 0 || !strings.Contains(errs, "Available: development, review, meeting") {
		t.Fatalf("unknown activity: %d %q", code, errs)
	}
	w.must("do", "mee")
	if out := w.must("status"); !strings.Contains(out, "meeting") {
		t.Fatalf("do: %q", out)
	}
	w.must("do", "-")

	// pause keeps the look
	w.must("off")
	if out := w.must(); !strings.HasPrefix(out, "paused (VENOM) since ") {
		t.Fatalf("paused status: %q", out)
	}
	if got := w.gs("org.cinnamon.desktop.interface.gtk-theme"); got != "'Mint-Y-Dark-Purple'" {
		t.Fatalf("pause changed the look: %s", got)
	}
	// resume with the remembered activity: "last" gives meeting back? no — do - cleared it, last = meeting
	w.must("on")
	w.must("off", "--neutral")
	if got := w.gs("org.cinnamon.desktop.interface.gtk-theme"); got != "'Mint-Y'" {
		t.Fatalf("neutral look not restored: %s", got)
	}
	if got := w.gs("org.cinnamon.desktop.background.picture-uri"); got != "'file://"+w.dir+"/neutral.jpg'" {
		t.Fatalf("neutral wallpaper not restored: %s", got)
	}
	if b, _ := os.ReadFile(hooks); !strings.Contains(string(b), "leave venom") {
		t.Fatalf("leave hook missing: %s", b)
	}

	// resume_activity: last
	w.must("on", "private")
	out := w.must("on", "venom")
	if !strings.Contains(out, "VENOM · meeting") {
		t.Fatalf("resume last: %q", out)
	}

	// a forgotten switch, entered later, shows up in the log but keeps the look
	at := time.Now().Add(-2 * time.Hour).Format("2006-01-02 15:04")
	w.must("on", "private", "--at", at)
	if out := w.must(); !strings.HasPrefix(out, "VENOM") {
		t.Fatalf("backdated switch changed the current hat: %q", out)
	}
	waitFor(t, "log", func() bool {
		out, _, _ := w.hat("log", "today")
		return strings.Contains(out, "Private") && strings.Contains(out, "Totals")
	})
	logOut := w.must("log")
	for _, want := range []string{"VENOM", "Private", "Sum", "now"} {
		if !strings.Contains(logOut, want) {
			t.Errorf("log lacks %q:\n%s", want, logOut)
		}
	}

	// German output
	w.env = append(w.env, "LC_ALL=de_DE.UTF-8")
	if _, errs, _ := w.hat("do", "x", "--at", "99:99"); !strings.Contains(errs, "Keine gültige Zeit") {
		t.Fatalf("german error: %q", errs)
	}

	// the Now page
	resp := w.must("server", "device", "list")
	if !strings.Contains(resp, "testbox") || !strings.Contains(resp, "Konfiguration") && !strings.Contains(resp, "configuration") {
		t.Fatalf("device list: %s", resp)
	}
	pageOut, err := exec.Command("curl", "-s", "-H", "Authorization: Bearer "+token, w.url+"/healthz").Output()
	if err != nil || strings.TrimSpace(string(pageOut)) != "ok" {
		t.Fatalf("healthz: %q %v", pageOut, err)
	}
}
