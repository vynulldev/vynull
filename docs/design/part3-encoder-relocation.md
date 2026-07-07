# Part 3 plan — relocate the Pioneer encoders (modular-backends step 2, final half)

## Goal / end state

- **`analysis/`** = pure DSP only. `AnalyzeTrack` produces a neutral
  `core.Analysis` (tempo, beats, key, band waveforms, phrases, cues) — no
  Pioneer bytes.
- **`link/prolink/anlz/`** = every Pioneer wire encoder (PWV4/5/6/7, ANLZ
  sections, PQT2, PVB, PSSI, beat-grid blob), consuming `core.Analysis` (+ raw
  samples where a format needs its own band pass).
- **Cache** stores neutral `core.Analysis`; the prolink backend encodes to
  Pioneer bytes (cached per-backend or on demand).
- **Serving path** (`dbserver`/`api`/`proto`) gets encoded bytes from the
  prolink backend, not from `analysis.Result`.

## What's actually coupled today (facts)

- **Encoders** live in `analysis/waveform.go` (PWV4/5/6/7 + mono/tiny previews),
  `analysis/anlz.go` (ANLZ section writers, PVB2), `analysis/pqt2.go`,
  `analysis/beatgrid.go`, `analysis/pvbr.go`, and `analysis/phrase.go`
  (`GeneratePSSI` — sits next to the pure-DSP `DetectPhrases`).
- **Shared low-level DSP** used by both encoders and the neutral extraction:
  `fft` (`fft.go`), `butterworthLow/High`, `applyBiquad`, `biquadCoeffs`,
  `splitBandsAndPeaks`, `computeHeights` (all in `waveform.go`).
- **`AnalyzeTrack` calls the encoders 11×** → `analysis` currently *owns*
  encoding. This is the dependency to break.
- **`Result` is gob-cached** (`%x.gob`) with the encoded blobs embedded —
  encoding happens at analyze-time, and the bytes are what's cached.
- **~14 serving-path read sites** across `dbserver`/`api`/`proto` read
  `Result.{BeatGrid×14, BeatGridPQT2×7, WaveDetail×5, WaveColorPreview×5,
  WavePreview×4, WaveDetailMono×4, WaveDetail3Band×4, SongStructure×4,
  WavePreview3Band×2}`.
- **Not encoders:** `render.go` (`RenderPreviewPNG`) is the *web-UI PNG* renderer,
  neutral-ish — leave it in `analysis` (or later move to `api`); it's out of
  scope for the prolink relocation.

## Key challenges (why this is the risky half)

1. **Dependency inversion.** If encoders move to `prolink` while `AnalyzeTrack`
   still calls them, `analysis → prolink` — backwards. So encoding must move out
   of `AnalyzeTrack` and become a prolink-side step.
2. **Shared low-level DSP.** `fft`/`biquad`/`splitBandsAndPeaks` can't live in a
   package that `prolink` shouldn't depend on wholesale — extract them so both
   pure DSP and the encoders can use them.
3. **Cache flip.** Moving encoding to serve-time means the cache stores neutral
   `core.Analysis` instead of Pioneer blobs (cacheVersion bump; existing caches
   re-analyze once — harmless).
4. **Band-representation mismatch.** Each encoder computes its *own* bands
   (PWV4 uses 8th-order filters at 1200 pts; PWV5 uses `splitBandsAndPeaks` at
   150/s; PWV6/7 use different scales). There is no single neutral waveform all
   four consume, so encoders keep taking `samples` for their own band pass — the
   neutral `core.Analysis.Detail/Overview` is a *convenience* for new backends,
   not a forced input for the Pioneer ones.
5. **Byte-exactness.** The golden tests (PWV4/5/6/7) guard the four colour
   waveforms; PVB/PQT2/ANLZ/PSSI have **no golden test yet** — add those before
   moving them.

## Staged sub-PRs

Each is shippable and golden-guarded; risk rises at 3c.

### 3a — extract shared low-level DSP into a `dsp` package  *(low risk)*
Move `fft`, `butterworth*`, `applyBiquad`, `biquadCoeffs`, `splitBandsAndPeaks`,
`computeHeights` into `analysis/dsp` (or top-level `dsp`). Update `analysis`
(incl. `bandwaveform.go`) and the encoders to import it. Pure move — golden
tests + full suite still pass. Verifiable by build + golden hashes.

### 3b — add golden tests for the remaining blobs  *(test-only, low risk)*
Lock `GeneratePQT2`, `GenerateBeatGrid[FromBeats]`, `GeneratePSSI`, the ANLZ
section writers, and PVB against the deterministic signal (extend
`waveform_golden_test.go`'s pattern). This completes the safety net before the
move.

### 3c — move the Pioneer encoders into `link/prolink/anlz`  *(structural, byte-safe)*
Move the encoder functions (keeping `samples`/inputs → bytes signatures so the
bytes are identical), importing `dsp` + `core`. Move the golden tests with them.
`GeneratePSSI` splits from `DetectPhrases` (DSP stays, encoder moves). Build +
golden hashes verify byte-for-byte.

### 3d — flip the flow: neutral cache + serve-time encoding  *(the risky one — HW validate)*
- `AnalyzeTrack` stops calling encoders; caches neutral `core.Analysis`
  (+ artwork). `cacheVersion` bump.
- The **prolink backend** gains an encode layer: `core.Analysis` (+ samples as
  needed) → the Pioneer blobs, cached per-track on the prolink side.
- Repoint the ~14 serving-path reads: `dbserver`/`api`/`proto` request bytes
  from the prolink backend instead of `analysis.Result`.
- Delete the encoded fields from `Result` (or retire `Result` in favour of
  `core.Analysis` + a prolink `Encoded` struct).
- **Golden tests still guard the bytes, but the wiring change needs a real-CDJ
  smoke test:** load a track on a deck and confirm waveform + beat grid +
  cues + phrases render, on at least one NXS2 and (if available) a CDJ-3000
  (for the 3-band PWV6/7 path).

### 3e — (optional) make encoders consume `core.Analysis`  *(cleanup)*
Where an encoder's bands match the neutral `Detail`/`Overview` (PWV5 especially),
have it take `core.Analysis` instead of recomputing from samples — deduping the
double band pass. Golden-guarded.

## Recommended sequencing

- **PR A = 3a + 3b + 3c** — all byte-safe structural work (extract DSP, finish
  golden net, move encoders). No serving-path or cache change; golden hashes
  prove zero byte drift. Mergeable with confidence.
- **PR B = 3d** — the flow flip + cache change, on its own, **with the hardware
  smoke test as the merge gate.** This is where a deck must be in the loop.
- **PR C = 3e** — optional dedupe cleanup, later.

## Open decisions

- **Cache granularity:** prolink caches encoded blobs per-track (fast serve,
  more disk) vs encodes on demand from cached `core.Analysis` (less disk, CPU on
  first serve). Suggest per-track encoded cache to match today's serve latency.
- **`Result`'s fate:** keep a slimmed neutral `Result` (metadata + `core.Analysis`)
  or replace it outright with `core.Analysis`. Prefer replacing, with a prolink
  `EncodedTrack` for the blobs.
- **`render.go`:** leave in `analysis` for now; a later pass can move web-PNG
  rendering to `api` since it consumes `core` band data, not Pioneer bytes.

## Verification checklist (every PR)

- `go build ./...`, `go vet`, `gofmt -l` clean.
- `go test ./analysis/... ./link/...` incl. **all golden hashes unchanged**.
- PR B only: **real-CDJ smoke test** (waveform / beat grid / cues / phrases on a
  deck) before merge.
