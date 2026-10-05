// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"path/filepath"
	"testing"
)

// TestBPMRangePersists: SetBPMRange writes to settings.json and a fresh load
// reads it back, and (0,0) clears it. This is the round-trip the web Analysis
// Setting relies on.
func TestBPMRangePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	s := NewCDJSettingsAt(path)
	if mn, mx := s.GetBPMRange(); mn != 0 || mx != 0 {
		t.Fatalf("fresh settings BPM range = %g-%g, want 0-0 (unset)", mn, mx)
	}

	s.SetBPMRange(88, 175)

	reloaded := NewCDJSettingsAt(path)
	if mn, mx := reloaded.GetBPMRange(); mn != 88 || mx != 175 {
		t.Fatalf("after reload BPM range = %g-%g, want 88-175", mn, mx)
	}

	reloaded.SetBPMRange(0, 0)
	if mn, mx := NewCDJSettingsAt(path).GetBPMRange(); mn != 0 || mx != 0 {
		t.Fatalf("after clear BPM range = %g-%g, want 0-0", mn, mx)
	}
}
