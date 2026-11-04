package icon

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestIcon(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(Icon("#7a1fa2", 64)))
	if err != nil || img.Bounds().Dx() != 64 {
		t.Fatalf("icon: %v", err)
	}
	at := func(x, y int) [4]uint32 {
		r, g, b, a := img.At(x, y).RGBA()
		return [4]uint32{r >> 8, g >> 8, b >> 8, a >> 8}
	}
	// the frame has the hat colour, the crown is cream, the band has the hat colour again
	if f := at(32, 7); f != [4]uint32{0x7a, 0x1f, 0xa2, 255} {
		t.Fatalf("frame pixel: %v", f)
	}
	if c := at(32, 22); c != [4]uint32{0xf0, 0xe6, 0xdc, 255} {
		t.Fatalf("crown pixel: %v", c)
	}
	if b := at(32, 34); b != [4]uint32{0x7a, 0x1f, 0xa2, 255} {
		t.Fatalf("band pixel: %v", b)
	}
	if d := at(10, 30); d != [4]uint32{0x2a, 0x2d, 0x33, 255} {
		t.Fatalf("background pixel: %v", d)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("corner should be transparent")
	}
	if small, err := png.Decode(bytes.NewReader(Icon("#7a1fa2", 22))); err != nil || small.Bounds().Dx() != 22 {
		t.Fatalf("small icon: %v", err)
	}
	if got := SVG("#2FA7E0"); !strings.Contains(got, `stroke="#2fa7e0"`) || !strings.Contains(got, `fill="#2fa7e0"`) {
		t.Fatalf("svg: %s", got)
	}
	if c := ParseColor("nonsense"); c.R != 0x8a {
		t.Fatalf("bad colour: %+v", c)
	}
}
