// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "testing"

// SetIfAbsent stores only for a track without a result, and TryImport hands
// back a result stored while its import ran rather than replacing it.
func TestSetIfAbsentKeepsCurrent(t *testing.T) {
	s := NewStore()
	first, second := &Result{BPM: 128}, &Result{BPM: 99}
	if !s.SetIfAbsent(1, first) || s.SetIfAbsent(1, second) || s.Get(1) != first {
		t.Fatalf("SetIfAbsent: stored %+v, want the first result kept", s.Get(1))
	}
	s.Importer = func(id uint32, _ string) *Result {
		s.Set(id, first) // stored while the import ran
		return second
	}
	if got := s.TryImport(2, ""); got != first || s.Get(2) != first {
		t.Errorf("TryImport returned %+v, stored %+v; want the result stored meanwhile", got, s.Get(2))
	}
}
