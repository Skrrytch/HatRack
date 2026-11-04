package modules

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // decode PNG wallpapers
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/image/draw"
)

// Choice is a value a setting can take on this device, for pick lists in
// the web UI: an installed theme, a wallpaper with a small preview.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Thumb string `json:"thumb,omitempty"` // data: URI of a small JPEG
}

// Chooser is implemented by backends that can list what is installed. The
// result maps a setting (e.g. "theme") to its choices.
type Chooser interface {
	Choices(ctx context.Context) map[string][]Choice
}

// Choices asks the working backend of every module for its choices. It does
// not hold the host's lock: listing files and making previews may take a
// while and must not hold up switching.
func (h *Host) Choices(ctx context.Context) map[string]map[string][]Choice {
	out := map[string]map[string][]Choice{}
	for _, m := range h.Reg.All() {
		x, ok := m.(interface{ Active(context.Context) Backend })
		if !ok {
			continue
		}
		if c, ok := x.Active(ctx).(Chooser); ok {
			if ch := c.Choices(ctx); len(ch) > 0 {
				out[m.ID()] = ch
			}
		}
	}
	return out
}

// ---- themes ---------------------------------------------------------------------

func homeDir() string { h, _ := os.UserHomeDir(); return h }

func dataDirs() []string {
	dirs := []string{filepath.Join(homeDir(), ".local", "share")}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	return append(dirs, filepath.SplitList(sys)...)
}

// themes lists theme directories that contain the given subdirectory
// (gtk-3.0 for GTK themes, cinnamon for Cinnamon shell themes).
func themes(sub string) []Choice {
	roots := []string{filepath.Join(homeDir(), ".themes")}
	for _, d := range dataDirs() {
		roots = append(roots, filepath.Join(d, "themes"))
	}
	seen := map[string]bool{}
	var names []string
	for _, root := range roots {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if seen[e.Name()] {
				continue
			}
			if fi, err := os.Stat(filepath.Join(root, e.Name(), sub)); err == nil && fi.IsDir() {
				seen[e.Name()] = true
				names = append(names, e.Name())
			}
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	out := make([]Choice, len(names))
	for i, n := range names {
		out[i] = Choice{Value: n}
	}
	return out
}

func (b *cinnamonColors) Choices(ctx context.Context) map[string][]Choice {
	return map[string][]Choice{"theme": themes("gtk-3.0"), "shell_theme": themes("cinnamon")}
}

// ---- wallpapers -------------------------------------------------------------------

// maxWallpapers keeps the list (and the previews sent to the server) small.
const maxWallpapers = 150

// picturesDir reads XDG_PICTURES_DIR from user-dirs.dirs, as set by
// xdg-user-dirs (e.g. ~/Bilder on a German system).
func picturesDir() string {
	home := homeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	if f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "XDG_PICTURES_DIR="); ok {
				return strings.ReplaceAll(strings.Trim(v, `"`), "$HOME", home)
			}
		}
	}
	return filepath.Join(home, "Pictures")
}

// WallpaperDirs are searched for wallpapers, two levels deep.
func WallpaperDirs() []string {
	return []string{picturesDir(), "/usr/share/backgrounds"}
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

func wallpapers(dirs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, root := range dirs {
		rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || len(out) >= maxWallpapers {
				return filepath.SkipDir
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && p != root {
					return filepath.SkipDir
				}
				if strings.Count(p, string(filepath.Separator))-rootDepth >= 2 {
					return filepath.SkipDir
				}
				return nil
			}
			if real, err := filepath.EvalSymlinks(p); err == nil && !seen[real] && isImage(p) {
				seen[real] = true
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

// thumbDir caches previews, so only new or changed images cost time.
func thumbDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".cache")
	}
	return filepath.Join(base, "hatrack", "thumbs")
}

// Thumb returns a small JPEG preview of an image as a data: URI, or "".
func Thumb(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())))
	cache := filepath.Join(thumbDir(), hex.EncodeToString(sum[:12])+".jpg")
	b, err := os.ReadFile(cache)
	if err != nil {
		if b, err = makeThumb(path, 160, 100); err != nil {
			return ""
		}
		os.MkdirAll(filepath.Dir(cache), 0o700)
		os.WriteFile(cache, b, 0o600)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

func makeThumb(path string, w, h int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	// cover: scale to fill w×h, then cut the middle
	sb := src.Bounds()
	scale := max(float64(w)/float64(sb.Dx()), float64(h)/float64(sb.Dy()))
	sw, sh := int(float64(w)/scale), int(float64(h)/scale)
	crop := image.Rect(sb.Min.X+(sb.Dx()-sw)/2, sb.Min.Y+(sb.Dy()-sh)/2, 0, 0)
	crop.Max = crop.Min.Add(image.Pt(sw, sh))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 70})
	return buf.Bytes(), err
}

func (b *cinnamonWallpaper) Choices(ctx context.Context) map[string][]Choice {
	out := builtinChoices()
	for _, p := range wallpapers(WallpaperDirs()) {
		if ctx.Err() != nil {
			break
		}
		out = append(out, Choice{Value: p, Label: filepath.Base(p), Thumb: Thumb(p)})
	}
	return map[string][]Choice{"image": out}
}
