package modules

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"hatrack/internal/assets"
	"hatrack/internal/core"
)

// Built-in wallpapers live in the binary and are written to a folder of the
// user's data directory when a device lists or applies one, so the desktop
// can read them as ordinary files.

// builtinDir is where the built-in wallpapers are written.
func builtinDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".local", "share")
	}
	return filepath.Join(base, "hatrack", "wallpapers")
}

// BuiltinWallpapers lists the names of the built-in wallpapers ("wp01-purple"),
// grouped by colour in the order of core.HatColors.
func BuiltinWallpapers() []string {
	entries, _ := fs.ReadDir(assets.Wallpapers, "wallpapers")
	rank := func(name string) int {
		for i, c := range core.HatColors {
			if strings.HasSuffix(name, "-"+c.Name) {
				return i
			}
		}
		return len(core.HatColors)
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), path.Ext(e.Name())))
	}
	sort.SliceStable(names, func(a, b int) bool {
		if ra, rb := rank(names[a]), rank(names[b]); ra != rb {
			return ra < rb
		}
		return names[a] < names[b]
	})
	return names
}

// ResolveWallpaper turns a built-in value ("hatrack:wp01-purple") into the
// path of its file, writing the file first if it is missing or different.
// Other values come back unchanged.
func ResolveWallpaper(v string) (string, error) {
	name, ok := strings.CutPrefix(v, core.BuiltinPrefix)
	if !ok {
		return v, nil
	}
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("wallpaper: unknown built-in %q", v)
	}
	data, err := assets.Wallpapers.ReadFile("wallpapers/" + name + ".jpg")
	if err != nil {
		return "", fmt.Errorf("wallpaper: unknown built-in %q", v)
	}
	p := filepath.Join(builtinDir(), name+".jpg")
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	return p, os.Rename(tmp, p)
}

// builtinChoices offers the built-in wallpapers with their values.
func builtinChoices() []Choice {
	var out []Choice
	for _, name := range BuiltinWallpapers() {
		p, err := ResolveWallpaper(core.BuiltinPrefix + name)
		if err != nil {
			continue
		}
		out = append(out, Choice{Value: core.BuiltinPrefix + name, Label: name, Thumb: Thumb(p)})
	}
	return out
}
