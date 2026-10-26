// SPDX-License-Identifier: GPL-3.0-or-later

package filetype

import "testing"

func TestForPath(t *testing.T) {
	for path, want := range map[string]Type{
		"/m/a.mp3": mp3, "/m/a.M4A": m4a, "/m/a.flac": flac, "/m/a.wav": wav,
		"/m/a.aiff": aiff, "/m/a.AIF": aiff,
	} {
		if got, ok := ForPath(path); !ok || got != want {
			t.Errorf("ForPath(%q) = %+v, %v", path, got, ok)
		}
		if got, ok := ByName(want.Name); !ok || got != want {
			t.Errorf("ByName(%q) = %+v, %v", want.Name, got, ok)
		}
	}
	for _, path := range []string{"/m/a.mp4", "/m/a.ogg", "/m/a", "/m.flac/a"} {
		if got, ok := ForPath(path); ok {
			t.Errorf("ForPath(%q) = %+v", path, got)
		}
	}
}
