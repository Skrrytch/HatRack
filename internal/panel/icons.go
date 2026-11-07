package panel

import (
	"image"
	"image/color"
	"math"
)

// Toolbar icons are drawn from small shape functions on a unit square, with
// 4x4 supersampling for smooth edges; no image files, no icon theme.

type shape func(u, v float64) bool

func rectS(u0, v0, u1, v1 float64) shape {
	return func(u, v float64) bool { return u >= u0 && u <= u1 && v >= v0 && v <= v1 }
}

func segDist(u, v, au, av, bu, bv float64) float64 {
	du, dv := bu-au, bv-av
	t := ((u-au)*du + (v-av)*dv) / (du*du + dv*dv)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(u-(au+t*du), v-(av+t*dv))
}

var iconShapes = map[string]shape{
	"pause": func(u, v float64) bool {
		return (rectS(.22, .12, .42, .88)(u, v) || rectS(.58, .12, .78, .88)(u, v))
	},
	"play": func(u, v float64) bool {
		return u >= .22 && u <= .86 && math.Abs(v-.5) <= (.86-u)*(.40/.64)
	},
	// stop, like on a tape recorder: a filled square
	// a check mark: two strokes
	"check": func(u, v float64) bool {
		return segDist(u, v, .12, .55, .40, .82) <= .09 || segDist(u, v, .40, .82, .90, .20) <= .09
	},
	"stop": rectS(.20, .20, .80, .80),
	// external link: a box open at the top right and an arrow leaving it
	"open": func(u, v float64) bool {
		box := (u >= .10 && u <= .74 && v >= .26 && v <= .90) &&
			!(u >= .20 && u <= .64 && v >= .36 && v <= .80) && !(u > .46 && v < .46)
		arrow := segDist(u, v, .42, .58, .84, .16) <= .055
		head := u >= .58 && u <= .92 && v >= .08 && v <= .42 && (u >= .83 || v <= .17)
		return box || arrow || head
	},
}

func drawIcon(img *image.RGBA, name string, r Rect, c color.RGBA) {
	f := iconShapes[name]
	if f == nil {
		return
	}
	const n = 4
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			hit := 0
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					u := (float64(x) + (float64(sx)+.5)/n) / float64(r.W)
					v := (float64(y) + (float64(sy)+.5)/n) / float64(r.H)
					if f(u, v) {
						hit++
					}
				}
			}
			if hit == 0 || !image.Pt(r.X+x, r.Y+y).In(img.Bounds()) {
				continue
			}
			t := float64(hit) / (n * n)
			d := img.RGBAAt(r.X+x, r.Y+y)
			mix := func(a, b uint8) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }
			img.SetRGBA(r.X+x, r.Y+y, color.RGBA{mix(d.R, c.R), mix(d.G, c.G), mix(d.B, c.B), 255})
		}
	}
}
