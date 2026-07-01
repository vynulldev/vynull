# [RESOLVED] CDJ freezes on USB insert with self-generated PDB containing 2+ track rows

**Resolution**: EVERY row in a multi-row data page (tracks, genres, artists, albums, etc.) must be **a multiple of 4 bytes**. The CDJ apparently uses 32-bit-aligned reads; a row that ends on a non-aligned boundary makes subsequent rows misaligned and locks the deck.

Found by binary bisection on the deck. An earlier "176-byte minimum" hypothesis (d9d7ab9) was a coincidence — 176 is a multiple of 4, and the test content happened to round up to 176. The actual rule across all row types:

  Row sizes that FROZE: 11, 30, 166, 170, 173, 175, 233, 235  (all non-multiples-of-4)
  Row sizes that LOADED: 12, 16, 168, 176, 180, 200, 236, 240  (all multiples-of-4)

Fix in commit `534f304` (`pdb: 4-byte alignment for ALL row types`): `tableBuilder.addRow` pads every row to the next 4-byte boundary with trailing zeros. Encoders don't need to know about the constraint.

Original investigation notes follow.

---


I'm building a Go writer for the Pioneer `export.pdb` USB format (Virtual CDJ / rekordbox emulation, in this project (vynull)). My writer produces a PDB that the CDJ freezes on **immediately on USB insert** as soon as the tracks table contains **2 or more rows**. A 1-row tracks table loads fine. Reference exports (rekordcrate's `complete_export/demo_tracks/` fixture, and a fresh rekordbox export the user produced) both load.

Hoping someone has seen this specific shape before.

## Confirmed behavior (CDJ, FAT32 USB)

| Test case | Result |
|---|---|
| rekordcrate's `complete_export/empty/.../export.pdb` (over our USB layout) | ✅ Loads (empty library) |
| rekordcrate's `complete_export/demo_tracks/.../export.pdb` (2 tracks at slots 5,6 with 5 deleted) | ✅ Loads |
| User's rekordbox export, 4 tracks | ✅ Loads |
| Our writer, empty (0 tracks) | ✅ Loads |
| Our writer, 1 track (any combination of metadata) | ✅ Loads |
| Our writer, 1 track + 1 each of artist/album/genre/key/label/playlist/artwork | ✅ Loads |
| Our writer, **2 tracks** (with or without playlists/metadata) | ❌ **Freezes** |
| Our writer, 20 tracks | ❌ **Freezes** |

USB freeze drops a 4-byte `TMP0000.TMP` (just `ABCD`) — a CDJ firmware marker, not useful diagnostic info.

Settings files (`MYSETTING.DAT`, `DJMMYSETTING.DAT`, `DEVSETTING.DAT`, `MYSETTING2.DAT`) and `djprofile.nxs` are ruled out: swapping rekordcrate's empty PDBs onto our USB while keeping our settings + djprofile **loads**.

## Offline-verified about our 2-track output

- `rekordcrate dump-pdb` parses our 2-track PDB without ANY error or warning. The parsed `Track` structs match expected values (correct title, file_path, IDs).
- `rekordcrate list-playlists` correctly lists the playlist and both track titles.
- Structural `pdbdiff` against user's real 4-track export (which loads) shows **zero structural differences** on the tracks data page beyond legitimate data-count diffs (different num_rows, different row offsets because rows are different sizes).
- Per-page comparator against rekordcrate's `demo_tracks` fixture shows the same — only legitimate data differences remain.

## Fixes already applied (each grounded against real exports)

1. **Twenty tables in ascending type order** (0x00..0x13). Earlier versions only emitted 9; CDJ rejected as "no valid database".
2. **Page count packed-bitfield at 0x18-0x1A**: `(num_row_offsets & 0x1FFF) | ((num_rows & 0x07FF) << 13)`. Earlier byte-only encoding made the CDJ see 0 rows per page.
3. **Sentinel page boilerplate**: 5-u32 header at heap (this_page_idx, first_data_page or 0x03FFFFFF, 0x03FFFFFF, 0, 0x1FFF_0000) followed by `0x1FFFFFF8` fill, trailing 20 zero bytes. Sentinel pages with empty heap were rejected.
4. **Empty placeholder page per empty table** allocated as the table's `empty_candidate` (NOT in the chain). Matches what rekordcrate's empty fixture has.
5. **Variable-size row index groups**: `rowsInGroup * 2 + 4` bytes per group (NOT fixed 36-byte slots). Real exports pack indices tightly.
6. **Populated default tables** with content copied byte-verbatim from rekordcrate's empty fixture: Colors (8 std colors), Columns (27 categories in UCS-2 with U+FFFA/FFFB framing), Menu (22 rows), Type12 (17 rows), History (1 row). Without these the CDJ likely can't render its browse menu.
7. **Per-table `trf` split**:
   - User-data tables (tracks, genres, artists, albums, labels, keys, playlist_tree, playlist_entries, artwork): `trf = single bit for the LAST row` (each row added as its own transaction).
   - Bulk-loaded default tables (colors, columns, menu, type12, history): `trf = rpf` (all rows touched at once).
8. **Per-table `tx_row_count`/`tx_row_index` split** with the same convention.
9. **`file_type` at row offset 0x5A derived from file extension** (Mp3=0x01, Flac=0x05, M4a=0x04, Wav=0x0B, Aiff=0x0C). We were hardcoding 0x01 (Mp3) which would mis-label FLAC tracks.
10. **`unknown_id_1` at row offset 0x14 set to a 28-bit FNV-1a hash of the track ID** so values spread across the 0..2^28 range (matching the distribution seen in real exports). We tried `t.ID` first; that landed all values in low buckets which seemed like potential hash-table collision territory.

## What I haven't been able to explain

After all the above, the 2-track tracks data page in our output is **byte-shape-identical** to user's real 4-track tracks data page — same page header structure, same row index layout, same per-row magic/index_shift/bitmask values, same `trf`/`rpf` pattern. The only remaining differences are legitimate data-count differences.

Yet the CDJ freezes.

## Specific question

Is there a known CDJ firmware check on the tracks data page that:
- Is not part of the format parsing (rekordcrate accepts both files), AND
- Triggers ONLY when `num_rows >= 2`, AND
- Is sensitive to something we're getting wrong but I can't see

Things I'm specifically suspicious of but can't confirm:
- The track row's `unknown_id_1` (0x14) may have a specific algorithm — both kaitai and rekordcrate document it as "purpose unknown". We use a hash of track ID; real values look like content-derived hashes (CRC32 of file_path doesn't match; CRC32 of audio file doesn't match; FNV-1a doesn't match).
- The track row's `unknown_id_2` (0x18) is a constant we copy verbatim from real (`0x3D0F7FC7`), but this value differs between exports — maybe it's a CRC of the file or some derived value we need to recompute.
- Possible CRC/checksum somewhere we're not aware of.

## Setup

- Hardware: CDJ, firmware (unknown specific version, current public)
- USB: FAT32, formatted by macOS
- Audio files at `/Contents/...`, ANLZ files at `/PIONEER/USBANLZ/Pxxx/xxxxxxxx/ANLZ000{0,1}.{DAT,EXT,2EX}`
- Settings: byte-verified DEVSETTING.DAT (PIONEER DJ / CDJ / 1.85 with space-padding), MYSETTING/MYSETTING2/DJMMYSETTING
- djprofile.nxs present
- exportExt.pdb optionally present (user confirms not required)

## Tools/code referenced

- Writer: the vynull/pdb package
- Reference: rekordcrate `complete_export/{empty,demo_tracks}/PIONEER/rekordbox/export.pdb`
- Spec used: deepsymmetry rekordbox_pdb.ksy + rekordcrate's Rust structs

Happy to share generated PDB samples (both the 1-row-loads version and the 2-row-freezes version) if useful for comparison.
