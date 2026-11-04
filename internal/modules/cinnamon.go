package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"hatrack/internal/core"
)

// gsettings is called as a command, which avoids a GLib dependency and works
// the same on every distribution.

const (
	cinnamonBackground = "org.cinnamon.desktop.background"
	cinnamonInterface  = "org.cinnamon.desktop.interface"
	cinnamonShell      = "org.cinnamon.theme"
	gnomeInterface     = "org.gnome.desktop.interface"
)

func gsGet(ctx context.Context, run Runner, schema, key string) (string, error) {
	out, err := run(ctx, "gsettings", "get", schema, key)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if len(out) >= 2 && out[0] == '\'' && out[len(out)-1] == '\'' {
		out = out[1 : len(out)-1]
	}
	return out, nil
}

func gsSet(ctx context.Context, run Runner, schema, key, value string) error {
	_, err := run(ctx, "gsettings", "set", schema, key, value)
	return err
}

// onCinnamon rejects other desktops when the desktop is known.
func onCinnamon() bool {
	d := os.Getenv("XDG_CURRENT_DESKTOP")
	return d == "" || strings.Contains(strings.ToLower(d), "cinnamon")
}

const commonProps = `"enabled": {"type": "boolean"}`

// NewWallpaper returns the wallpaper module with its backends.
func NewWallpaper(run Runner) Module {
	return &backendModule{
		id: "wallpaper",
		schema: `{"type":"object","additionalProperties":false,"required":["image"],"properties":{` + commonProps + `,
		  "image":{"type":"string","minLength":1},
		  "options":{"enum":["none","wallpaper","centered","scaled","stretched","zoom","spanned"]}}}`,
		backends: []Backend{&cinnamonWallpaper{run: run}},
	}
}

type cinnamonWallpaper struct{ run Runner }

func (b *cinnamonWallpaper) Name() string { return "cinnamon" }

func (b *cinnamonWallpaper) Detect(ctx context.Context) bool {
	if !onCinnamon() {
		return false
	}
	_, err := gsGet(ctx, b.run, cinnamonBackground, "picture-uri")
	return err == nil
}

func (b *cinnamonWallpaper) Apply(ctx context.Context, raw json.RawMessage) error {
	var cfg struct {
		Image   string `json:"image"`
		Options string `json:"options"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.Image == "" {
		return nil // an empty snapshot: nothing to restore
	}
	img, err := ResolveWallpaper(cfg.Image)
	if err != nil {
		return err
	}
	uri := img
	if !strings.Contains(uri, "://") {
		abs, err := filepath.Abs(core.ExpandPath(img))
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("wallpaper: %w", err)
		}
		uri = (&url.URL{Scheme: "file", Path: abs}).String()
	}
	if err := gsSet(ctx, b.run, cinnamonBackground, "picture-uri", uri); err != nil {
		return err
	}
	if cfg.Options != "" {
		return gsSet(ctx, b.run, cinnamonBackground, "picture-options", cfg.Options)
	}
	return nil
}

func (b *cinnamonWallpaper) Snapshot(ctx context.Context) (json.RawMessage, error) {
	uri, err := gsGet(ctx, b.run, cinnamonBackground, "picture-uri")
	if err != nil {
		return nil, err
	}
	snap := map[string]string{}
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		snap["image"] = u.Path
	} else if uri != "" {
		snap["image"] = uri
	}
	if opt, err := gsGet(ctx, b.run, cinnamonBackground, "picture-options"); err == nil && opt != "" {
		snap["options"] = opt
	}
	return json.Marshal(snap)
}

// NewColorScheme returns the colour scheme module with its backends.
func NewColorScheme(run Runner) Module {
	return &backendModule{
		id: "color_scheme",
		schema: `{"type":"object","additionalProperties":false,"properties":{` + commonProps + `,
		  "theme":{"type":"string","minLength":1},
		  "shell_theme":{"type":"string"},
		  "prefer":{"enum":["dark","light","default"]}}}`,
		backends: []Backend{&cinnamonColors{run: run}},
	}
}

type cinnamonColors struct{ run Runner }

func (b *cinnamonColors) Name() string { return "cinnamon" }

func (b *cinnamonColors) Detect(ctx context.Context) bool {
	if !onCinnamon() {
		return false
	}
	_, err := gsGet(ctx, b.run, cinnamonInterface, "gtk-theme")
	return err == nil
}

var preferToScheme = map[string]string{"dark": "prefer-dark", "light": "prefer-light", "default": "default"}

func (b *cinnamonColors) Apply(ctx context.Context, raw json.RawMessage) error {
	var cfg struct {
		Theme      string `json:"theme"`
		ShellTheme string `json:"shell_theme"`
		Prefer     string `json:"prefer"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.Theme != "" {
		if err := gsSet(ctx, b.run, cinnamonInterface, "gtk-theme", cfg.Theme); err != nil {
			return err
		}
	}
	if cfg.ShellTheme != "" {
		if err := gsSet(ctx, b.run, cinnamonShell, "name", cfg.ShellTheme); err != nil {
			return err
		}
	}
	if v, ok := preferToScheme[cfg.Prefer]; ok {
		// Best effort: whether Cinnamon honours this key depends on its
		// version (see docs/decisions.md), so a failure is not an error.
		_ = gsSet(ctx, b.run, gnomeInterface, "color-scheme", v)
	}
	return nil
}

func (b *cinnamonColors) Snapshot(ctx context.Context) (json.RawMessage, error) {
	snap := map[string]string{}
	if v, err := gsGet(ctx, b.run, cinnamonInterface, "gtk-theme"); err == nil {
		snap["theme"] = v
	} else {
		return nil, err
	}
	if v, err := gsGet(ctx, b.run, cinnamonShell, "name"); err == nil && v != "" {
		snap["shell_theme"] = v
	}
	if v, err := gsGet(ctx, b.run, gnomeInterface, "color-scheme"); err == nil {
		for p, s := range preferToScheme {
			if s == v {
				snap["prefer"] = p
			}
		}
	}
	return json.Marshal(snap)
}
