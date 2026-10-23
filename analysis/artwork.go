// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/vynulldev/vynull/internal/ffmpeg"
)

// ExtractArtwork tries to extract artwork for an audio file.
// First checks for embedded artwork via ffmpeg, then scans the track's
// directory for common cover art image files (cover.jpg, folder.jpg, etc.).
// Returns JPEG data or nil if no artwork found.
func ExtractArtwork(filePath string) []byte {
	// Try embedded artwork first.
	if art := extractEmbeddedArtwork(filePath); art != nil {
		return art
	}

	// Fall back to directory scan for cover art images.
	return findDirectoryArtwork(filepath.Dir(filePath))
}

// extractEmbeddedArtwork extracts embedded album art from an audio file using ffmpeg.
func extractEmbeddedArtwork(filePath string) []byte {
	return thumbnail(filePath, "-an")
}

// Common cover art filenames (case-insensitive).
var coverNames = []string{
	"cover", "folder", "album", "front", "artwork", "art",
	"albumart", "album_art", "albumartsmall", "thumb",
}

// Common image extensions.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".bmp": true, ".webp": true,
}

// findDirectoryArtwork scans a directory for common cover art image files.
// Returns JPEG data (converted via ffmpeg if needed) or nil.
func findDirectoryArtwork(dir string) []byte {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// First pass: look for files matching common cover art names.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !imageExts[ext] {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(name, ext))
		for _, cn := range coverNames {
			if base == cn {
				return loadAndResizeImage(filepath.Join(dir, name))
			}
		}
	}

	// Second pass: use any image file in the directory.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if imageExts[ext] {
			return loadAndResizeImage(filepath.Join(dir, e.Name()))
		}
	}

	return nil
}

// loadAndResizeImage loads an image file and converts/resizes to 240x240 JPEG via ffmpeg.
func loadAndResizeImage(path string) []byte {
	return thumbnail(path)
}

// thumbnail renders the picture in the file at path as a 240x240 JPEG,
// scaled to fit and padded, with inputArgs after the input; nil if ffmpeg
// finds none.
func thumbnail(path string, inputArgs ...string) []byte {
	args := append([]string{"-i", path}, inputArgs...)
	out, err := ffmpeg.Run(context.Background(), append(args,
		"-vf", "scale=240:240:force_original_aspect_ratio=decrease,pad=240:240:(ow-iw)/2:(oh-ih)/2",
		"-vframes", "1",
		"-f", "image2",
		"-c:v", "mjpeg",
		"-q:v", "5",
		"-",
	)...)
	if err != nil || len(out) < 100 {
		return nil
	}

	if len(out) >= 2 && out[0] == 0xFF && out[1] == 0xD8 {
		return out
	}
	return nil
}
