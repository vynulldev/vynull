// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/vynulldev/vynull/pdb"
)

const analysisRate = 44100

// cacheVersion is incremented when the analysis format changes.
// Cached results with a different version are discarded and re-analyzed.
// Bumped to 18 when the V1 PWV4 encoder was removed and V2 became the
// only encoder — old V1-encoded caches (saved as 17) and the older
// experimental V2 caches (saved as 2017 behind --pwv4-v2) both invalidate
// here, so the next run re-analyzes once and converges on a single format.
const cacheVersion = 25

// effectiveCacheVersion returns the version key used for on-disk caching,
// offset when RGB3BandMode is enabled so PWV5/PWV4 encoded under one mode
// don't satisfy a cache lookup made under the other mode.
func effectiveCacheVersion() int {
	v := cacheVersion
	if RGB3BandMode {
		v += 1000
	}
	return v
}

// PWV4Override and PWV5Override, when non-nil, replace every track's color
// preview / detail waveform at serve time. Set via the --pwv4-override /
// --pwv5-override CLI flags for probing the CDJ's per-point
// rendering formula by injecting synthetic byte patterns. Does NOT modify
// the on-disk cache — overrides are applied as a copy via ApplyOverrides.
var (
	PWV4Override []byte
	PWV5Override []byte
)

// ApplyOverrides returns r unchanged if no overrides are set, or a shallow
// clone with WaveColorPreview / WaveDetail swapped for the override bytes.
// Call at every consumer site (dbserver, api) so synthetic bytes flow to
// CDJs and HTTP clients without polluting cached results.
func ApplyOverrides(r *Result) *Result {
	if r == nil || (PWV4Override == nil && PWV5Override == nil) {
		return r
	}
	clone := *r
	if PWV4Override != nil {
		clone.WaveColorPreview = PWV4Override
	}
	if PWV5Override != nil {
		clone.WaveDetail = PWV5Override
	}
	return &clone
}

// Result holds computed analysis data for a single track.
type Result struct {
	CacheVersion     int // must match cacheVersion or result is stale
	BPM              float64
	Duration         uint16    // seconds
	KeyCamelot       string    // e.g., "8A", "11B"
	KeyStandard      string    // e.g., "Am", "Eb"
	Artwork          []byte    // JPEG artwork data
	WavePreview      []byte    // raw mono preview (~900 bytes, for dbserver)
	WavePreviewANLZ  []byte    // raw mono preview (~400 bytes, for ANLZ .DAT file)
	WaveTinyANLZ     []byte    // raw tiny mono preview (100 bytes, for PWV2 in .DAT)
	WaveColorPreview []byte    // raw color preview (~5400 bytes)
	WaveDetail       []byte    // raw color detail waveform, PWV5 format (2 bytes/entry, ~150/sec)
	WaveDetail3Band  []byte    // 3-band detail waveform, PWV7 format (3 bytes/entry: bass,mid,treble; ~150/sec)
	WavePreview3Band []byte    // 3-band preview waveform, PWV6 format (1200 entries × 3 bytes)
	Wave3BandColor   []byte    // 3-band colour metadata, PWVC section body (6 bytes) — imported only, not yet served
	WaveDetailMono   []byte    // raw monochrome detail waveform (1 byte/entry, ~150/sec)
	BeatGrid         []byte    // beat grid blob for 0x2204 response
	BeatGridPQT2     []byte    // PQT2 beat grid blob for 0x2c04 response (complete ANLZ section)
	Beats            []float64 // beat positions in ms (for ANLZ PQTZ generation)
	DownbeatIndex    int       // index into Beats of the first downbeat
	SongStructure    []byte    // PSSI phrase analysis blob for 0x2504 response
	Phrases          []Phrase  // detected phrases (intro/up/down/chorus/outro) — used by API/web UI
	GridEdited       bool      // user manually adjusted the beat grid — serve our blobs, not the on-disk ANLZ
}

// Store holds analysis results keyed by track ID.
// When cacheDir is set, results are persisted to disk using gob encoding,
// keyed by a hash of the track's file path for stable cache identity.
type Store struct {
	mu       sync.RWMutex
	results  map[uint32]*Result
	cacheDir string
	pathMap  map[uint32]string // trackID → filePath for cache key

	statusMu sync.RWMutex
	status   string // current activity (e.g., "analyzing track 5...")
	cached   int    // count of cache hits
	pending  int32  // count of tracks queued for analysis

	// In-flight analysis deduplication.
	inflight   map[uint32]bool
	inflightMu sync.Mutex
}

// NewStore creates an in-memory-only analysis store.
func NewStore() *Store {
	return &Store{
		results: make(map[uint32]*Result),
		pathMap: make(map[uint32]string),
	}
}

// NewStoreWithCache creates a store that persists results to disk.
func NewStoreWithCache(cacheDir string) *Store {
	os.MkdirAll(cacheDir, 0755)
	return &Store{
		results:  make(map[uint32]*Result),
		cacheDir: cacheDir,
		pathMap:  make(map[uint32]string),
	}
}

// Get returns the analysis result for a track, or nil.
// Checks disk cache if not in memory.
func (s *Store) Get(trackID uint32) *Result {
	s.mu.RLock()
	r := s.results[trackID]
	filePath := s.pathMap[trackID]
	s.mu.RUnlock()

	if r != nil {
		return r
	}

	// Try loading from disk cache.
	if s.cacheDir != "" && filePath != "" {
		if cached := s.loadFromDisk(filePath); cached != nil {
			s.mu.Lock()
			s.results[trackID] = cached
			s.mu.Unlock()
			s.incCached()
			log.Printf("analysis-cache: loaded track %d from disk", trackID)
			return cached
		}
	}

	return nil
}

// Set stores an analysis result for a track.
func (s *Store) Set(trackID uint32, r *Result) {
	s.mu.Lock()
	s.results[trackID] = r
	filePath := s.pathMap[trackID]
	s.mu.Unlock()

	// Persist to disk.
	if s.cacheDir != "" && filePath != "" {
		s.saveToDisk(filePath, r)
	}
}

// Status returns a human-readable status line for the monitor UI.
func (s *Store) Status() string {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()

	s.mu.RLock()
	total := len(s.results)
	s.mu.RUnlock()

	pending := atomic.LoadInt32(&s.pending)

	if s.status != "" {
		if pending > 1 {
			return fmt.Sprintf("%s (%d queued, %d tracks, %d cached)", s.status, pending-1, total, s.cached)
		}
		return fmt.Sprintf("%s (%d tracks, %d cached)", s.status, total, s.cached)
	}
	if total > 0 {
		return fmt.Sprintf("Ready (%d tracks, %d cached)", total, s.cached)
	}
	return "Idle"
}

// IncPending increments the pending analysis counter.
func (s *Store) IncPending() {
	atomic.AddInt32(&s.pending, 1)
}

// DecPending decrements the pending analysis counter.
func (s *Store) DecPending() {
	atomic.AddInt32(&s.pending, -1)
}

// SetStatus sets the current activity message.
func (s *Store) SetStatus(msg string) {
	s.statusMu.Lock()
	s.status = msg
	s.statusMu.Unlock()
}

// ClearStatus clears the current activity message.
func (s *Store) ClearStatus() {
	s.statusMu.Lock()
	s.status = ""
	s.statusMu.Unlock()
}

// Pending returns the number of tracks queued for analysis.
func (s *Store) Pending() int32 {
	return atomic.LoadInt32(&s.pending)
}

// Count returns the total number of analyzed tracks in memory.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.results)
}

// CachedCount returns the number of results loaded from disk cache.
func (s *Store) CachedCount() int {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.cached
}

func (s *Store) incCached() {
	s.statusMu.Lock()
	s.cached++
	s.statusMu.Unlock()
}

// AnalyzeInBackground starts analysis for a track if not already in progress.
// The callback is called with the result when analysis completes.
// Never blocks. Safe to call from multiple goroutines/sessions.
func (s *Store) AnalyzeInBackground(trackID uint32, filePath string, onDone func(*Result)) {
	s.inflightMu.Lock()
	if s.inflight == nil {
		s.inflight = make(map[uint32]bool)
	}
	if s.inflight[trackID] {
		s.inflightMu.Unlock()
		return // already in progress
	}
	s.inflight[trackID] = true
	s.inflightMu.Unlock()

	go func() {
		defer func() {
			s.inflightMu.Lock()
			delete(s.inflight, trackID)
			s.inflightMu.Unlock()
		}()

		log.Printf("lazy-analysis: analyzing track %d (%s)...", trackID, filepath.Base(filePath))
		s.SetStatus(fmt.Sprintf("Analyzing: %s", filepath.Base(filePath)))
		r, err := AnalyzeTrack(filePath)
		s.ClearStatus()
		if err != nil {
			log.Printf("lazy-analysis: track %d failed: %v", trackID, err)
			return
		}
		s.Set(trackID, r)
		if onDone != nil {
			onDone(r)
		}
		log.Printf("lazy-analysis: track %d done (BPM=%.1f key=%s dur=%ds)",
			trackID, r.BPM, r.KeyCamelot, r.Duration)
	}()
}

// SetPath associates a track ID with a file path for cache key generation.
// Must be called before Get/Set for disk caching to work.
func (s *Store) SetPath(trackID uint32, filePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pathMap[trackID] = filePath
}

func (s *Store) cacheFile(filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return filepath.Join(s.cacheDir, fmt.Sprintf("%x.gob", h[:8]))
}

// Invalidate drops both the in-memory and on-disk analysis cache for
// `trackID`, so the next Get() forces a fresh analysis. Used by the
// "re-analyze" button in the detail drawer when the user wants to
// recompute beats/key/waveform after editing the source file or
// suspecting a stale result.
func (s *Store) Invalidate(trackID uint32) {
	s.mu.Lock()
	delete(s.results, trackID)
	path := s.pathMap[trackID]
	s.mu.Unlock()
	if path == "" || s.cacheDir == "" {
		return
	}
	cacheFile := s.cacheFile(path)
	if err := os.Remove(cacheFile); err == nil {
		log.Printf("analysis-cache: invalidated track %d (%s)", trackID, cacheFile)
	}
}

// RenameCachedPath moves the on-disk analysis cache from the slot
// keyed by `oldPath` to the one keyed by `newPath` so a path remap
// doesn't force re-analysis. Silently noops when there's no cache.
func (s *Store) RenameCachedPath(oldPath, newPath string) {
	if s == nil || s.cacheDir == "" || oldPath == newPath {
		return
	}
	from := s.cacheFile(oldPath)
	to := s.cacheFile(newPath)
	if from == to {
		return
	}
	if _, err := os.Stat(from); err != nil {
		return
	}
	if err := os.Rename(from, to); err != nil {
		log.Printf("analysis-cache: rename %s -> %s: %v", from, to, err)
	}
}

func (s *Store) saveToDisk(filePath string, r *Result) {
	path := s.cacheFile(filePath)
	f, err := os.Create(path)
	if err != nil {
		log.Printf("analysis-cache: create %s: %v", path, err)
		return
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(r); err != nil {
		log.Printf("analysis-cache: encode %s: %v", path, err)
	}
}

func (s *Store) loadFromDisk(filePath string) *Result {
	path := s.cacheFile(filePath)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var r Result
	if err := gob.NewDecoder(f).Decode(&r); err != nil {
		log.Printf("analysis-cache: decode %s: %v", path, err)
		return nil
	}
	if r.CacheVersion != effectiveCacheVersion() {
		log.Printf("analysis-cache: stale version %d (want %d), discarding %s", r.CacheVersion, effectiveCacheVersion(), filepath.Base(path))
		os.Remove(path)
		return nil
	}
	return &r
}

// AnalyzeTrack decodes and analyzes a single track.
func AnalyzeTrack(filePath string) (*Result, error) {
	samples, err := DecodePCM(filePath, analysisRate)
	if err != nil {
		return nil, err
	}

	beatResult := DetectBeats(samples, analysisRate)
	bpm := beatResult.BPM
	camelot, standard := DetectKey(samples, analysisRate)
	artwork := ExtractArtwork(filePath)
	preview := GeneratePreview(samples, analysisRate)
	previewANLZ := GeneratePreviewANLZ(samples, analysisRate)
	tinyANLZ := GenerateTinyPreviewANLZ(samples)
	colorPreview := GenerateColorPreview(samples, analysisRate)
	detail := GenerateDetail(samples, analysisRate)
	detail3Band := GenerateDetail3Band(samples, analysisRate)
	preview3Band := GeneratePreview3Band(samples, analysisRate)
	detailMono := GenerateDetailMono(detail)
	durationSec := float64(len(samples)) / float64(analysisRate)
	durationMs := durationSec * 1000.0

	// Use beat positions for grid if available, otherwise fall back to BPM + downbeat.
	var beatGrid []byte
	var beatGridPQT2 []byte
	downbeatIdx := 0
	if len(beatResult.Beats) > 0 {
		beatGrid = GenerateBeatGridFromBeats(beatResult)
		// Find downbeat index for PQT2
		for i, b := range beatResult.Beats {
			if b >= beatResult.Downbeat-0.5 {
				downbeatIdx = i
				break
			}
		}
		beatGridPQT2 = GeneratePQT2(bpm, beatResult.Beats, downbeatIdx)
	} else {
		beatGrid = GenerateBeatGrid(bpm, durationMs, 0)
	}

	downbeat := beatResult.Downbeat
	phrases := DetectPhrases(samples, analysisRate, bpm, downbeat)
	AnnotateVocals(samples, analysisRate, bpm, downbeat, phrases)
	SetPhraseTimes(phrases, beatResult.Beats, bpm)
	songStructure := GeneratePSSI(phrases, bpm)

	return &Result{
		CacheVersion:     effectiveCacheVersion(),
		BPM:              bpm,
		Duration:         uint16(durationSec),
		KeyCamelot:       camelot,
		KeyStandard:      standard,
		Artwork:          artwork,
		WavePreview:      preview,
		WavePreviewANLZ:  previewANLZ,
		WaveTinyANLZ:     tinyANLZ,
		WaveColorPreview: colorPreview,
		WaveDetail:       detail,
		WaveDetail3Band:  detail3Band,
		WavePreview3Band: preview3Band,
		WaveDetailMono:   detailMono,
		BeatGrid:         beatGrid,
		BeatGridPQT2:     beatGridPQT2,
		Beats:            beatResult.Beats,
		DownbeatIndex:    downbeatIdx,
		SongStructure:    songStructure,
		Phrases:          phrases,
	}, nil
}

// AnalyzeAll processes all tracks using a worker pool.
// Sets each track's Tempo field from detected BPM.
// Returns a Store with waveform data.
// AnalyzeAll processes all tracks using a worker pool.
// Results are stored in the provided store. If store is nil, a new one is created.
// If progress is non-nil, it is called after each track with (done, total).
func AnalyzeAll(tracks []*pdb.Track, workers int, store *Store, progress func(done, total int)) *Store {
	if store == nil {
		store = NewStore()
	}

	if workers < 1 {
		workers = 1
	}

	type job struct {
		track *pdb.Track
	}

	jobs := make(chan job, len(tracks))
	var wg sync.WaitGroup
	var done int64
	total := len(tracks)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				t := j.track
				result, err := AnalyzeTrack(t.FilePath)
				if err != nil {
					log.Printf("analysis: %s: %v", t.FileName, err)
				} else {
					// Set BPM on track if not already set from tags.
					if t.Tempo == 0 && result.BPM > 0 {
						t.Tempo = uint32(result.BPM * 100)
					}
					// Set duration if not already set.
					if t.Duration == 0 && result.Duration > 0 {
						t.Duration = result.Duration
					}
					// Set key if not already set.
					if t.Key == "" && result.KeyCamelot != "" {
						t.Key = result.KeyCamelot
					}
					// Set artwork ID if artwork was extracted.
					if result.Artwork != nil && t.ArtworkID == 0 {
						t.ArtworkID = t.ID // use track ID as artwork ID
					}

					store.Set(t.ID, result)
					artSize := len(result.Artwork)
					log.Printf("analysis: %s BPM=%.1f key=%s (%s) dur=%ds art=%dB", t.FileName, result.BPM, result.KeyCamelot, result.KeyStandard, result.Duration, artSize)
				}
				if progress != nil {
					n := int(atomic.AddInt64(&done, 1))
					progress(n, total)
				}
			}
		}()
	}

	for i := range tracks {
		jobs <- job{track: tracks[i]}
	}
	close(jobs)
	wg.Wait()

	return store
}
