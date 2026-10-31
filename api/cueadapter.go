// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/vynulldev/vynull/dbserver"
)

// CueStoreAdapter wraps dbserver.CueStore to implement CueStoreInterface.
// Telling the decks about a change is the caller's part (Server.refresh).
type CueStoreAdapter struct {
	Store *dbserver.CueStore
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

// cuePoint converts an API cue to the store's form.
func cuePoint(cue CueInfo) dbserver.CuePoint {
	cueType := uint16(1) // cue
	if cue.Type == "loop" {
		cueType = 2
	}
	return dbserver.CuePoint{
		Number:  cue.Number,
		Type:    cueType,
		TimeMs:  cue.TimeMs,
		LoopMs:  cue.LoopMs,
		Status:  1, // active
		ColorID: cue.ColorID,
	}
}

func (a *CueStoreAdapter) SaveCue(trackID uint32, cue CueInfo) {
	// No raw blob for API-created cues — the dbserver handler will
	// synthesize one when the CDJ requests cues via 0x2b04.
	p := cuePoint(cue)
	a.Store.SaveCue(trackID, &p, nil)
}

// SaveCues stores several cues for a track as one change (an import).
func (a *CueStoreAdapter) SaveCues(trackID uint32, cues []CueInfo) {
	points := make([]dbserver.CuePoint, len(cues))
	for i, c := range cues {
		points[i] = cuePoint(c)
	}
	a.Store.SaveCues(trackID, points)
}

func (a *CueStoreAdapter) DeleteCue(trackID uint32, cueNumber uint16) {
	a.Store.DeleteCue(trackID, cueNumber)
}

func (a *CueStoreAdapter) DeleteAllForTrack(trackID uint32) {
	a.Store.DeleteAllForTrack(trackID)
}

// Rev returns the cue store's revision.
func (a *CueStoreAdapter) Rev() uint64 { return a.Store.Rev() }
