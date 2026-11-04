package modules

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hatrack/internal/core"
)

// fakeGS is a gsettings stand-in that remembers values.
type fakeGS struct {
	vals  map[string]string
	calls []string
	fail  map[string]bool
}

func newFakeGS() *fakeGS {
	return &fakeGS{
		vals: map[string]string{
			"org.cinnamon.desktop.background picture-uri":     "'file:///home/u/neutral.jpg'",
			"org.cinnamon.desktop.background picture-options": "'zoom'",
			"org.cinnamon.desktop.interface gtk-theme":        "'Mint-Y'",
			"org.cinnamon.theme name":                         "'Mint-Y'",
			"org.gnome.desktop.interface color-scheme":        "'default'",
		},
		fail: map[string]bool{},
	}
}

func (f *fakeGS) run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "gsettings" || len(args) < 3 {
		return "", errors.New("bad call")
	}
	k := args[1] + " " + args[2]
	if f.fail[k] {
		return "", errors.New("no such key")
	}
	switch args[0] {
	case "get":
		v, ok := f.vals[k]
		if !ok {
			return "", errors.New("no such key")
		}
		return v, nil
	case "set":
		f.vals[k] = "'" + args[3] + "'"
		return "", nil
	}
	return "", errors.New("bad verb")
}

func TestWallpaperCinnamon(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "X-Cinnamon")
	gs := newFakeGS()
	m := NewWallpaper(gs.run).(*backendModule)
	img := filepath.Join(t.TempDir(), "venom wall.jpg")
	os.WriteFile(img, []byte("x"), 0o644)

	snap, err := m.Snapshot(context.Background())
	if err != nil || !strings.Contains(string(snap), `"/home/u/neutral.jpg"`) {
		t.Fatalf("snapshot: %s %v", snap, err)
	}
	cfg, _ := json.Marshal(map[string]string{"image": img})
	if err := m.Enter(context.Background(), Switch{}, cfg); err != nil {
		t.Fatal(err)
	}
	got := gs.vals["org.cinnamon.desktop.background picture-uri"]
	if !strings.Contains(got, "file://") || !strings.Contains(got, "venom%20wall.jpg") {
		t.Fatalf("picture-uri: %s", got)
	}
	// a built-in wallpaper is written to the data folder and used from there
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, _ = json.Marshal(map[string]string{"image": "hatrack:wp02-pink"})
	if err := m.Enter(context.Background(), Switch{}, cfg); err != nil {
		t.Fatal(err)
	}
	if got := gs.vals["org.cinnamon.desktop.background picture-uri"]; !strings.Contains(got, "/hatrack/wallpapers/wp02-pink.jpg") {
		t.Fatalf("built-in picture-uri: %s", got)
	}
}

func TestWallpaperMissingFileAndOtherDesktop(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "X-Cinnamon")
	gs := newFakeGS()
	m := NewWallpaper(gs.run).(*backendModule)
	cfg, _ := json.Marshal(map[string]string{"image": "/nonexistent/x.jpg"})
	if err := m.Enter(context.Background(), Switch{}, cfg); err == nil {
		t.Fatal("missing image file should fail")
	}
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	m2 := NewWallpaper(gs.run).(*backendModule)
	if err := m2.Enter(context.Background(), Switch{}, cfg); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("other desktop: %v", err)
	}
}

func TestColorSchemeCinnamon(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "X-Cinnamon")
	gs := newFakeGS()
	m := NewColorScheme(gs.run).(*backendModule)
	snap, err := m.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"theme": "Mint-Y-Dark-Purple", "prefer": "dark"})
	if err := m.Enter(context.Background(), Switch{}, cfg); err != nil {
		t.Fatal(err)
	}
	if gs.vals["org.cinnamon.desktop.interface gtk-theme"] != "'Mint-Y-Dark-Purple'" ||
		gs.vals["org.gnome.desktop.interface color-scheme"] != "'prefer-dark'" {
		t.Fatalf("values: %v", gs.vals)
	}
	if err := m.Restore(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if gs.vals["org.cinnamon.desktop.interface gtk-theme"] != "'Mint-Y'" || gs.vals["org.gnome.desktop.interface color-scheme"] != "'default'" {
		t.Fatalf("after restore: %v", gs.vals)
	}
	// prefer is best effort
	gs.fail["org.gnome.desktop.interface color-scheme"] = true
	if err := m.Enter(context.Background(), Switch{}, cfg); err != nil {
		t.Fatalf("prefer failure must not fail the module: %v", err)
	}
}

func TestExecModule(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	m := NewExec()
	cfg, _ := json.Marshal(map[string]string{"enter": `echo "$HAT_HOOK $HAT_KEY $HAT_ACTIVITY" > ` + out, "leave": "exit 3"})
	if err := m.Enter(context.Background(), Switch{Hat: "venom", Activity: "review"}, cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if strings.TrimSpace(string(b)) != "enter venom review" {
		t.Fatalf("output: %q", b)
	}
	if err := m.Leave(context.Background(), Switch{}, cfg); err == nil {
		t.Fatal("failing script should report an error")
	}
	if err := m.Leave(context.Background(), Switch{}, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("no script: %v", err)
	}
}

func TestHostIsolatesFailures(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "X-Cinnamon")
	gs := newFakeGS()
	reg := &Registry{}
	reg.Register(NewWallpaper(gs.run))
	reg.Register(NewExec())
	h := NewHost(reg)
	marker := filepath.Join(t.TempDir(), "ran")
	cfgs := map[string]json.RawMessage{
		"wallpaper": json.RawMessage(`{"image":"/nonexistent.jpg"}`), // fails
		"exec":      json.RawMessage(`{"enter":"touch ` + marker + `"}`),
	}
	fails := h.Run(context.Background(), "enter", Switch{Hat: "x"}, cfgs)
	if len(fails) != 1 || fails[0].Module != "wallpaper" {
		t.Fatalf("failures: %v", fails)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("exec did not run after the wallpaper failed")
	}
	caps := h.Capabilities(context.Background())
	if !caps["wallpaper"].Available || caps["wallpaper"].Backend != "cinnamon" || !caps["exec"].Available {
		t.Fatalf("capabilities: %+v", caps)
	}
}

func TestExternalModule(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "demo")
	os.MkdirAll(mod, 0o755)
	os.WriteFile(filepath.Join(mod, "module.yaml"), []byte("id: demo\nhooks: [enter, leave]\nschema:\n  type: object\n  properties:\n    msg: {type: string}\n"), 0o644)
	script := `#!/bin/sh
in=$(cat)
case "$1" in
  enter) echo "$in" > "$(dirname "$0")/last.json"; echo '{"ok": true}';;
  leave) echo '{"ok": false, "error": "boom"}';;
esac
`
	os.WriteFile(filepath.Join(mod, "run"), []byte(script), 0o755)
	os.MkdirAll(filepath.Join(dir, "broken"), 0o755)

	mods, errs := LoadExternal(dir)
	if len(mods) != 1 || len(errs) != 1 {
		t.Fatalf("mods=%d errs=%v", len(mods), errs)
	}
	m := mods[0]
	if err := m.Enter(context.Background(), Switch{Hat: "venom"}, json.RawMessage(`{"msg":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(mod, "last.json"))
	if !strings.Contains(string(b), `"hat":"venom"`) || !strings.Contains(string(b), `"msg":"hi"`) {
		t.Fatalf("stdin: %s", b)
	}
	if err := m.Leave(context.Background(), Switch{}, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("leave: %v", err)
	}
	reg := &Registry{}
	reg.Register(m)
	cfg := core.NewConfig()
	cfg.Modules = map[string]core.ModuleCfg{"demo": {"msg": 5}}
	if err := Validate(reg, cfg); err == nil {
		t.Fatal("schema violation not detected")
	}
}

func TestValidate(t *testing.T) {
	reg := Builtin(newFakeGS().run)
	good, err := core.ParseConfig([]byte(`version: 1
modules:
  notify: {enabled: true}
hats:
  a:
    modules:
      wallpaper: {image: /x.jpg}
      color_scheme: {theme: T, prefer: dark}
      exec: {enter: "true"}
      unknown_external: {anything: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(reg, good); err != nil {
		t.Fatal(err)
	}
	for name, y := range map[string]string{
		"missing image": "wallpaper: {}",
		"bad prefer":    "color_scheme: {prefer: purple}",
		"extra key":     "exec: {enter: x, nope: 1}",
	} {
		cfg, err := core.ParseConfig([]byte("version: 1\nhats:\n  a:\n    modules:\n      " + y + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := Validate(reg, cfg); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	// a hat may set only what differs from the defaults
	layered, err := core.ParseConfig([]byte(`version: 1
modules:
  wallpaper: {image: /x.jpg}
hats:
  a:
    modules:
      wallpaper: {options: zoom}
  b: {}
  old:
    archived: true
    modules:
      wallpaper: {image: ""}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(reg, layered); err != nil {
		t.Fatalf("layered settings: %v", err)
	}
	// defaults without the required image are fine until a hat lacks it
	partial, _ := core.ParseConfig([]byte("version: 1\nmodules:\n  wallpaper: {options: zoom}\nhats:\n  a:\n    modules:\n      wallpaper: {image: /y.jpg}\n"))
	if err := Validate(reg, partial); err != nil {
		t.Fatalf("partial defaults: %v", err)
	}
	partial.Hats = append(partial.Hats, core.Hat{Key: "b"})
	if err := Validate(reg, partial); err == nil || !strings.Contains(err.Error(), "hats.b") {
		t.Fatalf("hat without image: %v", err)
	}
}

func TestWallpaperRestoreEdgeCases(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "X-Cinnamon")
	gs := newFakeGS()
	m := NewWallpaper(gs.run).(*backendModule)
	// an empty snapshot is a no-op, a URI snapshot is passed on as it is
	if err := m.Restore(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(context.Background(), json.RawMessage(`{"image":"https://example.org/x.jpg"}`)); err != nil {
		t.Fatal(err)
	}
	if got := gs.vals["org.cinnamon.desktop.background picture-uri"]; got != "'https://example.org/x.jpg'" {
		t.Fatalf("uri: %s", got)
	}
}

func TestExecDoesNotWaitForBackgroundPrograms(t *testing.T) {
	m := NewExec()
	cfg, _ := json.Marshal(map[string]string{"enter": "sleep 3 &"})
	start := time.Now()
	if err := m.Enter(context.Background(), Switch{Hat: "a"}, cfg); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a program started in the background held the hook for %v", d)
	}
	cfg, _ = json.Marshal(map[string]string{"enter": "echo kaputt >&2; exit 3"})
	if err := m.Enter(context.Background(), Switch{}, cfg); err == nil || !strings.Contains(err.Error(), "kaputt") {
		t.Fatalf("errors keep the output: %v", err)
	}
}
