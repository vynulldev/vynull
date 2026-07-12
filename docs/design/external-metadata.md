# Metadata for externally-sourced tracks (Level 2)

- **Status:** **working on hardware (2026-07-11).** The metadata source is the
  **NFS/PDB download** (packages `nfs` client + `mediadb`, wired to the monitor):
  a CDJ playing from its own USB now shows real title/artist/key. The earlier
  dbserver client (package `dbclient`) is retained but unwired: hardware showed
  CDJs refuse it because we use a rekordbox player number (17), not 1–4 — see
  "Hardware finding" below. The NFS path sidesteps that entirely and is what
  beat-link's CrateDigger and prolink-connect use.
- **Key wire detail:** a CDJ encodes every NFS/MOUNT path string (export names,
  LOOKUP filenames) in **UTF-16LE** — ASCII gets ACCES/STALE. This mirrors our
  own server, which already decodes the UTF-16LE names a CDJ sends it.
- **Scope:** live monitor / now-playing — fetch metadata for tracks a deck plays
  from a source other than us
- **Prereq:** Level 1 (shipped) — the monitor detects external tracks
  (`TrackDevice != SelfDevice`) and shows the *source* instead of a wrong,
  ID-colliding title. This doc scopes filling in the *real* metadata.

## Goal

When a deck loads a track from a **USB/SD, or another player** (its own media or
a linked rekordbox/CDJ), show the actual track — title, artist, BPM, key, and
artwork — in the web PLAYERS view, the TUI, and the now-playing overlay. Today
those cases show "USB · player 2" because the track's rekordbox ID is meaningless
in our database.

The metadata lives on the **source player's own dbserver**. This is exactly what
Deep Symmetry's beat-link does: act as a dbserver *client*, query the player that
owns the media, and render the returned metadata. We already implement the
dbserver *server* and have the `proto` packet structs, so the request/response
framing is familiar — but the client handshake and menu-render flow are new.

## What it involves

1. **Find the source player's dbserver port.** Connect TCP to the player's
   **port 12523** (the db-port lookup service) and read back the actual dbserver
   port (commonly 1051, but must be queried, not assumed). We already know each
   player's IP from the peer tracker.
2. **Client connection + handshake.** Open a persistent TCP connection to
   `ip:dbPort`, send the initial preamble + a menu-setup request carrying our
   player number. The dbserver is stateful: one request at a time, an
   incrementing transaction id, and a render-menu request/footer envelope around
   each query.
3. **Metadata request.** Send `0x2002` for `(targetPlayer, slot, trackType,
   trackID)`; parse the response, a sequence of menu-item rows (title, artist,
   album, genre, BPM, key, duration, colour, comment…), into a metadata struct.
   Slot comes from `TrackSlot` (USB=3, SD=2…), which we already parse.
4. **Fetch/cache manager.** Keyed by `(player, slot, trackID)`; the monitor
   enqueues a fetch when it sees a new external track, asynchronously (never
   block the status handler), and fills `PlayerState` when the result lands.
   Cache per load; drop on track change; handle players joining/leaving and
   connection drops/reconnects.
5. **Artwork.** The metadata carries an `artworkID`; fetch the image via the
   artwork request and cache it, then serve it (e.g. `/api/artwork/ext/...`) so
   the overlay's vinyl label and the player card can show it.

## Phases

### dbserver client (parked — refused by CDJs, see Hardware finding)

- **A — the client** *(done, unwired)*. db-port lookup + handshake +
  `0x2002`/`0x3000` render + parse the `0x4101` detail rows. Codec unit-tested;
  live setup is refused on hardware.
- **B — fetch/cache + wire to the monitor** *(done, unwired)*. `dbclient.Fetcher`.

### NFS/PDB download (the working path)

- **N1 — NFS client** *(done, hardware-confirmed)*. `nfs.Client` (same package as
  our server, reusing its XDR codec + constants): portmap GETPORT -> MOUNT MNT ->
  NFS LOOKUP -> chunked READ, plus MOUNT EXPORT discovery, all path strings in
  UTF-16LE. `pdb.OpenBytes` parses the downloaded database in memory. The chain
  is unit-tested against our own `nfs.Server` handlers (no sockets) and verified
  on a real deck (portmap :111 -> mount/nfs, export `/C/`, 233 KB export.pdb).
- **N2 — fetch/cache + wire to the monitor** *(done, hardware-confirmed)*.
  `mediadb.Fetcher` downloads `export.pdb` once per (player, slot), caches the
  parsed `*pdb.Database`, and answers `TrackByID` for the monitor's
  `ExternalMeta`. Async, non-blocking, serves a stale copy while refreshing;
  failures cool down 30s.
- **N3 — artwork** *(done)*. The PDB reader now parses the artwork table
  (`Database.Artwork`: ArtworkID -> real on-USB JPEG path). `mediadb.Fetcher.Artwork`
  downloads that JPEG over the same NFS client and negative-caches misses; the
  API serves it at `/api/artwork/ext/{player}/{slot}/{trackID}` (wired via
  `Server.ExtArtwork`), and the PLAYERS card shows a cover thumbnail
  (`PlayerInfo.ArtworkURL`; local art keeps using `/api/artwork/{id}`). Needs a
  deck to confirm the artwork path resolves on real media. The now-playing
  overlay reuse is cross-branch (lands when now-playing-overlay merges).
- **N4 — analysis** *(done)*. `analysis.ParseANLZBytes` / `ParseANLZCuesBytes`
  parse ANLZ from memory; `mediadb.Fetcher.Analysis` downloads a track's
  `.DAT`/`.EXT`/`.2EX` (via the PDB `AnalyzePath`) over the same NFS client and
  parses the colour detail waveform + cues, cached per (player, slot, trackID).
  Served at `/api/analysis/ext/{player}/{slot}/{trackID}` (via `Server.ExtAnalysis`);
  the PLAYERS card renders the real waveform and cue markers instead of
  "WAVEFORM UNAVAILABLE" (`PlayerInfo.AnalysisURL`). Needs a deck to confirm the
  ANLZ path resolves on real media. The now-playing overlay reuses this (waveform
  per deck) since the overlay merged. A beat-grid / zoom view for external tracks
  would need a dedicated inspector rather than the library-track detail drawer
  (which is built around a library track object and its deck-control features),
  so it is deferred as its own feature, not a loose end.

### Media slots

- The export-name -> slot mapping is confirmed on a CDJ-2000NXS2: USB(3)->/C/,
  SD(2)->/B/. `mediadb` passes `FetchExportPDB` the matching export per slot,
  tried before the advertised MOUNT EXPORT list and the common roots, so a
  player with both a USB and an SD resolves each to the right pdb instead of
  collapsing both onto the first export found.
- A CD (slot 1) carries no rekordbox export (the deck's MOUNT EXPORT list is
  empty and every mount is refused), so there is nothing to fetch. We skip the
  fetch entirely for the CD slot and fall back to the "CD · player N" source
  label, avoiding pointless NFS churn during CD playback.

## Hardware finding: the dbserver-client path is blocked by our player number

Three on-deck runs (2026-07-11) show the client path is **correct on the wire
but refused at the application layer**. The db-port lookup on 12523 works
(returns 1051), the deck echoes our handshake, and our setup message is
byte-perfect per the protocol (magic, txid `0xfffffffe`, type `0x0000`, one
int32 arg). The deck accepts the handshake and then **closes the connection at
setup** (`setup: EOF`).

Root cause, confirmed against Deep Symmetry's analysis: **CDJs only serve
dbserver metadata to devices using a standard player number 1-4.** We announce
as device **17** (rekordbox range), so the deck refuses. This is the exact wall
beat-link documents; its two escapes are:

1. `setUseStandardPlayerNumber()` - masquerade as an unused 1-4 number. Only
   possible with fewer than four real CDJs on the network, and it means our
   device number briefly leaves the rekordbox range (which our *server* role
   uses), so the two roles conflict.
2. **CrateDigger** - ignore the dbserver entirely and download the rekordbox DB
   export (`export.pdb`) plus ANLZ files from the player's **NFSv2** server.
   Works regardless of our player number, and hands us the full PDB (metadata,
   artwork, beat grid, cues, waveforms) in one shot - a superset of what the
   dbserver menu flow returns. This is the path prolink-connect and beat-link
   both settled on. We already have deep PDB and NFS knowledge (we serve both),
   so we would be building the NFS *client* side.

**Decision:** the dbserver client (Phases A-B) stays as-is behind the async
fetcher (it degrades cleanly to the "USB - player N" Level-1 fallback and does
no harm), but the metadata source we invest in next is the **NFS/PDB download**
(supersedes Phase C artwork and Phase D analysis too). See the new NFS-client
scope below.

The `0x00`-prefixed length framing and exact `0x4101` item-type codes remain
unvalidated but are moot unless we later masquerade as a 1-4 device.

## Risks / notes

- The client handshake and menu-render envelope are fiddly and only partly
  documented; dysentery / beat-link are the references. Expect protocol
  debugging.
- Stateful, one-request-at-a-time connections need careful serialization.
- Source players vary (older CDJ vs CDJ-with-USB vs a linked rekordbox laptop vs
  another Vynull); behaviour may differ per source.
- **Requires hardware to develop and validate** (a CDJ with a USB stick, or two
  linked players) — it touches nothing until a real external source is present,
  so it can't regress the normal path, but it also can't be verified without a
  deck.

## Package shape (proposed)

A self-contained `link/prolink/dbclient` (mirroring the server under
`link/prolink`): connection + handshake, request builders, a menu-row response
parser, and a small fetch/cache manager the monitor calls. Roughly 400-600 lines
plus hardware debugging; a spike (Phase A) first to de-risk the handshake.
