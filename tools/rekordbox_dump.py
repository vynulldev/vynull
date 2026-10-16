#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-3.0-or-later
"""Dump rekordbox 6 master.db to JSON for Go import.

Usage:
    rekordbox_dump.py <master.db> <key> > out.json

The key is the 64-hex SQLCipher decryption key for your own rekordbox
database, and is REQUIRED: this tool ships no key and does not extract one.
rekordbox does not surface the key in its UI — recover it from your own
rekordbox install with a third-party key-recovery tool, then pass it here.
The key is verified by actually opening the database.

Output schema (JSON):

    {
      "tracks":    [{ id, title, artist, album, genre, key, label,
                      file_path, file_size, bpm, duration_sec,
                      bitrate, sample_rate, rating, year, track_num,
                      disc_num, comment, play_count, date_added,
                      color_id, analyze_path, image_path }, ...],
      "playlists": [{ id, parent_id, name, is_folder, track_ids: [...] }, ...],
      "tags":      [{ id, name, parent_id, track_ids: [...] }, ...]
    }
"""

import json
import os
import sys
import xml.etree.ElementTree as ET
import sqlcipher3


def _norm_key(k):
    k = (k or "").strip().lower()
    if k.startswith("0x"):
        k = k[2:]
    return k


def open_db(db_path, provided_key):
    """Open the SQLCipher database with the user-supplied key, or exit with a
    clear error if the key is missing/invalid or doesn't decrypt the DB.

    rekordbox uses the 64-hex string as a *passphrase* (SQLCipher runs PBKDF2
    over the ASCII text), NOT a raw key — so it's PRAGMA key="<hex>", never the
    raw-key form PRAGMA key="x'<hex>'" (which derives a different key)."""
    key = _norm_key(provided_key)
    if len(key) != 64 or not all(c in "0123456789abcdef" for c in key):
        sys.stderr.write("FATAL: a 64-hex SQLCipher key is required "
                         "(pass it as the second argument)\n")
        sys.exit(1)
    conn = sqlcipher3.connect(db_path)
    try:
        cur = conn.cursor()
        cur.execute(f'PRAGMA key="{key}"')
        cur.execute("SELECT count(*) FROM sqlite_master")  # forces decryption
        cur.fetchone()
        return conn
    except Exception:
        try:
            conn.close()
        except Exception:
            pass
        sys.stderr.write(f"FATAL: could not decrypt {db_path} with the "
                         "provided key\n")
        sys.exit(1)


def parse_smartlist(xml_str, tags_by_id):
    """Parse a rekordbox djmdPlaylist.SmartList XML blob into a structured
    rule object. rekordbox encodes a smart playlist as:

        <NODE LogicalOperator="1|2" ...>
          <CONDITION PropertyName="bpm" Operator="3" ValueUnit="0"
                     ValueLeft="13500" ValueRight="0"/> ...
        </NODE>

    LogicalOperator: 1=AND, 2=OR. Operator codes (1=equal, 2=not-equal,
    3=greater, 4=less, 5=in-range, 6=in-last, 7=not-in-last, 8=contains,
    9=not-contains, 10=starts-with, 11=ends-with) are mapped on the Go side.
    `myTag` conditions store the tag ID as a SIGNED int32; djmdMyTag.ID is its
    unsigned form — resolve to the tag NAME here (only the DB knows the map).
    BPM values are ×100 (handled Go-side)."""
    try:
        root = ET.fromstring(xml_str)
    except Exception:
        return None
    conds = []
    for cnd in root.findall("CONDITION"):
        a = cnd.attrib
        prop = a.get("PropertyName", "")
        left = a.get("ValueLeft", "")
        if prop == "myTag" and left:
            try:
                uid = str(int(left) & 0xFFFFFFFF)  # signed int32 → unsigned key
                left = tags_by_id.get(uid, left)
            except ValueError:
                pass
        conds.append({
            "property": prop,
            "operator": int(a.get("Operator", "0") or 0),
            "unit":     a.get("ValueUnit", ""),
            "left":     left,
            "right":    a.get("ValueRight", ""),
        })
    return {
        "logical": int(root.attrib.get("LogicalOperator", "1") or 1),
        "conditions": conds,
    }


def main():
    if len(sys.argv) < 2:
        sys.stderr.write(__doc__)
        sys.exit(2)
    db_path = sys.argv[1]
    # The key may be passed as the second argument, or (for the conformance
    # harness, which does not want it on the command line) in VYNULL_REKORDBOX_KEY.
    provided = sys.argv[2] if len(sys.argv) >= 3 else os.environ.get("VYNULL_REKORDBOX_KEY", "")
    if not provided:
        sys.stderr.write(__doc__)
        sys.exit(2)

    conn = open_db(db_path, provided)
    cur = conn.cursor()

    out = {"tracks": [], "playlists": [], "tags": [], "cues": []}

    # Tracks
    cur.execute("""
        SELECT c.ID, c.Title, c.FolderPath, c.FileNameL, c.FileSize, c.BPM,
               c.Length, c.BitRate, c.SampleRate, c.Rating, c.ReleaseYear,
               c.TrackNo, c.DiscNo, c.Commnt, c.DJPlayCount, c.DateCreated, c.StockDate,
               c.ColorID, c.AnalysisDataPath, c.ImagePath,
               c.FileType, c.HotCueAutoLoad,
               a.Name AS Artist, al.Name AS Album, g.Name AS Genre,
               k.ScaleName AS Key, l.Name AS Label,
               c.GenreID, c.ArtistID, c.AlbumID, c.KeyID, c.LabelID,
               rmx.Name AS Remixer, oa.Name AS OrigArtist,
               c.RemixerID, c.OrgArtistID,
               c.Subtitle AS MixName, aa.Name AS AlbumArtist, cp.Name AS Composer
        FROM djmdContent c
        LEFT JOIN djmdArtist a ON c.ArtistID = a.ID
        LEFT JOIN djmdAlbum al ON c.AlbumID = al.ID
        LEFT JOIN djmdGenre g ON c.GenreID = g.ID
        LEFT JOIN djmdKey k ON c.KeyID = k.ID
        LEFT JOIN djmdLabel l ON c.LabelID = l.ID
        LEFT JOIN djmdArtist rmx ON c.RemixerID = rmx.ID
        LEFT JOIN djmdArtist oa ON c.OrgArtistID = oa.ID
        LEFT JOIN djmdArtist aa ON al.AlbumArtistID = aa.ID
        LEFT JOIN djmdArtist cp ON c.ComposerID = cp.ID
        WHERE COALESCE(c.rb_local_deleted, 0) = 0
    """)
    cols = [d[0] for d in cur.description]
    for row in cur.fetchall():
        d = dict(zip(cols, row))
        # rekordbox stores BPM × 100; Length in ms? Check schema.
        bpm = (d.get("BPM") or 0) / 100.0 if d.get("BPM") else 0
        # File path: FolderPath is a "/Users/.../Music/..." style native path
        # (may include Windows drive letters). Use as-is — Go-side caller
        # may need to translate to USB layout.
        out["tracks"].append({
            "id":           d["ID"],
            "title":        d.get("Title") or "",
            "artist":       d.get("Artist") or "",
            "album":        d.get("Album") or "",
            "genre":        d.get("Genre") or "",
            "key":          d.get("Key") or "",
            "label":        d.get("Label") or "",
            "file_path":    d.get("FolderPath") or "",
            "file_name":    d.get("FileNameL") or "",
            "file_size":    d.get("FileSize") or 0,
            "bpm":          bpm,
            "duration_sec": d.get("Length") or 0,
            "bitrate":      d.get("BitRate") or 0,
            "sample_rate":  d.get("SampleRate") or 0,
            # rekordbox FileType enum (1=mp3, 4=m4a, 5=flac, 11=wav, 12=aiff).
            # Authoritative for codec/compat flags; the file extension can lie
            # (rekordbox stores .wav-named files as FileType 1, say).
            "file_type_id": d.get("FileType") or 0,
            # Non-empty when the deck should auto-load this track's hot cues.
            "hot_cue_auto_load": d.get("HotCueAutoLoad") or "",
            "rating":       d.get("Rating") or 0,  # rekordbox stores the 0-5 star count directly
            "year":         d.get("ReleaseYear") or 0,
            "track_num":    d.get("TrackNo") or 0,
            "disc_num":     d.get("DiscNo") or 0,
            "comment":      d.get("Commnt") or "",
            "play_count":   d.get("DJPlayCount") or 0,
            # StockDate is rekordbox's "Date Added" (TRACK INFO, the DATE
            # ADDED menu); DateCreated is often empty.
            "date_added":   d.get("StockDate") or d.get("DateCreated") or "",
            "date_created": d.get("DateCreated") or "",  # smart rules' dateCreated
            # ColorID is stored as TEXT ("0".."8"); coerce to the palette int.
            "color_id":     int(d.get("ColorID") or 0),
            # rekordbox lookup IDs (djmd* row IDs), so served category rows can
            # carry rekordbox's IDs instead of hashed names. Strings, like `id`.
            "genre_id":     str(d.get("GenreID") or ""),
            "artist_id":    str(d.get("ArtistID") or ""),
            "album_id":     str(d.get("AlbumID") or ""),
            "key_id":       str(d.get("KeyID") or ""),
            "label_id":     str(d.get("LabelID") or ""),
            "remixer":        d.get("Remixer") or "",
            "original_artist": d.get("OrigArtist") or "",
            # Smart playlist text fields: mixName (Subtitle), albumArtist (the
            # album's artist) and producer (ComposerID).
            "mix_name":       d.get("MixName") or "",
            "album_artist":   d.get("AlbumArtist") or "",
            "composer":       d.get("Composer") or "",
            "remixer_id":     str(d.get("RemixerID") or ""),
            "orig_artist_id": str(d.get("OrgArtistID") or ""),
            # Paths are relative to the rekordbox `share/` root, e.g.
            # /PIONEER/USBANLZ/<bucket>/<uuid>/ANLZ0000.DAT and
            # /PIONEER/Artwork/<bucket>/<uuid>/artwork.jpg. The Go caller
            # resolves them against the extracted backup's share dir.
            "analyze_path": d.get("AnalysisDataPath") or "",
            "image_path":   d.get("ImagePath") or "",
        })

    # Playlists. Attribute: 0=playlist, 1=folder, 4=smart playlist (rule-based,
    # no static membership). Negative values are system entries (e.g. the
    # Cloud Library Sync trial) — skip them. Seq orders siblings as the user
    # arranged them in rekordbox.
    cur.execute("""
        SELECT ID, Name, ParentID, Attribute, SmartList, Seq
        FROM djmdPlaylist
        ORDER BY Seq
    """)
    pl_rows = cur.fetchall()
    # Song-to-playlist mappings
    cur.execute("""
        SELECT PlaylistID, ContentID, TrackNo
        FROM djmdSongPlaylist
        ORDER BY PlaylistID, TrackNo
    """)
    pl_tracks = {}
    for plid, cid, _ in cur.fetchall():
        pl_tracks.setdefault(str(plid), []).append(cid)

    # Tag ID → name, used to resolve `myTag` smart-playlist conditions.
    tags_by_id = {}
    try:
        cur.execute("SELECT ID, Name FROM djmdMyTag")
        tags_by_id = {str(tid): nm for tid, nm in cur.fetchall()}
    except sqlcipher3.dbapi2.OperationalError:
        pass

    for pid, name, parent, attr, smartlist, seq in pl_rows:
        if attr is not None and attr < 0:
            continue  # system playlist (cloud-sync trial etc.)
        entry = {
            "id":        pid,
            "name":      name,
            "parent_id": parent,
            "is_folder": attr == 1,
            "is_smart":  attr == 4,
            "seq":       int(seq) if seq is not None else 0,
            "track_ids": pl_tracks.get(str(pid), []),
        }
        if attr == 4 and smartlist:
            entry["smart"] = parse_smartlist(smartlist, tags_by_id)
        out["playlists"].append(entry)

    # MyTags + assignments
    try:
        cur.execute("SELECT ID, Name, ParentID FROM djmdMyTag")
        tag_rows = cur.fetchall()
        cur.execute("SELECT MyTagID, ContentID FROM djmdSongMyTag")
        tag_tracks = {}
        for tagid, cid in cur.fetchall():
            tag_tracks.setdefault(str(tagid), []).append(cid)
        for tid, name, parent in tag_rows:
            out["tags"].append({
                "id":        tid,
                "name":      name,
                "parent_id": parent,
                "track_ids": tag_tracks.get(str(tid), []),
            })
    except sqlcipher3.dbapi2.OperationalError:
        pass  # Older DBs may not have MyTag tables

    # Tag List membership (djmdSongTagList). Distinct from djmdSongMyTag: this
    # is the "Tag List" the deck reflects as the song-tag flag (menu-row
    # argument 7 bit 0), so it is dumped separately from MyTag assignments.
    out["tag_list"] = []
    try:
        cur.execute("SELECT ContentID FROM djmdSongTagList")
        out["tag_list"] = [str(cid) for (cid,) in cur.fetchall()]
    except sqlcipher3.dbapi2.OperationalError:
        pass  # Older DBs may not have the djmdSongTagList table

    # Related-track recommendations (djmdRecommendLike) back the MATCHING
    # browse. The relation is directional (ContentID1 -> ContentID2) but the
    # deck matches both ways, so both IDs are dumped as an unordered pair.
    out["recommendations"] = []
    try:
        cur.execute("SELECT ContentID1, ContentID2 FROM djmdRecommendLike "
                    "WHERE COALESCE(rb_local_deleted, 0) = 0")
        out["recommendations"] = [{"id1": str(a), "id2": str(b)} for a, b in cur.fetchall()]
    except sqlcipher3.dbapi2.OperationalError:
        pass  # Older DBs may not have the djmdRecommendLike table

    # Cue points (hot + memory). Kind: 0 = memory cue; hot cues are 1,2,3
    # then 5,6,7,8,9 for A,B,C,D,E,F,G,H — rekordbox reserves Kind 4, so the
    # caller must map Kind>=5 down by one to get the contiguous A..H slot.
    # OutMsec is the loop end (-1 if not a loop). ColorTableIndex is the
    # rekordbox hot-cue colour code (0x00-0x3e), same palette the deck uses;
    # NULL means no colour set (rekordbox shows those in its default green).
    try:
        cur.execute("SELECT ContentID, InMsec, OutMsec, Kind, ColorTableIndex, Comment "
                    "FROM djmdCue WHERE InMsec IS NOT NULL")
        for cid, inms, outms, kind, color, comment in cur.fetchall():
            out["cues"].append({
                "content_id": str(cid),
                "in_msec":    int(inms),
                "out_msec":   int(outms) if outms is not None else -1,
                "kind":       int(kind) if kind is not None else 0,
                "color":      int(color) if color is not None else -1,
                "comment":    comment or "",
            })
    except sqlcipher3.dbapi2.OperationalError:
        pass  # Older DBs may not have the djmdCue table

    # Hot Cue Banks: the folder/bank tree (djmdHotCueBanklist; Attribute 1 =
    # folder, top-level ParentID "root") and each bank's memberships
    # (djmdSongHotCueBanklist), which carry their own cue fields. Deleted rows
    # are kept with a flag: rekordbox hides a deleted bank in the tree but
    # still serves it by ID, and its catalog ignores a membership's flag while
    # its cue getters honour it. NULL cue fields read as 0, as rekordbox does.
    out["hot_cue_banks"] = []
    out["hot_cue_bank_members"] = []
    try:
        cur.execute("SELECT ID, ParentID, Seq, Name, Attribute, rb_local_deleted "
                    "FROM djmdHotCueBanklist")
        for bid, parent, seq, name, attr, deleted in cur.fetchall():
            out["hot_cue_banks"].append({
                "id": str(bid), "parent_id": str(parent or ""), "seq": int(seq or 0),
                "name": name or "", "folder": int(attr or 0) == 1,
                "deleted": int(deleted or 0) != 0,
            })
        cur.execute("SELECT HotCueBanklistID, ContentID, TrackNo, InMsec, OutMsec, "
                    "InMpegFrame, OutMpegFrame, InMpegAbs, OutMpegAbs, Color, ColorTableIndex, Comment, "
                    "BeatLoopSize, CueMicrosec, rb_local_deleted FROM djmdSongHotCueBanklist")
        for (bid, cid, no, inms, outms, inmf, outmf, inma, outma, color, colidx, comment,
             loop, micro, deleted) in cur.fetchall():
            out["hot_cue_bank_members"].append({
                "bank_id": str(bid), "content_id": str(cid), "track_no": int(no or 0),
                "in_msec": int(inms or 0), "out_msec": int(outms or 0),
                "in_mpeg_frame": int(inmf or 0), "out_mpeg_frame": int(outmf or 0),
                "in_mpeg_abs": int(inma or 0), "out_mpeg_abs": int(outma or 0),
                "color": int(color or 0), "color_table_index": int(colidx or 0),
                "comment": comment or "",
                "beat_loop_size": int(loop or 0), "cue_microsec": int(micro or 0),
                "deleted": int(deleted or 0) != 0,
            })
    except sqlcipher3.dbapi2.OperationalError:
        pass  # Older DBs may not have the Hot Cue Bank tables

    json.dump(out, sys.stdout, indent=2, default=str)


if __name__ == "__main__":
    main()
