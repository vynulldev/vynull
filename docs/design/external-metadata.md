# Metadata for externally-sourced tracks (Level 2)

- **Status:** Proposed
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

- **A — spike the client (the risk).** db-port lookup + handshake + a single
  `0x2002` request, parse title/artist/BPM/key for one external track. This is
  the uncertain part; validate on hardware before building further.
- **B — fetch/cache + wire to the monitor.** External `PlayerState` fills in;
  the PLAYERS view / TUI / overlay show the real track.
- **C — artwork.** Fetch + serve cover art for external tracks.
- **D — (optional) analysis.** Beat grid / cues / waveform for external tracks
  (reuses ANLZ requests) so the player card shows the real waveform + cues.

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
