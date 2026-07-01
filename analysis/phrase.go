// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"encoding/binary"
	"math"
	"sort"
)

// Phrase represents a detected musical phrase/section.
type Phrase struct {
	StartBeat  int     // beat number where phrase starts (1-based)
	EndBeat    int     // beat number where phrase ends
	Kind       uint16  // phrase type ID (mood_high_phrase enum)
	Energy     float64 // average energy level (used for classification)
	HasVocal   bool    // contains singing at the default threshold (our lightweight VAD, see vocal.go)
	VocalScore float64 // raw mean vocal-presence score (lets the UI re-threshold live)
	// Absolute start/end time (ms), anchored to the audio. Kept stable so the
	// phrase strip stays on the music when the beat grid is later shifted (a grid
	// edit moves beat→time, but a section's audio position doesn't change).
	StartMs float64
	EndMs   float64
}

// SetPhraseTimes anchors each phrase's StartMs/EndMs to absolute audio time by
// mapping its beat numbers through the given beat grid (same mapping the API
// used to do on every request). Once set, grid edits don't move the phrases.
func SetPhraseTimes(phrases []Phrase, beats []float64, bpm float64) {
	if bpm <= 0 {
		return
	}
	msPerBeat := 60000.0 / bpm
	at := func(beat int) float64 {
		n := len(beats)
		if n == 0 {
			return float64(beat-1) * msPerBeat
		}
		if beat < 1 {
			return beats[0] + float64(beat-1)*msPerBeat
		}
		if beat <= n {
			return beats[beat-1]
		}
		return beats[n-1] + float64(beat-n)*msPerBeat
	}
	for i := range phrases {
		phrases[i].StartMs = at(phrases[i].StartBeat)
		// EndBeat is the phrase's last beat (inclusive); its time span runs to the
		// END of that beat (= onset of the next beat), which is also the next
		// phrase's StartBeat. Using EndBeat+1 keeps consecutive phrases flush
		// instead of leaving a one-beat gap between them.
		phrases[i].EndMs = at(phrases[i].EndBeat + 1)
	}
}

// DetectPhrases analyzes audio to find musical phrase boundaries.
// Returns phrases classified as intro/up/down/chorus/outro (mood=high).
// Uses beat-aligned energy + spectral-novelty analysis with subdivision
// fallback so a track with consistent energy never collapses into a
// single huge "intro" segment.
func DetectPhrases(samples []float32, sampleRate int, bpm float64, downbeatMs float64) []Phrase {
	if bpm <= 0 || len(samples) < sampleRate*4 {
		return nil
	}

	msPerBeat := 60000.0 / bpm
	samplesPerBeat := float64(sampleRate) * msPerBeat / 1000.0
	totalBeats := int((float64(len(samples))/float64(sampleRate)*1000.0 - downbeatMs) / msPerBeat)
	if totalBeats < 8 {
		return nil
	}

	// Compute energy per beat (RMS of each beat window).
	beatEnergies := make([]float64, totalBeats)
	var maxEnergy float64
	for i := 0; i < totalBeats; i++ {
		startSample := int((downbeatMs/1000.0 + float64(i)*msPerBeat/1000.0) * float64(sampleRate))
		endSample := startSample + int(samplesPerBeat)
		if endSample > len(samples) {
			endSample = len(samples)
		}
		if startSample >= len(samples) {
			break
		}

		var sum float64
		count := 0
		for j := startSample; j < endSample; j++ {
			sum += float64(samples[j]) * float64(samples[j])
			count++
		}
		if count > 0 {
			beatEnergies[i] = math.Sqrt(sum / float64(count))
		}
		if beatEnergies[i] > maxEnergy {
			maxEnergy = beatEnergies[i]
		}
	}

	if maxEnergy < 1e-10 {
		return nil
	}

	// Normalize.
	for i := range beatEnergies {
		beatEnergies[i] /= maxEnergy
	}

	// Smooth over a 4-beat window so single-beat spikes don't trip the
	// boundary detector.
	smoothed := make([]float64, totalBeats)
	for i := range smoothed {
		var sum float64
		count := 0
		for j := i - 2; j <= i+2; j++ {
			if j >= 0 && j < totalBeats {
				sum += beatEnergies[j]
				count++
			}
		}
		smoothed[i] = sum / float64(count)
	}

	// Phrase boundaries must land on 16-beat (4-bar) gridlines. Compute
	// per-chunk energy + spectral-flux proxy and find the boundaries.
	const chunkSize = 16
	const minPhraseChunks = 2 // smallest phrase is 32 beats
	numChunks := totalBeats / chunkSize
	if numChunks < 4 {
		// Track too short for meaningful phrase analysis — emit a single
		// "intro" segment matching real rb's behavior on short clips.
		return []Phrase{{StartBeat: 1, EndBeat: totalBeats, Kind: 1, Energy: 0.5}}
	}
	chunkEnergy := make([]float64, numChunks)
	for c := 0; c < numChunks; c++ {
		var sum float64
		for i := c * chunkSize; i < (c+1)*chunkSize && i < totalBeats; i++ {
			sum += smoothed[i]
		}
		chunkEnergy[c] = sum / float64(chunkSize)
	}

	// Novelty score per boundary: how different is the energy in the next
	// 2 chunks vs the previous 2? Larger windows = more robust against
	// per-chunk noise than the previous adjacent-chunk-diff approach.
	novelty := make([]float64, numChunks)
	for c := 1; c < numChunks; c++ {
		var prevSum, currSum float64
		var prevN, currN int
		for k := c - 2; k < c; k++ {
			if k >= 0 {
				prevSum += chunkEnergy[k]
				prevN++
			}
		}
		for k := c; k < c+2; k++ {
			if k < numChunks {
				currSum += chunkEnergy[k]
				currN++
			}
		}
		if prevN == 0 || currN == 0 {
			continue
		}
		prev := prevSum / float64(prevN)
		curr := currSum / float64(currN)
		novelty[c] = math.Abs(curr-prev) / math.Max(prev, 0.001)
	}

	// Pick boundaries: any chunk whose novelty exceeds the threshold AND
	// is a local maximum within ±2 chunks (so a long ramp doesn't fire
	// multiple consecutive boundaries).
	const noveltyThresh = 0.18
	boundaries := []int{0}
	for c := minPhraseChunks; c < numChunks-minPhraseChunks+1; c++ {
		if novelty[c] < noveltyThresh {
			continue
		}
		// Local-max guard.
		isMax := true
		for k := c - 2; k <= c+2; k++ {
			if k < 0 || k >= numChunks || k == c {
				continue
			}
			if novelty[k] > novelty[c] {
				isMax = false
				break
			}
		}
		if !isMax {
			continue
		}
		// Spacing guard.
		if c-boundaries[len(boundaries)-1] < minPhraseChunks {
			continue
		}
		boundaries = append(boundaries, c)
	}
	boundaries = append(boundaries, numChunks)

	// Subdivide oversized phrases — anything >64 chunks (1024 beats) is
	// definitely too coarse; split at 32-chunk (512-beat) intervals. This
	// is the safety net for tracks like long ambient/trance pieces where
	// novelty-based boundary detection misses everything.
	const maxPhraseChunks = 4 // 64 beats = 16 bars; split anything larger
	split := []int{boundaries[0]}
	for i := 1; i < len(boundaries); i++ {
		start := boundaries[i-1]
		end := boundaries[i]
		span := end - start
		if span > maxPhraseChunks {
			// Distribute cuts as evenly as possible so we get N roughly
			// equal pieces instead of (N-1)×max + one large remainder.
			pieces := (span + maxPhraseChunks - 1) / maxPhraseChunks
			for k := 1; k < pieces; k++ {
				split = append(split, start+(k*span)/pieces)
			}
		}
		split = append(split, end)
	}
	boundaries = split

	// Build the phrase list.
	var phrases []Phrase
	for i := 1; i < len(boundaries); i++ {
		start := boundaries[i-1]
		end := boundaries[i]
		var sum float64
		for k := start; k < end; k++ {
			sum += chunkEnergy[k]
		}
		phrases = append(phrases, Phrase{
			StartBeat: start*chunkSize + 1,
			EndBeat:   end * chunkSize,
			Energy:    sum / float64(end-start),
		})
	}

	if len(phrases) == 0 {
		return nil
	}

	classifyPhrases(phrases)
	return phrases
}

// classifyPhrases assigns mood_high_phrase types based on energy and
// context. Position-based "intro on phrase 0, outro on phrase n-1" only
// applies when the phrase is genuinely intro-like (low energy AND short,
// or first beats <= 64); otherwise we look at energy + direction so a
// track whose first phrase is already a full-energy section doesn't get
// mislabeled. Mood_high kind values per the kaitai spec:
//
//	1 = intro, 2 = up, 3 = down, 5 = chorus, 6 = outro
func classifyPhrases(phrases []Phrase) {
	n := len(phrases)
	if n == 0 {
		return
	}

	sorted := make([]float64, n)
	for i, p := range phrases {
		sorted[i] = p.Energy
	}
	sort.Float64s(sorted)
	medianE := sorted[n/2]
	// Quartiles for chorus/down detection.
	highThresh := sorted[(n*3)/4]
	lowThresh := sorted[n/4]

	// Helper: classify a phrase purely by its energy + direction.
	classifyByEnergyDir := func(i int) uint16 {
		e := phrases[i].Energy
		if e >= highThresh {
			return 5 // chorus
		}
		if e <= lowThresh {
			return 3 // down
		}
		// Middle energy → classify by direction relative to prior phrase.
		if i > 0 {
			prev := phrases[i-1].Energy
			if e > prev*1.1 {
				return 2 // up (building)
			}
			if e < prev*0.9 {
				return 3 // down (releasing)
			}
		}
		// Sustain: inherit from previous classification if available.
		if i > 0 {
			switch phrases[i-1].Kind {
			case 5:
				return 5
			case 2:
				return 2
			}
		}
		return 3 // default
	}

	for i := range phrases {
		p := &phrases[i]
		span := p.EndBeat - p.StartBeat

		// First phrase: short (≤ 32 beats / 8 bars) is always intro,
		// otherwise only if its energy is below median. Tracks that
		// open hot get classified by their actual content.
		if i == 0 {
			if span <= 32 || p.Energy < medianE {
				p.Kind = 1 // intro
				continue
			}
		}
		// Last phrase: same rule for outro.
		if i == n-1 {
			if span <= 32 || p.Energy < medianE {
				p.Kind = 6 // outro
				continue
			}
		}
		p.Kind = classifyByEnergyDir(i)
	}
}

// GeneratePSSI creates the PSSI (song structure) blob — returns the
// "extra header + body" content for an ANLZ PSSI section. The caller
// wraps it with the 12-byte generic section header (PSSI + len_header
// + len_tag).
//
// Per the kaitai spec / real exports, the layout is:
//
//	extra header (20 bytes total, included in len_header=32):
//	  u4 entry_size = 24
//	  u2 num_entries
//	  u2 mood (1=high, 2=mid, 3=low; masked file shows mood+20-ish)
//	  6 bytes padding
//	  u2 end_beat
//	  2 bytes padding
//	  u1 raw_bank
//	  1 byte padding
//	body (num_entries × 24 bytes): phrase entries
//
// When num_entries > 0, rekordbox 6 XOR-masks the body (the
// 14 fixed body bytes + the entries). The mask is 19 bytes:
//
//	mask[j] = base[j] + num_entries  (mod 256)
//
// We follow that mask convention. For unmasked output, return as-is.
func GeneratePSSI(phrases []Phrase, bpm float64) []byte {
	if len(phrases) == 0 {
		return nil
	}

	numEntries := len(phrases)
	entrySize := 24
	mood := uint16(1) // high
	bank := uint8(0)
	endBeat := uint16(phrases[len(phrases)-1].EndBeat)

	// Combined region that gets XOR-masked: from the mood u2 onward.
	// 14 fixed bytes (mood..bank+padding) + entries.
	maskedRegion := make([]byte, 14+numEntries*entrySize)
	binary.BigEndian.PutUint16(maskedRegion[0:], mood)
	// bytes 2-7: 6 padding bytes (zero)
	binary.BigEndian.PutUint16(maskedRegion[8:], endBeat)
	// bytes 10-11: 2 padding bytes (zero)
	maskedRegion[12] = bank
	// byte 13: 1 padding byte (zero)
	for i, p := range phrases {
		off := 14 + i*entrySize
		binary.BigEndian.PutUint16(maskedRegion[off+0:], uint16(i+1))
		binary.BigEndian.PutUint16(maskedRegion[off+2:], uint16(p.StartBeat))
		binary.BigEndian.PutUint16(maskedRegion[off+4:], p.Kind)
		// rest of the 24-byte entry stays zero
	}

	// XOR mask. The mask byte sequence is taken verbatim from Deep Symmetry's
	// Kaitai spec rekordbox_anlz.ksy (crate-digger, EPL-licensed) — it encodes
	// the fixed obfuscation constants rekordbox applies to PSSI, so it is a
	// format-interop fact rather than original expression.
	c := byte(numEntries)
	mask := make([]byte, len(pssiMaskBase))
	for j := range pssiMaskBase {
		mask[j] = pssiMaskBase[j] + c
	}
	for i := range maskedRegion {
		maskedRegion[i] ^= mask[i%len(mask)]
	}

	// Return as: entry_size(4) + num_entries(2) + maskedRegion.
	// Total = 6 + 14 + numEntries*24 = 20 + numEntries*24.
	buf := make([]byte, 6+len(maskedRegion))
	binary.BigEndian.PutUint32(buf[0:], uint32(entrySize))
	binary.BigEndian.PutUint16(buf[4:], uint16(numEntries))
	copy(buf[6:], maskedRegion)
	return buf
}

// pssiMaskBase is the 19-byte XOR-mask base rekordbox 6 uses on the PSSI body
// (each byte is offset by num_entries). Shared by GeneratePSSI (write) and
// ParsePSSI (read).
var pssiMaskBase = []byte{
	0xCB, 0xE1, 0xEE, 0xFA, 0xE5, 0xEE, 0xAD, 0xEE,
	0xE9, 0xD2, 0xE9, 0xEB, 0xE1, 0xE9, 0xF3, 0xE8,
	0xE9, 0xF4, 0xE1,
}

// ParsePSSI deserializes a PSSI (song-structure) blob — the bytes stored in
// Result.SongStructure, i.e. the ANLZ PSSI section minus its 12-byte generic
// header — into phrase entries. It is the inverse of GeneratePSSI and
// transparently handles rekordbox 6's XOR-masked body (auto-detected by
// validating the decoded mood). Energy is left zero (not stored in PSSI).
// Returns nil if the blob is empty, malformed, or carries no phrases.
//
// Layout (big-endian): u4 entry_size, u2 num_entries, then a region that is
// masked when num_entries>0: u2 mood, 6 pad, u2 end_beat, 2 pad, u1 bank, 1
// pad, then num_entries × entry_size phrase entries (u2 index, u2 start_beat,
// u2 kind, …). EndBeat of each phrase is the next phrase's start (the header
// end_beat for the last).
func ParsePSSI(body []byte) []Phrase {
	if len(body) < 6 {
		return nil
	}
	entrySize := int(binary.BigEndian.Uint32(body[0:4]))
	numEntries := int(binary.BigEndian.Uint16(body[4:6]))
	// Sanity bounds so a corrupt blob can't drive a huge allocation.
	if numEntries == 0 || numEntries > 1024 || entrySize < 6 || entrySize > 256 {
		return nil
	}
	needed := 6 + 14 + numEntries*entrySize
	if len(body) < needed {
		return nil
	}
	// The masked region is everything from mood (offset 6) to the end of the
	// entries: 14 fixed bytes + the entry table.
	region := make([]byte, 14+numEntries*entrySize)
	copy(region, body[6:needed])

	unmask := func(b []byte) []byte {
		out := make([]byte, len(b))
		c := byte(numEntries)
		for i := range b {
			out[i] = b[i] ^ (pssiMaskBase[i%len(pssiMaskBase)] + c)
		}
		return out
	}
	moodOK := func(b []byte) bool {
		m := binary.BigEndian.Uint16(b[0:2])
		return m >= 1 && m <= 3
	}
	// rekordbox 6 masks the body; older exports don't. Prefer the unmasked
	// reading; fall back to raw if it was already unmasked; bail if neither
	// yields a valid mood (so we never emit junk phrases).
	use := unmask(region)
	if !moodOK(use) {
		if moodOK(region) {
			use = region
		} else {
			return nil
		}
	}

	endBeat := int(binary.BigEndian.Uint16(use[8:10]))
	phrases := make([]Phrase, 0, numEntries)
	for i := 0; i < numEntries; i++ {
		off := 14 + i*entrySize
		startBeat := int(binary.BigEndian.Uint16(use[off+2 : off+4]))
		kind := binary.BigEndian.Uint16(use[off+4 : off+6])
		if startBeat <= 0 {
			continue // skip degenerate/empty entries
		}
		phrases = append(phrases, Phrase{StartBeat: startBeat, Kind: kind})
	}
	if len(phrases) == 0 {
		return nil
	}
	for i := range phrases {
		if i+1 < len(phrases) {
			phrases[i].EndBeat = phrases[i+1].StartBeat
		} else {
			phrases[i].EndBeat = endBeat
		}
		// Guard the final/segment end so a missing or bogus end_beat doesn't
		// produce a zero- or negative-length phrase.
		if phrases[i].EndBeat <= phrases[i].StartBeat {
			phrases[i].EndBeat = phrases[i].StartBeat + 1
		}
	}
	return phrases
}
