package modules

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sys := t.TempDir()
	t.Setenv("XDG_DATA_DIRS", sys)
	apps := filepath.Join(sys, "applications")
	os.MkdirAll(apps, 0o755)
	os.WriteFile(filepath.Join(apps, "firefox.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox\n"), 0o644)
	os.WriteFile(filepath.Join(apps, "helper.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Helper\nNoDisplay=true\n"), 0o644)
	os.MkdirAll(filepath.Join(home, "Projekte", "venom"), 0o755)

	var started []string
	m := NewLaunch(func(name string, args ...string) error {
		started = append(started, name+" "+strings.Join(args, " "))
		return nil
	})
	cfg, _ := json.Marshal(map[string]any{
		"apps": []string{"firefox", "firefox.desktop", "nope"},
		"open": []string{"https://jira.example.org", "~/Projekte/venom", "~/missing"},
	})
	err := m.Enter(context.Background(), Switch{Hat: "venom"}, cfg)
	want := "gtk-launch firefox|gtk-launch firefox|xdg-open https://jira.example.org|xdg-open " + filepath.Join(home, "Projekte", "venom")
	if strings.Join(started, "|") != want {
		t.Fatalf("started:\n%s\nwant\n%s", strings.Join(started, "|"), want)
	}
	if err == nil || !strings.Contains(err.Error(), `no program "nope"`) || !strings.Contains(err.Error(), "missing does not exist") {
		t.Fatalf("errors: %v", err)
	}
	if err := m.Leave(context.Background(), Switch{}, cfg); err != nil || len(started) != 4 {
		t.Fatal("leave does nothing")
	}
	// the pick list shows visible programs only
	ch := installedApps()
	if len(ch) != 1 || ch[0].Value != "firefox" || ch[0].Label != "Firefox" {
		t.Fatalf("apps: %+v", ch)
	}
	if err := validateAgainst(m.ConfigSchema(), map[string]any{"apps": []any{"x"}, "open": []any{"y"}}, "launch"); err != nil {
		t.Fatal(err)
	}
}
