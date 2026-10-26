// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/fsutil"
	"github.com/vynulldev/vynull/internal/saver"
)

// CuePoint represents a saved hot cue or loop.
type CuePoint struct {
	Number  uint16 `json:"number"`   // 1-based: A=1, B=2, C=3, ...
	Type    uint16 `json:"type"`     // 1=cue, 2=loop
	TimeMs  uint32 `json:"time_ms"`  // cue position in ms
	LoopMs  int32  `json:"loop_ms"`  // loop end in ms (-1 if not a loop)
	Status  uint32 `json:"status"`   // 1=active
	ColorID uint32 `json:"color_id"` // color/flags
}

// CueStore persists cue points per track to disk: cues_<id>.json, plus
// cue_<id>_<n>.bin for each cue the deck saved with its own blob.
//
// A track's cue slice and blob map are replaced on every change, never
// edited in place, so readers and the background writer can hold them
// without the lock.
type CueStore struct {
	mu       sync.RWMutex
	dir      string
	data     map[uint32][]CuePoint        // trackID → parsed cues
	rawBlobs map[uint32]map[uint16][]byte // trackID → cueNumber → raw blob
	dirty    map[uint32]bool              // tracks changed since the last write
	saver    *saver.Saver                 // writes the dirty tracks in the background

	// onDisk is the writer's record of the blob files each track has on
	// disk, so a write skips unchanged blobs and removes stale ones. Only
	// loadAll and write touch it, and never at the same time.
	onDisk map[uint32]map[uint16][]byte
}

// NewCueStore creates a cue store that persists to the given directory.
func NewCueStore(dir string) *CueStore {
	os.MkdirAll(dir, 0755)
	cs := &CueStore{
		dir:      dir,
		data:     make(map[uint32][]CuePoint),
		rawBlobs: make(map[uint32]map[uint16][]byte),
		dirty:    make(map[uint32]bool),
		onDisk:   make(map[uint32]map[uint16][]byte),
	}
	cs.loadAll()
	cs.saver = saver.New("cuestore", saver.Interval, cs.write)
	return cs
}

// ParseCueBlob decodes a cue point blob from the CDJ. Handles both the
// legacy 76-byte format (CDJ-saved memory cues, colour at 0x34) and the
// 124-byte NXS2 format (rekordbox-style, colour palette idx at 0x4e).
func ParseCueBlob(blob []byte, trackID uint32) (*CuePoint, error) {
	if len(blob) < 76 {
		return nil, fmt.Errorf("cue blob too short: %d bytes", len(blob))
	}

	cue := &CuePoint{
		Number: binary.LittleEndian.Uint16(blob[0x04:]),
		Type:   binary.LittleEndian.Uint16(blob[0x06:]),
		TimeMs: binary.LittleEndian.Uint32(blob[0x0c:]),
		Status: binary.LittleEndian.Uint32(blob[0x18:]),
		LoopMs: int32(binary.LittleEndian.Uint32(blob[0x20:])),
	}
	// A 124-byte record keeps a loop's end at 0x10 (OutMsec, link-spec
	// HOT_CUE_BANK_ORACLE); the 76-byte form keeps it at 0x20.
	if out := binary.LittleEndian.Uint32(blob[0x10:]); len(blob) >= 124 && out != 0xffffffff && out != 0 {
		cue.LoopMs = int32(out)
	}
	cue.ColorID = cueColorFromBlob(blob)

	dlog.Debugf("cuestore: parsed cue #%d type=%d time=%dms loop=%d color_id=%d (0x%x) track=%d",
		cue.Number, cue.Type, cue.TimeMs, cue.LoopMs, cue.ColorID, cue.ColorID, trackID)
	return cue, nil
}

// NextFreeMemoryNumber returns the lowest unused cue number >= 9 for a
// track — the internal numbering for memory cues (hot cues are 1-8). The
// wire convention differs: decks mark memory cues with 0 in the blob's
// number field, so deck saves are remapped on ingest (see the 0x2705
// handler) and mapped back to 0 when serving (see MarshalCueBlob).
func (cs *CueStore) NextFreeMemoryNumber(trackID uint32) uint16 {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	used := make(map[uint16]bool)
	for _, c := range cs.data[trackID] {
		used[c.Number] = true
	}
	n := uint16(9)
	for used[n] {
		n++
	}
	return n
}

// MarshalCueBlob builds the cue blob served for a CuePoint — the inverse
// of ParseCueBlob, used for cues that have no raw deck blob (API-created
// or imported).
func MarshalCueBlob(cue *CuePoint) []byte {
	// Build a 124-byte NXS2 cue blob matching the layout rekordbox sends
	// over dbserver (all 16 distinct picker colours covered).
	//
	//   0x00-0x03: 7c000000  — LE uint32 length = 124
	//   0x04-0x05: cue number (LE u16)
	//   0x06-0x07: type (LE u16: 1=cue, 2=loop)
	//   0x08-0x09: 0000
	//   0x0a-0x0b: e803     — constant 1000
	//   0x0c-0x0f: time ms (LE u32, InMsec)
	//   0x10-0x13: loop end ms when type==2 (OutMsec), else ffffffff
	//   0x14-0x1f: zeros
	//   0x20-0x23: ffffffff — constant
	//   0x24-0x33: zeros (the MPEG frame/offset fields)
	//   0x34-0x37: 42000000 — the options area's length, 66 (link-spec
	//                          HOT_CUE_BANK_ORACLE); the colour is at 0x4e-0x51
	//   0x38-0x49: zeros
	//   0x4a-0x4d: 2c000000 — constant (44, purpose unknown)
	//   0x4e:      palette index (1 byte, copy of cue.ColorID)
	//   0x4f-0x51: RGB (3 bytes — what the CDJ actually paints)
	//   0x52-0x7b: zeros
	blob := make([]byte, 124)
	binary.LittleEndian.PutUint32(blob[0x00:], 124)
	// Wire convention: the number field carries the hot-cue pad (1-8);
	// memory cues (our internal 9+) are marked 0, which is how decks and
	// rekordbox distinguish them. Without this, synthesized memory cues
	// went out as nonexistent hot pads 9+.
	wireNum := cue.Number
	if wireNum >= 9 {
		wireNum = 0
	}
	binary.LittleEndian.PutUint16(blob[0x04:], wireNum)
	binary.LittleEndian.PutUint16(blob[0x06:], cue.Type)
	binary.LittleEndian.PutUint16(blob[0x0a:], 1000)
	binary.LittleEndian.PutUint32(blob[0x0c:], cue.TimeMs)
	// A loop's end goes in OutMsec at 0x10, as rekordbox serves it: with it
	// at 0x20 (the 76-byte form's place) a deck decoding the track as MP3
	// gave up loading it at the hot loop (CDJ-2000NXS2; a FLAC still loaded).
	binary.LittleEndian.PutUint32(blob[0x10:], 0xffffffff)
	if cue.LoopMs >= 0 {
		binary.LittleEndian.PutUint32(blob[0x10:], uint32(cue.LoopMs))
	}
	binary.LittleEndian.PutUint32(blob[0x20:], 0xffffffff)
	blob[0x34] = 0x42
	blob[0x4a] = 0x2c
	// Pre-normalise picker-encoded IDs by stripping the 0x30 offset so
	// 0x42 → palette idx 0x12 → green RGB. The raw palette tops out at 0x3e,
	// so anything ≥ 0x3f must be picker-encoded (0x30 + index). This boundary
	// must match the web UI's CUE_COLORS Proxy (n > 0x3e) so the swatch and
	// the on-deck colour agree at the 0x3f edge.
	idx := cue.ColorID
	if idx > 0x3e {
		idx -= 0x30
	}
	if idx > 0xff {
		idx = 0
	}
	blob[0x4e] = byte(idx)
	r, g, b := cueColorRGB(idx)
	blob[0x4f] = r
	blob[0x50] = g
	blob[0x51] = b
	return blob
}

// cueColorRGB returns the RGB triplet the CDJ should paint for a given
// hot-cue palette index. The full table mirrors the JS CUE_COLORS palette
// in api/web/index.html — the deck literally renders whatever 3 bytes we
// hand it at 0x4f-0x51, so the only requirement is that web UI swatch and
// on-deck colour stay consistent.
func cueColorRGB(idx uint32) (r, g, b byte) {
	if c, ok := analysis.CueColorPalette[idx]; ok {
		return c[0], c[1], c[2]
	}
	// Unknown index — fall back to the unset colour (green, matching index 0).
	return 0x28, 0xe2, 0x14
}

// cueColorFromBlob extracts the palette color_id from a cue blob. 124-byte NXS2
// blobs carry the picker index at 0x4e; when that index is 0 the real colour is
// in the RGB bytes (0x4f-0x51) — the CDJ encodes its default hot-cue green that
// way, and taking idx 0 alone would paint it Pioneer orange (palette 0x00) — so
// recover the nearest palette id from the RGB. 76-byte blobs store the raw index
// directly at 0x34. Callers must ensure len(blob) >= 76.
func cueColorFromBlob(blob []byte) uint32 {
	if len(blob) >= 124 && blob[0x34] == 0x42 {
		if idx := uint32(blob[0x4e]); idx != 0 {
			return idx
		}
		if r, g, b := blob[0x4f], blob[0x50], blob[0x51]; r|g|b != 0 {
			return analysis.NearestCueColorID(r, g, b)
		}
		return 0
	}
	return binary.LittleEndian.Uint32(blob[0x34:])
}

// SaveCue stores or updates a cue point for a track, including the raw blob
// (nil for a cue that didn't come from a deck: it's synthesized on serving).
func (cs *CueStore) SaveCue(trackID uint32, cue *CuePoint, rawBlob []byte) {
	cs.saveCues(trackID, []CuePoint{*cue}, [][]byte{rawBlob})
}

// SaveCues stores several cue points for a track as one change, as an import
// does: each replaces the track's cue with its number or is added. They carry
// no deck blob, so a replaced cue's blob goes and the cue is synthesized on
// serving.
func (cs *CueStore) SaveCues(trackID uint32, cues []CuePoint) {
	cs.saveCues(trackID, cues, nil)
}

// saveCues stores cues for a track as one change; blobs[i], when blobs is
// given, is the deck's blob for cues[i].
func (cs *CueStore) saveCues(trackID uint32, cues []CuePoint, blobs [][]byte) {
	if len(cues) == 0 {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()

	list := slices.Clone(cs.data[trackID])
	raw := maps.Clone(cs.rawBlobs[trackID])
	if raw == nil {
		raw = make(map[uint16][]byte)
	}
	for i, cue := range cues {
		// Replace the cue with the same number, or append.
		if j := slices.IndexFunc(list, func(c CuePoint) bool { return c.Number == cue.Number }); j >= 0 {
			list[j] = cue
		} else {
			list = append(list, cue)
		}
		if blobs != nil && len(blobs[i]) > 0 {
			raw[cue.Number] = bytes.Clone(blobs[i])
		} else {
			delete(raw, cue.Number)
		}
	}
	cs.data[trackID] = list
	cs.rawBlobs[trackID] = raw
	cs.changedLocked(trackID)
}

// GetCues returns all cue points for a track. The slice is shared: read it,
// don't edit it.
func (cs *CueStore) GetCues(trackID uint32) []CuePoint {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return slices.Clip(cs.data[trackID])
}

// AllCues returns a snapshot of cues for every track that has any. Used by
// the web UI to batch-fetch cues for library waveform overlays in a single
// request instead of per-track.
func (cs *CueStore) AllCues() map[uint32][]CuePoint {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	out := make(map[uint32][]CuePoint, len(cs.data))
	for id, cues := range cs.data {
		if len(cues) == 0 {
			continue
		}
		// Defensive copy so callers can't mutate our backing store.
		out[id] = slices.Clone(cues)
	}
	return out
}

// GetCombinedBlob returns all cue blobs concatenated for serving via 0x2b04.
// Uses raw blobs from CDJ saves when available, otherwise synthesizes from CuePoint data.
func (cs *CueStore) GetCombinedBlob(trackID uint32) []byte {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	cues := cs.data[trackID]
	if len(cues) == 0 {
		return nil
	}

	blobs := cs.rawBlobs[trackID]
	var combined []byte
	for i := range cues {
		if raw, ok := blobs[cues[i].Number]; ok && len(raw) > 0 {
			combined = append(combined, raw...)
		} else {
			// Synthesize blob for API-created cues.
			combined = append(combined, MarshalCueBlob(&cues[i])...)
		}
	}
	return combined
}

// DeleteCue removes a cue point by number.
func (cs *CueStore) DeleteCue(trackID uint32, cueNumber uint16) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cues := cs.data[trackID]
	i := slices.IndexFunc(cues, func(c CuePoint) bool { return c.Number == cueNumber })
	if i < 0 {
		return
	}
	if len(cues) == 1 {
		delete(cs.data, trackID)
		delete(cs.rawBlobs, trackID)
	} else {
		cs.data[trackID] = slices.Delete(slices.Clone(cues), i, i+1)
		blobs := maps.Clone(cs.rawBlobs[trackID])
		delete(blobs, cueNumber)
		cs.rawBlobs[trackID] = blobs
	}
	cs.changedLocked(trackID)
}

// DeleteAllForTrack removes every cue (and raw blob) for trackID; the
// background writer removes its files. Used when the track is being deleted
// from the library so we don't leak per-track JSON + .bin files in cs.dir.
func (cs *CueStore) DeleteAllForTrack(trackID uint32) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, ok := cs.data[trackID]; !ok && cs.rawBlobs[trackID] == nil {
		return
	}
	delete(cs.data, trackID)
	delete(cs.rawBlobs, trackID)
	cs.changedLocked(trackID)
}

// changedLocked records a change to a track's cues for the background
// writer. Callers hold cs.mu.
func (cs *CueStore) changedLocked(trackID uint32) {
	cs.dirty[trackID] = true
	cs.saver.Mark()
}

// Flush writes the changed tracks' cue files now.
func (cs *CueStore) Flush() error {
	return cs.saver.Flush()
}

// Close stops the background writer and writes any unsaved changes, on
// shutdown.
func (cs *CueStore) Close() error {
	return cs.saver.Close()
}

// write brings the files of every track changed since the last write in
// line with its cues, from the state taken under the lock. A track whose
// files fail to write stays dirty for the next write.
func (cs *CueStore) write() error {
	type trackCues struct {
		cues  []CuePoint
		blobs map[uint16][]byte
	}
	cs.mu.Lock()
	snap := make(map[uint32]trackCues, len(cs.dirty))
	for id := range cs.dirty {
		snap[id] = trackCues{cs.data[id], cs.rawBlobs[id]}
	}
	clear(cs.dirty)
	cs.mu.Unlock()

	var errs []error
	for id, tc := range snap {
		if err := cs.writeTrack(id, tc.cues, tc.blobs); err != nil {
			errs = append(errs, err)
			cs.mu.Lock()
			cs.dirty[id] = true
			cs.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}

// writeTrack writes one track's cues_<id>.json and the deck blobs that
// changed, and removes the files of cues that are gone. No cues: no files.
func (cs *CueStore) writeTrack(trackID uint32, cues []CuePoint, blobs map[uint16][]byte) error {
	jsonPath := filepath.Join(cs.dir, fmt.Sprintf("cues_%d.json", trackID))
	if len(cues) == 0 {
		if err := os.Remove(jsonPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		data, err := json.Marshal(cues)
		if err != nil {
			return err
		}
		if err := fsutil.WriteFile(jsonPath, data, 0644); err != nil {
			return err
		}
	}

	onDisk := cs.onDisk[trackID]
	if onDisk == nil {
		onDisk = make(map[uint16][]byte)
		cs.onDisk[trackID] = onDisk
	}
	for num, raw := range blobs {
		if len(raw) == 0 || bytes.Equal(onDisk[num], raw) {
			continue
		}
		if err := fsutil.WriteFile(cs.blobPath(trackID, num), raw, 0644); err != nil {
			return err
		}
		onDisk[num] = raw
	}
	for num := range onDisk {
		if len(blobs[num]) > 0 {
			continue
		}
		if err := os.Remove(cs.blobPath(trackID, num)); err != nil && !os.IsNotExist(err) {
			return err
		}
		delete(onDisk, num)
	}
	if len(onDisk) == 0 {
		delete(cs.onDisk, trackID)
	}
	return nil
}

func (cs *CueStore) blobPath(trackID uint32, num uint16) string {
	return filepath.Join(cs.dir, fmt.Sprintf("cue_%d_%d.bin", trackID, num))
}

func (cs *CueStore) loadAll() {
	entries, err := os.ReadDir(cs.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Load JSON cue metadata.
		var trackID uint32
		if _, err := fmt.Sscanf(e.Name(), "cues_%d.json", &trackID); err == nil {
			data, err := os.ReadFile(filepath.Join(cs.dir, e.Name()))
			if err != nil {
				continue
			}
			var cues []CuePoint
			if err := json.Unmarshal(data, &cues); err != nil {
				dlog.Debugf("cuestore: parse %s: %v", e.Name(), err)
				continue
			}
			cs.data[trackID] = cues
			dlog.Debugf("cuestore: loaded %d cues for track %d", len(cues), trackID)
		}
		// Load raw blob files.
		var bTrackID uint32
		var bCueNum uint16
		if _, err := fmt.Sscanf(e.Name(), "cue_%d_%d.bin", &bTrackID, &bCueNum); err == nil {
			data, err := os.ReadFile(filepath.Join(cs.dir, e.Name()))
			if err != nil {
				continue
			}
			if cs.onDisk[bTrackID] == nil {
				cs.onDisk[bTrackID] = make(map[uint16][]byte)
			}
			cs.onDisk[bTrackID][bCueNum] = data
		}
	}

	// Pair each track's blobs with its cues. A blob without a cue is left
	// over from a deleted cue (older versions kept the file); it stays in
	// onDisk only, so the track's next write removes it.
	for trackID, files := range cs.onDisk {
		for num, raw := range files {
			if !slices.ContainsFunc(cs.data[trackID], func(c CuePoint) bool { return c.Number == num }) {
				continue
			}
			if cs.rawBlobs[trackID] == nil {
				cs.rawBlobs[trackID] = make(map[uint16][]byte)
			}
			cs.rawBlobs[trackID][num] = raw
		}
	}

	// Re-derive the colour of CDJ-saved cues from their raw blob. Cues stored
	// before the RGB-colour recovery kept whatever color_id was persisted (a
	// default green came through as 0 → orange); re-reading the blob now applies
	// the fix on load. Web-UI/synthesized cues have no blob (or a stub), so they
	// keep their stored colour.
	for trackID, cues := range cs.data {
		blobs := cs.rawBlobs[trackID]
		for i := range cues {
			if raw := blobs[cues[i].Number]; len(raw) >= 76 {
				cues[i].ColorID = cueColorFromBlob(raw)
			}
		}
	}
}
