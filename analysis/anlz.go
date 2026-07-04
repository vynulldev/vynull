// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// ANLZ section tag FourCCs (big-endian).
const (
	tagPPTH = "PPTH" // path
	tagPVBR = "PVBR" // variable-bitrate seek index
	tagPQTZ = "PQTZ" // beat grid
	tagPWAV = "PWAV" // waveform preview (mono)
	tagPWV2 = "PWV2" // tiny waveform preview (mono)
	tagPCOB = "PCOB" // cue list (memory / hot cues)
	tagPCO2 = "PCO2" // cue list v2 (with comment + color)
	tagPSSI = "PSSI" // song structure info (phrases)
	tagPQT2 = "PQT2" // extended beat grid
	tagPVB2 = "PVB2" // extended VBR seek index
	tagPWV3 = "PWV3" // mono scroll waveform (.EXT, used by the CDJ)
	tagPWV4 = "PWV4" // color waveform preview (.EXT)
	tagPWV5 = "PWV5" // color waveform detail/scroll (.EXT)
	tagPWV6 = "PWV6" // 3-band waveform preview (.2EX, CDJ-3000)
	tagPWV7 = "PWV7" // 3-band waveform detail/scroll (.2EX, CDJ-3000)
	tagPWVC = "PWVC" // 3-band waveform colour metadata (.2EX, CDJ-3000)
)

// usbanlzFolder reproduces rekordbox's algorithm for deriving the
// /PIONEER/USBANLZ/Pxxx/xxxxxxxx/ folder name from a track's file_path.
// The CDJ ignores the PDB's analyze_path field and re-derives the
// folder location from file_path using this exact algorithm — so our ANLZ
// files MUST be placed at the derived folder for the deck to find them.
//
// The algorithm: hash the UTF-16 code points of the path, modulo 0x30D43 →
// `inner`; pack 7 specific bits of `inner` into `p`. Matched against four
// known file_path → folder mappings until it reproduced them exactly.
func usbanlzFolder(filePath string) (p, inner uint32) {
	var h uint32 = 0
	for _, ch := range filePath {
		h = h*0x34F5501D + uint32(ch)*0x93B6
	}
	inner = h % 0x30D43
	p = (inner >> 2) & 0x4000
	p |= inner & 0x2000
	p >>= 3
	p |= inner & 0x200
	p >>= 1
	p |= inner & 0xC0
	p >>= 3
	p |= inner & 0x4
	p >>= 1
	p |= inner & 0x1
	return p, inner
}

// WriteANLZFiles generates .DAT and .EXT analysis files for a track.
// Returns the analyze_path relative to USB root (e.g., /PIONEER/USBANLZ/P05C/00011EF8/ANLZ0000.DAT).
func WriteANLZFiles(outDir string, trackID uint32, trackPath string, r *Result) (string, error) {
	// The deck derives this folder from file_path; we MUST match its scheme.
	p, inner := usbanlzFolder(trackPath)
	subDir := fmt.Sprintf("P%03X/%08X", p, inner)
	anlzDir := filepath.Join(outDir, "PIONEER", "USBANLZ", subDir)
	if err := os.MkdirAll(anlzDir, 0o755); err != nil {
		return "", err
	}

	datPath := filepath.Join(anlzDir, "ANLZ0000.DAT")
	extPath := filepath.Join(anlzDir, "ANLZ0000.EXT")

	// .DAT file: PMAI header + PPTH + PQTZ + PWAV
	if err := writeDATFile(datPath, trackPath, r); err != nil {
		return "", fmt.Errorf("write DAT: %w", err)
	}

	// .EXT file: PMAI header + PPTH + PWV3 + PWV4 + PWV5
	if err := writeEXTFile(extPath, trackPath, r); err != nil {
		return "", fmt.Errorf("write EXT: %w", err)
	}

	// .2EX file: CDJ-3000 3-band waveforms (PWV7 + PWV6 + PWVC). Only written
	// when 3-band data is present; the deck derives this path from the .DAT the
	// same way it does the .EXT, so no PDB change is needed.
	if len(r.WaveDetail3Band) >= 3 || len(r.WavePreview3Band) >= 3 {
		ex2Path := filepath.Join(anlzDir, "ANLZ0000.2EX")
		if err := write2EXFile(ex2Path, trackPath, r); err != nil {
			return "", fmt.Errorf("write 2EX: %w", err)
		}
	}

	// Return relative path (what goes in PDB analyze_path field).
	return "/" + filepath.Join("PIONEER", "USBANLZ", subDir, "ANLZ0000.DAT"), nil
}

// write2EXFile writes the CDJ-3000 3-band waveform file. Section order in
// the .2EX: PPTH, PWV7 (detail), PWV6 (preview), PWVC (colour). The
// 3-band sections are built with WrapANLZ (byte-verified headers) and emitted
// without its 4-byte dbserver length prefix. PWVC is only present for imported
// tracks (we don't synthesize colour metadata).
func write2EXFile(path, trackPath string, r *Result) error {
	var sections []byte
	sections = append(sections, makePathSection(trackPath)...)
	if len(r.WaveDetail3Band) >= 3 {
		sections = append(sections, WrapANLZ(tagPWV7, 3, r.WaveDetail3Band)[4:]...)
	}
	if len(r.WavePreview3Band) >= 3 {
		sections = append(sections, WrapANLZ(tagPWV6, 3, r.WavePreview3Band)[4:]...)
	}
	if len(r.Wave3BandColor) == 6 {
		sections = append(sections, WrapANLZ(tagPWVC, 6, r.Wave3BandColor)[4:]...)
	}
	return writeANLZFile(path, sections)
}

func writeDATFile(path, trackPath string, r *Result) error {
	var sections []byte

	// Section order in the .DAT:
	//   PPTH, PVBR, PQTZ, PWAV, PWV2, PCOB(hot), PCOB(memory)

	sections = append(sections, makePathSection(trackPath)...)

	// PVBR: VBR seek index. We don't have real seek data, but the section
	// is present in every export (even for FLAC/CBR) so the deck may
	// require its presence. 400 entries × u4 = 1600 bytes, all zero.
	sections = append(sections, makeEmptyVBRSection()...)

	if len(r.Beats) > 0 {
		sections = append(sections, makeBeatGridSection(r.BPM, r.Beats, r.DownbeatIndex)...)
	} else if r.BeatGrid != nil {
		sections = append(sections, makeBeatGridSectionFromBlob(r.BPM, r.BeatGrid)...)
	}

	pwav := r.WavePreviewANLZ
	if pwav == nil {
		pwav = r.WavePreview
	}
	if pwav != nil {
		sections = append(sections, makeWavePreviewSection(pwav)...)
	}

	if r.WaveTinyANLZ != nil {
		sections = append(sections, makeTinyPreviewSection(r.WaveTinyANLZ)...)
	}

	sections = append(sections, makeEmptyCueList(tagPCOB, 1)...)
	sections = append(sections, makeEmptyCueList(tagPCOB, 0)...)

	return writeANLZFile(path, sections)
}

func writeEXTFile(path, trackPath string, r *Result) error {
	var sections []byte

	// Section order in the .EXT:
	//   PPTH, PWV3, PCOB(hot), PCOB(memory), PCO2(hot), PCO2(memory), PWV5, PWV4
	// We don't yet write PQT2/PVB2/PSSI — those can appear but the deck
	// seems to skip them.

	sections = append(sections, makePathSection(trackPath)...)

	if r.WaveDetailMono != nil {
		sections = append(sections, makeMonoScrollSection(r.WaveDetailMono)...)
	}

	// Empty cue lists. Exports always write both list types (hot=1,
	// memory=0) in both PCOB (v1) and PCO2 (v2) flavours, even when the
	// track has no cues at all.
	sections = append(sections, makeEmptyCueList(tagPCOB, 1)...)
	sections = append(sections, makeEmptyCueList(tagPCOB, 0)...)
	sections = append(sections, makeEmptyCueListV2(tagPCO2, 1)...)
	sections = append(sections, makeEmptyCueListV2(tagPCO2, 0)...)

	// PQT2 (extended beat grid). GeneratePQT2 returns a complete ANLZ
	// section with a 4-byte LE length prefix used by the dbserver
	// transport; strip that prefix when emitting to a file.
	if r.BeatGridPQT2 != nil && len(r.BeatGridPQT2) > 4 {
		sections = append(sections, r.BeatGridPQT2[4:]...)
	}

	if r.WaveDetail != nil {
		sections = append(sections, makeColorScrollSection(r.WaveDetail)...)
	}
	if r.WaveColorPreview != nil {
		sections = append(sections, makeColorPreviewSection(r.WaveColorPreview)...)
	}

	// PVB2 (extended VBR seek index): this is emitted only for
	// some tracks (present for some mp3s, absent for others).
	// Our empty placeholder may interfere with parsing
	// for tracks the deck doesn't expect to have one. Omit until we can
	// determine the actual selection rule.

	// PSSI (song structure). Exports always include this section
	// with phrase data; an empty PSSI may signal "still analysing" to
	// the deck and gate visualisations like the coloured phrase bar
	// underneath the waveform.
	if len(r.SongStructure) > 0 {
		sections = append(sections, makeSSISection(r.SongStructure)...)
	} else {
		sections = append(sections, makeEmptySSI()...)
	}

	return writeANLZFile(path, sections)
}

func writeANLZFile(path string, sections []byte) error {
	// PMAI header: magic(4) + len_header(4) + len_file(4) + padding.
	headerLen := uint32(28) // PMAI header is 28 bytes
	fileLen := headerLen + uint32(len(sections))

	header := make([]byte, headerLen)
	copy(header[0:4], "PMAI")
	binary.BigEndian.PutUint32(header[4:], headerLen)
	binary.BigEndian.PutUint32(header[8:], fileLen)
	// bytes 12-27: version/flags
	binary.BigEndian.PutUint32(header[12:], 1)       // unknown, always 1
	binary.BigEndian.PutUint32(header[16:], 0x10000) // version? 00 01 00 00
	binary.BigEndian.PutUint32(header[20:], 0x10000) // version? 00 01 00 00

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(header); err != nil {
		return err
	}
	_, err = f.Write(sections)
	return err
}

// makeSection builds a tagged section: fourcc(4) + len_header(4) + len_tag(4) + body.
func makeSection(fourcc string, headerExtra, body []byte) []byte {
	lenHeader := uint32(12 + len(headerExtra))
	lenTag := lenHeader + uint32(len(body))
	buf := make([]byte, lenTag)
	copy(buf[0:4], fourcc)
	binary.BigEndian.PutUint32(buf[4:], lenHeader)
	binary.BigEndian.PutUint32(buf[8:], lenTag)
	copy(buf[12:], headerExtra)
	copy(buf[12+len(headerExtra):], body)
	return buf
}

// makePathSection creates a PPTH section with a UTF-16BE encoded path.
// rekordcrate's parser asserts `(path.len() + 1) * 2 == len_path`, i.e. the
// trailing UCS-2 NUL is included in both the bytes written and len_path.
func makePathSection(trackPath string) []byte {
	runes := []rune(trackPath)
	utf16Data := make([]byte, (len(runes)+1)*2) // +1 for NUL terminator
	for i, r := range runes {
		binary.BigEndian.PutUint16(utf16Data[i*2:], uint16(r))
	}

	extra := make([]byte, 4)
	binary.BigEndian.PutUint32(extra, uint32(len(utf16Data)))
	return makeSection(tagPPTH, extra, utf16Data)
}

// makeBeatGridSection creates a PQTZ section from actual beat positions.
// Beat grid entries: u2 beat_number, u2 tempo, u4 time_ms (all big-endian).
func makeBeatGridSection(bpm float64, beats []float64, downbeatIdx int) []byte {
	numBeats := len(beats)
	tempo := uint16(bpm * 100)
	entries := make([]byte, numBeats*8)
	for i := 0; i < numBeats; i++ {
		off := i * 8
		beatInBar := ((i - downbeatIdx) % 4)
		if beatInBar < 0 {
			beatInBar += 4
		}
		binary.BigEndian.PutUint16(entries[off:], uint16(beatInBar+1))
		binary.BigEndian.PutUint16(entries[off+2:], tempo)
		binary.BigEndian.PutUint32(entries[off+4:], uint32(beats[i]))
	}

	extra := make([]byte, 12)
	binary.BigEndian.PutUint32(extra[0:], 0)
	binary.BigEndian.PutUint32(extra[4:], 0x80000)
	binary.BigEndian.PutUint32(extra[8:], uint32(numBeats))
	return makeSection(tagPQTZ, extra, entries)
}

// makeBeatGridSectionFromBlob creates a PQTZ section from evenly-spaced beats (fallback).
func makeBeatGridSectionFromBlob(bpm float64, beatGridBlob []byte) []byte {
	msPerBeat := 60000.0 / bpm
	durationMs := float64(len(beatGridBlob)) / 2 * msPerBeat
	numBeats := int(durationMs / msPerBeat)

	tempo := uint16(bpm * 100)
	entries := make([]byte, numBeats*8)
	for i := 0; i < numBeats; i++ {
		off := i * 8
		binary.BigEndian.PutUint16(entries[off:], uint16((i%4)+1))
		binary.BigEndian.PutUint16(entries[off+2:], tempo)
		binary.BigEndian.PutUint32(entries[off+4:], uint32(float64(i)*msPerBeat))
	}

	extra := make([]byte, 12)
	binary.BigEndian.PutUint32(extra[0:], 0)
	binary.BigEndian.PutUint32(extra[4:], 0x80000)
	binary.BigEndian.PutUint32(extra[8:], uint32(numBeats))
	return makeSection(tagPQTZ, extra, entries)
}

// makeWavePreviewSection creates a PWAV section.
func makeWavePreviewSection(preview []byte) []byte {
	// Header extra: u4 len_data, u4 constant(0x10000).
	extra := make([]byte, 8)
	binary.BigEndian.PutUint32(extra[0:], uint32(len(preview)))
	binary.BigEndian.PutUint32(extra[4:], 0x10000)
	return makeSection(tagPWAV, extra, preview)
}

// makeTinyPreviewSection creates a PWV2 section (100-entry tiny waveform).
// Same wire format as PWAV.
func makeTinyPreviewSection(preview []byte) []byte {
	extra := make([]byte, 8)
	binary.BigEndian.PutUint32(extra[0:], uint32(len(preview)))
	binary.BigEndian.PutUint32(extra[4:], 0x10000)
	return makeSection(tagPWV2, extra, preview)
}

// makeEmptyVB2Section creates an empty PVB2 (extended VBR) section.
// The format uses len_header=32, len_tag=8032 (32 + 8000 byte body).
// Extra header has a u4 file_size embedded — we use 0 since this is a
// placeholder; the deck appears to treat it as advisory.
func makeEmptyVB2Section() []byte {
	extra := make([]byte, 20)
	// bytes 8-11: file size (u4) — leave 0 for placeholder
	binary.BigEndian.PutUint32(extra[12:], 0x00000190) // 400
	binary.BigEndian.PutUint32(extra[16:], 0x00000014) // 20
	body := make([]byte, 8000)
	return makeSection(tagPVB2, extra, body)
}

// GeneratePVB2 returns a placeholder PVB2 (extended VBR seek index) section
// wrapped with the 4-byte little-endian length prefix used by the dbserver
// ANLZ blob format (same layout ReadANLZSection returns). The dbserver
// serves an 8036-byte blob (LE len + 8032-byte PVB2 section) in reply to a
// 0x2c04 PVB2 request; withholding it makes the deck fall back to a raw
// 0x2805 tagged-section read, which — if unanswered — deadlocks the deck's
// dbserver channel (see the PVB2 case in dbserver/track.go). The seek index
// body is zeroed here (we don't yet compute real VBR offsets), which is
// enough for linear playback; ANLZ-backed tracks serve the real section.
func GeneratePVB2() []byte {
	section := makeEmptyVB2Section()
	blob := make([]byte, 4+len(section))
	binary.LittleEndian.PutUint32(blob, uint32(len(section)))
	copy(blob[4:], section)
	return blob
}

// makeEmptyVBRSection creates a PVBR (variable-bitrate seek index) section.
// The format always has len_header=16, len_tag=1620 (4 bytes extra + 1604
// bytes body = 401 × u4 seek offsets). We write all zeros
// since we don't compute a real seek table; the deck appears to require the
// section's presence even for FLAC/CBR.
func makeEmptyVBRSection() []byte {
	extra := make([]byte, 4)
	body := make([]byte, 1604)
	return makeSection(tagPVBR, extra, body)
}

// makeColorPreviewSection creates a PWV4 section.
func makeColorPreviewSection(data []byte) []byte {
	entrySize := 6
	numEntries := len(data) / entrySize
	// Header extra: u4 entry_size, u4 num_entries, u4 unknown.
	extra := make([]byte, 12)
	binary.BigEndian.PutUint32(extra[0:], uint32(entrySize))
	binary.BigEndian.PutUint32(extra[4:], uint32(numEntries))
	binary.BigEndian.PutUint32(extra[8:], 0x00000000) // PWV4 uses 0 here
	return makeSection(tagPWV4, extra, data)
}

// makeEmptyCueList creates an empty PCOB cue list section (v1 format).
// listType: 0 = memory cues, 1 = hot cues. Exports always emit both,
// using 0xffffffff as a sentinel for memory_count.
func makeEmptyCueList(fourcc string, listType uint32) []byte {
	extra := make([]byte, 12)
	binary.BigEndian.PutUint32(extra[0:], listType)
	binary.BigEndian.PutUint16(extra[4:], 0)          // unk
	binary.BigEndian.PutUint16(extra[6:], 0)          // lencues = 0
	binary.BigEndian.PutUint32(extra[8:], 0xffffffff) // memory_count sentinel
	return makeSection(fourcc, extra, nil)
}

// makeEmptyCueListV2 creates an empty PCO2 cue list section (v2/nxs2 format).
// Header is 20 bytes (8 bytes of extra): type, lencues(u16), unk(u16).
func makeEmptyCueListV2(fourcc string, listType uint32) []byte {
	extra := make([]byte, 8)
	binary.BigEndian.PutUint32(extra[0:], listType)
	binary.BigEndian.PutUint16(extra[4:], 0) // lencues = 0
	binary.BigEndian.PutUint16(extra[6:], 0) // unk
	return makeSection(fourcc, extra, nil)
}

// makeSSISection wraps a GeneratePSSI blob (which is `entry_size(4) +
// num_entries(2) + maskedRegion`) with the standard ANLZ section header.
// PSSI has len_header=32 = 12 (generic header) + 20 (entry_size +
// num_entries + 14 masked body header bytes). Body = num_entries × 24.
func makeSSISection(pssi []byte) []byte {
	if len(pssi) < 20 {
		return makeEmptySSI()
	}
	extra := pssi[:20]
	body := pssi[20:]
	return makeSection(tagPSSI, extra, body)
}

// makeEmptySSI creates an empty PSSI (song structure) section.
//
// Layout (len_header = 0x20):
//
//	u4 len_entry_bytes = 24
//	u2 len_entries     = 0
//	u2 mood            = 1 (mood_high — any small value keeps it unmasked)
//	6 bytes padding
//	u2 end_beat        = 0
//	2 bytes padding
//	u1 raw_bank        = 0
//	1 byte padding
//
// The `is_masked` check is `raw_mood > 20`; mood=1 stays raw so no XOR
// mask is needed. Total tag size = 32 bytes, no body.
func makeEmptySSI() []byte {
	extra := make([]byte, 20)
	binary.BigEndian.PutUint32(extra[0:], 24) // len_entry_bytes
	binary.BigEndian.PutUint16(extra[4:], 0)  // len_entries
	binary.BigEndian.PutUint16(extra[6:], 1)  // mood = 1 (unmasked)
	// bytes 8..13: zero padding
	// bytes 14..15: end_beat = 0
	// bytes 16..17: zero padding
	// byte 18:      raw_bank = 0
	// byte 19:      zero padding
	return makeSection(tagPSSI, extra, nil)
}

// makeMonoScrollSection creates a PWV3 section (mono scrolling waveform).
// 1 byte per entry, where each byte is `(brightness << 5) | (height & 0x1f)`.
// The extra header is u4 entry_size=1, u4 num_entries, u4 0x00960000
// (entries_per_sec=150 in the high half, no format flags).
func makeMonoScrollSection(data []byte) []byte {
	extra := make([]byte, 12)
	binary.BigEndian.PutUint32(extra[0:], 1)
	binary.BigEndian.PutUint32(extra[4:], uint32(len(data)))
	binary.BigEndian.PutUint32(extra[8:], 0x00960000)
	return makeSection(tagPWV3, extra, data)
}

// makeColorScrollSection creates a PWV5 section (detail/scrolling waveform).
func makeColorScrollSection(data []byte) []byte {
	entrySize := 2
	numEntries := len(data) / entrySize
	// Header extra: u4 entry_size, u4 num_entries, u4 unknown.
	extra := make([]byte, 12)
	binary.BigEndian.PutUint32(extra[0:], uint32(entrySize))
	binary.BigEndian.PutUint32(extra[4:], uint32(numEntries))
	binary.BigEndian.PutUint32(extra[8:], 0x960305) // entries_per_sec(150) + format flags
	return makeSection(tagPWV5, extra, data)
}

// ReadANLZSection reads a specific section from an ANLZ file.
// Returns the section including fourcc + header + data, prefixed with LE length.
func ReadANLZSection(filePath string, tag string) []byte {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 28 {
		return nil
	}
	// Skip PMAI header
	hdrLen := int(binary.BigEndian.Uint32(data[4:8]))
	pos := hdrLen
	for pos+12 <= len(data) {
		fourcc := string(data[pos : pos+4])
		secLen := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		if secLen <= 0 || pos+secLen > len(data) {
			break
		}
		if fourcc == tag {
			section := data[pos : pos+secLen]
			// Prefix with LE length (dbserver ANLZ blob format)
			blob := make([]byte, 4+len(section))
			binary.LittleEndian.PutUint32(blob, uint32(secLen))
			copy(blob[4:], section)
			return blob
		}
		pos += secLen
	}
	return nil
}
