// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"encoding/binary"
	"log"
	"math"
	"strings"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// track.go contains the per-track handlers — anything keyed off a
// specific trackID rather than a category. Metadata, info, artwork,
// waveform previews, beat grid (with override), extended analysis
// (PWV4/PWV5/PVB2/PQT2), song-structure, NXS2 cues, mount info, and
// the legacy cue-point endpoints.

func (h *Handler) handleGetMetadata(msg *proto.DBMessage) []*proto.DBMessage {
	if len(msg.Args) < 2 {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}
	trackID := msg.Args[1].Int()

	// Kick off lazy analysis in background — don't block the metadata response.
	// The CDJ times out if we take more than ~3 seconds to respond.
	// BPM/key/duration may be empty on first load but will be available
	// for subsequent loads and waveform requests.
	go h.lazyAnalyze(trackID)

	// Look up from PDB first, then library.
	var title, artist, album, genre, key, comment, dateAdded string
	var tID, artworkID, keyID, duration uint32
	if h.pdb != nil {
		pt := h.pdb.TrackByID(trackID)
		if pt != nil {
			tID = pt.ID
			title = pt.Title
			artist = pt.Artist
			album = pt.Album
			genre = pt.Genre
			key = pt.Key
			comment = pt.Comment
			dateAdded = pt.DateAdded
			artworkID = pt.ArtworkID
			keyID = pt.KeyID
			duration = uint32(pt.Duration) * 1000 // seconds to ms
		}
	}
	if title == "" && h.lib != nil {
		track := h.lib.Track(trackID)
		if track != nil {
			tID = track.ID
			title = track.Title
			artist = track.Artist
			album = track.Album
			genre = track.Genre
			key = track.Key
			comment = track.Comment
			artworkID = track.ArtID
			if !track.DateAdded.IsZero() {
				dateAdded = track.DateAdded.Format("2006-01-02")
			}
			if track.Duration > 0 {
				duration = uint32(track.Duration.Seconds()) * 1000
			}
		}
	}
	if title == "" {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}

	log.Printf("dbserver: metadata for track %d: %q by %q", trackID, title, artist)

	// Metadata items in the standard format.
	// Item types: 0x000b=duration(secs), 0x000d=BPM(*100), 0x000f=key
	tempo := uint32(0)
	if h.pdb != nil {
		if pt := h.pdb.TrackByID(trackID); pt != nil {
			tempo = pt.Tempo
		}
	}
	// Fall back to library track BPM
	if tempo == 0 && h.lib != nil {
		if t := h.lib.Track(trackID); t != nil && t.BPM > 0 {
			tempo = uint32(t.BPM * 100)
		}
	}
	// Look up additional fields from library track.
	var bitrate, year, colorID, rating uint32
	var originalArtist, remixer string
	if h.lib != nil {
		if t := h.lib.Track(trackID); t != nil {
			bitrate = uint32(t.Bitrate)
			year = uint32(t.Year)
			originalArtist = t.OriginalArtist
			remixer = t.Remixer
			colorID = uint32(t.ColorID)
			rating = uint32(t.Rating)
		}
	}
	if h.pdb != nil {
		if pt := h.pdb.TrackByID(trackID); pt != nil {
			if colorID == 0 {
				colorID = uint32(pt.ColorID)
			}
		}
	}

	// Use 0x7FFFFFFF as sentinel for unknown key.
	if keyID == 0 && key == "" {
		keyID = 0x7FFFFFFF
	}

	fileType := h.resolveFileType(trackID)

	// Metadata items in the standard format (16 items).
	// ParentID values: some items have parent=1, others parent=0.
	metaItems := []*menuItem{
		{ID: tID, Label1: title, ArtID: artworkID, ItemType: 0x0004, ParentID: 1, FileType: fileType}, // title
		{ID: tID, Label1: artist, ItemType: 0x0007, ParentID: 1},                                      // artist
		{ID: tID, Label1: album, ItemType: 0x0002, ParentID: 1},                                       // album
		{ID: duration / 1000, Label1: "", ItemType: 0x000b},                                           // duration (parent=0)
		{ID: tempo, Label1: "", ItemType: 0x000d, ParentID: 1},                                        // BPM (* 100)
		{ID: keyID, Label1: key, ItemType: 0x000f, ParentID: 1},                                       // key
		{ID: rating, Label1: "", ItemType: 0x000a},                                                    // rating
		// TRACK INFO colour row. Match the pattern every other
		// metadata row uses: ItemType is just the field type marker
		// (0x0013 here, no high-byte colour encoding — that was a
		// guess that made the deck wrap the label in "[…]" and skip
		// the chip rendering). ID carries the actual colour value
		// (0 = none, 1 = Pink, … 8 = Purple) the deck looks up in
		// its own palette.
		{ID: colorID, Label1: trackColorName(uint8(colorID)),
			ItemType: 0x0013},
		{ID: 0, Label1: genre, ItemType: 0x0006, ParentID: 1},       // genre
		{ID: tID, Label1: dateAdded, ItemType: 0x002e, ParentID: 1}, // date added
		{ID: tID, Label1: comment, ItemType: 0x0023},                // comment (parent=0)
		{ID: 0, Label1: "", ItemType: 0x000e, ParentID: 1},          // label
		{ID: bitrate, Label1: "", ItemType: 0x0010},                 // bitrate (parent=0)
		{ID: year, Label1: "", ItemType: 0x0011, ParentID: 1},       // year
		{ID: 0, Label1: originalArtist, ItemType: 0x0028},           // original artist (parent=0)
		{ID: 0, Label1: remixer, ItemType: 0x0029},                  // remixer (parent=0)
	}
	h.setPendingAll(msg, metaItems)

	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(uint32(len(metaItems))),
		},
	}}
}

func (h *Handler) handleSetRating(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2107: CDJ sets the rating for a track.
	// Args: [spec, track_id, rating (0-5)]
	if len(msg.Args) < 3 {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}
	trackID := msg.Args[1].Int()
	rating := msg.Args[2].Int()

	log.Printf("dbserver: rating update track=%d rating=%d", trackID, rating)

	if h.lib != nil {
		if t := h.lib.Track(trackID); t != nil {
			t.Rating = uint8(rating)
			h.lib.Save()
		}
	}

	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(1)},
	}}
}

func (h *Handler) handleGetArtwork(msg *proto.DBMessage) []*proto.DBMessage {
	for i, a := range msg.Args {
		log.Printf("dbserver: 0x%04x artwork arg[%d] = 0x%08x (%d)", msg.Type, i, a.Int(), a.Int())
	}
	if len(msg.Args) < 2 {
		return []*proto.DBMessage{h.success(msg)}
	}

	artID := msg.Args[1].Int()

	// 0x2103 uses track ID, not artwork ID — resolve to artwork ID.
	if msg.Type == 0x2103 && h.pdb != nil {
		if t := h.pdb.TrackByID(artID); t != nil && t.ArtworkID > 0 {
			artID = t.ArtworkID
		}
	}

	art := h.lib.Artwork.Get(artID)
	// If not found by direct ID, try resolving via track's ArtID.
	if art == nil && h.lib != nil {
		if t := h.lib.Track(artID); t != nil && t.ArtID > 0 {
			log.Printf("dbserver: artwork %d → track %d artID=%d", artID, t.ID, t.ArtID)
			art = h.lib.Artwork.Get(t.ArtID)
		}
	}

	// The CDJ freezes and drops the link when handed an oversized artwork blob
	// (real thumbnails are a few KB; a full-res import can be hundreds of KB).
	// Downscale anything over the cap to a 240px thumbnail and cache the small
	// version back so it's a one-time cost; if it can't be resized, skip it
	// rather than risk the deck.
	const maxCDJArtBytes = 32 * 1024
	if art != nil && len(art.Data) > maxCDJArtBytes {
		if small, err := library.ThumbnailJPEG(art.Data, 240); err == nil && len(small) <= maxCDJArtBytes {
			log.Printf("dbserver: artwork %d oversized (%d bytes) → resized to %d for CDJ", art.ID, len(art.Data), len(small))
			h.lib.Artwork.AddWithID(art.ID, "image/jpeg", small)
			art = h.lib.Artwork.Get(art.ID)
		} else {
			log.Printf("dbserver: artwork %d oversized (%d bytes), resize failed (%v) — skipping to protect the deck", art.ID, len(art.Data), err)
			art = nil
		}
	}

	if art == nil {
		log.Printf("dbserver: artwork %d not found", artID)
		// The response is 0x4002 with status=0x32 (not found) + phantom blob arg.
		return []*proto.DBMessage{{
			TxID:             msg.TxID,
			Type:             0x4002,
			DeclaredArgCount: 4,
			ExtraTags:        []byte{0x03}, // phantom binary arg
			Args: []proto.DBArg{
				proto.ArgI32(uint32(msg.Type)),
				proto.ArgI32(0x32), // not found status
				proto.ArgI32(0),
			},
		}}
	}

	log.Printf("dbserver: artwork %d: %d bytes (%s)", artID, len(art.Data), art.MIMEType)

	// Response type 0x4002 with [echo_type, 0, size, jpeg_blob].
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: 0x4002,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(0),
			proto.ArgI32(uint32(len(art.Data))),
			proto.ArgBlob(art.Data),
		},
	}}
}

func (h *Handler) handleGetWavePreview(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2004: wave preview. The response is type 0x4402
	// with [echo_type, 0, size, blob].
	var trackID uint32
	if len(msg.Args) >= 3 {
		trackID = msg.Args[2].Int() // arg layout: [DMST, 4, trackID, 0]
	} else if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}

	if trackID > 0 {
		if r := h.lazyAnalyze(trackID); r != nil && len(r.WavePreview) > 0 {
			blob := r.WavePreview
			log.Printf("dbserver: wave preview for track %d (%d bytes)", trackID, len(blob))
			return []*proto.DBMessage{{
				TxID: msg.TxID, Type: 0x4402,
				Args: []proto.DBArg{
					proto.ArgI32(uint32(msg.Type)),
					proto.ArgI32(0),
					proto.ArgI32(uint32(len(blob))),
					proto.ArgBlob(blob),
				},
			}}
		}
	}

	log.Printf("dbserver: wave preview 0x%04x (no data)", msg.Type)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4402,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(0),
			proto.ArgI32(0),
		},
	}}
}

func (h *Handler) handleGetWaveDetail(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2904: waveform detail (scrolling waveform). Response type 0x4a02.
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}

	if trackID > 0 {
		if r := h.lazyAnalyze(trackID); r != nil && len(r.WaveDetailMono) > 0 {
			log.Printf("dbserver: wave detail mono for track %d (%d bytes)", trackID, len(r.WaveDetailMono))
			return []*proto.DBMessage{{
				TxID: msg.TxID, Type: 0x4a02,
				Args: []proto.DBArg{
					proto.ArgI32(uint32(msg.Type)),
					proto.ArgI32(0),
					proto.ArgI32(uint32(len(r.WaveDetailMono))),
					proto.ArgBlob(r.WaveDetailMono),
					proto.ArgI32(1), // data-valid flag
				},
			}}
		}
	}

	log.Printf("dbserver: wave detail 0x2904 (no data)")
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4a02,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(0),
			proto.ArgI32(0),
		},
	}}
}

func (h *Handler) handleGetBeatGrid(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2204: beat grid. The response is type 0x4602.
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}

	// Beat grid: always use our generated format (16-byte LE entries with
	// 20-byte preamble). Raw ANLZ PQTZ data uses a different format
	// (8-byte BE entries) that crashes the CDJ via dbserver.
	if trackID > 0 {
		if r := h.lazyAnalyze(trackID); r != nil && len(r.BeatGrid) > 0 {
			blob := h.beatGridForTrack(trackID, r)
			log.Printf("dbserver: beat grid for track %d (%d bytes)", trackID, len(blob))
			return []*proto.DBMessage{{
				TxID: msg.TxID, Type: 0x4602,
				Args: []proto.DBArg{
					proto.ArgI32(0x2204),
					proto.ArgI32(0),
					proto.ArgI32(uint32(len(blob))),
					proto.ArgBlob(blob),
					proto.ArgI32(1), // data-valid flag
				},
			}}
		}
	}

	log.Printf("dbserver: beat grid (no data)")
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4602,
		Args: []proto.DBArg{
			proto.ArgI32(0x2204),
			proto.ArgI32(0),
			proto.ArgI32(0),
			proto.ArgBlob(nil),
			proto.ArgI32(0),
		},
	}}
}

// beatGridForTrack returns the beat-grid blob for trackID, applying
// any manual overrides set on the library track. Returns the cached
// blob from the analysis result when no override is set.
//
// Override behaviour:
//   - BPM override (t.BPM != r.BPM, with t.DetectedBPM tracking the
//     original): regenerate a uniform grid at the new BPM, anchored
//     to the detected first-beat position.
//   - BeatPhaseShift in 1..3: relabel the beats so beat 1 falls N
//     positions ahead of where the detector placed it.
func (h *Handler) beatGridForTrack(trackID uint32, r *analysis.Result) []byte {
	if h.lib == nil || r == nil {
		return r.BeatGrid
	}
	t := h.lib.Track(trackID)
	if t == nil {
		return r.BeatGrid
	}
	overrideBPM := t.BPM > 0 && t.DetectedBPM > 0 && math.Abs(t.BPM-t.DetectedBPM) > 0.01
	phaseShift := t.BeatPhaseShift % 4
	if phaseShift < 0 {
		phaseShift += 4
	}
	if !overrideBPM && phaseShift == 0 {
		return r.BeatGrid
	}
	// Reconstruct beat positions from the cached blob (16-byte entries
	// after a 20-byte preamble). Without the source BeatResult, this
	// is the only way to reapply overrides on top of detected beats.
	if len(r.BeatGrid) < 20 {
		return r.BeatGrid
	}
	const preamble = 20
	const entrySize = 16
	numBeats := (len(r.BeatGrid) - preamble) / entrySize
	if numBeats < 2 {
		return r.BeatGrid
	}
	beats := make([]float64, numBeats)
	downbeatIdx := -1
	for i := 0; i < numBeats; i++ {
		off := preamble + i*entrySize
		beatNum := binary.LittleEndian.Uint16(r.BeatGrid[off : off+2])
		timeMs := binary.LittleEndian.Uint32(r.BeatGrid[off+4 : off+8])
		beats[i] = float64(timeMs)
		if downbeatIdx < 0 && beatNum == 1 {
			downbeatIdx = i
		}
	}
	if downbeatIdx < 0 {
		downbeatIdx = 0
	}

	effectiveBPM := t.BPM
	if !overrideBPM {
		effectiveBPM = r.BPM
	}
	// Effective downbeat = first detected beat 1, shifted by phaseShift
	// positions forward in the detected sequence.
	newDownbeatIdx := (downbeatIdx + phaseShift) % numBeats

	if overrideBPM {
		// Recompute as a uniform grid at the new BPM, starting at the
		// effective downbeat time. Beat positions detected at the old
		// BPM are discarded — the user's BPM override implies the
		// detector's per-beat times can't be trusted either.
		// library.Track.Duration is a time.Duration (nanoseconds), so
		// use Seconds() to convert correctly — earlier code did
		// float64(t.Duration)*1000 which produced ~10^14 ms and made
		// GenerateBeatGrid try to allocate ~880GB for the beat slice.
		durationMs := t.Duration.Seconds() * 1000.0
		if durationMs <= 0 && numBeats > 0 {
			durationMs = beats[numBeats-1]
		}
		return analysis.GenerateBeatGrid(effectiveBPM, durationMs, beats[newDownbeatIdx])
	}

	// Phase-shift only: keep detected beat positions, relabel.
	tempo := uint16(effectiveBPM * 100)
	buf := make([]byte, preamble+numBeats*entrySize)
	binary.LittleEndian.PutUint32(buf[0:], 0x00080000)
	binary.LittleEndian.PutUint32(buf[4:], uint32(numBeats))
	binary.LittleEndian.PutUint32(buf[8:], uint32(numBeats*entrySize))
	binary.LittleEndian.PutUint32(buf[12:], 1)
	binary.LittleEndian.PutUint32(buf[16:], 1)
	for i := 0; i < numBeats; i++ {
		off := preamble + i*entrySize
		bib := ((i - newDownbeatIdx) % 4)
		if bib < 0 {
			bib += 4
		}
		binary.LittleEndian.PutUint16(buf[off+0:], uint16(bib+1))
		binary.LittleEndian.PutUint16(buf[off+2:], tempo)
		binary.LittleEndian.PutUint32(buf[off+4:], uint32(beats[i]))
		for j := 8; j < 16; j++ {
			buf[off+j] = 0xff
		}
	}
	return buf
}

// extAnalysisTags is the tag descriptor for all 0x4f02 responses.
// The response always declares 5 args: int32, int32, int32, blob, int32.
// PWV4 and NOT_FOUND send only 4 on wire (arg4 is phantom).
// PWV5/PQT2/PVB2 send all 5 (arg4 = data-valid flag).
var extAnalysisTags = []byte{0x06, 0x06, 0x06, 0x03, 0x06}

func (h *Handler) handleGetExtAnalysis(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2c04: extended analysis (ANLZ tags like PWV4, PWV5, PVB2, PQT2).
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}

	// Decode tag fourcc from arg[2]. The CDJ sends it as a big-endian int32
	// where the bytes are the reversed ASCII: e.g., 0x34565750 = "4VWP" = PWV4.
	tagFourCC := ""
	if len(msg.Args) >= 3 {
		v := msg.Args[2].Int()
		b := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		// Reverse to get the actual fourcc.
		tagFourCC = string([]byte{b[3], b[2], b[1], b[0]})
	}
	log.Printf("dbserver: 0x2c04 track=%d tag=%q", trackID, tagFourCC)

	if trackID > 0 {
		r := h.lazyAnalyze(trackID)
		if r != nil {
			switch tagFourCC {
			case "PWV4": // color waveform preview
				// Try loading real Pioneer ANLZ data from .EXT file first.
				if h.pdb != nil {
					t := h.pdb.TrackByID(trackID)
					if t != nil && t.AnalyzePath != "" {
						extPath := strings.Replace(t.AnalyzePath, ".DAT", ".EXT", 1)
						if h.exportRoot != "" {
							extPath = h.exportRoot + extPath
						}
						if realBlob := analysis.ReadANLZSection(extPath, "PWV4"); realBlob != nil {
							log.Printf("dbserver: PWV4 for track %d (%d bytes, from ANLZ file)", trackID, len(realBlob))
							return []*proto.DBMessage{{
								TxID: msg.TxID, Type: 0x4f02,
								Args: []proto.DBArg{
									proto.ArgI32(0x2c04),
									proto.ArgI32(0),
									proto.ArgI32(uint32(len(realBlob))),
									proto.ArgBlob(realBlob),
									proto.ArgI32(1),
								},
							}}
						}
					}
				}
				if len(r.WaveColorPreview) > 0 {
					blob := analysis.WrapANLZ("PWV4", 6, r.WaveColorPreview)
					log.Printf("dbserver: PWV4 for track %d (%d bytes, generated)", trackID, len(blob))
					return []*proto.DBMessage{{
						TxID: msg.TxID, Type: 0x4f02,
						Args: []proto.DBArg{
							proto.ArgI32(0x2c04),
							proto.ArgI32(0),
							proto.ArgI32(uint32(len(blob))),
							proto.ArgBlob(blob),
							proto.ArgI32(1),
						},
					}}
				}
			case "PWV5": // color waveform detail (scrolling)
				if h.pdb != nil {
					t := h.pdb.TrackByID(trackID)
					if t != nil && t.AnalyzePath != "" {
						extPath := strings.Replace(t.AnalyzePath, ".DAT", ".EXT", 1)
						if h.exportRoot != "" {
							extPath = h.exportRoot + extPath
						}
						if realBlob := analysis.ReadANLZSection(extPath, "PWV5"); realBlob != nil {
							log.Printf("dbserver: PWV5 for track %d (%d bytes, from ANLZ file)", trackID, len(realBlob))
							return []*proto.DBMessage{{
								TxID: msg.TxID, Type: 0x4f02,
								Args: []proto.DBArg{
									proto.ArgI32(0x2c04),
									proto.ArgI32(0),
									proto.ArgI32(uint32(len(realBlob))),
									proto.ArgBlob(realBlob),
									proto.ArgI32(1), // data-valid flag
								},
							}}
						}
					}
				}
				if len(r.WaveDetail) > 0 {
					blob := analysis.WrapANLZ("PWV5", 2, r.WaveDetail)
					log.Printf("dbserver: PWV5 for track %d (%d bytes, generated)", trackID, len(blob))
					return []*proto.DBMessage{{
						TxID: msg.TxID, Type: 0x4f02,
						Args: []proto.DBArg{
							proto.ArgI32(0x2c04),
							proto.ArgI32(0),
							proto.ArgI32(uint32(len(blob))),
							proto.ArgBlob(blob),
							proto.ArgI32(1), // data-valid flag
						},
					}}
				}
			case "PWV6", "PWV7", "PWVC": // CDJ-3000 3-band waveforms (.2EX)
				// The cached field holds the section body — rekordbox's verbatim
				// for imported tracks, ours (generated) otherwise. PWVC is only
				// present for imported tracks (we don't synthesize colour meta).
				var body []byte
				entrySize := 3
				switch tagFourCC {
				case "PWV6":
					body = r.WavePreview3Band
				case "PWV7":
					body = r.WaveDetail3Band
				case "PWVC":
					body, entrySize = r.Wave3BandColor, 6
				}
				if len(body) > 0 {
					blob := analysis.WrapANLZ(tagFourCC, entrySize, body)
					log.Printf("dbserver: %s for track %d (%d bytes)", tagFourCC, trackID, len(blob))
					return []*proto.DBMessage{{
						TxID: msg.TxID, Type: 0x4f02,
						Args: []proto.DBArg{
							proto.ArgI32(0x2c04),
							proto.ArgI32(0),
							proto.ArgI32(uint32(len(blob))),
							proto.ArgBlob(blob),
							proto.ArgI32(1), // data-valid flag
						},
					}}
				}
			case "PSSI": // song structure / phrase analysis
				if r.SongStructure != nil {
					blob := analysis.WrapANLZ("PSSI", 24, r.SongStructure)
					log.Printf("dbserver: PSSI for track %d (%d bytes, generated)", trackID, len(blob))
					return []*proto.DBMessage{{
						TxID: msg.TxID, Type: 0x4f02,
						Args: []proto.DBArg{
							proto.ArgI32(0x2c04),
							proto.ArgI32(0),
							proto.ArgI32(uint32(len(blob))),
							proto.ArgBlob(blob),
							proto.ArgI32(1),
						},
					}}
				}
			case "PVB2": // extended VBR seek index
				// PVB2 is SERVED here (0x4f02, ~8036-byte blob). If we
				// withhold it, the deck retries via a raw 0x2805 tagged-section
				// read; that path is unanswered and deadlocks the deck's
				// dbserver channel (blank details, hung browse) while NFS/audio
				// keep working — the FLAC-load freeze. So we must return PVB2.
				//
				// Prefer the real .EXT section (byte-exact); otherwise serve a
				// generated placeholder so the deck stops falling back to 0x2805.
				if h.pdb != nil {
					t := h.pdb.TrackByID(trackID)
					if t != nil && t.AnalyzePath != "" {
						extPath := strings.Replace(t.AnalyzePath, ".DAT", ".EXT", 1)
						if h.exportRoot != "" {
							extPath = h.exportRoot + extPath
						}
						if realBlob := analysis.ReadANLZSection(extPath, "PVB2"); realBlob != nil {
							log.Printf("dbserver: PVB2 for track %d (%d bytes, from ANLZ file)", trackID, len(realBlob))
							return []*proto.DBMessage{{
								TxID: msg.TxID, Type: 0x4f02,
								Args: []proto.DBArg{
									proto.ArgI32(0x2c04),
									proto.ArgI32(0),
									proto.ArgI32(uint32(len(realBlob))),
									proto.ArgBlob(realBlob),
									proto.ArgI32(1), // data-valid flag
								},
							}}
						}
					}
				}
				// No ANLZ file: generate a real seek index from the audio
				// frames (cached). A correct index (true byte offsets) is what
				// stops the deck rejecting it and uploading its own via 0x2805.
				// Falls back to the zeroed placeholder only if the file can't
				// be probed.
				blob := analysis.VBRSeekIndex(h.resolveTrackPath(trackID))
				if blob != nil {
					log.Printf("dbserver: PVB2 for track %d (%d bytes, generated seek index)", trackID, len(blob))
				} else {
					blob = analysis.GeneratePVB2()
					log.Printf("dbserver: PVB2 for track %d (%d bytes, placeholder — probe failed)", trackID, len(blob))
				}
				return []*proto.DBMessage{{
					TxID: msg.TxID, Type: 0x4f02,
					Args: []proto.DBArg{
						proto.ArgI32(0x2c04),
						proto.ArgI32(0),
						proto.ArgI32(uint32(len(blob))),
						proto.ArgBlob(blob),
						proto.ArgI32(1), // data-valid flag
					},
				}}
			case "PQT2": // phrase quantize v2
				// Try reading real PQT2 from ANLZ .EXT file first — UNLESS the user
				// manually adjusted the grid, in which case the on-disk file is stale
				// and we must serve our regenerated blob (r.BeatGridPQT2) below.
				if h.pdb != nil && !r.GridEdited {
					t := h.pdb.TrackByID(trackID)
					if t != nil && t.AnalyzePath != "" {
						extPath := strings.Replace(t.AnalyzePath, ".DAT", ".EXT", 1)
						if h.exportRoot != "" {
							extPath = h.exportRoot + extPath
						}
						if realBlob := analysis.ReadANLZSection(extPath, "PQT2"); realBlob != nil {
							log.Printf("dbserver: PQT2 for track %d (%d bytes, from ANLZ file)", trackID, len(realBlob))
							return []*proto.DBMessage{{
								TxID: msg.TxID, Type: 0x4f02,
								Args: []proto.DBArg{
									proto.ArgI32(0x2c04),
									proto.ArgI32(0),
									proto.ArgI32(uint32(len(realBlob))),
									proto.ArgBlob(realBlob),
									proto.ArgI32(1), // data-valid flag
								},
							}}
						}
					}
				}
				if r.BeatGridPQT2 != nil {
					blob := r.BeatGridPQT2 // complete ANLZ section with 56-byte header
					log.Printf("dbserver: PQT2 for track %d (%d bytes, generated)", trackID, len(blob))
					return []*proto.DBMessage{{
						TxID: msg.TxID, Type: 0x4f02,
						Args: []proto.DBArg{
							proto.ArgI32(0x2c04),
							proto.ArgI32(0),
							proto.ArgI32(uint32(len(blob))),
							proto.ArgBlob(blob),
							proto.ArgI32(1),
						},
					}}
				}
			}
		}
	}

	log.Printf("dbserver: ext analysis 0x2c04 tag=%q (not found)", tagFourCC)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4f02,
		DeclaredArgCount: 5,
		OverrideTags:     extAnalysisTags,
		Args: []proto.DBArg{
			proto.ArgI32(0x2c04),
			proto.ArgI32(0x32), // status=0x32: not found
			proto.ArgI32(0),
			proto.ArgI32(0), // 4 int32 on wire, 5th phantom
		},
	}}
}

func (h *Handler) handleGetSongStructure(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2504: song structure / phrase analysis. Response type 0x4502.
	trackID := uint32(0)
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}
	log.Printf("dbserver: song structure track=%d", trackID)

	var data []byte
	if r := h.analysis.Get(trackID); r != nil && r.SongStructure != nil {
		data = r.SongStructure
	}

	if data == nil {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: 0x4502,
			Args: []proto.DBArg{
				proto.ArgI32(0x2504),
				proto.ArgI32(0),
				proto.ArgI32(0),
				proto.ArgI32(0),
			},
		}}
	}

	// The response is always a 1604-byte blob for 0x2504,
	// even for un-analyzed tracks. The actual PSSI data is served
	// via 0x2c04 with the PSSI tag. This response signals that
	// phrase data may be available.
	placeholder := make([]byte, 1604)
	log.Printf("dbserver: song structure placeholder for track %d (1604 bytes)", trackID)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4502,
		Args: []proto.DBArg{
			proto.ArgI32(0x2504),
			proto.ArgI32(0),
			proto.ArgI32(1604),
			proto.ArgBlob(placeholder),
		},
	}}
}

// handleWritePVB2 acknowledges a 0x2805 PVB2 write. When the deck decides the
// PVB2 (extended VBR seek index) we served is unusable, it computes its own
// from the audio (read over NFS) and uploads it here — arg[6] is a complete
// PVB2 ANLZ section. This is a WRITE analogous to the 0x2705 cue-write; if we
// don't answer it, the deck blocks its whole dbserver request channel forever
// (blank details, hung browse) while NFS/audio keep working. This path is only
// hit when the served PVB2 is invalid, so we mirror the 0x2705 cue-write
// pattern (reply echoing the written blob) using the 0x4f02 PVB2 response the
// deck already accepts for 0x2c04 reads.
func (h *Handler) handleWritePVB2(msg *proto.DBMessage) []*proto.DBMessage {
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}
	// The uploaded section is the last (binary) argument.
	var section []byte
	for i := len(msg.Args) - 1; i >= 0; i-- {
		if len(msg.Args[i].Bytes) > 0 {
			section = msg.Args[i].Bytes
			break
		}
	}
	log.Printf("dbserver: PVB2 write 0x2805 track=%d (%d-byte section) — acking", trackID, len(section))

	// Wrap with the 4-byte little-endian length prefix used by the dbserver
	// ANLZ blob format (matches ReadANLZSection / GeneratePVB2 output).
	blob := make([]byte, 4+len(section))
	binary.LittleEndian.PutUint32(blob, uint32(len(section)))
	copy(blob[4:], section)

	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: 0x4f02,
		Args: []proto.DBArg{
			proto.ArgI32(0x2c04),
			proto.ArgI32(0),
			proto.ArgI32(uint32(len(blob))),
			proto.ArgBlob(blob),
			proto.ArgI32(1), // data-valid flag
		},
	}}
}

func (h *Handler) handleGetNXS2Cues(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x3d03: NXS2 cue/loop point data. The response returns count=6.
	// The CDJ checks this count but doesn't render items for it.
	log.Printf("dbserver: NXS2 cue data (count=6)")
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(0x3d03),
			proto.ArgI32(6),
		},
	}}
}

func (h *Handler) handleMountInfo(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x3100 is the SHORT CUT preview query. Before a SHORT CUT button
	// (TRACK / PLAYLIST / SEARCH) opens its list, the deck asks which
	// root-menu slot that shortcut lives in: arg[1] is the target
	// category's item ID and the response value is that item's *index*
	// within the root menu. The deck then renders a 1-item preview at
	// that offset (0x3000, count=1) for the top-bar label and opens the
	// list there. Returning a fixed 0 makes every shortcut resolve to
	// root-menu slot 0 (ARTIST), so TRACK/PLAYLIST both open ARTIST.
	//
	// Look the requested ID up in the actual root menu so the offset
	// stays correct regardless of the configured menu order. Unknown or
	// absent IDs fall back to 0 (the old ack behaviour) — e.g. plain
	// mount notifications that carry no category ID.
	offset := 0
	wantID := uint32(0)
	if len(msg.Args) >= 2 {
		wantID = msg.Args[1].Int()
		if wantID != 0 {
			for i, item := range h.rootMenu() {
				if item.ID == wantID {
					offset = i
					break
				}
			}
		}
	}
	log.Printf("dbserver: mount info (0x3100) shortcut id=0x%x -> root offset %d", wantID, offset)
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(0x3100),
			proto.ArgI32(uint32(offset)),
		},
	}}
}

func (h *Handler) handleGetTrackInfo(msg *proto.DBMessage) []*proto.DBMessage {
	// Returns file path and basic track info.
	for i, a := range msg.Args {
		log.Printf("dbserver: 0x2102 arg[%d] = 0x%08x (%d)", i, a.Int(), a.Int())
	}
	if len(msg.Args) < 2 {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}
	trackID := msg.Args[1].Int()

	// Kick off lazy analysis in background — don't block the track info response.
	go h.lazyAnalyze(trackID)

	// Look up track from PDB or library.
	var relPath string
	var tID uint32
	if h.pdb != nil {
		pt := h.pdb.TrackByID(trackID)
		if pt != nil {
			relPath = pt.FilePath
			tID = pt.ID
		}
	}
	if relPath == "" {
		track := h.lib.Track(trackID)
		if track != nil {
			relPath = track.FilePath
			if strings.HasPrefix(relPath, h.exportRoot) {
				relPath = relPath[len(h.exportRoot):]
			}
			tID = track.ID
		}
	}
	if relPath == "" {
		return []*proto.DBMessage{{
			TxID: msg.TxID, Type: proto.DBMsgSuccess,
			Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
		}}
	}
	if !strings.HasPrefix(relPath, "/") {
		relPath = "/" + relPath
	}

	// Look up additional metadata for duration, BPM, key, comment.
	var duration, tempo, keyID, fileSize uint32
	var key, comment string
	if h.pdb != nil {
		pt := h.pdb.TrackByID(trackID)
		if pt != nil {
			duration = uint32(pt.Duration)
			tempo = pt.Tempo // BPM * 100
			keyID = pt.KeyID
			key = pt.Key
			comment = pt.Comment
			fileSize = pt.FileSize
		}
	}
	// Fall back to library track metadata (used in lazy-analysis mode without PDB).
	if (duration == 0 || tempo == 0) && h.lib != nil {
		if t := h.lib.Track(trackID); t != nil {
			if duration == 0 && t.Duration > 0 {
				duration = uint32(t.Duration.Seconds())
			}
			if tempo == 0 && t.BPM > 0 {
				tempo = uint32(t.BPM * 100)
			}
			if key == "" {
				key = t.Key
			}
			if fileSize == 0 {
				fileSize = uint32(t.FileSize)
			}
			// Estimate duration from file size if still unknown.
			// CDJ rejects tracks with dur=0. Use ~192kbps as estimate.
			if duration == 0 && t.FileSize > 0 {
				duration = uint32(t.FileSize / 24000) // rough: bytes / (192kbps/8)
				if duration < 30 {
					duration = 180 // default 3 min
				}
			}
			// CDJ also rejects tracks with bpm=0. Default to 120 BPM
			// (common dance music tempo) until analysis completes.
			if tempo == 0 {
				tempo = 12000 // 120.00 BPM
			}
		}
	}

	log.Printf("dbserver: track info for track %d: path=%s dur=%d bpm=%d", trackID, relPath, duration, tempo)

	// The response returns 7 items: title, duration, BPM, comment, path, unknown, key.
	infoItems := []*menuItem{
		{ID: trackInfoTitleID(h.resolveFileType(trackID)), ItemType: 0x0004, FileType: h.resolveFileType(trackID), TrackInfo: true}, // title
		{ID: duration, ItemType: 0x000b},                                 // duration (seconds)
		{ID: tempo, ItemType: 0x000d},                                    // BPM (value * 100)
		{ID: tID, Label1: comment, ItemType: 0x0023},                     // comment
		{ID: tID, Label1: relPath, ItemType: 0x0000, ParentID: fileSize}, // file path (arg[0] = file size)
		{ID: 1, ItemType: 0x002f},                                        // unknown (always 1)
		{ID: keyID, Label1: key, ItemType: 0x000f},                       // musical key
	}
	h.setPendingAll(msg, infoItems)

	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{
			proto.ArgI32(uint32(msg.Type)),
			proto.ArgI32(uint32(len(infoItems))),
		},
	}}
}

func (h *Handler) handleGetCuePoints(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2104: legacy (pre-NXS2) cue points, response type 0x4702.
	// Saved cues are served via the NXS2 path (0x2b04) instead; this older
	// format is unsupported, so we return an empty list. Modern players use 0x2b04.
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}
	log.Printf("dbserver: cue points 0x2104 track=%d (empty)", trackID)
	return []*proto.DBMessage{{
		TxID: msg.TxID, Type: proto.DBMsgSuccess,
		Args: []proto.DBArg{proto.ArgI32(uint32(msg.Type)), proto.ArgI32(0)},
	}}
}

func (h *Handler) handleGetNXS2CuePoints(msg *proto.DBMessage) []*proto.DBMessage {
	// 0x2b04: NXS2 extended cue points. Response type 0x4e02.
	var trackID uint32
	if len(msg.Args) >= 2 {
		trackID = msg.Args[1].Int()
	}

	var blob []byte
	var cueCount uint32
	if h.cues != nil {
		blob = h.cues.GetCombinedBlob(trackID)
		cues := h.cues.GetCues(trackID)
		cueCount = uint32(len(cues))
	}

	if len(blob) == 0 {
		log.Printf("dbserver: NXS2 cues 0x2b04 track=%d (empty)", trackID)
		// Wire format: descriptor=06 06 06 03 06, sends 4 int32 on wire.
		// Arg3 typed as binary(03) in descriptor but sent as int32(0).
		// Arg4 is phantom (declared but not sent).
		return []*proto.DBMessage{{
			TxID:             msg.TxID,
			Type:             0x4e02,
			DeclaredArgCount: 5,
			OverrideTags:     []byte{0x06, 0x06, 0x06, 0x03, 0x06},
			Args: []proto.DBArg{
				proto.ArgI32(0x2b04),
				proto.ArgI32(1),
				proto.ArgI32(0),
				proto.ArgI32(0),
			},
		}}
	}

	log.Printf("dbserver: NXS2 cues 0x2b04 track=%d (%d cues, %d bytes)", trackID, cueCount, len(blob))
	return []*proto.DBMessage{{
		TxID: msg.TxID,
		Type: 0x4e02,
		Args: []proto.DBArg{
			proto.ArgI32(0x2705),
			proto.ArgI32(0),
			proto.ArgI32(uint32(len(blob))),
			proto.ArgBlob(blob),
			proto.ArgI32(cueCount),
		},
	}}
}
