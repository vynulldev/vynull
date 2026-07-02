// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/vynulldev/vynull/dbserver"
)

// CueStoreAdapter wraps dbserver.CueStore to implement CueStoreInterface.
//
// OnChange fires after any mutation (save or delete). main.go wires it to
// the virtual device's broadcast hook so connected CDJs re-fetch cues
// and pick up colour changes without a track reload.
type CueStoreAdapter struct {
	Store    *dbserver.CueStore
	OnChange func(trackID uint32)
}

func (a *CueStoreAdapter) GetCues(trackID uint32) []CueInfo {
	return convertCues(a.Store.GetCues(trackID))
}

// AllCues returns every track that has any cues, keyed by track ID.
// Backs the GET /api/cues batch endpoint used by the library UI.
func (a *CueStoreAdapter) AllCues() map[uint32][]CueInfo {
	all := a.Store.AllCues()
	out := make(map[uint32][]CueInfo, len(all))
	for id, cues := range all {
		out[id] = convertCues(cues)
	}
	return out
}

func convertCues(dbCues []dbserver.CuePoint) []CueInfo {
	out := make([]CueInfo, len(dbCues))
	for i, c := range dbCues {
		t := "cue"
		if c.Type == 2 {
			t = "loop"
		}
		out[i] = CueInfo{
			Number:  c.Number,
			Type:    t,
			TimeMs:  c.TimeMs,
			LoopMs:  c.LoopMs,
			ColorID: c.ColorID,
		}
	}
	return out
}

func (a *CueStoreAdapter) SaveCue(trackID uint32, cue CueInfo) {
	cueType := uint16(1) // cue
	if cue.Type == "loop" {
		cueType = 2
	}
	dbCue := &dbserver.CuePoint{
		Number:  cue.Number,
		Type:    cueType,
		TimeMs:  cue.TimeMs,
		LoopMs:  cue.LoopMs,
		Status:  1, // active
		ColorID: cue.ColorID,
	}
	// No raw blob for API-created cues — the dbserver handler will
	// synthesize one when the CDJ requests cues via 0x2b04.
	a.Store.SaveCue(trackID, dbCue, nil)
	if a.OnChange != nil {
		a.OnChange(trackID)
	}
}

func (a *CueStoreAdapter) DeleteCue(trackID uint32, cueNumber uint16) {
	a.Store.DeleteCue(trackID, cueNumber)
	if a.OnChange != nil {
		a.OnChange(trackID)
	}
}

func (a *CueStoreAdapter) DeleteAllForTrack(trackID uint32) {
	a.Store.DeleteAllForTrack(trackID)
}
