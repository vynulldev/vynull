// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "encoding/binary"

// ParseANLZ reads a rekordbox ANLZ .DAT/.EXT/.2EX set and assembles a Result
// from the analysis rekordbox already computed — waveforms, beat grid, and
// song-structure phrases — so a backup import can reuse the exact rekordbox
// data instead of re-analysing the audio. bpm and durationSec come from the
// database (the ANLZ sections don't carry them reliably). twoEXPath may be ""
// (older libraries have no .2EX). Returns nil if no file yields any usable
// section.
//
// This is the inverse of the writeDATFile / writeEXTFile section layout:
// each section is fourcc(4) + len_header(4) + len_tag(4) + extra + body, and
// the per-section raw entry data lives at [len_header:len_tag].
func ParseANLZ(datPath, extPath, twoEXPath string, bpm float64, durationSec int) *Result {
	r := &Result{
		CacheVersion: effectiveCacheVersion(),
		BPM:          bpm,
		Duration:     uint16(durationSec),
	}
	got := false

	// .DAT — mono preview, tiny preview, beat grid.
	if body := anlzSectionBody(datPath, tagPWAV); body != nil {
		r.WavePreviewANLZ = body
		got = true
	}
	if body := anlzSectionBody(datPath, tagPWV2); body != nil {
		r.WaveTinyANLZ = body
		got = true
	}
	if body := anlzSectionBody(datPath, tagPQTZ); body != nil {
		if beats, downbeat, gridBPM := parseBeatGrid(body); len(beats) > 0 {
			r.Beats = beats
			r.DownbeatIndex = downbeat
			// rekordbox's stored grid tempo is authoritative; use it when the
			// DB gave us no BPM so the track still reports one.
			if r.BPM <= 0 {
				r.BPM = gridBPM
			}
			// Build the LE beat-grid blob the dbserver serves for 0x2204.
			// AnalyzeTrack does this via GenerateBeatGridFromBeats; the import
			// path must too, or imported tracks show no beat grid on the CDJ.
			br := &BeatResult{BPM: r.BPM, Beats: beats}
			if downbeat >= 0 && downbeat < len(beats) {
				br.Downbeat = beats[downbeat]
			}
			r.BeatGrid = GenerateBeatGridFromBeats(br)
			got = true
		}
	}

	// .EXT — colour/scroll waveforms, extended beat grid, phrases.
	if body := anlzSectionBody(extPath, tagPWV3); body != nil {
		r.WaveDetailMono = body
		got = true
	}
	if body := anlzSectionBody(extPath, tagPWV4); body != nil {
		r.WaveColorPreview = body
		got = true
	}
	if body := anlzSectionBody(extPath, tagPWV5); body != nil {
		r.WaveDetail = body
		got = true
	}
	// PQT2 is consumed downstream (writeEXTFile, dbserver 0x2c04) as a
	// complete section with the dbserver's 4-byte LE length prefix — which
	// is exactly what ReadANLZSection returns, so store it verbatim.
	if blob := ReadANLZSection(extPath, tagPQT2); blob != nil {
		r.BeatGridPQT2 = blob
		got = true
	}
	// PSSI: SongStructure is the section minus its generic 12-byte header
	// (i.e. extra+body), matching what makeSSISection re-wraps on export. We
	// keep the raw bytes (served verbatim to the CDJ via 0x2504) and also
	// deserialize them into Phrases so the web UI's phrase strip works for
	// imported tracks, not just ones we analyze ourselves.
	if pssi := anlzSectionAfterHeader(extPath, tagPSSI); pssi != nil {
		r.SongStructure = pssi
		if ph := ParsePSSI(pssi); len(ph) > 0 {
			r.Phrases = ph
		}
		got = true
	}

	// .2EX — CDJ-3000 3-band waveforms. We store the raw section bodies so a
	// later serving path can wrap them back into PWV6/PWV7/PWVC sections; the
	// CDJ never requests these, so nothing serves them yet.
	if twoEXPath != "" {
		if body := anlzSectionBody(twoEXPath, tagPWV6); body != nil {
			r.WavePreview3Band = body
			got = true
		}
		if body := anlzSectionBody(twoEXPath, tagPWV7); body != nil {
			r.WaveDetail3Band = body
			got = true
		}
		if body := anlzSectionBody(twoEXPath, tagPWVC); body != nil {
			r.Wave3BandColor = body
			got = true
		}
	}

	if !got {
		return nil
	}
	return r
}

// anlzSectionBody returns the raw entry data of a section (the bytes at
// [len_header:len_tag]), or nil if the section/file is absent.
func anlzSectionBody(filePath, tag string) []byte {
	blob := ReadANLZSection(filePath, tag)
	if blob == nil {
		return nil
	}
	s := blob[4:] // strip dbserver LE length prefix → fourcc(4)+lenHdr(4)+lenTag(4)+...
	if len(s) < 12 {
		return nil
	}
	lenHeader := binary.BigEndian.Uint32(s[4:8])
	lenTag := binary.BigEndian.Uint32(s[8:12])
	// lenHeader >= lenTag means the section has a header but no entry body —
	// treat it as absent (return nil) so ParseANLZ doesn't record an empty
	// waveform/grid and mask the re-analysis fallback.
	if lenHeader >= lenTag || int(lenTag) > len(s) || lenHeader < 12 {
		return nil
	}
	return s[lenHeader:lenTag]
}

// anlzSectionAfterHeader returns the section bytes after the generic 12-byte
// header (extra + body together). Used for PSSI, whose downstream consumer
// expects the entry-size/num-entries header bytes kept inline.
func anlzSectionAfterHeader(filePath, tag string) []byte {
	blob := ReadANLZSection(filePath, tag)
	if blob == nil {
		return nil
	}
	s := blob[4:]
	if len(s) < 12 {
		return nil
	}
	lenTag := binary.BigEndian.Uint32(s[8:12])
	if int(lenTag) > len(s) || lenTag < 12 {
		return nil
	}
	return s[12:lenTag]
}

// parseBeatGrid decodes a PQTZ body (numBeats × 8 bytes: u2 beat_number,
// u2 tempo, u4 time_ms, all big-endian) into beat positions in ms, the
// index of the first downbeat (beat_number == 1), and the grid tempo in BPM
// (from the first beat's tempo field, which rekordbox stores as BPM×100).
func parseBeatGrid(body []byte) (beats []float64, downbeat int, bpm float64) {
	n := len(body) / 8
	if n == 0 {
		return nil, 0, 0
	}
	beats = make([]float64, n)
	downbeat = -1
	for i := 0; i < n; i++ {
		off := i * 8
		beatNum := binary.BigEndian.Uint16(body[off:])
		timeMs := binary.BigEndian.Uint32(body[off+4:])
		beats[i] = float64(timeMs)
		if i == 0 {
			bpm = float64(binary.BigEndian.Uint16(body[off+2:])) / 100.0
		}
		if downbeat < 0 && beatNum == 1 {
			downbeat = i
		}
	}
	if downbeat < 0 {
		downbeat = 0
	}
	return beats, downbeat, bpm
}
