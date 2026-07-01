// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// RenderPreviewPNG draws the waveform preview as a PNG image.
// Width = number of data points, height = 64 pixels.
func RenderPreviewPNG(data []byte, path string) error {
	w := len(data)
	h := 64
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// Black background.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.Black)
		}
	}

	// Draw waveform bars from center.
	mid := h / 2
	for x := 0; x < w; x++ {
		amplitude := int(data[x])
		barH := amplitude * mid / 31
		if barH < 1 && amplitude > 0 {
			barH = 1
		}
		c := color.RGBA{R: 0, G: 180, B: 255, A: 255} // cyan
		for y := mid - barH; y <= mid+barH; y++ {
			if y >= 0 && y < h {
				img.Set(x, y, c)
			}
		}
	}

	return writePNG(img, path)
}

// RenderColorPreviewPNG draws the color waveform preview as a PNG.
// Each entry is 6 bytes: [height, red, green, blue, ?, ?].
func RenderColorPreviewPNG(data []byte, path string) error {
	entrySize := 6
	n := len(data) / entrySize
	if n == 0 {
		return fmt.Errorf("no color preview data")
	}

	w := n
	h := 64
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.Black)
		}
	}

	mid := h / 2
	for x := 0; x < n; x++ {
		off := x * entrySize
		amplitude := int(data[off] & 0x1f)
		r := data[off+3]
		g := data[off+4]
		b := data[off+5]

		barH := amplitude * mid / 31
		if barH < 1 && amplitude > 0 {
			barH = 1
		}

		c := color.RGBA{
			R: r,
			G: g,
			B: b,
			A: 255,
		}

		for y := mid - barH; y <= mid+barH; y++ {
			if y >= 0 && y < h {
				img.Set(x, y, c)
			}
		}
	}

	return writePNG(img, path)
}

// RenderDetailPNG draws the scrolling waveform detail as a PNG.
// PWV5 format: 16-bit big-endian per entry: R(3) G(3) B(3) H(5) unused(2).
func RenderDetailPNG(data []byte, path string) error {
	n := len(data) / 2
	if n == 0 {
		return fmt.Errorf("no detail data")
	}

	// Scale width to be reasonable (max 4000px, downsample if needed).
	scale := 1
	for n/scale > 4000 {
		scale++
	}
	w := n / scale
	h := 80
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 10, B: 20, A: 255})
		}
	}

	mid := h / 2
	for x := 0; x < w; x++ {
		var maxAmp int
		var sumR, sumG, sumB, count int
		for s := 0; s < scale; s++ {
			idx := (x*scale + s) * 2
			if idx+1 >= len(data) {
				break
			}
			word := uint16(data[idx])<<8 | uint16(data[idx+1])
			if data[idx] == 0xff && data[idx+1] == 0x80 {
				continue // skip padding
			}
			amp := int((word >> 2) & 0x1f)
			r3 := int((word >> 13) & 7)
			g3 := int((word >> 10) & 7)
			b3 := int((word >> 7) & 7)
			if amp > maxAmp {
				maxAmp = amp
			}
			sumR += r3
			sumG += g3
			sumB += b3
			count++
		}

		barH := maxAmp * mid / 31
		if barH < 1 && maxAmp > 0 {
			barH = 1
		}

		// Scale 3-bit RGB (0-7) to 8-bit (0-255)
		var r, g, b uint8
		if count > 0 {
			r = uint8(sumR / count * 255 / 7)
			g = uint8(sumG / count * 255 / 7)
			b = uint8(sumB / count * 255 / 7)
		}
		c := color.RGBA{R: r, G: g, B: b, A: 255}

		for y := mid - barH; y <= mid+barH; y++ {
			if y >= 0 && y < h {
				img.Set(x, y, c)
			}
		}
	}

	return writePNG(img, path)
}

func writePNG(img image.Image, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
