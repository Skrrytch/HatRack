package panel

import (
	"bytes"
	"image"
	"image/png"
)

func decodePNG(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
