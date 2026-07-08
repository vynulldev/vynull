# Modular backends: a brand-neutral core

- **Status:** Partly implemented; sequence reprioritized (see Status)
- **Scope:** architecture / refactor
- **Related:** [import-layer.md](import-layer.md) — refines/reprioritizes step 5

## Status (2026-07)

Steps 1–2 have shipped: `core` exists, and `analysis` is split into pure DSP
(`core.Analysis`) plus the Pioneer encoders under `link/prolink`. The rest has
been **reordered** from the sequence below:

- **3d (neutral analysis cache / encode-on-serve)** and **steps 3–4** (move
  device/dbserver under `link/prolink`, define `core.Backend`, decouple `api`)
  are **deferred**: they change the live serving path and only pay off with a
  second protocol, which can't be validated without Denon/StageLinq hardware.
- **Step 5 (library formats)** was **pulled forward** and is the productive axis
  now — Traktor import has shipped. Its design lives in
  [import-layer.md](import-layer.md), which **revises this doc on one point**:
  the import adapter lands in `library` (`library.Importer` + `ImportBundle`),
  not `core`, because real imports carry rich rekordbox-shaped side-data (tags,
  cues, colours, ANLZ assets, smart-playlist rules) the thin `core.Importer`
  never modelled. That thin `core.Importer` from step 1 is slated for deletion;
  it can be lifted back to `core` if a non-library consumer ever appears.

## Goal

Serve multiple live link protocols (Pro DJ Link today, Denon StageLinq/EAAS
next) and read/write multiple library formats (rekordbox today; Engine, Serato,
Traktor next) from **one brand-neutral core**. Everything on the wire or in a
file becomes a pluggable adapter, not a fork.

## Package layout

```
core/                   neutral domain + interfaces (no wire, no file formats, no DSP)
  track.go              Track, Playlist, TrackID, DurationSec
  cue.go                Cue, CueKind, Key
  analysis.go           Analysis, Beat, BandWaveform  — DSP *results*, brand-agnostic
  player.go             PlayerState, MixerState, Event
  backend.go            Backend, Source interfaces
  format.go             Library, Importer, Exporter interfaces

analysis/               PURE DSP → produces core.Analysis (no PWV/ANLZ/PDB)
  bpm.go beatgrid.go key.go waveform.go onset.go cues.go ...

link/                   live protocol backends (each implements core.Backend)
  prolink/              Pro DJ Link — today's stack, regrouped
    device.go           VirtualDevice: claim / announce / keepalive / status   (was device/)
    dbserver/           TCP menu / query server                                (was dbserver/)
    nfs/                NFS file transport                                      (was nfs/)
    anlz/               ANLZ / PWV4 / PWV5 / PQT / PVB encoders (core.Analysis → bytes)
    pdb/                export.pdb writer                                       (was pdb/)
    proto/              packet structs                                          (was proto/)
  stagelinq/            Denon — future
    device.go           StageLinq discovery + StateMap + BeatInfo (via go-stagelinq)
    eaas/               EAAS library server + HTTP audio transport
    engine/             Engine Library SQLite writer + zlib/qCompress BLOB codecs

format/                 library import/export (each implements core.Importer / Exporter)
  rekordbox/            XML, master.db, backup .zip (in) + PDB / USB (out)  — reuses link/prolink/{pdb,anlz}
  engine/  (future)     Engine Library SQLite                              — reuses link/stagelinq/engine
  serato/ traktor/ m3u/ (future)

api/                    HTTP + web UI + stores (cue/tag/playlist/menu) — depends on core only
cmd/ (or main.go)       wiring: choose backend(s) + importers/exporters, inject into api + TUI
```

**Dependency rule**
- `core` imports nothing from `analysis` / `link` / `format` / `api`. Everyone imports `core`.
- `api` and the TUI import `core` **only** — never a concrete backend.
- Pioneer codecs (`anlz`, `pdb`) live under `link/prolink` and are shared by
  `format/rekordbox` (rekordbox files *are* Pioneer's format). Same pattern for Engine.

## Core interfaces

```go
// Source = the neutral data a Backend serves TO players.
type Source interface {
    Track(TrackID) (*Track, bool)
    Tracks() []*Track
    Playlists() []*Playlist
    Analysis(TrackID) (*Analysis, bool)              // beatgrid, waveforms, cues, key
    Open(TrackID) (io.ReadSeekCloser, int64, error)  // audio bytes; transport streams this
}

// Backend = a pluggable live link/source protocol.
type Backend interface {
    Name() string                                  // "prolink" | "stagelinq"
    Start(ctx context.Context, src Source) error   // announce + serve until ctx cancels
    Players() []PlayerState
    Mixers()  []MixerState
    Load(deck int, id TrackID) error
    Events() <-chan Event                           // player/beat/state changes for UI + TUI
}
```

`Analysis` is brand-agnostic; each backend **encodes** it to its own wire format
(Pioneer PWV/ANLZ vs Engine zlib BLOB). Same idea for `Importer`/`Exporter`.

## The one genuine refactor

Today `analysis.Result` mixes DSP results with Pioneer-encoded blobs
(`WaveDetail` PWV5, `BeatGridPQT2`, `SongStructure` PSSI, …). Split it:

- Keep the DSP (BPM, beatgrid, key, waveform band-data, cues) in `analysis`,
  returning `core.Analysis`.
- Move the encoders (`anlz.go`, `pqt2.go`, `pvb2.go`, `render.go`, PWV packers)
  to `link/prolink/anlz`, taking `core.Analysis` → bytes.
- The analysis cache stores neutral `core.Analysis`; each backend encodes on demand.

Then decouple `api` from its thin Pioneer surface (it only touches `device` for
settings/VirtualDevice and `dbserver` for cues): move cues into `core`, put CDJ
settings behind a `core.Settings` interface (no-op for StageLinq), and `api`
imports `core` only.

## Migration order (each a shippable PR; 1–4 are pure refactors)

1. **Introduce `core`** with the model types + interfaces (this doc's PR). Purely
   additive — nothing imports it yet, zero behavior change. **✅ done.**
2. **Split `analysis`** into DSP (→ `core.Analysis`) vs encoders (→ `anlz`).
   Highest-value cleanup; unblocks everything. **✅ done** (encoders now under `link/prolink`).
3. **Move `proto/device/dbserver/nfs/pdb` under `link/prolink`**, define + implement `core.Backend`. **⏸ deferred** (see Status).
4. **Decouple `api`** from `device`/`dbserver` via `core` interfaces. **⏸ deferred.**
5. **Extract library formats** behind `Importer`/`Exporter`. **➡️ in progress** as `library.Importer` — see [import-layer.md](import-layer.md).
6. `stagelinq` spike + `format/engine` slot in with no core changes. **⬜ future.**

Steps 1–2 were pure refactors, fully testable against current hardware, and have
landed. Steps 3–4 remain pure refactors but are on hold until a second protocol
is worth validating (see Status) — they leave the code cleaner even if a second
protocol never ships. The end-state is unchanged: running two backends at once
means the library appears to CDJs *and* Denon players simultaneously.
