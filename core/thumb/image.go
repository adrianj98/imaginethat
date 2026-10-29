// Package thumb renders and caches previews of vault media. Image decoding
// for the formats Go understands lives here; HEIC, RAW and video frames come
// from the host through the Decoder interface in service.go.
package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	// Registered for their decoders only.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

func init() {
	// x/image/bmp registers no decoder by default in some versions; make sure
	// image.Decode knows about it.
	image.RegisterFormat("bmp", "BM", bmp.Decode, bmp.DecodeConfig)
}

// Quality is the JPEG quality of generated previews, the value the macOS
// app used for its own cache.
const Quality = 80

// Make decodes an image and re-encodes it as a JPEG whose longest
// edge is at most maxEdge pixels. Images already smaller are left at their
// original dimensions rather than upscaled.
func Make(src []byte, maxEdge int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("image has zero extent")
	}

	dw, dh := w, h
	if w > maxEdge || h > maxEdge {
		if w >= h {
			dw = maxEdge
			dh = h * maxEdge / w
		} else {
			dh = maxEdge
			dw = w * maxEdge / h
		}
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// CatmullRom is slower than nearest-neighbour but a grid of thumbnails is
	// the first thing a user sees, and the cache means each is scaled once.
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf.Bytes(), nil
}
