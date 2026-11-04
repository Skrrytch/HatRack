package core

import "strings"

// NamedColor is a colour offered for hats.
type NamedColor struct {
	Name string // purple, orange, …; also the suffix of its wallpapers
	Hex  string
}

// HatColors are the colours offered for hats, chosen to be told apart easily
// (CIELAB distance at least 33 for every pair). Each has built-in
// wallpapers (see BuiltinPrefix).
var HatColors = []NamedColor{
	{"purple", "#8c4ea3"},
	{"orange", "#ee6d24"},
	{"aqua", "#2f999c"},
	{"brown", "#8a4a26"},
	{"pink", "#c24e63"},
	{"mint", "#bde292"},
}

// ColorName returns the name of a palette colour, or "" for any other.
func ColorName(hex string) string {
	for _, c := range HatColors {
		if strings.EqualFold(c.Hex, hex) {
			return c.Name
		}
	}
	return ""
}

// BuiltinPrefix marks a built-in wallpaper in the configuration
// ("hatrack:wp01-purple"). Each device resolves it to a file of its own, so
// one configuration fits machines with different home folders.
const BuiltinPrefix = "hatrack:"
