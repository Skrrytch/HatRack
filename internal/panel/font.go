package panel

import (
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// The panel draws its own text with the embedded Go fonts, so it needs no
// toolkit and no installed fonts.

type face struct {
	f       font.Face
	ascent  int
	descent int
}

var (
	fontMu    sync.Mutex
	faceCache = map[faceKey]*face{}
	regular   *opentype.Font
	bold      *opentype.Font
)

type faceKey struct {
	bold bool
	px   int
}

func getFace(isBold bool, px int) *face {
	fontMu.Lock()
	defer fontMu.Unlock()
	k := faceKey{isBold, px}
	if f, ok := faceCache[k]; ok {
		return f
	}
	if regular == nil {
		var err error
		if regular, err = opentype.Parse(goregular.TTF); err != nil {
			panic(err)
		}
		if bold, err = opentype.Parse(gobold.TTF); err != nil {
			panic(err)
		}
	}
	src := regular
	if isBold {
		src = bold
	}
	fc, err := opentype.NewFace(src, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	m := fc.Metrics()
	f := &face{f: fc, ascent: m.Ascent.Ceil(), descent: m.Descent.Ceil()}
	faceCache[k] = f
	return f
}

// textWidth returns the width of s in pixels.
func textWidth(s string, isBold bool, px int) int {
	return font.MeasureString(getFace(isBold, px).f, s).Ceil()
}

// fit shortens s with an ellipsis so that it is at most w pixels wide.
func fit(s string, isBold bool, px, w int) string {
	if textWidth(s, isBold, px) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if t := string(r) + "…"; textWidth(t, isBold, px) <= w {
			return t
		}
	}
	return "…"
}
