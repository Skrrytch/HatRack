// Package assets holds files built into the binary: the wallpapers that go
// with the hat colours.
package assets

import "embed"

// Wallpapers are abstract colour gradients, two sets for each colour of
// core.HatColors, named wpNN-<colour>.jpg.
//
//go:embed wallpapers/*.jpg
var Wallpapers embed.FS
