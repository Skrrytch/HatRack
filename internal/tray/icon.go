package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
)

// ParseColor reads #rrggbb; anything else gives grey.
func ParseColor(s string) color.RGBA {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 6 {
		if v, err := strconv.ParseUint(s, 16, 32); err == nil {
			return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
		}
	}
	return color.RGBA{0x9a, 0x9a, 0x9a, 255}
}

func shade(c color.RGBA, f float64) color.RGBA {
	return color.RGBA{uint8(float64(c.R) * f), uint8(float64(c.G) * f), uint8(float64(c.B) * f), 255}
}

// Icon draws a top hat in the given colour as a PNG. It is drawn in code, so
// the tray needs no theme, no files and no toolkit.
func Icon(hex string, size int) []byte {
	c := ParseColor(hex)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size) / 64
	// coverage with a one pixel soft edge for smooth curves
	set := func(x, y int, col color.RGBA, a float64) {
		if a <= 0 {
			return
		}
		if a > 1 {
			a = 1
		}
		img.SetRGBA(x, y, color.RGBA{col.R, col.G, col.B, uint8(a * 255)})
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := (float64(x)+.5)/s, (float64(y)+.5)/s
			// brim: a wide flat ellipse
			brim := 1 - math.Hypot((fx-32)/27, (fy-47)/6.5)
			// crown: a rounded rectangle
			cx := math.Max(math.Abs(fx-32)-11, 0)
			cy := math.Max(math.Abs(fy-27)-14, 0)
			crown := 1 - math.Hypot(cx, cy)/4
			if fy > 41 {
				crown = 0
			}
			// band around the crown, darker
			band := fy > 34 && fy < 41 && math.Abs(fx-32) < 15.5
			a := math.Max(brim*s*6, crown*s*4)
			col := c
			if band && crown > 0 {
				col = shade(c, .55)
			}
			if brim > 0 && crown <= 0 {
				col = shade(c, .85)
			}
			set(x, y, col, a)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}
