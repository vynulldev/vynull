# CDJ menu configuration

Controls which categories the connected CDJs show in their root LINK
menu, and in what order. Mirrors rekordbox's "Preferences →
Categories" panel.

## UI

Settings tab → **Configure CDJ Menu…** opens a modal with two lists:

- **Inactive Categories** (left) — present but not shown on the deck.
- **Active Categories** (right) — surfaced in the CDJ root menu, in
  the order shown.

Move items between lists with the arrows; reorder within the active
list with **Up** / **Down**. Cmd/Ctrl-click extends the selection.
**Reset to Defaults** restores the factory layout (which matches
rekordbox's defaults).

### Locked categories

Five categories are *locked* on (greyed in the active list, can't be
moved to inactive): `TRACK`, `PLAYLIST`, `HISTORY`, `SEARCH`,
`FOLDER`. rekordbox treats these the same way — they're always
present in the deck's menu.

### Track-list detail column

The dropdown at the top of the modal sets which field renders in the
right-hand column of every CDJ track list (BPM, Key, Color, etc.).
Independent of category configuration; affects every menu that lists
tracks.

## Default category list

In order, on a fresh install (post-reset matches this set):

| Active        | Locked? | Wire ID | ItemType |
|---------------|---------|---------|----------|
| ARTIST        |         |  2      | 0x81     |
| ALBUM         |         |  3      | 0x82     |
| TRACK         | ✓       |  4      | 0x83     |
| KEY           |         | 12      | 0x8b     |
| PLAYLIST      | ✓       |  5      | 0x84     |
| HISTORY       | ✓       | 22      | 0x95     |
| SEARCH        | ✓       | 18      | 0x91     |
| FOLDER        | ✓       | 13      | 0x8d     |
| BPM           |         |  6      | 0x85     |
| LABEL         |         | 10      | 0x89     |
| YEAR          |         |  8      | 0x87     |
| COLOR         |         | 15      | 0x8e     |
| FILE NAME     |         | 21      | 0x94     |
| HOT CUE BANK  |         | 23      | 0x98     |
| RATING        |         |  7      | 0x86     |
| TIME          |         | 19      | 0x92     |

Inactive by default (matches rekordbox's "Inactive" list):

| Inactive          | Wire ID | ItemType |
|-------------------|---------|----------|
| BITRATE           | 20      | 0x93     |
| GENRE             |  1      | 0x80     |
| MATCHING          | 26      | 0xaa     |
| ORIGINAL ARTIST   | 11      | 0x8a     |
| REMIXER           |  9      | 0x88     |

Wire IDs / ItemTypes verified against rekordbox's root-menu
response in a packet capture.

**DATE ADDED** is intentionally omitted — rekordbox doesn't
surface it to the CDJ in any capture we have, and we don't know its
wire opcode. If someone later figures it out, add an entry to
`defaultMenuItems` in `api/menustore.go`.

## Wire ID known ≠ category works

Surfacing a category in the root menu is just the visible-name step.
For the CDJ to actually browse *into* it (returning a list of values
or tracks), the dbserver also has to implement that category's
drill-in handler. The "advanced" categories are visibly different
from a track listing:

- **HISTORY** — separate playlist-like menu populated from played
  tracks. Now wired: the drill-in handlers (`0x1016` list / `0x1116`
  session, in `dbserver/handler.go`) surface our per-day in-app
  history playlist (`appendToHistoryPlaylist` in `main.go`).
- **SEARCH** — opens a search text box on the deck and queries by
  prefix. Needs a streaming-search dbserver handler.
- **HOT CUE BANK** — view of saved hot-cue banks; we don't store them
  yet.
- **MATCHING** — rekordbox's compatible-track recommender.

These show up in the root menu list. HISTORY is implemented; SEARCH,
HOT CUE BANK, and MATCHING still return nothing until their dbserver
handlers exist — filed as follow-up work.

## Persistence + wire

Items are persisted to `<data-dir>/menu.json`. Edits propagate to
connected CDJs on their next root-menu poll (≈1s) — no restart or
explicit "push" needed.

Server-side authoritative fields (Label / ID / ItemType / Locked) are
preserved across `PUT /api/menu-items`; the UI can only toggle
`visible` and reorder.

## Related

- `column.png` reference shot — the track-detail dropdown options.
- `sort.png` reference shot — rekordbox's sort-options config (same
  two-list pattern, not yet implemented here).
