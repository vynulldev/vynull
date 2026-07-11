# Metadata for externally-sourced tracks (Level 2)

- **Status:** the metadata source is now the **NFS/PDB download** (packages
  `nfs` client + `mediadb`, wired to the monitor). The earlier dbserver client
  (package `dbclient`) is retained but unwired: hardware showed CDJs refuse it
  because we use a rekordbox player number (17), not 1–4 — see "Hardware
  finding" below. The NFS path sidesteps that entirely and is what beat-link's
  CrateDigger and prolink-connect use. Needs a deck to confirm the CDJ's export
  layout (port lookup, export name, mount/read chain), but the whole client is
  unit-tested against our own `nfs.Server`.
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

- **N1 — NFS client** *(done)*. `nfs.Client` (same package as our server, reusing
  its XDR codec + constants): portmap GETPORT -> MOUNT MNT -> NFS LOOKUP -> chunked
  READ, plus MOUNT EXPORT discovery. `pdb.OpenBytes` parses the downloaded
  database in memory. The whole chain is unit-tested against our own `nfs.Server`
  handlers (no sockets). Needs a deck to confirm the CDJ's portmap port and
  export name.
- **N2 — fetch/cache + wire to the monitor** *(done)*. `mediadb.Fetcher` downloads
  `export.pdb` once per (player, slot), caches the parsed `*pdb.Database`, and
  answers `TrackByID` for the monitor's `ExternalMeta`. Async, non-blocking,
  serves a stale copy while refreshing; failures cool down 30s.
- **N3 — artwork** *(pending)*. Download `/PIONEER/Artwork/...` over the same NFS
  client and serve it, so the player card and now-playing overlay show the real
  cover instead of a colliding local ID.
- **N4 — (optional) analysis** *(pending)*. Fetch the track's ANLZ `.DAT`/`.EXT`
  for beat grid / cues / waveform on external tracks.

### Known limitations (need a deck)

- The export-name -> slot mapping is best-effort: `FetchExportPDB` tries the
  advertised MOUNT EXPORT list, then `/C/`, `/B/`, `/`. If a deck has both a USB
  and an SD with databases, both slot keys currently resolve to the first export
  found. Fixable once we see a real deck's export list per slot.

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
