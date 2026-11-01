// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── TAGS TAB (catalog management) ─────────

let allCategoriesCache = null;
async function ensureAllCategories(forceRefresh) {
  if (allCategoriesCache && !forceRefresh) return allCategoriesCache;
  const r = await maybeJSON('/api/tag-categories');
  if (Array.isArray(r)) allCategoriesCache = r;
  return allCategoriesCache || [];
}

// Re-render whenever the user switches to the tab or any catalog mutation
// happens. Counts come from allTracks (the library cache) — no API needed.
function tagUsageCounts() {
  const counts = {};
  for (const t of allTracks) {
    if (!t.tags) continue;
    for (const name of t.tags) {
      counts[name] = (counts[name] || 0) + 1;
    }
  }
  return counts;
}

// ───────── PLAYLISTS TAB ─────────
// User-defined playlists + folders. The store lives at
// /api/playlists; this tab exposes CRUD + ordered track lists. Phase 2
// already wired the same data into the CDJ's PLAYLIST menu via the
// dbserver, so anything we create here shows up on the deck too.

let playlistsCache = [];      // flat list from /api/playlists
let plSelectedID = null;      // currently-selected playlist ID (null when source='collection')
let plSelectedTracks = [];    // ordered TrackInfo for the active playlist
// selectedSource is the current left-sidebar selection. 'collection'
// means "show all library tracks" (the default). Any number is a
// playlist or folder ID. Folders display an empty pane (we treat them
// as organisational containers); playlists/smart playlists drive the
// track table.
let selectedSource = 'collection';

async function loadPlaylists(preserveSelection) {
  let r;
  try {
    r = await fetchJSON('/api/playlists');
  } catch (e) {
    toast('PLAYLISTS NOT LOADED · ' + e.message.toUpperCase(), 'error');
    return;
  }
  playlistsCache = Array.isArray(r) ? r : [];
  if (preserveSelection && typeof selectedSource === 'number') {
    const still = playlistsCache.find(p => p.id === selectedSource);
    if (still && !still.is_folder) {
      await loadPlaylistTracks(selectedSource);
    } else if (!still) {
      // Selected playlist was deleted — drop back to collection so the
      // table doesn't show stale rows.
      selectedSource = 'collection';
      plSelectedID = null;
      plSelectedTracks = [];
    }
  }
  await loadHotCueBanks();
  renderLibrarySidebar();
  refreshLibraryView();
}

async function loadPlaylistTracks(id) {
  const r = await fetchJSON(`/api/playlists/${id}/tracks`).catch(e => {
    toast('PLAYLIST NOT LOADED · ' + e.message.toUpperCase(), 'error');
    return null;
  });
  plSelectedTracks = Array.isArray(r) ? r : [];
}

// ───────── HOT CUE BANKS (read-only) ─────────
// rekordbox Hot Cue Banks from a master.db import, as the decks browse them:
// a folder/bank tree in the sidebar, and a selected bank's tracks with the
// cue each holds. Positions 1-8 are pads A-H, as in rekordbox and on the deck.
// Selecting a bank sets selectedSource to 'hcb:<id>'.
let hcbCache = [];          // flat list from /api/hotcuebanks
let hcbSelectedEntries = []; // the selected bank's /tracks
let hcbSelectedID = null;

function isHotCueBankSource() {
  return typeof selectedSource === 'string' && selectedSource.startsWith('hcb:');
}

async function loadHotCueBanks() {
  let r;
  try {
    r = await fetchJSON('/api/hotcuebanks');
  } catch (e) {
    toast('HOT CUE BANKS NOT LOADED · ' + e.message.toUpperCase(), 'error');
    return;
  }
  hcbCache = Array.isArray(r) ? r : [];
  if (isHotCueBankSource() && !hcbCache.some(b => b.id === hcbSelectedID)) {
    selectedSource = 'collection';
    hcbSelectedID = null;
    hcbSelectedEntries = [];
  }
}

function renderHotCueBankSection() {
  const head = document.getElementById('lib-hcb-head');
  const host = document.getElementById('lib-sidebar-hcb');
  if (!head || !host) return;
  if (hcbCache.length === 0) {
    head.style.display = 'none';
    host.innerHTML = '';
    return;
  }
  head.style.display = '';
  const byParent = new Map();
  for (const b of hcbCache) {
    if (!byParent.has(b.parent_id)) byParent.set(b.parent_id, []);
    byParent.get(b.parent_id).push(b);
  }
  for (const kids of byParent.values()) kids.sort((a, b) => a.seq - b.seq);
  const render = (parentID, depth) => (byParent.get(parentID) || []).map(b => `
      <div class="pl-node hcb-node ${b.is_folder ? 'folder' : ''} ${!b.is_folder && b.id === hcbSelectedID && isHotCueBankSource() ? 'selected' : ''}"
           data-hcb-id="${b.id}" data-is-folder="${b.is_folder ? 1 : 0}"
           style="padding-left: ${14 + depth * 14}px">
        <span class="icon">${b.is_folder ? '▸' : '◆'}</span>
        <span class="name">${escape(b.name)}</span>
        ${b.is_folder ? '' : `<span class="count">${b.track_count}</span>`}
      </div>` + (b.is_folder ? render(b.id, depth + 1) : '')).join('');
  host.innerHTML = render(0, 0);
  host.querySelectorAll('.hcb-node').forEach(node => {
    node.addEventListener('click', async () => {
      if (node.getAttribute('data-is-folder') === '1') return;
      const id = parseInt(node.getAttribute('data-hcb-id'), 10);
      let r;
      try {
        r = await fetchJSON(`/api/hotcuebanks/${id}/tracks`);
      } catch (e) {
        toast('HOT CUE BANK NOT LOADED · ' + e.message.toUpperCase(), 'error');
        return;
      }
      hcbSelectedEntries = Array.isArray(r) ? r : [];
      hcbSelectedID = id;
      selectedSource = 'hcb:' + id;
      plSelectedID = null;
      plSelectedTracks = [];
      refreshLibraryView();
      closeMobilePlaylists();
    });
  });
}

function renderHotCueBankView() {
  const bank = hcbCache.find(b => b.id === hcbSelectedID);
  const name = bank ? bank.name.toUpperCase() : 'HOT CUE BANK';
  const sourceName = document.getElementById('lib-source-name');
  if (sourceName) sourceName.textContent = name;
  const toggleName = document.getElementById('lib-src-toggle-name');
  if (toggleName) toggleName.textContent = name;
  const actions = document.getElementById('lib-playlist-actions');
  if (actions) actions.innerHTML = '';
  // The filter bar and column picker belong to the track table; this view
  // has fixed columns. The text filter still narrows by title or artist.
  const bar = document.getElementById('lib-filterbar');
  if (bar) bar.style.display = 'none';
  const colsBtn = document.getElementById('lib-columns-btn');
  if (colsBtn) colsBtn.style.display = 'none';
  const q = (document.getElementById('lib-search').value || '').toLowerCase();
  const shown = !q ? hcbSelectedEntries : hcbSelectedEntries.filter(e =>
    (e.track.title || '').toLowerCase().includes(q) || (e.track.artist || '').toLowerCase().includes(q));
  const stats = document.getElementById('lib-stats');
  const missing = hcbSelectedEntries.filter(e => e.track.file_missing).length;
  if (stats) stats.textContent = `${hcbSelectedEntries.length} TRACKS${missing ? ` · ${missing} MISSING` : ''} · READ-ONLY`;

  const el = document.getElementById('library-content');
  if (el) {
    if (hcbSelectedEntries.length === 0) {
      el.innerHTML = '<div class="empty-state">— EMPTY BANK —</div>';
    } else if (shown.length === 0) {
      el.innerHTML = '<div class="empty-state">NO TRACKS MATCH</div>';
    } else {
      const pad = p => (p >= 1 && p <= 8) ? 'ABCDEFGH'[p - 1] : '';
      // A cue with a loop end is a loop (rekordbox's rule). The swatch is the
      // cue's palette colour; unset is green for a cue and orange for a loop,
      // as rekordbox and the deck show them.
      const isLoop = e => typeof e.out_ms === 'number';
      const swatch = e => e.color_id ? (CUE_COLORS[e.color_id] || CUE_COLORS[0]) : (isLoop(e) ? CUE_COLORS[0x24] : CUE_COLORS[0]);
      const rows = shown.map(e => `
        <tr data-hcb-track="${e.track.id}" class="${e.track.file_missing ? 'hcb-missing' : ''}"
            title="${e.track.file_missing ? 'File missing on this machine: not sent to decks, so this pad is empty there' : pad(e.position) ? 'Pad ' + pad(e.position) : 'No pad (positions 1-8 are pads A-H)'}">
          <td>${pad(e.position) ? `<b>${pad(e.position)}</b>` : '—'}</td>
          <td><span class="hcb-swatch" style="background:${swatch(e)}" title="${e.color_id ? 'Colour 0x' + e.color_id.toString(16).padStart(2, '0') : 'Default colour'}"></span>${isLoop(e) ? '<span class="hcb-loop-tag" title="Loop">⟲ LOOP</span>' : ''}</td>
          <td>${e.position}</td>
          <td>${escape(e.track.title || '')}${e.track.file_missing ? ' <span class="hcb-missing-tag">MISSING FILE</span>' : ''}</td>
          <td>${escape(e.track.artist || '')}</td>
          <td>${e.track.bpm ? Number(e.track.bpm).toFixed(1) : ''}</td>
          <td>${escape(e.track.key || '')}</td>
          <td>${formatTime(e.in_ms)}</td>
          <td>${isLoop(e) ? formatTime(e.out_ms) : '—'}</td>
          <td>${escape(e.comment || '')}</td>
        </tr>`).join('');
      el.innerHTML = `<div class="lib-table-wrap"><table class="lib">
        <thead><tr><th>PAD</th><th>COLOUR</th><th>POS</th><th>TITLE</th><th>ARTIST</th><th>BPM</th><th>KEY</th><th>CUE</th><th>LOOP END</th><th>COMMENT</th></tr></thead>
        <tbody>${rows}</tbody></table></div>`;
      el.querySelectorAll('tr[data-hcb-track]').forEach(tr =>
        tr.addEventListener('click', () => openDetail(parseInt(tr.getAttribute('data-hcb-track'), 10))));
    }
  }

  // Selection highlight: this bank only.
  document.querySelectorAll('#lib-sidebar-tree .pl-node, #lib-sidebar-history .pl-node').forEach(n => n.classList.remove('selected'));
  document.querySelectorAll('.lib-src[data-source="collection"]').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('#lib-sidebar-hcb .hcb-node').forEach(n =>
    n.classList.toggle('selected', parseInt(n.getAttribute('data-hcb-id'), 10) === hcbSelectedID));
}

