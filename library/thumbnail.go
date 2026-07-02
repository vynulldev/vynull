// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder for image.Decode
	"image/jpeg"
	_ "image/png" // register PNG decoder for image.Decode

	"golang.org/x/image/draw"
)

// ThumbnailJPEG decodes an image (JPEG/PNG/GIF) and re-encodes it as a JPEG
// that fits within a dim×dim box, preserving aspect ratio and never upscaling.
//
// It exists to keep artwork small enough for CDJs: a deck freezes and drops the
// Pro DJ Link connection when handed an oversized artwork blob over dbserver
// (real thumbnails are a few KB; a full-resolution import can be hundreds of
// KB). This is pure in-memory (no ffmpeg subprocess), so it's cheap enough to
// call on the request path.
func ThumbnailJPEG(data []byte, dim int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil, fmt.Errorf("empty image")
	}

	dw, dh := sw, sh
	if sw > dim || sh > dim { // fit within dim×dim, never upscale
		if sw >= sh {
			dw, dh = dim, int(float64(sh)*float64(dim)/float64(sw)+0.5)
		} else {
			dw, dh = int(float64(sw)*float64(dim)/float64(sh)+0.5), dim
		}
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf.Bytes(), nil
}
