// SPDX-License-Identifier: GPL-3.0-or-later

package dbserver

import (
	"sync"

	"github.com/vynulldev/vynull/proto"
)

// recentLists holds the list each request context last queried, shared by the
// server's sessions. rekordbox keeps its materialized menus process-wide,
// keyed by the request's packed context (player, menu location, media slot,
// track type), and they outlive the connection that filled them (link-spec
// PROTOCOL_REFERENCE, "process-wide materialized menus keyed by the packed
// context"). A deck queries and renders each list in one context: it keeps
// its main list in location 1 while it previews the highlighted row in
// location 2, so a preview never stands in for the main list.
type recentLists struct {
	mu    sync.Mutex
	lists map[uint32][]*menuItem
}

// set records the list a query in context ctx returned. The list is shared
// from here on, so it must not be edited after.
func (r *recentLists) set(ctx uint32, items []*menuItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lists == nil {
		r.lists = make(map[uint32][]*menuItem)
	}
	r.lists[ctx] = items
}

// get returns the list last queried in context ctx, or nil.
func (r *recentLists) get(ctx uint32) []*menuItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lists[ctx]
}

// forget drops the lists of every context of one player (the context's top
// byte), whose deck is back at its root menu.
func (r *recentLists) forget(player uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ctx := range r.lists {
		if uint8(ctx>>24) == player {
			delete(r.lists, ctx)
		}
	}
}

// recentLists returns the server's shared lists (a handler of its own, as in
// tests, gets a set of its own).
func (h *Handler) recentLists() *recentLists {
	if h.recent == nil {
		h.recent = &recentLists{}
	}
	return h.recent
}

// dmstContext returns a request's packed context (its first argument), or 0
// when there is none.
func dmstContext(msg *proto.DBMessage) uint32 {
	if len(msg.Args) > 0 {
		return msg.Args[0].Int()
	}
	return 0
}
