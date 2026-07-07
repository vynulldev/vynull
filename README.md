# Vynull

[![CI](https://github.com/vynulldev/vynull/actions/workflows/ci.yml/badge.svg)](https://github.com/vynulldev/vynull/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/vynulldev/vynull?color=ff7714&label=release)](https://github.com/vynulldev/vynull/releases)
[![License: GPLv3](https://img.shields.io/badge/license-GPLv3-ff7714)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/vynulldev/vynull?color=ff7714)](go.mod)
![Platform](https://img.shields.io/badge/platform-linux%20x86--64%20%7C%20arm64-ff7714)

Vynull is a virtual CDJ / rekordbox source and library manager for Linux. It serves music files to CDJs (DJM mixers are also seen on the link) over the Pro DJ Link protocol. Point it at your music collection and CDJs see it as a connected USB drive or rekordbox instance. Includes a browser-based library manager and one-click import from your existing rekordbox library.

## Before you start

This is a hobby project. I built it to learn some Go, poke at my CDJs, and see how far I could get building something real with Claude — further than I expected, as it turns out. It speaks a protocol Pioneer never documented, which I only understand thanks to other people's reverse-engineering work (see [Acknowledgements](#acknowledgements)). Expect bugs and the occasional strange deck behaviour. It is not affiliated with Pioneer DJ / AlphaTheta.

None of it is polished, and it's not meant to be taken as a finished product. There are bugs, some features are half-finished or only work in certain setups, and not everything behaves the way it should. A fair amount of the code is me figuring things out as I went rather than the cleanest or most correct way to do it — you'll find rough edges, inconsistent corners, and the occasional "why is it written like that." I've tried to be honest in the docs about what's solid and what isn't, but treat the whole thing as a work in progress. Bug reports and PRs are welcome if you want to help sand it down.

Use it at your own risk, and back up your rekordbox library before importing anything. I wouldn't run it at a real gig, and if it does something strange to your network or your gear, that's yours to sort out.

## Features

- **Virtual CDJ/Rekordbox** on the Pro DJ Link network (UDP ports 50000/50001/50002)
- **Web UI** — browser-based library manager (`--web`): browse/search, edit metadata, manage cues/tags/playlists, view live players, zoom waveform with beat grid + beat-jump, and configure CDJ settings
- **Browse tracks** on CDJs by artist, album, genre, BPM, key, label, year, remixer, folder
- **Import from rekordbox** — `rekordbox.xml`, an encrypted `master.db`, or a full library-backup `.zip`: tracks, MyTags (with categories), track colors, (smart) playlists, cue points (hot + memory, with colors/loops), ANLZ analysis (waveforms/beat grids/phrases), and artwork
- **Smart playlists** — rekordbox rule sets imported and evaluated live (BPM/key/genre/date/tag/… conditions)
- **DJM mixer awareness** — surfaces DJM channel/master state on the link
- **Native FLAC/WAV/AIFF playback** — CDJ decodes lossless formats directly over NFS
- **Color waveforms** on the CDJ (PWV4 overview + PWV5 scrolling, generated via FFT spectral analysis); honors the CDJ "waveform color" setting (blue / RGB / 3-band) in both the deck and the web UI
- **BPM detection** with dynamic programming beat tracker and multi-ratio correction
- **Key detection** using chromagram analysis (octaves 4-7, Krumhansl-Kessler profiles)
- **Beat grid** generation with QM-DSP-inspired DP tracking, zero-phase scoring, and BPM rounding
- **Phrase/song structure** detection (PSSI format)
- **Cue point management** — save/load via CDJ + add/edit/delete via HTTP API/web UI with color support
- **NFS v2 file server** streams audio to CDJs (with optional FLAC/WAV/AIFF to MP3 transcoding)
- **Lazy analysis** mode for instant startup (tracks analyzed on-demand when CDJs request them); artwork is also extracted lazily
- **Library mode** with no music directory required (add tracks dynamically via HTTP API)
- **Editable metadata** — edit label/remixer/original-artist/mix-name/comment and relink a moved track's file path (library DB only — never touches the audio file); missing files are flagged
- **Analysis API** — access beat grids, waveforms, cue points; manually adjust BPM, phase, downbeat
- **Analysis caching** to disk with versioned cache invalidation
- **CDJ settings** — waveform size/color/overview, key display, track-detail column, root-menu layout via API with live notification to CDJs
- **Tag management** — user-defined tags, tag categories, and track colors via API
- **Remote track loading** via API (auto-adds tracks not in library)
- **USB export** — write a rekordbox-compatible USB structure (PDB + ANLZ + settings) from the library
- **Live monitor** TUI showing connected CDJs, playback state, track history, and analysis status

## Requirements

- Linux (tested on x86_64)
- Go 1.21+
- `ffmpeg` in PATH (for audio decoding)
- Python 3 with the `sqlcipher3` package — **only** to import an encrypted `master.db` or a library-backup `.zip` (it shells out to `tools/rekordbox_dump.py`); XML import and everything else need no Python
- Network interface on the same subnet as CDJs (typically 169.254.x.x link-local)
- Permission to bind the RPC portmapper on **UDP 111** — see below (rekordbox mode does not need it)

### Port 111 (CDJ mode only)

A deck that links to us as a **CDJ-USB source** (`--mode cdj`) finds our NFS mount by
querying the standard RPC portmapper on **UDP 111**, a privileged port (<1024). If we
can't bind it, browsing still works (that's dbserver-only over TCP) but **track loads
fail** — the deck can't locate the file server. The deck chooses port 111; we can't
move it to a higher port.

**rekordbox mode (the default) is unaffected** — for a rekordbox source the deck queries
Pioneer's non-standard port **50111**, which is unprivileged, so it works without any
extra setup. Prefer `--mode rekordbox` unless you specifically need to appear as a CDJ.

If you do run `--mode cdj`, grant port 111 with **one** of (no need to run the whole app
as root):

```bash
# Best: lower the privileged-port threshold (system-wide, survives rebuilds)
sudo sysctl -w net.ipv4.ip_unprivileged_port_start=111

# Or: grant just this binary the capability (re-run after every `go build`)
sudo setcap 'cap_net_bind_service=+ep' ./vynull

# Or: redirect 111 → our unprivileged 50111 listener
sudo iptables -t nat -A PREROUTING -i eth1 -p udp --dport 111 -j REDIRECT --to-ports 50111

# Or simply run with sudo
```

The Makefile wraps the run commands: `make run MUSIC=<dir>` starts the default
rekordbox mode, and `make run-cdj MUSIC=<dir>` starts CDJ mode (which needs
port 111 granted as above).

## Quick Start

### Build

```bash
make build
```

### Library Mode (recommended)

Start with no music directory. Add tracks dynamically via the HTTP API:

```bash
./vynull --interface eth1
```

This defaults to `--mode rekordbox`, which needs no privileged ports. Use
`--mode cdj` to appear as a CDJ-USB source instead (requires UDP 111 — see
Requirements).

Then add tracks:

```bash
# Add a single file
curl -X POST http://localhost:9443/api/tracks/add \
  -d '{"paths":["/path/to/track.mp3"]}'

# Add an entire directory (use find)
find /path/to/music -name "*.mp3" -o -name "*.flac" -o -name "*.m4a" | \
  jq -R -s 'split("\n") | map(select(length > 0)) | {paths: .}' | \
  curl -X POST http://localhost:9443/api/tracks/add -d @-
```

Tracks are analyzed in the background and cached to `~/.vynull/`.

### Web UI

Add `--web` to serve a browser-based library manager alongside the JSON API.
By default it listens on `127.0.0.1:9443`; use `--listen 0.0.0.0:9443` to reach
it from another device on the LAN.

```bash
sudo ./vynull --interface eth1 --mode rekordbox --web --listen 0.0.0.0:9443
# then open http://<this-machine>:9443/
```

The UI provides a sortable/searchable library table, an editable track-detail
drawer (metadata, cues, tags, file path, zoom waveform with beat grid +
beat-jump), a live PLAYERS view of connected CDJs/mixers, playlist + tag
management, USB export, and a SETTINGS panel that pushes CDJ display settings
live.

### Import from rekordbox

Import an existing rekordbox library — a `rekordbox.xml` export, an encrypted
`master.db`, or a full library-backup `.zip`:

```bash
# XML export
curl -X POST http://localhost:9443/api/import/rekordbox \
  -F 'file=@/path/to/rekordbox.xml'

# Encrypted master.db or a library-backup .zip (pass the 64-hex SQLCipher
# key for your own database)
curl -X POST http://localhost:9443/api/import/rekordbox \
  -F 'file=@/path/to/backup.zip' -F 'key=<64-hex sqlcipher key>'
```

A `.zip`/`.db` import brings in tracks, MyTags (with their categories), track
colors, (smart) playlists, cue points (hot + memory), ANLZ
analysis (waveforms + beat grids + phrases), and artwork. The web UI's LIBRARY
tab has an import button for the same flow.
Reading an encrypted `master.db` requires Python 3 with the `sqlcipher3`
package (it shells out to `tools/rekordbox_dump.py`).

### Directory Mode

Scan a music directory at startup:

```bash
sudo ./vynull --interface eth1 --music-dir /path/to/music --lazy-analysis
```

### Rekordbox USB Mode

If you have an existing rekordbox-exported USB drive:

```bash
sudo ./vynull --interface eth1 --music-dir /media/usb --mode cdj
```

### Generate USB Structure

Create a rekordbox-compatible USB structure from a music directory:

```bash
./vynull --generate /media/usb --music-dir /path/to/music
```

## Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--interface` | (required) | Network interface (e.g., `eth1`) |
| `--music-dir` | (optional) | Music directory to scan at startup |
| `--mode` | `rekordbox` | Emulation mode: `rekordbox` or `cdj` (cdj needs UDP 111 — see Requirements) |
| `--device-number` | `17` (rb) / `3` (cdj) | Device number on the network |
| `--device-name` | auto | Name shown on CDJs |
| `--data-dir` | `~/.vynull` | Directory for analysis cache, cue points, settings, library DB |
| `--web` | `false` | Serve the browser UI alongside the JSON API |
| `--tui` | `true` | Show the interactive terminal monitor; `--tui=false` runs headless (logs to stdout, for systemd/nohup/non-TTY) |
| `--listen` | `127.0.0.1:9443` | API + web listen address (use `0.0.0.0:9443` to expose on the LAN) |
| `--log-level` | `info` | `error`, `warn`, `info`, `debug`, or `trace` (trace adds per-packet hex dumps) |
| `--log-file` | (auto) | Append logs to this file (default: a temp file while the TUI is shown, or stdout when headless) |
| `--history-file` | (auto) | Append each completed play to one rolling history file (default: `<data-dir>/history.<ext>`) |
| `--history-format` | `text` | Track-history format: `text`, `csv`, or `json` (JSONL — one object per line) |
| `--lazy-analysis` | `false` | Analyze tracks on-demand instead of at startup |
| `--transcode` | `false` | Transcode FLAC/WAV/AIFF to MP3 (default: serve natively) |
| `--settings` | `<data-dir>/settings.json` | Path to the JSON CDJ settings config |
| `--import-settings` | | Import rekordbox MYSETTING/DEVSETTING `.DAT` files from a `/PIONEER` dir, then exit |
| `--generate` | | Generate rekordbox USB structure at this path (no server) |
| `--copy-files` | `false` | Copy files when generating USB (default: symlink) |
| `--export-playlist` | | With `--generate`, export only this playlist (by name) |
| `--replay` | | Replay recorded response packets |

## HTTP API

The API server runs on `http://127.0.0.1:9443`.

### Endpoints

#### `GET /api/status`

Returns device status, connected peers, and player states.

#### `GET /api/tracks`

Returns all tracks in the library with metadata.

#### `POST /api/tracks/add`

Add tracks to the library. Accepts absolute file paths.

```json
{
  "paths": [
    "/path/to/track1.mp3",
    "/path/to/track2.flac"
  ]
}
```

Response:

```json
{
  "status": "ok",
  "added": 2,
  "total": 15
}
```

#### `POST /api/load`

Remotely load a track on a CDJ. If `file_path` is provided and the track isn't in the library, it's automatically added and analyzed.

```json
{
  "track_id": 5,
  "device_number": 1
}
```

Or by file path:

```json
{
  "file_path": "/path/to/track.mp3",
  "device_number": 2
}
```

#### `GET /api/peers`

Returns connected Pro DJ Link devices.

#### `GET /api/players`

Returns CDJ player states (playing, BPM, track, etc.).

#### `GET /api/history`

Returns track play history.

### Library & Import Endpoints

#### `POST /api/import/rekordbox`

Import a rekordbox library. `multipart/form-data` with a `file` field — a
`rekordbox.xml`, an encrypted `master.db`, or a library-backup `.zip`. The
`key` field carries the 64-hex SQLCipher key, required for the encrypted
`master.db`/`.zip` forms (XML needs none). Imports tracks, MyTags
(+ categories), colors, (smart) playlists, cue points, ANLZ analysis, and
artwork; returns counts plus a `files_missing` warning for tracks whose audio
isn't present on this machine.

#### `PUT /api/tracks/{trackID}/metadata`

Edit free-text metadata in the library DB only (never the audio file). Send any
subset of `label`, `remixer`, `original_artist`, `mix_name`, `comment`.

#### `PUT /api/tracks/{trackID}/path`

Repoint a track at a new file path (e.g. relink a moved file). Updates the
library DB only, rekeys the analysis cache so existing waveforms/beat grid
follow the track, and re-checks file presence.

```json
{"path": "/new/location/track.flac"}
```

#### `POST /api/library/remap-paths`

Bulk-rewrite a file-path prefix across the whole library (e.g.
`Z:/Music/` → `/media/usb/Music/`), rekeying analysis caches.

```json
{"from": "Z:/Music/", "to": "/media/usb/Music/"}
```

#### `POST /api/export`

Write a rekordbox-compatible USB export (PDB + ANLZ + settings) from the
library. `POST /api/export/preview` returns what would be written.

### Analysis Endpoints

#### `GET /api/analysis/{trackID}`

Returns complete analysis data: beat grid, BPM, key, duration, cue points. Triggers on-demand analysis if data is missing or stale.

```json
{
  "track_id": 12345,
  "bpm": 130.0,
  "key": "5A",
  "key_standard": "Cm",
  "duration_sec": 450,
  "beat_count": 947,
  "downbeat_index": 2,
  "beats": [{"time_ms": 58.8, "beat_in_bar": 3}, ...],
  "cues": [{"number": 1, "type": "cue", "time_ms": 4975, "color_id": 2}, ...]
}
```

#### `GET /api/analysis/waveform/{trackID}?type=detail|preview|color_preview`

Returns waveform data as JSON. Triggers on-demand analysis if data is missing or stale.

- `detail` — decoded PWV5 (150 entries/sec with RGB+height). Add `&style=3band`
  for the CDJ-3000 3-band representation (`{bass,mid,high}`) when available.
- `color_preview` — decoded PWV4 (1200 entries with bass/mid/treble)
- `preview` — raw PWAV bytes

#### `GET /api/analysis/waveform-png/{trackID}?type=detail&color=rgb|blue|3band&overview=full|half&w=280&h=56`

Returns a pre-rendered PNG (disk-cached) — used by the web UI's row thumbnails.
`color` and `overview` mirror the CDJ waveform-color / overview settings.

#### `GET /api/artwork/{trackID}`

Returns the track's cover art (JPEG), extracted lazily from the file on first
request and cached.

#### `POST /api/analysis/beatgrid/adjust`

Manually adjust the beat grid. All operations persist to the analysis cache and regenerate beat grid blobs.

```json
// Shift all beats earlier by 25ms
{"track_id": 12345, "offset_ms": -25.0}

// Set BPM to exactly 130 (rebuilds grid keeping same phase)
{"track_id": 12345, "bpm": 130.0}

// Set beat 1 at specific position (shifts grid + sets downbeat)
{"track_id": 12345, "set_downbeat_ms": 4975.0}
```

### Cue Point Endpoints

#### `GET /api/tracks/{trackID}/cues`

Returns all cue points for a track.

#### `POST /api/tracks/{trackID}/cues`

Add or update a cue point.

```json
{
  "number": 1,
  "type": "cue",
  "time_ms": 4975,
  "loop_ms": -1,
  "color_id": 2,
  "label": "Drop"
}
```

Color IDs: 0=default, 1=pink, 2=red, 3=orange, 4=yellow, 5=green, 6=aqua, 7=blue, 8=purple.

#### `DELETE /api/tracks/{trackID}/cues/{number}`

Delete a cue point by number (1=A, 2=B, 3=C, ...).

### Tag Endpoints

#### `GET /api/tags`

Returns all user-defined tags.

#### `POST /api/tags`

Create a new tag.

```json
{"name": "Peak Time"}
```

#### `PUT /api/tags/{id}`

Rename a tag.

```json
{"name": "Warm Up"}
```

#### `DELETE /api/tags/{id}`

Delete a tag (removes from all tracks).

#### `GET /api/tracks/{trackID}/tags`

Returns tags assigned to a track.

#### `POST /api/tracks/{trackID}/tags`

Set tags for a track (replaces all existing).

```json
{"tag_ids": [1, 3]}
```

#### `GET /api/tracks/{trackID}/color`

Returns the track's color ID.

#### `POST /api/tracks/{trackID}/color`

Set a track's color.

```json
{"color_id": 4}
```

#### Tag categories

`GET/POST /api/tag-categories` and `PUT/DELETE /api/tag-categories/{id}` manage
MyTag categories (Genre, Mood, …). Create a tag inside a category with
`POST /api/tags {"name": "...", "category_id": N}`.

### Playlist Endpoints

#### `GET /api/playlists`

Returns the playlist/folder tree (regular and smart playlists).

#### `POST /api/playlists`

Create a playlist, folder, or smart playlist. A smart playlist carries a
rule set (combinator + conditions) that's evaluated live against the library.

```json
{
  "name": "Peak Time",
  "is_smart": true,
  "rules": {"combinator": "all", "conditions": [
    {"field": "bpm", "operator": "between", "value": [126, 132]},
    {"field": "tag", "operator": "has", "value": "Energy"}
  ]}
}
```

#### `PUT /api/playlists/{id}` · `DELETE /api/playlists/{id}` · `GET /api/playlists/{id}/tracks`

Rename/reparent/edit-rules, delete, and list a playlist's (resolved) tracks.

### Settings Endpoints

#### `GET /api/settings`

Returns current CDJ display settings.

```json
{
  "waveform_size": "full",
  "waveform_color": "rgb",
  "waveform_position": "center",
  "key_display": "alphanumeric",
  "track_detail": "artist"
}
```

#### `POST /api/settings`

Update CDJ display settings. Only include fields you want to change. Waveform settings are pushed to connected CDJs via the settings notification protocol.

```json
{"waveform_color": "3band", "track_detail": "bpm"}
```

Values:
- `waveform_size`: `"full"` or `"half"`
- `waveform_color`: `"blue"`, `"rgb"`, or `"3band"`
- `waveform_position`: `"left"` or `"center"`
- `key_display`: `"classic"` (Am, Eb) or `"alphanumeric"` (8A, 5B)
- `track_detail`: field shown next to track titles when browsing on CDJ

Track detail values:

| Value | Displayed on CDJ |
|-------|-----------------|
| `"artist"` | Artist name (default) |
| `"bpm"` | BPM and key (e.g., "126.0 bpm - 6A") |
| `"key"` | Key and BPM (e.g., "6A - 126.0 bpm") |
| `"album"` | Album name |
| `"genre"` | Genre name |
| `"label"` | Record label |
| `"bitrate"` | Bitrate in kbps |
| `"time"` | Track duration |
| `"rating"` | Star rating |
| `"color"` | Track color name |
| `"comments"` | Comment text |
| `"original_artist"` | Original artist |
| `"remixer"` | Remixer name |
| `"dj_play_count"` | Play count |
| `"date_added"` | Date added (YYYY-MM-DD) |
| `"none"` | No secondary field |

## Architecture

```
vynull/
  main.go              CLI entrypoint, flag parsing, orchestration
  config.go            Config struct, defaults, validation
  proto/               Packet encoding/decoding
    header.go          Magic bytes, common header
    announce.go        Announcement/keep-alive packets (UDP 50000)
    status.go          CDJ/mixer status + refresh-trigger packets (UDP 50002)
    cdj_status.go      Parse incoming CDJ status (play state, BPM, track)
    dbmessage.go       Dbserver binary message encoding
  device/              Virtual device lifecycle
    device.go          Startup sequence, keep-alive loop, settings notification
    peers.go           Track connected CDJs/mixers
    monitor.go         Live player-state tracking + track history
    tui.go             Terminal monitor UI (players/library/settings tabs)
    settings.go        CDJ settings persistence (DEVSETTING/MYSETTING)
    settings_schema.go Settings config that drives *SETTING.DAT + the web UI
    settings_import.go Parse rekordbox *SETTING.DAT files on import
  dbserver/            TCP metadata server (port 12523 + dynamic)
    server.go          Listener, session handling, message framing, replay
    handler.go         Message dispatcher, menu/metadata responses
    render.go          0x3000 Render dispatcher + pending-item paging
    categories.go      Top-level category lists (artists/albums/BPM/key/…)
    drilldown.go       Multi-level drill + search (post-category)
    menuitem.go        menuItem type, sort dispatch, track→item conversion
    playlist.go        Playlist / folder / history menu handlers
    track.go           Track detail + analysis (info/waveform/beat grid/PVB2/cues)
    cuepoints.go       Cue point save/load/delete with wire format synthesis
  nfs/                 Minimal NFS v2 server (read-only)
    portmap.go         Portmapper
    mount.go           Mount protocol
    server.go          NFS v2 GETATTR/LOOKUP/READ/READDIR (path traversal protected)
    rpc.go             Sun RPC / XDR framing (bounds-checked)
    flac.go            Transparent FLAC/WAV/AIFF to MP3 transcoding
  analysis/            Audio analysis
    analysis.go        Pipeline, versioned cache, background workers, on-demand reanalysis
    bpm.go             DP beat tracker, BPM estimation, phase alignment
    beatgrid.go        Beat grid generation (PQTZ/PQT2 formats)
    waveform.go        PWV4/PWV5 color waveform + monochrome generation
    pqt2.go            PQT2 extended beat grid format
    pvb2.go            PVB2 VBR seek index from the audio frame map (ffprobe)
    pvbr.go            PVBR/PVB2 seek-index section builders
    fft.go             Radix-2 Cooley-Tukey FFT
    fft_batch.go       Parallel-CPU batch FFT
    phrase.go          Song structure / phrase detection (PSSI)
    vocal.go           Lightweight per-phrase vocal-presence detection
    key.go             Musical key detection (chromagram + Pearson correlation)
    decode.go          FFmpeg PCM audio decoding
    render.go          Waveform preview → PNG rendering (web UI)
    anlz.go            ANLZ file format (.DAT/.EXT/.2EX)
    import_anlz.go     Reconstruct analysis from rekordbox ANLZ files on import
    import_cues.go     Parse cue points from rekordbox ANLZ on import
    artwork.go         Artwork extraction (embedded tags via ffmpeg)
  library/             Music library (thread-safe)
    scanner.go         Directory walk, tag reading
    track.go           Track struct (matches rekordbox PDB fields)
    index.go           In-memory indices, persistence, sequential IDs
    artwork.go         Artwork cache
    thumbnail.go       JPEG thumbnail resizing (oversized CDJ artwork)
    decode_check.go    ffmpeg decode-health check (flags CDJ-freezing files)
    import_xml.go      Import a rekordbox.xml export (tracks, tags, colors)
    import_masterdb.go Import an encrypted master.db / backup .zip (via rekordbox_dump.py)
  api/                 HTTP API server
    api.go             REST endpoints, analysis/cue/waveform/settings/import APIs
    fs.go              Sandboxed filesystem browser for "add files/folders"
    diag.go            Log ring buffer + diagnostic endpoints (status/logs/stats)
    web.go             //go:embed of the single-page web UI (served at GET /)
    web/index.html     Browser-based library manager (UI)
    cueadapter.go      Bridge between API cue types and dbserver cue store
    tagstore.go        User-defined tags, tag categories, track colors (batched saves)
    playliststore.go   Playlist/folder tree + smart-playlist persistence
    smart_playlist.go  Smart-playlist rule model + live evaluation
    smart_import.go    Translate rekordbox SmartList rules → app rules
    menustore.go       CDJ root-menu layout + track-detail column config
  pdb/                 Pioneer database format
    pdb.go             PDB parser
    folders.go         Folder/playlist tree
    settings.go        MYSETTING/DEVSETTING generation
    artwork.go         Artwork USB path scheme + table rows
    defaults.go        Default rows every export needs (colors/columns/menu)
    writer.go          export.pdb file writer
    writer_ext.go      exportExt.pdb extension-table writers (NXS2+ data)
  internal/
    fsutil/atomic.go   Atomic file writes (temp + fsync + rename) for the stores
    netutil/netutil.go Interface discovery, broadcast addr
  tools/
    rekordbox_dump.py  Reads an encrypted rekordbox master.db (SQLCipher) for import
```

## Protocol Details

### Network Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 50000 | UDP | Device announcement and keep-alive |
| 50001 | UDP | Beat packets |
| 50002 | UDP | CDJ status, media queries, settings, link activation |
| 12523 | TCP | Database server discovery (responds with dynamic port) |
| (dynamic) | TCP | Database server (OS-assigned ephemeral port) |
| 50111 | UDP | NFS portmapper |
| 2049 | UDP | NFS v2 file server |
| (dynamic) | UDP | NFS mount protocol |
| 9443 | TCP | HTTP API server |

### Menu Category Routing

The CDJ derives query message types from root menu item IDs (`0x1000 + ID`):

| ID | Category | Message Type |
|----|----------|-------------|
| 1 | Genre | 0x1001 |
| 2 | Artist | 0x1002 |
| 3 | Album | 0x1003 |
| 4 | Track | 0x1004 |
| 6 | BPM | 0x1006 |
| 7 | Rating | 0x1007 |
| 8 | Year | 0x1008 |
| 9 | Remixer | 0x1009 |
| 10 | Label | 0x100a |
| 11 | Original Artist | 0x100b |
| 12 | Key | 0x1014 |

### Waveform Formats

- **PWAV**: Mono preview waveform (dbserver: 200x2 height+whiteness + 200x2 + 100 + footer; ANLZ .DAT: 400 entries)
- **PWV4**: Color preview waveform (1200 entries x 6 bytes: d0/d1=steepness, d2=low-half energy, d3=bass(R), d4=mid(G), d5=treble(B)). CDJ derives height: back=max(d3,d4,d5), front=d5. Color: each byte * 255 / max. Values are globally normalized (0-255 relative to track peak).
- **PWV5**: Color detail waveform (150 points/sec, 2 bytes each, 16-bit big-endian: `RRRGGGBBBHHHHH00`)
- **PWV6**: 3-band preview waveform (1200 entries x 3 bytes: mid, high, low). CDJ-3000 format, in .2EX files.
- **PWV7**: 3-band detail waveform (150 points/sec x 3 bytes: mid, high, low). CDJ-3000 format, in .2EX files.
- **Mono detail**: Non-color scrolling waveform (150 points/sec, 1 byte each: `brightness3 height5`)

### Track Info Title ID (Audio Decoder Selection)

The track info response title item ID tells the CDJ which audio decoder to use:

| ID | Decoder | Formats |
|----|---------|---------|
| 1 | MP3 | MP3 |
| 4 | AAC | M4A/AAC |
| 5 | Lossless compressed | FLAC |
| 11 (0x0b) | Raw PCM | WAV |
| 12 (0x0c) | Raw PCM | AIFF |

### Cue Points

Cue points are saved and loaded via the dbserver protocol:

- **Write**: CDJ sends type `0x2705` with 76-byte cue blob (LE, time in ms at offset 0x0c)
- **Read**: CDJ requests type `0x2b04`, server responds with `0x4e02` containing concatenated cue blobs
- **API-created cues**: Synthesized 76-byte blobs matching CDJ wire format
- **Persistence**: Raw blobs saved to `<data-dir>/cues/` alongside parsed JSON metadata

### Settings Notification Protocol

When settings are changed via the API:

1. Server sends `0x4a` (settings notification) to CDJ
2. CDJ responds with `0x46` (link ack)
3. Server sends `0x47` (link activation) with DEVSETTING bytes embedded at offset 0x30

DEVSETTING layout (6 bytes): `[unknown, overview_type, waveform_color, unknown, key_display, wave_position]`

## Supported Audio Formats

| Format | Direct NFS | Transcoded (with `--transcode`) |
|--------|-----------|------|
| MP3 | Yes | - |
| M4A/AAC | Yes | - |
| FLAC | Yes | to MP3 |
| WAV | Yes | to MP3 |
| AIFF | Yes | to MP3 |

By default, all formats are served natively. FLAC/WAV/AIFF use the appropriate decoder ID in the track info response so the CDJ selects the correct decoder. Pass `--transcode` to convert lossless formats to MP3 if needed.

## Acknowledgements

This wouldn't exist without the people who reverse-engineered Pioneer's Pro DJ Link protocol and rekordbox's file formats and then published what they found. Most of what this project knows, it learned from their work:

- **[Deep Symmetry](https://deepsymmetry.org/)** (James Elliott and contributors) — by far the biggest source. The [dysentery](https://github.com/Deep-Symmetry/dysentery) research and its [protocol writeup](https://djl-analysis.deepsymmetry.org/), the [beat-link](https://github.com/Deep-Symmetry/beat-link) library, [crate-digger](https://github.com/Deep-Symmetry/crate-digger), and the Kaitai specs (`rekordbox_pdb.ksy`, `rekordbox_anlz.ksy`) are behind most of the protocol, PDB, and ANLZ code here.
- **[rekordcrate](https://github.com/Holzhaus/rekordcrate)** (Jan Holthuis) — Rust parsers and reference exports I leaned on to get the `export.pdb` writer byte-for-byte right.
- **[python-prodj-link](https://github.com/flesniak/python-prodj-link)** (Florian Sniak) — a working Python implementation that helped me understand the dbserver menu and metadata flow.
- **[pyrekordbox](https://github.com/dylanljones/pyrekordbox)** (Dylan Jones) — format docs and the starting point for decrypting `master.db`.
- The Kaitai Struct community for the format definitions, and everyone who posted issues and helped along the way.
- **Audio-analysis algorithms** — the beat tracker is an independent implementation of D. Ellis's dynamic-programming method (*"Beat Tracking by Dynamic Programming"*, J. New Music Research, 2007); key detection uses the Krumhansl-Kessler probe-tone profiles (1982). These are published academic methods — the same DP beat-tracking approach is also implemented by [QM-DSP](https://github.com/c4dm/qm-dsp) and [Mixxx](https://github.com/mixxxdj/mixxx), which are worth a look, though the code here derives from the papers rather than those projects.

A few things aren't covered by the documented specs — the on-the-wire settings handshake, NXS2 cue writes, and some of the waveform encodings — and were worked out independently; the settings *file-format* field schema and the ANLZ section framing still follow the documented specs above. Without the projects listed here there'd have been nowhere to start. Thanks to everyone listed.

This is an independent, unofficial project. It isn't affiliated with or endorsed by Pioneer DJ / AlphaTheta, Deep Symmetry, or any of the projects listed here.

## License

Vynull is free software licensed under the **GNU General Public License v3.0**
(GPLv3) — see [`LICENSE`](LICENSE). You're free to use, study, share, and
modify it under those terms; derivative works must also be GPLv3.

Copyright (C) 2026 the Vynull authors.

A few small portions derive from third-party reverse-engineering work and carry
their own upstream provenance (noted inline in the source):

- Default PDB table bytes (`pdb/defaults.go`) are rekordbox's own export output,
  captured by [rekordcrate](https://github.com/Holzhaus/rekordcrate) (MPL-2.0).
- The PSSI obfuscation constant (`analysis/phrase.go`) is a format constant from
  Deep Symmetry's Kaitai spec ([crate-digger](https://github.com/Deep-Symmetry/crate-digger),
  EPL-2.0 with an LGPL-3.0 secondary option).
- File-format layouts and protocol field offsets throughout are interoperability
  facts documented by the projects in [Acknowledgements](#acknowledgements).

The original code in this project is GPLv3; the items above are noted for
provenance and interoperability.
