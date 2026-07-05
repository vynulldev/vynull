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
    if len(sys.argv) < 3:
        sys.stderr.write(__doc__)
        sys.exit(2)
    db_path = sys.argv[1]
    provided = sys.argv[2]

    conn = open_db(db_path, provided)
    cur = conn.cursor()

    out = {"tracks": [], "playlists": [], "tags": [], "cues": []}

    # Tracks
    cur.execute("""
        SELECT c.ID, c.Title, c.FolderPath, c.FileNameL, c.FileSize, c.BPM,
               c.Length, c.BitRate, c.SampleRate, c.Rating, c.ReleaseYear,
               c.TrackNo, c.DiscNo, c.Commnt, c.DJPlayCount, c.DateCreated,
               c.ColorID, c.AnalysisDataPath, c.ImagePath,
               a.Name AS Artist, al.Name AS Album, g.Name AS Genre,
               k.ScaleName AS Key, l.Name AS Label
        FROM djmdContent c
        LEFT JOIN djmdArtist a ON c.ArtistID = a.ID
        LEFT JOIN djmdAlbum al ON c.AlbumID = al.ID
        LEFT JOIN djmdGenre g ON c.GenreID = g.ID
        LEFT JOIN djmdKey k ON c.KeyID = k.ID
        LEFT JOIN djmdLabel l ON c.LabelID = l.ID
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
            "rating":       (d.get("Rating") or 0) // 20,  # 0-100 → 0-5
            "year":         d.get("ReleaseYear") or 0,
            "track_num":    d.get("TrackNo") or 0,
            "disc_num":     d.get("DiscNo") or 0,
            "comment":      d.get("Commnt") or "",
            "play_count":   d.get("DJPlayCount") or 0,
            "date_added":   d.get("DateCreated") or "",
            # ColorID is stored as TEXT ("0".."8"); coerce to the palette int.
            "color_id":     int(d.get("ColorID") or 0),
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
        SELECT ID, Name, ParentID, Attribute, SmartList
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

    for pid, name, parent, attr, smartlist in pl_rows:
        if attr is not None and attr < 0:
            continue  # system playlist (cloud-sync trial etc.)
        entry = {
            "id":        pid,
            "name":      name,
            "parent_id": parent,
            "is_folder": attr == 1,
            "is_smart":  attr == 4,
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

    json.dump(out, sys.stdout, indent=2, default=str)


if __name__ == "__main__":
    main()
