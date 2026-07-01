// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"vynull/internal/fsutil"
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

// CueStore persists cue points per track to disk.
type CueStore struct {
	mu       sync.RWMutex
	dir      string
	data     map[uint32][]CuePoint        // trackID → parsed cues
	rawBlobs map[uint32]map[uint16][]byte // trackID → cueNumber → raw blob
}

// NewCueStore creates a cue store that persists to the given directory.
func NewCueStore(dir string) *CueStore {
	os.MkdirAll(dir, 0755)
	cs := &CueStore{
		dir:      dir,
		data:     make(map[uint32][]CuePoint),
		rawBlobs: make(map[uint32]map[uint16][]byte),
	}
	cs.loadAll()
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
	// 124-byte blobs put the picker palette index at byte 0x4e (with RGB
	// at 0x4f-0x51), and reserve 0x34 as a fixed 0x42 marker. 76-byte
	// blobs put the raw palette index directly at 0x34.
	if len(blob) >= 124 && blob[0x34] == 0x42 {
		cue.ColorID = uint32(blob[0x4e])
	} else {
		cue.ColorID = binary.LittleEndian.Uint32(blob[0x34:])
	}

	log.Printf("cuestore: parsed cue #%d type=%d time=%dms loop=%d color_id=%d (0x%x) track=%d",
		cue.Number, cue.Type, cue.TimeMs, cue.LoopMs, cue.ColorID, cue.ColorID, trackID)
	return cue, nil
}

// MarshalCueBlob builds a 76-byte cue blob from a CuePoint.
// This is the inverse of ParseCueBlob, used for API-created cues that
// don't have a raw blob from the CDJ.
//
// Wire format offsets (little-endian):
//
//	0x00-0x03: header/magic (zeroed for synthesized blobs)
//	0x04-0x05: cue number (uint16)
//	0x06-0x07: type (uint16, 1=cue 2=loop)
//	0x08-0x0b: (unknown)
//	0x0c-0x0f: time_ms (uint32)
//	0x10-0x17: (unknown)
//	0x18-0x1b: status (uint32, 1=active)
//	0x1c-0x1f: (unknown)
//	0x20-0x23: loop_ms (int32, -1 if not loop)
//	0x24-0x33: (unknown)
//	0x34-0x37: color_id (uint32)
//	0x38-0x4b: (unknown/padding)
func MarshalCueBlob(cue *CuePoint) []byte {
	// Build a 124-byte NXS2 cue blob matching the exact layout rekordbox
	// sends over dbserver (verified against a packet capture of all 16
	// distinct picker colours, byte positions confirmed by diffing).
	//
	//   0x00-0x03: 7c000000  — LE uint32 length = 124
	//   0x04-0x05: cue number (LE u16)
	//   0x06-0x07: type (LE u16: 1=cue, 2=loop)
	//   0x08-0x09: 0000
	//   0x0a-0x0b: e803     — constant 1000
	//   0x0c-0x0f: time ms (LE u32)
	//   0x10-0x13: ffffffff — constant
	//   0x14-0x1f: zeros
	//   0x20-0x23: ffffffff (or loop_ms LE u32 when type==2)
	//   0x24-0x33: zeros
	//   0x34-0x37: 42000000 — constant "NXS2 picker-coloured cue" marker
	//                          (the actual colour is at 0x4e-0x51, not here)
	//   0x38-0x49: zeros
	//   0x4a-0x4d: 2c000000 — constant (44, purpose unknown)
	//   0x4e:      palette index (1 byte, copy of cue.ColorID)
	//   0x4f-0x51: RGB (3 bytes — what the CDJ actually paints)
	//   0x52-0x7b: zeros
	blob := make([]byte, 124)
	binary.LittleEndian.PutUint32(blob[0x00:], 124)
	binary.LittleEndian.PutUint16(blob[0x04:], cue.Number)
	binary.LittleEndian.PutUint16(blob[0x06:], cue.Type)
	binary.LittleEndian.PutUint16(blob[0x0a:], 1000)
	binary.LittleEndian.PutUint32(blob[0x0c:], cue.TimeMs)
	binary.LittleEndian.PutUint32(blob[0x10:], 0xffffffff)
	if cue.LoopMs < 0 {
		binary.LittleEndian.PutUint32(blob[0x20:], 0xffffffff)
	} else {
		binary.LittleEndian.PutUint32(blob[0x20:], uint32(cue.LoopMs))
	}
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
	if c, ok := cueColorPalette[idx]; ok {
		return c[0], c[1], c[2]
	}
	// Default "no colour set" — Pioneer orange.
	return 0xff, 0x6a, 0x00
}

var cueColorPalette = map[uint32][3]byte{
	0x00: {0xff, 0x6a, 0x00},
	0x01: {0x30, 0x5a, 0xff}, 0x02: {0x50, 0x73, 0xff}, 0x03: {0x50, 0x8c, 0xff}, 0x04: {0x50, 0xa0, 0xff},
	0x05: {0x50, 0xb4, 0xff}, 0x06: {0x50, 0xb0, 0xf2}, 0x07: {0x50, 0xae, 0xe8}, 0x08: {0x45, 0xac, 0xdb},
	0x09: {0x00, 0xe0, 0xff}, 0x0a: {0x19, 0xda, 0xf0}, 0x0b: {0x32, 0xd2, 0xe6}, 0x0c: {0x21, 0xb4, 0xb9},
	0x0d: {0x20, 0xaa, 0xa0}, 0x0e: {0x1f, 0xa3, 0x92}, 0x0f: {0x19, 0xa0, 0x8c}, 0x10: {0x14, 0xa5, 0x84},
	0x11: {0x14, 0xaa, 0x7d}, 0x12: {0x10, 0xb1, 0x76}, 0x13: {0x30, 0xd2, 0x6e}, 0x14: {0x37, 0xde, 0x5a},
	0x15: {0x3c, 0xeb, 0x50}, 0x16: {0x28, 0xe2, 0x14}, 0x17: {0x7d, 0xc1, 0x3d}, 0x18: {0x8c, 0xc8, 0x32},
	0x19: {0x9b, 0xd7, 0x23}, 0x1a: {0xa5, 0xe1, 0x16}, 0x1b: {0xa5, 0xdc, 0x0a}, 0x1c: {0xaa, 0xd2, 0x08},
	0x1d: {0xb4, 0xc8, 0x05}, 0x1e: {0xb4, 0xbe, 0x04}, 0x1f: {0xba, 0xb4, 0x04}, 0x20: {0xc3, 0xaf, 0x04},
	0x21: {0xe1, 0xaa, 0x00}, 0x22: {0xff, 0xa0, 0x00}, 0x23: {0xff, 0x96, 0x00}, 0x24: {0xff, 0x8c, 0x00},
	0x25: {0xff, 0x75, 0x00}, 0x26: {0xe0, 0x64, 0x1b}, 0x27: {0xe0, 0x46, 0x1e}, 0x28: {0xe0, 0x30, 0x1e},
	0x29: {0xe0, 0x28, 0x23}, 0x2a: {0xe6, 0x28, 0x28}, 0x2b: {0xff, 0x37, 0x6f}, 0x2c: {0xff, 0x2d, 0x6f},
	0x2d: {0xff, 0x12, 0x7b}, 0x2e: {0xf5, 0x1e, 0x8c}, 0x2f: {0xeb, 0x2d, 0xa0}, 0x30: {0xe6, 0x37, 0xb4},
	0x31: {0xde, 0x44, 0xcf}, 0x32: {0xde, 0x44, 0x8d}, 0x33: {0xe6, 0x30, 0xb4}, 0x34: {0xe6, 0x19, 0xdc},
	0x35: {0xe6, 0x00, 0xff}, 0x36: {0xdc, 0x00, 0xff}, 0x37: {0xcc, 0x00, 0xff}, 0x38: {0xb4, 0x32, 0xff},
	0x39: {0xb9, 0x3c, 0xff}, 0x3a: {0xc5, 0x42, 0xff}, 0x3b: {0xaa, 0x5a, 0xff}, 0x3c: {0xaa, 0x72, 0xff},
	0x3d: {0x82, 0x72, 0xff}, 0x3e: {0x64, 0x73, 0xff},
}

// SaveCue stores or updates a cue point for a track, including the raw blob.
func (cs *CueStore) SaveCue(trackID uint32, cue *CuePoint, rawBlob []byte) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cues := cs.data[trackID]

	// Replace existing cue with same number, or append.
	found := false
	for i, c := range cues {
		if c.Number == cue.Number {
			cues[i] = *cue
			found = true
			break
		}
	}
	if !found {
		cues = append(cues, *cue)
	}
	cs.data[trackID] = cues

	// Store raw blob keyed by cue number.
	if cs.rawBlobs[trackID] == nil {
		cs.rawBlobs[trackID] = make(map[uint16][]byte)
	}
	cs.rawBlobs[trackID][cue.Number] = append([]byte(nil), rawBlob...)

	cs.saveToDisk(trackID)
	cs.saveRawToDisk(trackID)
}

// GetCues returns all cue points for a track.
func (cs *CueStore) GetCues(trackID uint32) []CuePoint {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.data[trackID]
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
		cp := make([]CuePoint, len(cues))
		copy(cp, cues)
		out[id] = cp
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
	for i, c := range cues {
		if c.Number == cueNumber {
			cs.data[trackID] = append(cues[:i], cues[i+1:]...)
			delete(cs.rawBlobs[trackID], cueNumber)
			cs.saveToDisk(trackID)
			cs.saveRawToDisk(trackID)
			return
		}
	}
}

// DeleteAllForTrack removes every cue (and raw blob) for trackID, both
// in memory and on disk. Used when the track is being deleted from the
// library so we don't leak per-track JSON + .bin files in cs.dir.
func (cs *CueStore) DeleteAllForTrack(trackID uint32) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, ok := cs.data[trackID]; !ok && cs.rawBlobs[trackID] == nil {
		return
	}
	// Disk: cues_<id>.json + cue_<id>_<n>.bin
	jsonPath := filepath.Join(cs.dir, fmt.Sprintf("cues_%d.json", trackID))
	if err := os.Remove(jsonPath); err != nil && !os.IsNotExist(err) {
		log.Printf("cuestore: remove %s: %v", jsonPath, err)
	}
	for num := range cs.rawBlobs[trackID] {
		binPath := filepath.Join(cs.dir, fmt.Sprintf("cue_%d_%d.bin", trackID, num))
		if err := os.Remove(binPath); err != nil && !os.IsNotExist(err) {
			log.Printf("cuestore: remove %s: %v", binPath, err)
		}
	}
	delete(cs.data, trackID)
	delete(cs.rawBlobs, trackID)
}

func (cs *CueStore) saveRawToDisk(trackID uint32) {
	blobs := cs.rawBlobs[trackID]
	if len(blobs) == 0 {
		return
	}
	// Save each raw blob as a separate binary file.
	for num, raw := range blobs {
		path := filepath.Join(cs.dir, fmt.Sprintf("cue_%d_%d.bin", trackID, num))
		if err := fsutil.WriteFile(path, raw, 0644); err != nil {
			log.Printf("cuestore: write raw error: %v", err)
		}
	}
}

func (cs *CueStore) saveToDisk(trackID uint32) {
	path := filepath.Join(cs.dir, fmt.Sprintf("cues_%d.json", trackID))
	data, err := json.MarshalIndent(cs.data[trackID], "", "  ")
	if err != nil {
		log.Printf("cuestore: marshal error: %v", err)
		return
	}
	if err := fsutil.WriteFile(path, data, 0644); err != nil {
		log.Printf("cuestore: write error: %v", err)
	}
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
				log.Printf("cuestore: parse %s: %v", e.Name(), err)
				continue
			}
			cs.data[trackID] = cues
			log.Printf("cuestore: loaded %d cues for track %d", len(cues), trackID)
		}
		// Load raw blob files.
		var bTrackID uint32
		var bCueNum uint16
		if _, err := fmt.Sscanf(e.Name(), "cue_%d_%d.bin", &bTrackID, &bCueNum); err == nil {
			data, err := os.ReadFile(filepath.Join(cs.dir, e.Name()))
			if err != nil {
				continue
			}
			if cs.rawBlobs[bTrackID] == nil {
				cs.rawBlobs[bTrackID] = make(map[uint16][]byte)
			}
			cs.rawBlobs[bTrackID][bCueNum] = data
		}
	}
}

// MarshalCueResponse builds the cue point response for 0x2104.
// Returns menu items for each cue point the CDJ can display.
func MarshalCueResponse(cues []CuePoint) []byte {
	// TODO: build PCOB/PCO2 format response
	return nil
}
