package panel

import (
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"hatrack/internal/icon"
)

// State of the pointer and keyboard for drawing.
type View struct {
	Hover, Focus int
	Filter       string
}

// Render draws a layout into a new image.
func Render(l Layout, v View) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, l.W, l.H))
	draw.Draw(img, img.Bounds(), image.NewUniform(cBG), image.Point{}, draw.Src)
	for _, b := range l.Boxes {
		drawBox(img, b)
	}
	for i, h := range l.Hits {
		drawHit(img, h, i == v.Hover, i == v.Focus, l.Scale)
	}
	for _, t := range l.Texts {
		drawText(img, t)
	}
	if l.IconRect != nil {
		if p, err := decodePNG(icon.Icon(l.IconHex, l.IconRect.W)); err == nil {
			draw.Draw(img, image.Rect(l.IconRect.X, l.IconRect.Y, l.IconRect.X+l.IconRect.W, l.IconRect.Y+l.IconRect.H), p, image.Point{}, draw.Over)
		}
	}
	// hint line at the bottom
	m := newMetrics(l.Scale)
	hint := l.HintFor(v.Hover, v.Focus)
	if v.Filter != "" {
		hint = "⌕ " + v.Filter + "   " + hint
	}
	if hint != "" {
		hy := l.H - m.hintH + (m.hintH-m.chipPx)/2 - 1
		drawText(img, Text{X: m.pad, Y: hy, S: fit(hint, false, m.chipPx, l.W-2*m.pad), Px: m.chipPx, Color: cDim})
	}
	// window border
	border(img, cBorder)
	return img
}

func border(img *image.RGBA, c color.RGBA) {
	b := img.Bounds()
	for x := b.Min.X; x < b.Max.X; x++ {
		img.SetRGBA(x, b.Min.Y, c)
		img.SetRGBA(x, b.Max.Y-1, c)
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		img.SetRGBA(b.Min.X, y, c)
		img.SetRGBA(b.Max.X-1, y, c)
	}
}

func drawBox(img *image.RGBA, b Box) {
	if b.Border != (color.RGBA{}) {
		fillRound(img, b.R, b.Radius, b.Border)
		in := Rect{b.R.X + 2, b.R.Y + 2, b.R.W - 4, b.R.H - 4}
		fillRound(img, in, max(b.Radius-2, 0), b.Color)
	} else {
		fillRound(img, b.R, b.Radius, b.Color)
	}
}

func fillRect(img *image.RGBA, r Rect, c color.RGBA) {
	rr := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H).Intersect(img.Bounds())
	draw.Draw(img, rr, image.NewUniform(c), image.Point{}, draw.Src)
}

// fillRound fills a rectangle with rounded corners (no anti-aliasing at the
// corners beyond a simple coverage test, which is enough at this size).
func fillRound(img *image.RGBA, r Rect, rad int, c color.RGBA) {
	if rad <= 0 {
		fillRect(img, r, c)
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			cov := cornerCoverage(x-r.X, y-r.Y, r.W, r.H, rad)
			if cov <= 0 || !image.Pt(x, y).In(img.Bounds()) {
				continue
			}
			if cov >= 1 {
				img.SetRGBA(x, y, c)
				continue
			}
			d := img.RGBAAt(x, y)
			mix := func(a, b uint8) uint8 { return uint8(float64(a)*(1-cov) + float64(b)*cov) }
			img.SetRGBA(x, y, color.RGBA{mix(d.R, c.R), mix(d.G, c.G), mix(d.B, c.B), 255})
		}
	}
}

func cornerCoverage(x, y, w, h, rad int) float64 {
	cx, cy := -1, -1
	switch {
	case x < rad && y < rad:
		cx, cy = rad, rad
	case x >= w-rad && y < rad:
		cx, cy = w-rad-1, rad
	case x < rad && y >= h-rad:
		cx, cy = rad, h-rad-1
	case x >= w-rad && y >= h-rad:
		cx, cy = w-rad-1, h-rad-1
	default:
		return 1
	}
	dx, dy := float64(x-cx), float64(y-cy)
	d := sqrt(dx*dx + dy*dy)
	switch {
	case d <= float64(rad)-1:
		return 1
	case d >= float64(rad):
		return 0
	}
	return float64(rad) - d
}

func sqrt(v float64) float64 {
	if v <= 0 {
		return 0
	}
	z := v
	for i := 0; i < 20; i++ {
		z = (z + v/z) / 2
	}
	return z
}

func drawText(img *image.RGBA, t Text) {
	f := getFace(t.Bold, t.Px)
	x := t.X
	if t.Right {
		x -= textWidth(t.S, t.Bold, t.Px)
	}
	d := font.Drawer{Dst: img, Src: image.NewUniform(t.Color), Face: f.f,
		Dot: fixed.P(x, t.Y+f.ascent)}
	d.DrawString(t.S)
}

func drawHit(img *image.RGBA, h Hit, hover, focus bool, scale float64) {
	m := newMetrics(scale)
	switch h.Kind {
	case KindHat:
		// the card gets a frame in the hat's colour when worn, a softer one on hover
		switch {
		case h.Current:
			frame(img, h.Card, m.s6(), 2, h.Color, cCard)
		case hover || focus:
			frame(img, h.Card, m.s6(), 2, mixc(cCard, h.Color, .6), cCard)
		}
		tc := cFG
		if !h.Match {
			tc = cDim
		}
		px := m.txPx
		tx := h.R.X + m.chipPad + m.dot + m.chipPad/2
		label := fit(h.Label, true, px, h.R.X+h.R.W-m.chipPad-tx)
		fillRound(img, Rect{h.R.X + m.chipPad, h.R.Y + (h.R.H-m.dot)/2, m.dot, m.dot}, m.dot/2, h.Color)
		drawText(img, Text{X: tx, Y: h.R.Y + (h.R.H-px)/2 - 1, S: label, Bold: true, Px: px, Color: tc})
	case KindActivity:
		bg := cChip
		fg := cFG
		switch {
		case h.Current:
			bg, fg = h.Color, color.RGBA{0x18, 0x1a, 0x1e, 255}
		case hover || focus:
			bg = mixc(cChip, h.Color, .55)
		case h.Match:
			bg = mixc(cChip, h.Color, .3)
		}
		fillRound(img, h.R, h.R.H/2, bg)
		if focus {
			frame(img, h.R, h.R.H/2, 1, cFG, bg)
		}
		label := fit(h.Label, h.Current, m.chipPx, h.R.W-2*m.chipPad)
		drawText(img, Text{X: h.R.X + m.chipPad, Y: h.R.Y + (h.R.H-m.chipPx)/2 - 1, S: label, Bold: h.Current, Px: m.chipPx, Color: fg})
	case KindTool:
		bg := cCard
		if hover || focus {
			bg = cChip
		}
		fillRound(img, h.R, m.s6(), bg)
		if focus {
			frame(img, h.R, m.s6(), 1, cDim, bg)
		}
		sz := m.toolBtnH * 5 / 9
		ir := Rect{h.R.X + (h.R.W-sz)/2, h.R.Y + (h.R.H-sz)/2, sz, sz}
		if h.Icon != "" {
			drawIcon(img, h.Icon, ir, cFG)
		} else {
			drawText(img, Text{X: h.R.X + (h.R.W-textWidth(h.Label, false, m.chipPx))/2, Y: h.R.Y + (h.R.H-m.chipPx)/2 - 1, S: h.Label, Px: m.chipPx, Color: cFG})
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// frame draws a rounded outline of the given width on top of a filled
// rounded shape: the outer shape in c, then the inner one back in fill.
func frame(img *image.RGBA, r Rect, rad, width int, c, fill color.RGBA) {
	fillRound(img, r, rad, c)
	fillRound(img, Rect{r.X + width, r.Y + width, r.W - 2*width, r.H - 2*width}, max(rad-width, 0), fill)
}

func mixc(a, b color.RGBA, t float64) color.RGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 255}
}
