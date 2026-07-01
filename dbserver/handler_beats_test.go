// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"testing"
	"time"

	"vynull/analysis"
	"vynull/library"
)

// makeBeatGrid is a tiny test-side encoder mirroring the production
// format (20-byte preamble, 16-byte entries). Used to seed a fake
// "previously analysed" blob the override path will rewrite.
func makeBeatGrid(t *testing.T, bpm float64, numBeats int, downbeatIdx int, msPerBeat float64) []byte {
	t.Helper()
	const preamble = 20
	const entrySize = 16
	buf := make([]byte, preamble+numBeats*entrySize)
	binary.LittleEndian.PutUint32(buf[0:], 0x00080000)
	binary.LittleEndian.PutUint32(buf[4:], uint32(numBeats))
	binary.LittleEndian.PutUint32(buf[8:], uint32(numBeats*entrySize))
	binary.LittleEndian.PutUint32(buf[12:], 1)
	binary.LittleEndian.PutUint32(buf[16:], 1)
	tempo := uint16(bpm * 100)
	for i := 0; i < numBeats; i++ {
		off := preamble + i*entrySize
		bib := ((i - downbeatIdx) % 4)
		if bib < 0 {
			bib += 4
		}
		binary.LittleEndian.PutUint16(buf[off+0:], uint16(bib+1))
		binary.LittleEndian.PutUint16(buf[off+2:], tempo)
		binary.LittleEndian.PutUint32(buf[off+4:], uint32(float64(i)*msPerBeat))
	}
	return buf
}

// readBeat extracts (beatNum, tempo*100, timeMs) for the i-th entry.
func readBeat(t *testing.T, blob []byte, i int) (uint16, uint16, uint32) {
	t.Helper()
	const preamble = 20
	const entrySize = 16
	off := preamble + i*entrySize
	if off+entrySize > len(blob) {
		t.Fatalf("beat %d out of range (blob len %d)", i, len(blob))
	}
	return binary.LittleEndian.Uint16(blob[off:]),
		binary.LittleEndian.Uint16(blob[off+2:]),
		binary.LittleEndian.Uint32(blob[off+4:])
}

func numBeats(blob []byte) int {
	if len(blob) < 8 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(blob[4:]))
}

// newTestHandler returns a Handler with just the library set — the
// only field beatGridForTrack reads.
func newTestHandler(tracks ...*library.Track) *Handler {
	lib := library.NewLibrary(tracks, nil)
	return &Handler{lib: lib}
}

func TestBeatGridForTrack_NoOverride(t *testing.T) {
	tr := &library.Track{ID: 1, BPM: 128.0, Duration: library.DurationSec(60 * time.Second)}
	h := newTestHandler(tr)
	original := makeBeatGrid(t, 128.0, 16, 0, 60000.0/128)
	r := &analysis.Result{BPM: 128.0, BeatGrid: original}

	got := h.beatGridForTrack(1, r)
	if &got[0] != &original[0] {
		t.Errorf("expected unchanged cached blob to be returned by reference")
	}
}

func TestBeatGridForTrack_BPMOverride(t *testing.T) {
	// Track was detected at 128 BPM, user overrode to 130. The new
	// grid should be uniform at 130 BPM (msPerBeat ≈ 461.54).
	tr := &library.Track{
		ID:          1,
		BPM:         130.0,
		DetectedBPM: 128.0,
		Duration:    library.DurationSec(60 * time.Second),
	}
	h := newTestHandler(tr)
	original := makeBeatGrid(t, 128.0, 16, 0, 60000.0/128)
	r := &analysis.Result{BPM: 128.0, BeatGrid: original}

	got := h.beatGridForTrack(1, r)
	if got == nil {
		t.Fatal("expected non-nil blob")
	}
	// Tempo in every entry should reflect the override (130 → 13000).
	_, tempo, _ := readBeat(t, got, 0)
	if tempo != 13000 {
		t.Errorf("first beat tempo = %d, want 13000", tempo)
	}
	// Beat 1 should be at t=0 (detector's downbeat).
	beatNum, _, timeMs := readBeat(t, got, 0)
	if beatNum != 1 || timeMs != 0 {
		t.Errorf("first beat = (num=%d, t=%d), want (1, 0)", beatNum, timeMs)
	}
	// At 130 BPM, beat 5 (index 4) should be ~1846 ms.
	_, _, t4 := readBeat(t, got, 4)
	wantF := float64(4) * 60000.0 / 130.0
	want := uint32(wantF)
	if absDiff(t4, want) > 1 {
		t.Errorf("beat 5 time = %d ms, want ≈ %d", t4, want)
	}
}

func TestBeatGridForTrack_BPMOverride_DurationUnitRegression(t *testing.T) {
	// Regression: library.Track.Duration is time.Duration (ns).
	// Earlier beatGridForTrack did float64(t.Duration)*1000 which
	// produced ~10^14 ms for a 291s track, blew numBeats past 6e11,
	// and made GenerateBeatGrid try to allocate ~880 GB. The clamp
	// in GenerateBeatGrid would return nil even if the unit error
	// regressed, but the unit fix should keep numBeats sensible.
	tr := &library.Track{
		ID:          1,
		BPM:         130.0,
		DetectedBPM: 128.0,
		Duration:    library.DurationSec(291 * time.Second),
	}
	h := newTestHandler(tr)
	original := makeBeatGrid(t, 128.0, 620, 0, 60000.0/128)
	r := &analysis.Result{BPM: 128.0, BeatGrid: original}

	got := h.beatGridForTrack(1, r)
	if got == nil {
		t.Fatal("expected non-nil blob — clamp fired, suggesting durationMs is wrong")
	}
	// 291s × 130 BPM / 60 ≈ 630 beats. Anything wildly off (10x or
	// more) means the unit conversion regressed.
	n := numBeats(got)
	if n < 600 || n > 700 {
		t.Errorf("numBeats = %d, want ≈ 630 (suggests Duration unit conversion is wrong)", n)
	}
}

func TestBeatGridForTrack_PhaseShift(t *testing.T) {
	// Detector placed downbeat at index 0 (1-2-3-4-1-2-3-4-…). User
	// shifts phase by +1 → new downbeat at index 1, so:
	//   idx 0: was 1, now 4 (one position before new downbeat)
	//   idx 1: 1 (new downbeat)
	//   idx 2: 2
	//   idx 3: 3
	//   idx 4: 4
	//   idx 5: 1
	tr := &library.Track{
		ID:             1,
		BPM:            128.0,
		BeatPhaseShift: 1,
		Duration:       library.DurationSec(60 * time.Second),
	}
	h := newTestHandler(tr)
	original := makeBeatGrid(t, 128.0, 16, 0, 60000.0/128)
	r := &analysis.Result{BPM: 128.0, BeatGrid: original}

	got := h.beatGridForTrack(1, r)
	if got == nil {
		t.Fatal("expected non-nil blob")
	}
	expect := []uint16{4, 1, 2, 3, 4, 1, 2, 3}
	for i, want := range expect {
		num, _, _ := readBeat(t, got, i)
		if num != want {
			t.Errorf("beat[%d] = %d, want %d", i, num, want)
		}
	}
	// Phase-shift-only path keeps the original beat positions.
	wantT5F := float64(5) * 60000.0 / 128.0
	wantT5 := uint32(wantT5F)
	_, _, gotT5 := readBeat(t, got, 5)
	if absDiff(gotT5, wantT5) > 1 {
		t.Errorf("beat[5] time = %d, want %d (positions should be unchanged)", gotT5, wantT5)
	}
}

func TestBeatGridForTrack_NilLib(t *testing.T) {
	h := &Handler{}
	original := []byte{0xde, 0xad, 0xbe, 0xef}
	r := &analysis.Result{BPM: 128.0, BeatGrid: original}
	got := h.beatGridForTrack(1, r)
	if &got[0] != &original[0] {
		t.Errorf("expected pass-through of cached blob when lib is nil")
	}
}

func TestBeatGridForTrack_UnknownTrack(t *testing.T) {
	h := newTestHandler()
	original := []byte{0xca, 0xfe}
	r := &analysis.Result{BPM: 128.0, BeatGrid: original}
	got := h.beatGridForTrack(999, r)
	if &got[0] != &original[0] {
		t.Errorf("expected pass-through when track is missing from lib")
	}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
