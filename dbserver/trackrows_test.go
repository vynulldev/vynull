// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"runtime"
	"slices"
	"sort"
	"sync"
	"testing"
	"weak"

	"github.com/vynulldev/vynull/internal/benchlib"
	"github.com/vynulldev/vynull/library"
	"github.com/vynulldev/vynull/proto"
)

// detailMenu is a MenuSource whose track lists show a settable detail column.
type detailMenu struct{ detail *string }

func (detailMenu) RootMenu() []RootMenuEntry { return DefaultRootMenu() }
func (m detailMenu) TrackDetail() string     { return *m.detail }

// trackList asks for the TRACK list with a sort and returns its rows.
func trackList(h *Handler, order uint32) []*menuItem {
	h.Handle(&proto.DBMessage{TxID: 1, Type: proto.DBMsgGetTracks,
		Args: []proto.DBArg{proto.ArgI32(trackListCtx), proto.ArgI32(order)}})
	return h.pendingItems
}

// rowFor returns the row of the track with the given ID, or nil.
func rowFor(items []*menuItem, id uint32) *menuItem {
	for _, m := range items {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// TestTrackRowsFollowChanges checks that the prebuilt rows are rebuilt for
// every library change and for a change of the configured detail column.
func TestTrackRowsFollowChanges(t *testing.T) {
	lib := fixtureLibrary()
	detail := "bpm"
	h := &Handler{lib: lib, menu: detailMenu{&detail}}
	id := lib.Tracks()[0].ID
	if m := rowFor(trackList(h, 0), id); m.Label1 != "Alpha One" || m.Label2 != "120.0 bpm - Am" {
		t.Fatalf("row %q %q, want Alpha One, 120.0 bpm - Am", m.Label1, m.Label2)
	}

	lib.Update(id, func(tr *library.Track) error { tr.Title = "Renamed"; tr.BPM = 99.5; return nil })
	if m := rowFor(trackList(h, 0), id); m.Label1 != "Renamed" || m.Label2 != "99.5 bpm - Am" {
		t.Errorf("after an edit: row %q %q, want Renamed, 99.5 bpm - Am", m.Label1, m.Label2)
	}
	lib.SetRating(id, 4)
	if m := rowFor(trackList(h, 5), id); m.ParentID != 4 {
		t.Errorf("after a rating: RATING column %d, want 4", m.ParentID)
	}
	lib.SetArtwork(id, 77)
	if m := rowFor(trackList(h, 0), id); m.ArtID != 77 {
		t.Errorf("after artwork: art ID %d, want 77", m.ArtID)
	}
	detail = "artist"
	if m := rowFor(trackList(h, 0), id); m.Label2 != "Alpha Artist" || m.ItemType != 0x0704 {
		t.Errorf("after a detail change: %q type %#x, want Alpha Artist, 0x0704", m.Label2, m.ItemType)
	}
	added := lib.AddTrack(&library.Track{Title: "Aaa Added", Artist: "New", FilePath: "/added.mp3"})
	if items := trackList(h, 0); items[0].ID != added {
		t.Errorf("after an added track: first row %d %q, want the added track %d", items[0].ID, items[0].Label1, added)
	}
}

// TestTrackRowsSharedBySessions checks that sessions share one set of rows,
// and that a session's sort columns stay on its own copies.
func TestTrackRowsSharedBySessions(t *testing.T) {
	lib := fixtureLibrary()
	var rows rowCache
	a := &Handler{lib: lib, rowCache: &rows}
	b := &Handler{lib: lib, rowCache: &rows}
	if a.trackRows() != b.trackRows() {
		t.Fatal("two sessions built their own rows")
	}
	before := slices.Clone(rows.rows.rows)
	for order := uint32(1); order <= 17; order++ {
		trackList(a, order)
	}
	if !slices.EqualFunc(before, rows.rows.rows, func(x, y menuItem) bool { return x == y }) {
		t.Error("sorting changed the shared rows")
	}
	for _, m := range trackList(b, 0) {
		if m.ItemType != 0x0d04 || m.contextTrackType != 0 {
			t.Fatalf("default list row %q has type %#x, context %#x: another session's sort leaked", m.Label1, m.ItemType, m.contextTrackType)
		}
	}
}

// TestTrackRowsConcurrentSessions runs sessions' sorted lists, searches and
// drill-downs at once on fresh rows, whose lazily built parts they race to
// build (run with -race).
func TestTrackRowsConcurrentSessions(t *testing.T) {
	lib := benchlib.Library(2000)
	var rows rowCache
	var wg sync.WaitGroup
	for s := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := &Handler{lib: lib, rowCache: &rows}
			for order := uint32(0); order <= 17; order++ {
				trackList(h, (order+uint32(s))%18)
			}
			h.Handle(&proto.DBMessage{TxID: 2, Type: 0x1300, Args: []proto.DBArg{
				proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(4), proto.ArgStr("\x00N\x00\x00")}})
			h.Handle(&proto.DBMessage{TxID: 3, Type: proto.DBMsgGetByArtist, Args: []proto.DBArg{
				proto.ArgI32(trackListCtx), proto.ArgI32(0), proto.ArgI32(h.catID("artist", lib.Tracks()[s].Artist))}})
		}()
	}
	wg.Wait()
}

// TestSortRowsIsStable checks the position-tiebroken sort against a stable
// sort for every sort ID, on a library with many ties.
func TestSortRowsIsStable(t *testing.T) {
	h := &Handler{lib: benchlib.Library(2000)}
	c := h.trackRows()
	for order, spec := range sortSpecs {
		items := c.rowsAt(c.libraryOrder())
		want := slices.Clone(items)
		order := order
		if spec.kind == sortTitleOrd {
			order = sortTitle
		}
		sort.SliceStable(want, func(i, j int) bool {
			a := sortEntry{m: want[i], key: sortCollationKey(want[i].sv.text(order))}
			b := sortEntry{m: want[j], key: sortCollationKey(want[j].sv.text(order))}
			return compareRows(&a, &b, order, spec) < 0
		})
		sortRows(c, items, order, spec)
		if !slices.Equal(items, want) {
			t.Errorf("sort %d (%s): order differs from a stable sort", order, spec.label)
		}
	}
}

// TestRowCopiesDontKeepRows checks that a list a session keeps doesn't keep
// the prebuilt rows it was copied from alive once the library has moved on.
func TestRowCopiesDontKeepRows(t *testing.T) {
	lib := fixtureLibrary()
	h := &Handler{lib: lib}
	items := trackList(h, 2) // sorted: a sort column on every copy
	old := weak.Make(h.trackRows())
	lib.Touch()
	h.trackRows() // the next request replaces them
	runtime.GC()
	if old.Value() != nil {
		t.Error("the old rows are still alive")
	}
	runtime.KeepAlive(items)
}
