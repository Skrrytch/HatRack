// Package icon draws the HatRack icon: a dark rounded square with a frame in
// the hat's colour and a cream top hat. It is used by the tray and the panel.
package icon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
)

// Grey is the frame colour while paused or without a hat.
const Grey = "#8a8f98"

// ParseColor reads #rrggbb; anything else gives the paused grey.
func ParseColor(s string) color.RGBA {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 6 {
		if v, err := strconv.ParseUint(s, 16, 32); err == nil {
			return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
		}
	}
	return color.RGBA{0x8a, 0x8f, 0x98, 255}
}

type pt struct{ x, y float64 }

// The icon is a dark rounded square with a frame in the hat's colour and a
// cream top hat whose band has the same colour (see internal/server/static/hatrack.svg). All
// coordinates are on a 64 x 64 grid.
var (
	cream = color.RGBA{0xf0, 0xe6, 0xdc, 255}
	dark  = color.RGBA{0x2a, 0x2d, 0x33, 255}

	crown = []pt{{21, 17}, {21.1, 16}, {21.7, 15.3}, {22.5, 15}, {23.5, 15},
		{40.5, 15}, {41.5, 15}, {42.3, 15.2}, {42.9, 15.9}, {43, 17},
		{41.5, 41}, {22.5, 41}}
	band = []pt{{22, 31}, {42, 31}, {41.6, 37}, {22.4, 37}}
)

// inRRect reports whether (x, y) lies inside a rounded rectangle.
func inRRect(x, y, rx0, ry0, rx1, ry1, r float64) bool {
	if x < rx0 || x > rx1 || y < ry0 || y > ry1 {
		return false
	}
	cx, cy := x, y
	switch {
	case x < rx0+r:
		cx = rx0 + r
	case x > rx1-r:
		cx = rx1 - r
	}
	switch {
	case y < ry0+r:
		cy = ry0 + r
	case y > ry1-r:
		cy = ry1 - r
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// inPoly is the even-odd rule for a polygon.
func inPoly(x, y float64, p []pt) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i].y > y) != (p[j].y > y) && x < (p[j].x-p[i].x)*(y-p[i].y)/(p[j].y-p[i].y)+p[i].x {
			in = !in
		}
	}
	return in
}

// shapeAt returns the colour of the icon at a point of the 64 x 64 grid and
// whether anything is drawn there.
func shapeAt(x, y float64, accent color.RGBA) (color.RGBA, bool) {
	if !inRRect(x, y, 2, 6, 62, 58, 10) { // outer edge of the frame
		return color.RGBA{}, false
	}
	switch {
	case inRRect(x, y, 12, 40, 52, 46, 3): // brim
		return cream, true
	case inPoly(x, y, band):
		return accent, true
	case inPoly(x, y, crown):
		return cream, true
	case inRRect(x, y, 6, 10, 58, 54, 6): // inside the frame
		return dark, true
	}
	return accent, true
}

// Icon draws the tray icon as a PNG, size x size pixels. It is drawn in code,
// so the tray needs no theme, no files and no toolkit.
func Icon(hex string, size int) []byte {
	accent := ParseColor(hex)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // sub-samples per axis for smooth edges
	k := 64 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, n float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+.5)/ss) * k
					y := (float64(py) + (float64(sy)+.5)/ss) * k
					if c, ok := shapeAt(x, y, accent); ok {
						r, g, b, n = r+float64(c.R), g+float64(c.G), b+float64(c.B), n+1
					}
				}
			}
			if n > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(n / (ss * ss) * 255)})
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// SVG returns the same icon as a vector graphic.
func SVG(hex string) string {
	c := strings.ToLower(hex)
	return `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
  <rect x="4" y="8" width="56" height="48" rx="8" fill="#2a2d33" stroke="` + c + `" stroke-width="4"/>
  <path d="M21 17 Q21 15 23.5 15 L40.5 15 Q43 15 43 17 L41.5 41 L22.5 41 Z" fill="#f0e6dc"/>
  <path d="M22 31 L42 31 L41.6 37 L22.4 37 Z" fill="` + c + `"/>
  <rect x="12" y="40" width="40" height="6" rx="3" fill="#f0e6dc"/>
</svg>
`
}
