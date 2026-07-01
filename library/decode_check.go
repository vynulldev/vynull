// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"os/exec"
	"strings"
)

// DecodeStatus values stored on a Track after CheckDecode runs.
const (
	DecodeStatusUnchecked = ""        // CheckDecode hasn't run yet
	DecodeStatusOK        = "ok"      // ffmpeg decoded cleanly
	DecodeStatusWarn      = "warning" // recoverable issues — usually plays fine
	DecodeStatusError     = "error"   // hard decode errors — CDJ may freeze
)

// CheckDecode runs ffmpeg over the file looking for frame-level decode
// errors. Returns (status, issue) where status is one of
// DecodeStatusOK/Warn/Error and issue is the first ffmpeg complaint
// (empty if OK). If ffmpeg isn't available, returns (Unchecked, "").
//
// Why: CDJ hardware MP3 decoders are less tolerant than ffmpeg. A
// malformed frame ffmpeg recovers from often freezes the deck mid-
// playback. Surfacing this at library-add time lets the user re-encode
// problem files before they get burned on a USB.
func CheckDecode(path string) (status, issue string) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return DecodeStatusUnchecked, ""
	}
	cmd := exec.Command("ffmpeg", "-v", "warning", "-i", path, "-f", "null", "-")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Run() // ignore exit code — partial decodes are normal

	for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ll := strings.ToLower(line)
		// Hard errors empirically tied to CDJ mid-playback freezes.
		if strings.Contains(ll, "header missing") ||
			strings.Contains(ll, "invalid data found") ||
			strings.Contains(ll, "error while decoding") ||
			strings.Contains(ll, "frame missing") ||
			strings.Contains(ll, "broken header") {
			return DecodeStatusError, line
		}
		// Recoverable issues (estimated duration, missing VBR header).
		if status == "" {
			status = DecodeStatusWarn
			issue = line
		}
	}
	if status == "" {
		return DecodeStatusOK, ""
	}
	return status, issue
}
