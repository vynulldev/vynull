// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func encodeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x*7 + y*3), uint8(x ^ y), uint8(x + y*5), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestThumbnailJPEG_DownscalesSquare(t *testing.T) {
	small, err := ThumbnailJPEG(encodeTestJPEG(t, 1000, 1000), 240)
	if err != nil {
		t.Fatalf("ThumbnailJPEG: %v", err)
	}
	if len(small) == 0 || len(small) > 32*1024 {
		t.Fatalf("thumbnail is %d bytes (want >0 and well under the 32KB CDJ cap)", len(small))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(small))
	if err != nil {
		t.Fatalf("thumbnail not decodable: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, want jpeg", format)
	}
	if cfg.Width != 240 || cfg.Height != 240 {
		t.Errorf("square 1000x1000 → %dx%d, want 240x240", cfg.Width, cfg.Height)
	}
}

func TestThumbnailJPEG_NonSquareFits(t *testing.T) {
	small, err := ThumbnailJPEG(encodeTestJPEG(t, 800, 400), 240)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > 240 || cfg.Height > 240 {
		t.Errorf("800x400 → %dx%d, exceeds 240 box", cfg.Width, cfg.Height)
	}
	if cfg.Width != 240 { // wide image → width is the limiting side
		t.Errorf("wide image width = %d, want 240", cfg.Width)
	}
}

func TestThumbnailJPEG_NoUpscale(t *testing.T) {
	small, err := ThumbnailJPEG(encodeTestJPEG(t, 100, 100), 240)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 100 || cfg.Height != 100 {
		t.Errorf("100x100 upscaled to %dx%d — should stay 100x100", cfg.Width, cfg.Height)
	}
}

func TestThumbnailJPEG_BadInput(t *testing.T) {
	if _, err := ThumbnailJPEG([]byte("not an image"), 240); err == nil {
		t.Error("expected error on non-image input")
	}
}
