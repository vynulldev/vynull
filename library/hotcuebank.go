// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/vynulldev/vynull/internal/dlog"
	"github.com/vynulldev/vynull/internal/fsutil"
)

// Hot Cue Banks are rekordbox's saved sets of hot cues, each a list of
// (track, cue) memberships, organised in a folder tree. They come from a
// rekordbox master.db import (djmdHotCueBanklist, djmdSongHotCueBanklist) and
// are served to decks over the dbserver; link-spec docs/HOT_CUE_BANK_ORACLE.md
// describes rekordbox's semantics, which these lookups follow.

// HotCueBank is one node of the bank tree: a folder or a bank (leaf).
type HotCueBank struct {
	ID       uint32 `json:"id"`
	ParentID uint32 `json:"parent_id"` // 0 at the top level
	Seq      int    `json:"seq"`
	Name     string `json:"name"`
	Folder   bool   `json:"folder"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// HotCueBankMember is one bank membership: a track and the cue it holds at
// position TrackNo. The cue fields are the membership's own, as rekordbox
// serves them, not the track's djmdCue rows.
type HotCueBankMember struct {
	BankID       uint32 `json:"bank_id"`
	TrackID      uint32 `json:"track_id,omitempty"` // library track; 0 if it didn't resolve
	ContentID    uint32 `json:"content_id"`         // rekordbox ContentID as imported
	TrackNo      int32  `json:"track_no"`
	InMsec       int32  `json:"in_msec"`
	OutMsec      int32  `json:"out_msec"` // -1 when not a loop
	InMpegFrame  uint32 `json:"in_mpeg_frame,omitempty"`
	OutMpegFrame uint32 `json:"out_mpeg_frame,omitempty"`
	InMpegAbs    uint32 `json:"in_mpeg_abs,omitempty"`
	OutMpegAbs   uint32 `json:"out_mpeg_abs,omitempty"`
	Color        int    `json:"color,omitempty"`
	// ColorTableIndex is the cue's rekordbox hot-cue palette colour (0 =
	// unset, shown as the default green). Decks aren't sent it.
	ColorTableIndex int    `json:"color_table_index,omitempty"`
	Comment         string `json:"comment,omitempty"`
	BeatLoopSize    uint32 `json:"beat_loop_size,omitempty"`
	CueMicrosec     uint32 `json:"cue_microsec,omitempty"`
	Deleted         bool   `json:"deleted,omitempty"`
}

// hotCueBankSet is the persisted bank tree and memberships.
type hotCueBankSet struct {
	Banks   []HotCueBank       `json:"banks"`
	Members []HotCueBankMember `json:"members"`
}

// SetHotCueBanks replaces the library's hot cue banks and persists them.
func (l *Library) SetHotCueBanks(banks []HotCueBank, members []HotCueBankMember) {
	set := &hotCueBankSet{Banks: banks, Members: members}
	l.mu.Lock()
	l.hotCueBanks = set
	l.mu.Unlock()
	l.saveHotCueBanks(set)
}

// HotCueBankChildren returns the live folders and banks directly under
// parentID (0 for the top level) in rekordbox's Seq order. Deleted nodes are
// hidden here, though a deleted bank is still served by ID.
func (l *Library) HotCueBankChildren(parentID uint32) []HotCueBank {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.hotCueBanks == nil {
		return nil
	}
	var out []HotCueBank
	for _, b := range l.hotCueBanks.Banks {
		if b.ParentID == parentID && !b.Deleted {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// HotCueBankList returns every live folder and bank, in no particular order.
func (l *Library) HotCueBankList() []HotCueBank {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.hotCueBanks == nil {
		return nil
	}
	var out []HotCueBank
	for _, b := range l.hotCueBanks.Banks {
		if !b.Deleted {
			out = append(out, b)
		}
	}
	return out
}

// HotCueBankMembers returns every membership of bankID, deleted ones
// included (rekordbox's catalog ignores the membership flag), ordered by
// signed TrackNo. Duplicates are kept: a track can sit in a bank twice.
func (l *Library) HotCueBankMembers(bankID uint32) []HotCueBankMember {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.hotCueBanks == nil {
		return nil
	}
	var out []HotCueBankMember
	for _, m := range l.hotCueBanks.Members {
		if m.BankID == bankID {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TrackNo < out[j].TrackNo })
	return out
}

// HotCueBankSlot returns the live membership of bankID at position slot, the
// lookup rekordbox's cue getters make for each requested slot, or nil.
func (l *Library) HotCueBankSlot(bankID uint32, slot int32) *HotCueBankMember {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.hotCueBanks == nil {
		return nil
	}
	for i := range l.hotCueBanks.Members {
		m := &l.hotCueBanks.Members[i]
		if m.BankID == bankID && m.TrackNo == slot && !m.Deleted {
			c := *m
			return &c
		}
	}
	return nil
}

// hotCueBanksPath is where the banks persist: beside the track list.
func (l *Library) hotCueBanksPath() string {
	if l.dbPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(l.dbPath), "hotcuebanks.json")
}

func (l *Library) saveHotCueBanks(set *hotCueBankSet) {
	path := l.hotCueBanksPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		dlog.Errorf("library: hot cue banks: %v", err)
		return
	}
	if err := fsutil.WriteFile(path, data, 0644); err != nil {
		dlog.Errorf("library: write %s: %v", path, err)
	}
}

func (l *Library) loadHotCueBanks() {
	path := l.hotCueBanksPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // none saved yet
	}
	var set hotCueBankSet
	if err := json.Unmarshal(data, &set); err != nil {
		dlog.Errorf("library: parse %s: %v", path, err)
		return
	}
	l.mu.Lock()
	l.hotCueBanks = &set
	l.mu.Unlock()
}
