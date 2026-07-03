// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// PVB2 (extended VBR seek index) maps decoded sample positions to byte offsets
// within a variable-frame-size audio file (FLAC, ALAC, …) so the CDJ can seek
// accurately. rekordbox serves this for every lossless track; when we
// withhold it or serve a zeroed placeholder, the deck computes its own from
// the audio and uploads it via a 0x2805 write — and if that write is dropped,
// the deck deadlocks its dbserver channel. Generating a correct index here
// makes the deck accept ours and never regenerate.
//
// Wire format (reverse-engineered from rekordbox .EXT captures):
//
//	section header: "PVB2", len_header=32, len_tag=32+body
//	extra (20 bytes): u4 0, u4 0, u4 total_samples, u4 num_entries, u4 entry_size=20
//	body: num_entries × 20-byte entries, each:
//	  u8 sample_position (BE) — decoded PCM sample index (packet pts)
//	  u8 byte_offset     (BE) — offset of that frame relative to the first
//	                            audio frame (packet pos − first packet pos)
//	  u4 block_size      (BE) — always 4096 (FLAC max blocksize)
//
// Entries are recorded roughly every 6 frames, matching rekordbox's density.

const pvb2FrameStep = 6      // record a seek point every N frames (~rekordbox)
const pvb2BlockSize = 0x1000 // 4096 — constant in real exports, even last frame

// pvb2Cache memoises the generated blob per absolute file path. A cached nil
// means generation was attempted and failed (non-seekable / not probeable), so
// callers fall back to the placeholder without re-running ffprobe every load.
var pvb2Cache sync.Map // string -> []byte

// VBRSeekIndex returns the PVB2 blob (wrapped with the 4-byte LE length prefix
// used by the dbserver ANLZ format) for the file, generating and caching it on
// first call. Returns nil if the file can't be probed.
func VBRSeekIndex(filePath string) []byte {
	if v, ok := pvb2Cache.Load(filePath); ok {
		b, _ := v.([]byte)
		return b
	}
	blob, err := generateVBRSeekIndex(filePath)
	if err != nil {
		pvb2Cache.Store(filePath, []byte(nil))
		return nil
	}
	pvb2Cache.Store(filePath, blob)
	return blob
}

// generateVBRSeekIndex walks the file's audio frames via ffprobe and builds the
// PVB2 seek index.
func generateVBRSeekIndex(filePath string) ([]byte, error) {
	pts, pos, err := probeFrames(filePath)
	if err != nil {
		return nil, err
	}
	if len(pts) < 2 {
		return nil, fmt.Errorf("pvb2: too few frames (%d) in %s", len(pts), filePath)
	}
	firstPos := pos[0]
	totalSamples := probeTotalSamples(filePath)
	if totalSamples == 0 {
		totalSamples = pts[len(pts)-1] + pvb2BlockSize
	}

	// Build entries every pvb2FrameStep frames, always including the first.
	var body bytes.Buffer
	entry := make([]byte, 20)
	numEntries := uint32(0)
	for i := 0; i < len(pts); i += pvb2FrameStep {
		off := pos[i] - firstPos
		if off < 0 {
			off = 0
		}
		binary.BigEndian.PutUint64(entry[0:8], uint64(pts[i]))
		binary.BigEndian.PutUint64(entry[8:16], uint64(off))
		binary.BigEndian.PutUint32(entry[16:20], pvb2BlockSize)
		body.Write(entry)
		numEntries++
	}

	extra := make([]byte, 20)
	binary.BigEndian.PutUint32(extra[8:12], uint32(totalSamples))
	binary.BigEndian.PutUint32(extra[12:16], numEntries)
	binary.BigEndian.PutUint32(extra[16:20], 20) // entry_size
	section := makeSection(tagPVB2, extra, body.Bytes())

	blob := make([]byte, 4+len(section))
	binary.LittleEndian.PutUint32(blob, uint32(len(section)))
	copy(blob[4:], section)
	return blob, nil
}

// probeFrames returns per-frame (pts, pos) for the first audio stream, using a
// single ffprobe pass. ffprobe emits key=value lines; we parse by key so we're
// robust to field ordering.
func probeFrames(filePath string) (pts, pos []int64, err error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "packet=pts,pos",
		"-of", "default=nokey=0:noprint_wrappers=1",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("pvb2: ffprobe packets: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	var curPts int64 = -1
	havePts := false
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "pts":
			curPts, _ = strconv.ParseInt(v, 10, 64)
			havePts = true
		case "pos":
			p, e := strconv.ParseInt(v, 10, 64)
			// A packet with pos but no valid pts (or "N/A") is unusable.
			if e != nil || !havePts || curPts < 0 {
				havePts = false
				continue
			}
			pts = append(pts, curPts)
			pos = append(pos, p)
			havePts = false
		}
	}
	return pts, pos, nil
}

// probeTotalSamples returns the decoded sample count (duration × sample_rate),
// or 0 if it can't be determined. Cheap — no per-packet enumeration.
func probeTotalSamples(filePath string) int64 {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=duration,sample_rate",
		"-of", "default=nokey=0:noprint_wrappers=1",
		filePath,
	).Output()
	if err != nil {
		return 0
	}
	var dur float64
	var rate int64
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "duration":
			dur, _ = strconv.ParseFloat(v, 64)
		case "sample_rate":
			rate, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if dur <= 0 || rate <= 0 {
		return 0
	}
	return int64(dur*float64(rate) + 0.5)
}
