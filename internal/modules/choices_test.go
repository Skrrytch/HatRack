package modules

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hatrack/internal/core"
)

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, h/2, color.RGBA{200, 50, 50, 255})
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	jpeg.Encode(f, img, nil)
}

func TestThemeChoices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sys := t.TempDir()
	t.Setenv("XDG_DATA_DIRS", sys)
	for _, d := range []string{
		filepath.Join(sys, "themes", "Mint-Y-Dark", "gtk-3.0"),
		filepath.Join(sys, "themes", "Mint-Y-Dark", "cinnamon"),
		filepath.Join(sys, "themes", "Adwaita", "gtk-3.0"),
		filepath.Join(sys, "themes", "OnlyIcons", "index"),
		filepath.Join(home, ".themes", "my-theme", "gtk-3.0"),
	} {
		os.MkdirAll(d, 0o755)
	}
	ch := (&cinnamonColors{}).Choices(context.Background())
	var gtk, shell []string
	for _, c := range ch["theme"] {
		gtk = append(gtk, c.Value)
	}
	for _, c := range ch["shell_theme"] {
		shell = append(shell, c.Value)
	}
	if strings.Join(gtk, ",") != "Adwaita,Mint-Y-Dark,my-theme" || strings.Join(shell, ",") != "Mint-Y-Dark" {
		t.Fatalf("gtk %v shell %v", gtk, shell)
	}
}

func TestWallpaperChoices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	os.MkdirAll(filepath.Join(home, "config"), 0o755)
	os.WriteFile(filepath.Join(home, "config", "user-dirs.dirs"), []byte(`XDG_PICTURES_DIR="$HOME/Bilder"`+"\n"), 0o644)
	if picturesDir() != filepath.Join(home, "Bilder") {
		t.Fatalf("pictures dir: %s", picturesDir())
	}
	writeJPEG(t, filepath.Join(home, "Bilder", "a.jpg"), 800, 450)
	writeJPEG(t, filepath.Join(home, "Bilder", "hats", "venom.jpg"), 300, 600)
	writeJPEG(t, filepath.Join(home, "Bilder", "a", "b", "too-deep.jpg"), 10, 10)
	os.WriteFile(filepath.Join(home, "Bilder", "notes.txt"), []byte("x"), 0o644)
	list := wallpapers([]string{filepath.Join(home, "Bilder")})
	var names []string
	for _, p := range list {
		names = append(names, filepath.Base(p))
	}
	if strings.Join(names, ",") != "a.jpg,venom.jpg" {
		t.Fatalf("wallpapers: %v", names)
	}
	th := Thumb(list[0])
	if !strings.HasPrefix(th, "data:image/jpeg;base64,") || len(th) > 20000 {
		t.Fatalf("thumb: %d bytes", len(th))
	}
	cached, _ := os.ReadDir(filepath.Join(home, "cache", "hatrack", "thumbs"))
	if len(cached) != 1 {
		t.Fatalf("thumb cache: %d files", len(cached))
	}
	if Thumb(list[0]) != th {
		t.Fatal("the cached thumb is reused")
	}
	if Thumb(filepath.Join(home, "Bilder", "notes.txt")) != "" {
		t.Fatal("no thumb for a non-image")
	}
}

func TestBuiltinWallpapers(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(data, "cache"))
	names := BuiltinWallpapers()
	if len(names) != 2*len(core.HatColors) || names[0] != "wp01-purple" || names[1] != "wp02-purple" || names[len(names)-1] != "wp02-mint" {
		t.Fatalf("built-ins grouped by colour: %v", names)
	}
	p, err := ResolveWallpaper("hatrack:wp01-aqua")
	if err != nil || p != filepath.Join(data, "hatrack", "wallpapers", "wp01-aqua.jpg") {
		t.Fatalf("resolve: %s %v", p, err)
	}
	fi, _ := os.Stat(p)
	// unchanged files are not written again; changed ones are repaired
	os.WriteFile(p, []byte("broken"), 0o644)
	if p2, err := ResolveWallpaper("hatrack:wp01-aqua"); err != nil || p2 != p {
		t.Fatal(err)
	}
	if fi2, _ := os.Stat(p); fi2.Size() != fi.Size() {
		t.Fatal("a changed file is written again")
	}
	for _, bad := range []string{"hatrack:", "hatrack:../x", "hatrack:wp09-teal"} {
		if _, err := ResolveWallpaper(bad); err == nil {
			t.Fatalf("%s: no error", bad)
		}
	}
	if p, _ := ResolveWallpaper("/tmp/x.jpg"); p != "/tmp/x.jpg" {
		t.Fatal("paths stay")
	}
	ch := builtinChoices()
	if len(ch) != len(names) || ch[0].Value != "hatrack:wp01-purple" || !strings.HasPrefix(ch[0].Thumb, "data:image/jpeg") {
		t.Fatalf("choices: %+v", ch[0])
	}
}
