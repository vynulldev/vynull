// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── LIBRARY ─────────
// Column registry. `default: true` columns are visible on first load; user
// toggles in the COLUMNS popover override this and persist to localStorage.
// `locked: true` means the column can't be hidden (TITLE is the row identity).
function fmtDuration(sec) {
  if (!sec || sec <= 0) return '';
  const m = Math.floor(sec / 60), s = sec % 60;
  return m + ':' + String(s).padStart(2, '0');
}
// Renders an interactive 5-star widget for trackID. rating is 0-5; 0 means
// unset (all stars dim). Click handlers are wired in renderLibrary /
// renderDetail post-render — both delegate to setTrackRating.
function fmtStars(rating, trackID) {
  rating = Math.max(0, Math.min(5, rating || 0));
  let html = `<span class="stars" data-rating-track="${trackID}" data-rating-current="${rating}">`;
  // Render 5→1 so flex row-reverse lays them out 1→5 left-to-right but
  // :hover ~ .star can still highlight all stars to the left of the cursor.
  for (let i = 5; i >= 1; i--) {
    const filled = i <= rating ? 'filled' : '';
    const label = i + (i > 1 ? ' stars' : ' star');
    html += `<button type="button" class="star ${filled}" data-rating-set="${i}" title="${label}">★</button>`;
  }
  html += '</span>';
  return html;
}
function fmtPath(p) {
  if (!p) return '';
  // Show last 40 chars with ellipsis on the left so the filename stays visible.
  const s = p.length > 40 ? '…' + p.slice(-40) : p;
  return `<span class="col-path" title="${escape(p)}">${escape(s)}</span>`;
}

// Called on every library-row <img> load (onload="thumbLoaded(this)").
// Fades the image in (loading→loaded class swap) and, if the server returned
// the 1×1 placeholder PNG (analysis pending), schedules a cache-busted
// re-fetch so the thumbnail self-heals once the worker pool catches up
// without needing a full library re-render. Random jitter spreads retries
// out so we don't stampede the server after a fresh page load.
function thumbLoaded(img) {
  img.classList.replace('loading', 'loaded');
  if (img.naturalWidth !== 1) {
    // Real waveform. If it arrived via a cache-busted self-heal URL, swap
    // back to the natural URL (preload keeps the frame up): the natural URL
    // was last fetched when it returned the no-store placeholder, so the
    // browser has nothing cached under it — without this, the next table
    // re-render (view switch) starts cache-cold and the freshly-analyzed
    // track sits blank again waiting on a full round-trip. The swap request
    // hits the server's disk-cached PNG, so it is one cheap fetch that
    // makes every later re-render instant.
    const tid = img.getAttribute('data-libtrack');
    if (tid && /[?&]_=/.test(img.src)) {
      swapImgSrc(img, waveThumbSrc(tid));
    }
    return;
  }
  scheduleThumbRetry(img);
}

// scheduleThumbRetry keeps a pending thumbnail's self-heal alive. Analysis
// can sit behind a LONG queue (bulk add, or the one-time cacheVersion
// re-analysis pass), so a short fixed retry budget silently gave up before
// the track's turn came — and the thumbnail then never appeared without a
// page refresh. Quick retries for the first minute (analysis normally wins
// that race), then back off to ~30-45s for up to roughly two hours; the
// placeholder responses being retried are no-store and cost the server
// almost nothing.
function scheduleThumbRetry(img) {
  const tries = parseInt(img.dataset.retries || '0', 10);
  if (tries >= 250) return; // ~2h with backoff — permanently-failed analysis stops here
  img.dataset.retries = String(tries + 1);
  const base = tries < 12 ? 5000 : 30000;
  const delayMs = base + Math.floor(Math.random() * base * 0.6);
  const tid = img.getAttribute('data-libtrack');
  setTimeout(() => {
    if (!tid || !document.body.contains(img)) return;
    // Direct src set on purpose: the retry chain rides the onload cycle
    // (src -> onload -> thumbLoaded -> next retry), and the cell is showing
    // the 1x1 placeholder anyway, so there is no frame to preserve.
    img.src = waveThumbSrc(tid, true);
  }, delayMs);
}
// `sort` is the accessor used by the comparator. `sortable: false` opts a
// column out of header-click sorting (e.g. the waveform thumbnail).
// artHTML renders a track's artwork <img>. The request is made optimistically
// for tracks whose file has never been probed (art_id 0 with art_checked
// unset): GET /api/artwork/{id} is what triggers the server's lazy ffmpeg
// extraction, so the first view of a newly added track pulls the art out of
// the file instead of waiting for a CDJ load to analyze it. Tracks already
// probed and found artless (art_checked true) go straight to the placeholder,
// so there is no repeated 404 traffic; onerror degrades to the placeholder
// for the artless-probe and concurrent-probe cases.
function artHTML(t, imgClass, fbClass, fbText) {
  if (t.art_id > 0 || !t.art_checked) {
    return `<img class="${imgClass}" loading="lazy" alt="" src="/api/artwork/${t.id}"` +
      ` data-fb-class="${fbClass}" data-fb-text="${fbText}" onerror="artFallback(this)">`;
  }
  return `<div class="${fbClass}">${fbText}</div>`;
}
function artFallback(img) {
  const d = document.createElement('div');
  d.className = img.getAttribute('data-fb-class') || 'lib-art empty';
  d.textContent = img.getAttribute('data-fb-text') || '';
  img.replaceWith(d);
}

const LIBRARY_COLUMNS = [
  { key: 'art',      label: 'ART',    default: false, thClass: '',    tdClass: 'art-cell',
    sortable: false,
    cell: t => artHTML(t, 'lib-art', 'lib-art empty', '') },
  { key: 'waveform', label: 'WV',     default: true,  thClass: '',    tdClass: 'wave-cell',
    sortable: false,
    cell: t => `<div class="wave-thumb"><img class="loading" data-libtrack="${t.id}" alt="" loading="lazy" src="${waveThumbSrc(t.id)}" onload="thumbLoaded(this)" onerror="scheduleThumbRetry(this)">${cueMarkerOverlay(t.id, t.duration)}</div>` },
  { key: 'id',       label: 'ID',     default: true,  thClass: 'num', tdClass: 'num',
    sort: t => t.id,
    cell: t => String(t.id).padStart(4, '0') },
  { key: 'title',    label: 'TITLE',  default: true,  locked: true,
    sort: t => (t.title || '').toLowerCase(),
    cell: t => (t.file_missing ? `<span class="missing-flag" title="File not found at ${escape(t.file_path || '')}">⚠</span>` : '') + escape(t.title || '') },
  { key: 'artist',   label: 'ARTIST', default: true,
    sort: t => (t.artist || '').toLowerCase(),
    cell: t => `<span style="color: var(--text2)">${escape(t.artist || '')}</span>` },
  { key: 'album',    label: 'ALBUM',  default: true,
    sort: t => (t.album || '').toLowerCase(),
    cell: t => `<span style="color: var(--text3)">${escape(t.album || '')}</span>` },
  { key: 'genre',    label: 'GENRE',  default: false,
    sort: t => (t.genre || '').toLowerCase(),
    cell: t => `<span style="color: var(--text2)">${escape(t.genre || '')}</span>` },
  { key: 'bpm',      label: 'BPM',    default: true,  thClass: 'num', tdClass: 'num',
    sort: t => t.bpm || 0,
    cell: t => (t.bpm || 0).toFixed(1) },
  { key: 'key',      label: 'KEY',    default: true,
    sort: t => t.key || '',
    cell: t => `<span style="color: var(--text2)">${escape(fmtKey(t.key))}</span>` },
  { key: 'duration', label: 'TIME',   default: false, thClass: 'num', tdClass: 'num',
    sort: t => t.duration || 0,
    cell: t => fmtDuration(t.duration) },
  { key: 'year',     label: 'YEAR',   default: false, thClass: 'num', tdClass: 'num',
    sort: t => t.year || 0,
    cell: t => t.year ? String(t.year) : '' },
  { key: 'rating',   label: 'RATING', default: false,
    sort: t => t.rating || 0,
    cell: t => fmtStars(t.rating, t.id) },
  { key: 'color',    label: 'COLOR',  default: false,
    sort: t => t.color_id || 0,
    cell: t => t.color_id
      ? `<button type="button" class="col-color-chip" data-color-pick="${t.id}" style="background:${TRACK_COLORS[t.color_id] || '#666'}" title="${escape(t.color_name || 'Set color')}"></button>`
      : `<button type="button" class="col-color-chip empty" data-color-pick="${t.id}" title="Set color"></button>` },
  { key: 'date_added', label: 'ADDED', default: false,
    sort: t => t.date_added || '',
    cell: t => `<span style="color: var(--text3)">${escape(t.date_added || '')}</span>` },
  { key: 'date_created', label: 'CREATED', default: false,
    sort: t => t.date_created || '',
    cell: t => `<span style="color: var(--text3)">${escape(t.date_created || '')}</span>` },
  { key: 'release_date', label: 'RELEASED', default: false,
    sort: t => t.release_date || '',
    cell: t => `<span style="color: var(--text3)">${escape(t.release_date || '')}</span>` },
  { key: 'play_count', label: 'PLAYS', default: false, thClass: 'num', tdClass: 'num',
    sort: t => t.play_count || 0,
    cell: t => t.play_count ? String(t.play_count) : '' },
  { key: 'bitrate',  label: 'KBPS',   default: false, thClass: 'num', tdClass: 'num',
    sort: t => t.bitrate || 0,
    cell: t => t.bitrate ? String(t.bitrate) : '' },
  { key: 'file_type', label: 'TYPE',  default: false,
    sort: t => t.file_type || '',
    cell: t => `<span style="color: var(--text3)">${escape(t.file_type || '')}</span>` },
  { key: 'comment',  label: 'COMMENT', default: false,
    sort: t => (t.comment || '').toLowerCase(),
    cell: t => `<span style="color: var(--text3)">${escape(t.comment || '')}</span>` },
  { key: 'label',    label: 'LABEL',  default: false,
    sort: t => (t.label || '').toLowerCase(),
    cell: t => `<span style="color: var(--text2)">${escape(t.label || '')}</span>` },
  { key: 'remixer',  label: 'REMIXER', default: false,
    sort: t => (t.remixer || '').toLowerCase(),
    cell: t => `<span style="color: var(--text2)">${escape(t.remixer || '')}</span>` },
  { key: 'tags',     label: 'TAGS',   default: false,
    sort: t => (t.tags && t.tags.length) ? t.tags.join(',').toLowerCase() : '',
    cell: t => (t.tags && t.tags.length)
      ? `<div class="col-tag-chips">${t.tags.map(tg => `<span class="chip">${escape(tg)}</span>`).join('')}</div>`
      : '' },
  { key: 'file_path', label: 'PATH',  default: false,
    sort: t => (t.file_path || '').toLowerCase(),
    cell: t => fmtPath(t.file_path) },
];

const COLUMNS_LS_KEY = 'pdj-library-columns';
function loadVisibleColumns() {
  try {
    const raw = localStorage.getItem(COLUMNS_LS_KEY);
    if (raw) {
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) return new Set(arr);
    }
  } catch (e) { /* fall through to defaults */ }
  return new Set(LIBRARY_COLUMNS.filter(c => c.default).map(c => c.key));
}
function saveVisibleColumns() {
  try { localStorage.setItem(COLUMNS_LS_KEY, JSON.stringify([...visibleColumns])); }
  catch (e) { /* quota / private mode — ignore */ }
}

// Column order (left→right): an array of column keys, persisted separately
// from visibility. The COLUMNS popover toggles which columns show; dragging a
// header reorders them. Unknown stored keys are dropped and any columns added
// in a newer app version are appended, so a stored order survives updates.
const COLUMN_ORDER_LS_KEY = 'pdj-library-column-order';
function loadColumnOrder() {
  const allKeys = LIBRARY_COLUMNS.map(c => c.key);
  try {
    const raw = localStorage.getItem(COLUMN_ORDER_LS_KEY);
    if (raw) {
      const arr = JSON.parse(raw);
      if (Array.isArray(arr)) {
        const known = arr.filter(k => allKeys.includes(k));
        const missing = allKeys.filter(k => !known.includes(k));
        return [...known, ...missing];
      }
    }
  } catch (e) { /* fall through to defaults */ }
  return allKeys;
}
function saveColumnOrder() {
  try { localStorage.setItem(COLUMN_ORDER_LS_KEY, JSON.stringify(columnOrder)); }
  catch (e) { /* quota / private mode — ignore */ }
}
// Move column srcKey to just before (or after) targetKey, then persist.
function moveColumn(srcKey, targetKey, before) {
  if (srcKey === targetKey) return;
  const order = columnOrder.filter(k => k !== srcKey);
  let idx = order.indexOf(targetKey);
  if (idx < 0) return;
  if (!before) idx += 1;
  order.splice(idx, 0, srcKey);
  columnOrder = order;
  saveColumnOrder();
}
let columnOrder = loadColumnOrder();

// Row height (px). Persists in localStorage, applied via the --lib-row-h
// CSS var so every td height + art-cell sizing tracks it. Clamped on
// load to avoid stuck-extreme values from older sessions.
const ROW_H_LS_KEY = 'pdj-library-row-h';
const ROW_H_MIN = 24;
const ROW_H_MAX = 120;
function loadRowHeight() {
  try {
    const v = parseInt(localStorage.getItem(ROW_H_LS_KEY), 10);
    if (Number.isFinite(v) && v >= ROW_H_MIN && v <= ROW_H_MAX) return v;
  } catch (e) {}
  return 36;
}
function saveRowHeight(v) {
  try { localStorage.setItem(ROW_H_LS_KEY, String(v)); } catch (e) {}
}
function applyRowHeight(v) {
  document.documentElement.style.setProperty('--lib-row-h', v + 'px');
}
let libraryRowHeight = loadRowHeight();
applyRowHeight(libraryRowHeight);

// Album art fit mode: 'cover-top' = rekordbox-style fixed width, top-
// cropped; 'square' = old row-h × row-h thumb. Persists via localStorage,
// applied through html[data-art-fit] for the CSS rules above to swap.
const ART_FIT_LS_KEY = 'pdj-library-art-fit';
function loadArtFit() {
  try {
    const v = localStorage.getItem(ART_FIT_LS_KEY);
    if (v === 'square' || v === 'cover-top') return v;
  } catch (e) {}
  return 'cover-top';
}
function saveArtFit(v) { try { localStorage.setItem(ART_FIT_LS_KEY, v); } catch (e) {} }
function applyArtFit(v) { document.documentElement.setAttribute('data-art-fit', v); }
let libraryArtFit = loadArtFit();
applyArtFit(libraryArtFit);
let visibleColumns = loadVisibleColumns();
// TITLE is locked-on; guarantee it survives any stored state from older versions.
LIBRARY_COLUMNS.forEach(c => { if (c.locked) visibleColumns.add(c.key); });

function activeColumns() {
  return columnOrder
    .map(k => LIBRARY_COLUMNS.find(c => c.key === k))
    .filter(c => c && visibleColumns.has(c.key));
}

// ───────── SORT STATE ─────────
const SORT_LS_KEY = 'pdj-library-sort';
function loadLibrarySort() {
  try {
    const raw = localStorage.getItem(SORT_LS_KEY);
    if (raw) {
      const v = JSON.parse(raw);
      if (v && typeof v.key === 'string' && (v.dir === 'asc' || v.dir === 'desc')) {
        return v;
      }
    }
  } catch (e) { /* fall through */ }
  return { key: null, dir: 'asc' };
}
function saveLibrarySort() {
  try { localStorage.setItem(SORT_LS_KEY, JSON.stringify(librarySort)); }
  catch (e) { /* quota — ignore */ }
}
let librarySort = loadLibrarySort();

// Cycles a column through asc → desc → cleared on repeated header clicks.
function cycleLibrarySort(colKey) {
  if (librarySort.key !== colKey) {
    librarySort = { key: colKey, dir: 'asc' };
  } else if (librarySort.dir === 'asc') {
    librarySort = { key: colKey, dir: 'desc' };
  } else {
    librarySort = { key: null, dir: 'asc' };
  }
  saveLibrarySort();
}

function sortedRows(rows) {
  if (!librarySort.key) return rows;
  const col = LIBRARY_COLUMNS.find(c => c.key === librarySort.key);
  if (!col || !col.sort) return rows;
  const mul = librarySort.dir === 'desc' ? -1 : 1;
  // Slice so we don't mutate allTracks.
  return rows.slice().sort((a, b) => {
    const va = col.sort(a), vb = col.sort(b);
    if (va == null && vb == null) return 0;
    if (va == null) return 1;        // nulls sort last regardless of direction
    if (vb == null) return -1;
    if (typeof va === 'number' && typeof vb === 'number') return (va - vb) * mul;
    return String(va).localeCompare(String(vb)) * mul;
  });
}

// ───────── BULK SELECTION ─────────
// Tracks the user has checked in the library table — drives the
// bottom action bar. lastBulkClicked anchors shift-click range
// selection. Cleared when the source changes (switching to a
// different playlist).
let bulkSelectedIDs = new Set();
let lastBulkClicked = null;

function clearBulkSelection() {
  bulkSelectedIDs = new Set();
  lastBulkClicked = null;
  updateBulkBar();
  document.querySelectorAll('tr[data-track-row]').forEach(r => r.classList.remove('selected-bulk'));
  document.querySelectorAll('input[data-sel-row]').forEach(cb => { cb.checked = false; });
  const all = document.getElementById('lib-sel-all');
  if (all) { all.checked = false; all.indeterminate = false; }
}

function toggleBulkSelection(trackID, ev) {
  // Shift-click extends the range from the last-clicked checkbox to
  // the current one, using the order of rows currently in the table.
  // The end-state of every row in the range matches the just-clicked
  // checkbox's new state — same semantics as Finder/Explorer.
  const cb = ev.currentTarget;
  if (ev.shiftKey && lastBulkClicked != null) {
    const ids = Array.from(document.querySelectorAll('tr[data-track-row]'))
      .map(r => parseInt(r.getAttribute('data-track-row'), 10));
    const a = ids.indexOf(lastBulkClicked), b = ids.indexOf(trackID);
    if (a >= 0 && b >= 0) {
      const lo = Math.min(a, b), hi = Math.max(a, b);
      const desired = cb.checked;
      for (let i = lo; i <= hi; i++) {
        if (desired) bulkSelectedIDs.add(ids[i]);
        else         bulkSelectedIDs.delete(ids[i]);
      }
    }
  } else {
    if (cb.checked) bulkSelectedIDs.add(trackID);
    else            bulkSelectedIDs.delete(trackID);
  }
  lastBulkClicked = trackID;
  // Reflect into the visible checkboxes + row highlight without a
  // full table re-render (cheap — only touches rows actually in the DOM).
  document.querySelectorAll('tr[data-track-row]').forEach(r => {
    const id = parseInt(r.getAttribute('data-track-row'), 10);
    r.classList.toggle('selected-bulk', bulkSelectedIDs.has(id));
    const c = r.querySelector('input[data-sel-row]');
    if (c) c.checked = bulkSelectedIDs.has(id);
  });
  updateBulkBar();
}

function wireBulkSelectionHandlers() {
  document.querySelectorAll('input[data-sel-row]').forEach(cb => {
    cb.addEventListener('click', ev => ev.stopPropagation()); // don't open drawer
    cb.addEventListener('change', ev => {
      const tid = parseInt(cb.getAttribute('data-sel-row'), 10);
      toggleBulkSelection(tid, ev);
    });
  });
  const all = document.getElementById('lib-sel-all');
  if (all) {
    all.addEventListener('change', () => {
      const ids = Array.from(document.querySelectorAll('tr[data-track-row]'))
        .map(r => parseInt(r.getAttribute('data-track-row'), 10));
      if (all.checked) ids.forEach(id => bulkSelectedIDs.add(id));
      else             ids.forEach(id => bulkSelectedIDs.delete(id));
      document.querySelectorAll('tr[data-track-row]').forEach(r => {
        const id = parseInt(r.getAttribute('data-track-row'), 10);
        r.classList.toggle('selected-bulk', bulkSelectedIDs.has(id));
        const c = r.querySelector('input[data-sel-row]');
        if (c) c.checked = bulkSelectedIDs.has(id);
      });
      lastBulkClicked = null;
      updateBulkBar();
    });
  }
  refreshSelAllState();
}

// Tri-state the "select all" checkbox: checked when every visible
// row is selected, indeterminate when some are, unchecked when none.
function refreshSelAllState() {
  const all = document.getElementById('lib-sel-all');
  if (!all) return;
  const ids = Array.from(document.querySelectorAll('tr[data-track-row]'))
    .map(r => parseInt(r.getAttribute('data-track-row'), 10));
  if (ids.length === 0) {
    all.checked = false; all.indeterminate = false; return;
  }
  let n = 0;
  for (const id of ids) if (bulkSelectedIDs.has(id)) n++;
  all.checked = n === ids.length;
  all.indeterminate = n > 0 && n < ids.length;
}

function updateBulkBar() {
  const bar = document.getElementById('bulk-bar');
  if (!bar) return;
  const n = bulkSelectedIDs.size;
  if (n === 0) { bar.classList.remove('show'); bar.innerHTML = ''; return; }
  bar.classList.add('show');
  bar.innerHTML = `
    <span class="count">${n} track${n === 1 ? '' : 's'} selected</span>
    <button id="bulk-color">COLOR…</button>
    <button id="bulk-rating">RATING…</button>
    <button id="bulk-tag-add">+ TAG…</button>
    <button id="bulk-tag-remove">− TAG…</button>
    <button id="bulk-playlist">+ PLAYLIST…</button>
    ${activeRegularPlaylist() ? '<button id="bulk-pl-remove" title="Remove selected from this playlist">− PLAYLIST</button>' : ''}
    <button id="bulk-export">EXPORT…</button>
    <button class="danger" id="bulk-delete">DELETE</button>
    <span class="spacer"></span>
    <button class="danger" id="bulk-clear">CLEAR</button>
  `;
  bar.querySelector('#bulk-clear').onclick = clearBulkSelection;
  bar.querySelector('#bulk-color').onclick   = e => openBulkColorPicker(e.currentTarget);
  bar.querySelector('#bulk-rating').onclick  = e => openBulkRatingPicker(e.currentTarget);
  bar.querySelector('#bulk-tag-add').onclick = e => openBulkTagPicker(e.currentTarget, 'add');
  bar.querySelector('#bulk-tag-remove').onclick = e => openBulkTagPicker(e.currentTarget, 'remove');
  bar.querySelector('#bulk-playlist').onclick = e => openBulkPlaylistPicker(e.currentTarget);
  const plRemove = bar.querySelector('#bulk-pl-remove');
  if (plRemove) plRemove.onclick = bulkRemoveFromPlaylist;
  bar.querySelector('#bulk-export').onclick = () => {
    const sel = Array.from(bulkSelectedIDs);
    openExportModal('selection:' + sel.join(','), `${sel.length} SELECTED TRACK${sel.length === 1 ? '' : 'S'}`);
  };
  bar.querySelector('#bulk-delete').onclick  = bulkDeleteTracks;
  refreshSelAllState();
}

// Bulk delete: removes selected tracks from the library (and playlist /
// tag / cue references on the server). Does NOT touch the underlying
// audio files. Confirms first because there's no undo.
async function bulkDeleteTracks() {
  const ids = Array.from(bulkSelectedIDs);
  if (ids.length === 0) return;
  const msg = `Remove ${ids.length} track${ids.length === 1 ? '' : 's'} from the library?\n\n` +
    `This won't touch the audio file${ids.length === 1 ? '' : 's'}, but it will:\n` +
    `  • drop the track${ids.length === 1 ? '' : 's'} from all playlists\n` +
    `  • clear tags, colour, rating, and saved cues\n` +
    `  • remove the library entr${ids.length === 1 ? 'y' : 'ies'}\n\n` +
    `This can't be undone from the UI — you'd need to re-import.`;
  if (!await openConfirmModal({
    title: `REMOVE ${ids.length} TRACK${ids.length === 1 ? '' : 'S'}`,
    message: msg, okLabel: 'REMOVE', danger: true,
  })) return;
  const results = await Promise.allSettled(ids.map(id =>
    fetch(`/api/tracks/${id}`, { method: 'DELETE' })));
  const failed = results.filter(r => r.status !== 'fulfilled' || !r.value.ok).length;
  // Optimistically prune local state for whatever succeeded; the next
  // poll will resync against the server.
  const ok = ids.filter((_, i) => {
    const r = results[i];
    return r.status === 'fulfilled' && r.value.ok;
  });
  if (ok.length > 0) {
    const okSet = new Set(ok);
    allTracks = allTracks.filter(t => !okSet.has(t.id));
    rememberTracksSnapshot();
    clearBulkSelection();
    refreshLibraryView();
  }
  if (failed > 0) {
    toast(`DELETED ${ok.length}, FAILED ${failed}`, 'error');
  } else {
    toast(`DELETED ${ok.length} TRACK${ok.length === 1 ? '' : 'S'}`, 'success');
  }
}

// Bulk colour: reuses the swatch picker UI; one PUT /api/tracks/color
// for the whole selection. Local cache (allTracks) is patched so the
// COLOR column reflects the change without waiting for the 1Hz poll.
function openBulkColorPicker(anchorEl) {
  closeColorPicker();
  const menu = document.createElement('div');
  menu.className = 'color-picker';
  const swatches = ['none','1','2','3','4','5','6','7','8'];
  menu.innerHTML = swatches.map(s => {
    if (s === 'none') return `<div class="swatch none" data-color="0" title="Clear colour"></div>`;
    const id = parseInt(s, 10);
    const name = (typeof COLOR_NAMES !== 'undefined' && COLOR_NAMES[id]) || ('Color ' + id);
    return `<div class="swatch" data-color="${id}" style="background:${TRACK_COLORS[id]}" title="${name}"></div>`;
  }).join('');
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  menu.style.left = Math.max(8, r.left) + 'px';
  menu.style.top = (r.top - menu.offsetHeight - 6) + 'px';
  menu.querySelectorAll('.swatch').forEach(sw => sw.addEventListener('click', async () => {
    const cid = parseInt(sw.getAttribute('data-color'), 10);
    const ids = Array.from(bulkSelectedIDs);
    const res = await fetchJSON('/api/tracks/color', {
      method: 'PUT', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ track_ids: ids, color_id: cid }),
    });
    if (!res) { closeColorPicker(); toast('COLOR FAILED', 'error'); return; }
    const patch = { color_id: cid, color_name: COLOR_NAMES[cid] || '' };
    ids.forEach(id => {
      markPendingTrackEdit(id, patch);
      const t = allTracks.find(x => x.id === id);
      if (t) Object.assign(t, patch);
    });
    rememberTracksSnapshot();
    closeColorPicker();
    refreshLibraryView();
    toast(`COLOR · ${ids.length} TRACK${ids.length === 1 ? '' : 'S'}`, 'success');
  }));
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}

function openBulkRatingPicker(anchorEl) {
  closeColorPicker(); // reuse close-on-outside infra
  const menu = document.createElement('div');
  menu.className = 'color-picker'; // reuses styles — small horizontal popover
  menu.innerHTML = [0,1,2,3,4,5].map(n =>
    `<div class="rating-swatch" data-rating="${n}" title="${n === 0 ? 'Unrated' : n + ' star' + (n > 1 ? 's' : '')}">${n === 0 ? '–' : '★'.repeat(n)}</div>`
  ).join('');
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  menu.style.left = Math.max(8, r.left) + 'px';
  menu.style.top = (r.top - menu.offsetHeight - 6) + 'px';
  menu.querySelectorAll('[data-rating]').forEach(sw => sw.addEventListener('click', async () => {
    const rating = parseInt(sw.getAttribute('data-rating'), 10);
    const ids = Array.from(bulkSelectedIDs);
    const res = await fetchJSON('/api/tracks/rating', {
      method: 'PUT', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ track_ids: ids, rating }),
    });
    if (!res) { closeColorPicker(); toast('RATING FAILED', 'error'); return; }
    ids.forEach(id => {
      markPendingTrackEdit(id, { rating });
      const t = allTracks.find(x => x.id === id);
      if (t) t.rating = rating;
    });
    rememberTracksSnapshot();
    closeColorPicker();
    refreshLibraryView();
    toast(`RATING ${rating || '–'} · ${ids.length} TRACK${ids.length === 1 ? '' : 'S'}`, 'success');
  }));
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}

// Bulk tag add/remove: opens a small popover listing every tag in
// allTagsCache; clicking a tag adds it to (or removes it from) every
// selected track with one POST /api/tracks/tags. The server applies it
// to each track's own tags, so other tags on the tracks are kept.
async function openBulkTagPicker(anchorEl, mode) {
  closeColorPicker();
  const cats = await ensureAllCategories();
  const tags = (allTagsCache || []).slice().sort((a,b) => a.name.localeCompare(b.name));
  const menu = document.createElement('div');
  menu.className = 'tag-picker bulk-picker';
  menu.style.maxWidth = '320px';
  menu.innerHTML = `<div class="head">${mode === 'add' ? 'ADD TAG TO SELECTION' : 'REMOVE TAG FROM SELECTION'}</div>
    <div class="list">${tags.length === 0
      ? '<div class="picker-empty">NO TAGS YET</div>'
      : groupedTagPickerHTML(tags, cats, { showDot: true })
    }</div>`;
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  menu.style.left = Math.max(8, r.left) + 'px';
  menu.style.top = (r.top - Math.min(menu.offsetHeight, 360) - 6) + 'px';
  menu.querySelectorAll('.picker-item').forEach(item => item.addEventListener('click', async () => {
    const tagID = parseInt(item.getAttribute('data-tag-id'), 10);
    const ids = Array.from(bulkSelectedIDs);
    const res = await fetchJSON('/api/tracks/tags', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ track_ids: ids, [mode]: [tagID] }),
    });
    closeColorPicker();
    if (!res) { toast('TAG FAILED', 'error'); return; }
    const tagName = (tags.find(t => t.id === tagID) || {}).name || 'TAG';
    toast(`${mode === 'add' ? '+ ' : '− '}${tagName.toUpperCase()} · ${ids.length} TRACK${ids.length === 1 ? '' : 'S'}`, 'success');
  }));
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}

function openBulkPlaylistPicker(anchorEl) {
  closeColorPicker();
  const hist = historyPlaylistIDs();
  const leaves = (playlistsCache || []).filter(p => !p.is_folder && !p.is_smart && !hist.has(p.id));
  const menu = document.createElement('div');
  menu.className = 'tag-picker bulk-picker';
  menu.style.maxWidth = '320px';
  menu.innerHTML = `<div class="head">ADD TO PLAYLIST</div>
    <div class="list">${leaves.length === 0
      ? '<div class="picker-empty">NO PLAYLISTS</div>'
      : leaves.map(p => `<div class="picker-item" data-pl-id="${p.id}"><span class="picker-name">${escape(p.name)}</span></div>`).join('')
    }</div>`;
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  menu.style.left = Math.max(8, r.left) + 'px';
  menu.style.top = (r.top - Math.min(menu.offsetHeight, 360) - 6) + 'px';
  menu.querySelectorAll('.picker-item').forEach(item => item.addEventListener('click', async () => {
    const plID = parseInt(item.getAttribute('data-pl-id'), 10);
    const ids = Array.from(bulkSelectedIDs);
    const cur = await fetchJSON(`/api/playlists/${plID}/tracks`);
    const have = Array.isArray(cur) ? cur.map(t => t.id) : [];
    const next = [...have];
    let added = 0;
    for (const tid of ids) {
      if (!next.includes(tid)) { next.push(tid); added++; }
    }
    await fetch(`/api/playlists/${plID}/tracks`, {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ track_ids: next }),
    });
    closeColorPicker();
    await loadPlaylists(true);
    const pl = (playlistsCache || []).find(p => p.id === plID);
    toast(`+ ${added} → ${(pl && pl.name || '').toUpperCase()}`, 'success');
  }));
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}

// ───────── MOBILE LIBRARY (cards) ─────────
// On narrow screens the desktop table is replaced by a touch-friendly card
// list. It renders the same filtered + sorted rows and reuses the same
// helpers (waveform thumbnail, rating stars, colour chip, LOAD-to-deck), so
// the data path and interactions stay in sync with desktop.
// Matches the CSS media query: phones (≤640px) or any touch tablet
// (coarse pointer). Kept in sync so the JS renders cards exactly when the
// CSS switches to the touch layout.
const mobileMQ = window.matchMedia('(max-width: 640px), (pointer: coarse)');
function isMobileLayout() { return mobileMQ.matches; }
// Re-render the library when crossing the breakpoint (rotate / resize),
// so the table ↔ cards swap happens live.
mobileMQ.addEventListener('change', () => {
  closeMobilePlaylists(); // reconcile the off-canvas panel across the breakpoint
  if (typeof refreshLibraryView === 'function') refreshLibraryView();
});

// The playlist sidebar is an off-canvas panel on mobile — opened by the
// toolbar source button, dismissed by the backdrop or by picking a source.
function openMobilePlaylists() {
  document.getElementById('lib-sidebar')?.classList.add('open');
  document.getElementById('lib-sidebar-backdrop')?.classList.add('open');
}
function closeMobilePlaylists() {
  document.getElementById('lib-sidebar')?.classList.remove('open');
  document.getElementById('lib-sidebar-backdrop')?.classList.remove('open');
}
document.getElementById('lib-src-toggle')?.addEventListener('click', openMobilePlaylists);
document.getElementById('lib-sidebar-backdrop')?.addEventListener('click', closeMobilePlaylists);

// The sidebar lives inside <main> for the desktop two-column grid, but the
// library <section class="panel"> has an entrance animation whose transform
// creates a containing block — which would pin the mobile off-canvas panel
// (position:fixed) to the panel's full height instead of the viewport,
// dropping its footer off-screen. So on the mobile layout, move the panel and
// its backdrop out to <body> (mirroring the bulk-bar/detail-drawer), and put
// them back in the grid on desktop. All handlers are id-based, so they survive
// the move; only the DOM parent changes.
function placeMobileSidebar() {
  const layout = document.querySelector('.lib-layout');
  const sidebar = document.getElementById('lib-sidebar');
  const backdrop = document.getElementById('lib-sidebar-backdrop');
  const main = layout?.querySelector('.lib-main');
  if (!layout || !sidebar) return;
  if (isMobileLayout()) {
    if (sidebar.parentElement !== document.body) document.body.appendChild(sidebar);
    if (backdrop && backdrop.parentElement !== document.body) document.body.appendChild(backdrop);
  } else {
    // Restore original order: backdrop, then sidebar, both before .lib-main.
    if (sidebar.parentElement !== layout && main) layout.insertBefore(sidebar, main);
    if (backdrop && backdrop.parentElement !== layout) layout.insertBefore(backdrop, sidebar);
    closeMobilePlaylists();
  }
}
placeMobileSidebar();
mobileMQ.addEventListener('change', placeMobileSidebar);

function libCardHtml(t) {
  const art = artHTML(t, 'lib-art', 'lib-art placeholder', '♪');
  const facts = [];
  if (t.bpm) facts.push((+t.bpm).toFixed(1));
  if (t.key) facts.push(t.key);
  const dur = fmtDuration(t.duration);
  if (dur) facts.push(dur);
  const color = t.color_id > 0
    ? `<button type="button" class="col-color-chip" data-color-pick="${t.id}" style="background:${TRACK_COLORS[t.color_id] || '#666'}" title="${escape(t.color_name || '')}"></button>`
    : `<button type="button" class="col-color-chip empty" data-color-pick="${t.id}" title="Set color"></button>`;
  return `<div class="lib-card ${t.file_missing ? 'file-missing' : ''}" data-track-card="${t.id}">
      ${art}
      <div class="lib-card-main">
        <div class="lib-card-title">${escape(t.title || 'Untitled')}</div>
        <div class="lib-card-artist">${escape(t.artist || '')}</div>
        <div class="lib-card-meta"><span class="mc-facts">${escape(facts.join(' · '))}</span>${fmtStars(t.rating || 0, t.id)}${color}</div>
        <div class="lib-card-wave"><img class="loading" data-libtrack="${t.id}" alt="" loading="lazy" src="${waveThumbSrc(t.id)}" onload="thumbLoaded(this)">${cueMarkerOverlay(t.id, t.duration)}</div>
      </div>
      <button class="load-btn ${currentPlayers.length === 0 ? '' : 'ready'}" data-track="${t.id}" title="Load to deck"><span class="arrow">▶</span></button>
    </div>`;
}

function renderLibraryCards(el, rows) {
  el.innerHTML = `<div class="lib-cards">${rows.map(libCardHtml).join('')}</div>`;
  el.querySelectorAll('.load-btn').forEach(btn =>
    btn.addEventListener('click', e => handleLoadClick(e, btn)));
  el.querySelectorAll('[data-color-pick]').forEach(chip =>
    chip.addEventListener('click', e => {
      e.stopPropagation();
      const tid = parseInt(chip.getAttribute('data-color-pick'), 10);
      const tr = allTracks.find(x => x.id === tid);
      openColorPicker(chip, tid, (tr && tr.color_id) || 0);
    }));
  wireStarClicks(el);
  el.querySelectorAll('.lib-card').forEach(card =>
    card.addEventListener('click', e => {
      // The LOAD button, colour chip and stars handle their own taps.
      if (e.target.closest('.load-btn, [data-color-pick], .stars')) return;
      openDetail(parseInt(card.getAttribute('data-track-card'), 10));
    }));
  if (typeof syncStickyOffsets === 'function') syncStickyOffsets();
}

function renderLibrary(tracks) {
  const el = document.getElementById('library-content');
  const stats = document.getElementById('lib-stats');
  const q = document.getElementById('lib-search').value.toLowerCase();
  let rows = tracks;
  if (q) {
    rows = rows.filter(t =>
      (t.title || '').toLowerCase().includes(q) ||
      (t.artist || '').toLowerCase().includes(q) ||
      (t.album || '').toLowerCase().includes(q));
  }
  rows = applyLibraryFilters(rows);
  stats.innerHTML = `<strong>${rows.length}</strong> / ${tracks.length} TRACKS`;
  if (rows.length === 0) {
    if (selectedSource === 'collection' && allTracks.length === 0) {
      // First-run onboarding: the whole library is empty → invite an import.
      el.innerHTML = `<div class="lib-empty-cta">
        <div class="lib-empty-title">Your library is empty</div>
        <button type="button" id="lib-empty-add" class="source-btn">+ Add tracks or folders</button>
        <div class="lib-empty-sub">MP3 · M4A · FLAC · WAV · AIFF</div>
        <div class="lib-empty-or">or</div>
        <button type="button" id="lib-empty-import" class="source-btn">↓ Import your library</button>
        <div class="lib-empty-sub">rekordbox · Traktor</div>
      </div>`;
      const b = document.getElementById('lib-empty-import');
      if (b) b.onclick = () => openImportRekordboxDialog();
      const a = document.getElementById('lib-empty-add');
      if (a) a.onclick = () => openAddBrowserDialog();
    } else {
      el.innerHTML = '<div class="empty-state">NO TRACKS MATCH</div>';
    }
    return;
  }
  const cols = activeColumns();
  rows = sortedRows(rows);
  // On phones, render touch cards instead of the desktop table (same rows).
  if (isMobileLayout()) { renderLibraryCards(el, rows); return; }
  // Render every filtered row — no cap. Off-screen rows are made cheap by
  // `content-visibility: auto` on table.lib tr[data-track-row] (see CSS), so
  // the browser skips their layout/paint until they scroll near the viewport,
  // and the `contain-intrinsic-size` reserves the right height so the
  // scrollbar still represents the full list.
  const visible = rows;
  const head = cols.map(c => {
    const sortable = c.sortable !== false && typeof c.sort === 'function';
    const isActive = sortable && librarySort.key === c.key;
    const cls = [c.thClass || '', sortable ? 'sortable' : '', isActive ? 'active' : '']
      .filter(Boolean).join(' ');
    const dataAttr = sortable ? ` data-sort-col="${c.key}"` : '';
    let ind = '';
    if (isActive) {
      ind = `<span class="sort-ind">${librarySort.dir === 'asc' ? '▲' : '▼'}</span>`;
    } else if (sortable) {
      ind = '<span class="sort-ind hint">↕</span>';
    }
    const title = sortable ? 'Click to sort · drag to reorder' : 'Drag to reorder';
    return `<th class="${cls} col-reorder" data-col-key="${c.key}" draggable="true" title="${title}"${dataAttr}>${c.label}${ind}</th>`;
  }).join('');
  // Prepend the always-on selection checkbox column; append the load
  // button column. The sel-all checkbox in the header toggles every
  // row at once — now that the cap is gone every filtered row is rendered,
  // so this matches the full filtered total shown in the stats chip.
  const head2 = `<th class="sel-col"><input type="checkbox" id="lib-sel-all" title="Select all"></th>` + head + '<th></th>';
  // Source-aware row extras: when viewing a regular playlist, rows can be
  // drag-reordered and get a per-row × that removes them from the playlist.
  const reorderMode = activeRegularPlaylist();
  const body = visible.map(t => {
    const cells = cols.map(c => `<td class="${c.tdClass || ''}">${c.cell(t)}</td>`).join('');
    const bulkSelected = bulkSelectedIDs.has(t.id);
    return `<tr data-track-row="${t.id}" draggable="true" class="${selectedTrackID === t.id ? 'selected' : ''} ${bulkSelected ? 'selected-bulk' : ''} ${t.file_missing ? 'file-missing' : ''}">` +
      `<td class="sel-col"><input type="checkbox" data-sel-row="${t.id}" ${bulkSelected ? 'checked' : ''}></td>` +
      cells +
      `<td class="load-cell">
        ${reorderMode ? `<button class="pl-rm-btn" data-pl-remove="${t.id}" title="Remove from playlist">×</button>` : ''}
        <button class="load-btn ${currentPlayers.length === 0 ? '' : 'ready'}" data-track="${t.id}">
          <span class="arrow">▶</span>LOAD
        </button>
      </td></tr>`;
  }).join('');
  // <colgroup> drives the column widths under table-layout:fixed. Order matches
  // the header/body: sel checkbox, each data column, trailing load column (auto).
  const colgroup = `<colgroup><col class="c-sel">` +
    cols.map(c => `<col data-col-key="${c.key}">`).join('') +
    `<col class="c-load"></colgroup>`;
  el.innerHTML = `<div class="lib-table-wrap"><table class="lib">
    ${colgroup}
    <thead><tr>${head2}</tr></thead>
    <tbody>${body}</tbody>
  </table></div>`;
  applyColWidthsLive(el);   // set <col> widths + table min-width from the store
  attachColResizers(el);    // draggable header edges
  el.querySelectorAll('.load-btn').forEach(btn => {
    btn.addEventListener('click', e => handleLoadClick(e, btn));
  });
  // Per-row × (playlist view only) → drop the track from this playlist.
  // Looks the index up by ID so an active search/sort can't misalign it.
  el.querySelectorAll('[data-pl-remove]').forEach(btn => {
    btn.addEventListener('click', e => {
      e.stopPropagation();
      const tid = parseInt(btn.getAttribute('data-pl-remove'), 10);
      const idx = plSelectedTracks.findIndex(t => t.id === tid);
      if (idx !== -1) removePlaylistTrack(idx);
    });
  });
  // Bulk-selection checkboxes (per-row + the header sel-all) — wires
  // toggleBulkSelection on change so the bottom bulk-action bar shows.
  wireBulkSelectionHandlers();
  updateBulkBar();
  // Header click → cycle sort. The header is sticky so this stays reachable
  // even when the table scrolls.
  el.querySelectorAll('th[data-sort-col]').forEach(th => {
    th.addEventListener('click', () => {
      cycleLibrarySort(th.getAttribute('data-sort-col'));
      refreshLibraryView();
    });
  });
  // Drag a header cell onto another to reorder columns. Click-to-sort still
  // works — a plain click (no movement) doesn't start a drag. The dragover
  // guard on colDragKey means a row drag (which leaves colDragKey null) never
  // gets treated as a column drop.
  let colDragKey = null;
  const clearColDropHints = () => el.querySelectorAll('th[data-col-key]')
    .forEach(h => h.classList.remove('col-drop-before', 'col-drop-after'));
  el.querySelectorAll('th[data-col-key]').forEach(th => {
    th.addEventListener('dragstart', e => {
      colDragKey = th.getAttribute('data-col-key');
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', colDragKey);
      th.classList.add('col-dragging');
    });
    th.addEventListener('dragend', () => {
      th.classList.remove('col-dragging');
      clearColDropHints();
      colDragKey = null;
    });
    th.addEventListener('dragover', e => {
      if (!colDragKey) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      clearColDropHints();
      if (th.getAttribute('data-col-key') === colDragKey) return;
      const rect = th.getBoundingClientRect();
      const before = (e.clientX - rect.left) < rect.width / 2;
      th.classList.add(before ? 'col-drop-before' : 'col-drop-after');
    });
    th.addEventListener('drop', e => {
      if (!colDragKey) return;
      e.preventDefault();
      const rect = th.getBoundingClientRect();
      const before = (e.clientX - rect.left) < rect.width / 2;
      moveColumn(colDragKey, th.getAttribute('data-col-key'), before);
      colDragKey = null;
      refreshLibraryView();
    });
  });
  // Color-chip click → open picker. Stops propagation so the row click doesn't
  // also fire and open the drawer.
  el.querySelectorAll('[data-color-pick]').forEach(chip => {
    chip.addEventListener('click', e => {
      e.stopPropagation();
      const tid = parseInt(chip.getAttribute('data-color-pick'), 10);
      const tr = allTracks.find(x => x.id === tid);
      openColorPicker(chip, tid, (tr && tr.color_id) || 0);
    });
  });
  // Star clicks (RATING column) → setTrackRating.
  wireStarClicks(el);
  // Row click → open detail drawer. Load button stops propagation so it
  // doesn't. Drag behaviour is source-aware: when viewing a regular
  // playlist, dragging a row reorders within that playlist; otherwise
  // (Collection, or a smart playlist) the drag pops up the floating
  // playlist drop overlay so the user can add the track elsewhere.
  el.querySelectorAll('tr[data-track-row]').forEach((row, rowIdx) => {
    const tid = parseInt(row.getAttribute('data-track-row'), 10);
    row.addEventListener('click', (e) => {
      // Clicks inside the bulk-select column shouldn't pop the drawer.
      if (e.target.closest('td.sel-col')) return;
      openDetail(tid);
    });
    row.addEventListener('dragstart', e => {
      if (reorderMode) {
        dnd = { type: 'track', idx: rowIdx };
        e.dataTransfer.effectAllowed = 'move';
      } else {
        dnd = { type: 'libtrack', trackID: tid };
        e.dataTransfer.effectAllowed = 'copy';
        showPlaylistDropOverlay();
      }
      e.dataTransfer.setData('text/plain', String(tid));
      row.classList.add('dragging');
    });
    if (reorderMode) {
      // dragover paints an orange edge above or below the row based on
      // cursor Y; drop splices plSelectedTracks at the chosen index and
      // posts back. row index === plSelectedTracks index since the
      // table renders the playlist in insertion order.
      row.addEventListener('dragover', e => {
        if (!dnd || dnd.type !== 'track') return;
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
        const rect = row.getBoundingClientRect();
        const before = (e.clientY - rect.top) < rect.height / 2;
        document.querySelectorAll('tr[data-track-row]').forEach(r => {
          r.classList.remove('drop-before', 'drop-after');
        });
        row.classList.add(before ? 'drop-before' : 'drop-after');
      });
      row.addEventListener('drop', e => {
        if (!dnd || dnd.type !== 'track') return;
        e.preventDefault();
        const targetIdx = rowIdx;
        const rect = row.getBoundingClientRect();
        const before = (e.clientY - rect.top) < rect.height / 2;
        let insertAt = targetIdx + (before ? 0 : 1);
        const src = dnd.idx;
        if (src === insertAt || (src + 1 === insertAt && !before)) return;
        const ids = plSelectedTracks.map(t => t.id);
        const [moved] = ids.splice(src, 1);
        if (src < insertAt) insertAt--;
        ids.splice(insertAt, 0, moved);
        postPlaylistTrackIDs(plSelectedID, ids);
      });
    }
  });
  // Library-row thumbnails are now <img src="/api/analysis/waveform-png/...">
  // — the browser handles lazy-loading (loading="lazy"), and the PNG is
  // server-rendered + disk-cached, with long HTTP cache headers so reloads
  // come from the browser cache without hitting the server at all.

  // Realign the sticky column-header row to the toolbar's actual
  // height — the table just re-rendered, so the th elements are new.
  if (typeof syncStickyOffsets === 'function') syncStickyOffsets();
}
(() => {
  const inp = document.getElementById('lib-search');
  const clr = document.getElementById('lib-search-clear');
  const sync = () => {
    if (clr) clr.classList.toggle('show', inp.value.length > 0);
  };
  inp.addEventListener('input', () => {
    sync();
    refreshLibraryView();
  });
  if (clr) {
    clr.addEventListener('click', () => {
      inp.value = '';
      sync();
      inp.focus();
      refreshLibraryView();
    });
  }
  sync();
})();

// ───────── COLUMNS POPOVER ─────────
let columnsPopover = null;
function closeColumnsPopover() {
  if (columnsPopover) {
    columnsPopover.remove();
    columnsPopover = null;
    document.getElementById('lib-columns-btn').classList.remove('active');
  }
}
// The COLUMNS popover's checkbox list, rendered in the current display order
// (columnOrder) so it mirrors the table. Each row is draggable to reorder.
function columnsPopoverListHTML() {
  return columnOrder.map(key => {
    const c = LIBRARY_COLUMNS.find(x => x.key === key);
    if (!c) return '';
    const checked = visibleColumns.has(c.key) ? 'checked' : '';
    const locked = c.locked ? 'disabled' : '';
    const cls = ['col-pop-row', c.locked ? 'locked' : ''].filter(Boolean).join(' ');
    return `<label class="${cls}" data-col-key="${c.key}" draggable="true" title="Drag to reorder">` +
      `<input type="checkbox" data-col="${c.key}" ${checked} ${locked}>${c.label}</label>`;
  }).join('');
}
// Wire drag-to-reorder on the popover's column rows. Re-callable after the
// list is re-rendered (the drop handler regenerates .cols-list innerHTML).
function wireColumnsPopoverDrag(pop) {
  let dragKey = null;
  const list = pop.querySelector('.cols-list');
  if (!list) return;
  const clear = () => list.querySelectorAll('.col-pop-row')
    .forEach(l => l.classList.remove('col-drop-before', 'col-drop-after'));
  list.querySelectorAll('label[data-col-key]').forEach(label => {
    label.addEventListener('dragstart', e => {
      dragKey = label.getAttribute('data-col-key');
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', dragKey);
      label.classList.add('col-dragging');
    });
    label.addEventListener('dragend', () => { label.classList.remove('col-dragging'); clear(); dragKey = null; });
    label.addEventListener('dragover', e => {
      if (!dragKey) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      clear();
      if (label.getAttribute('data-col-key') === dragKey) return;
      const rect = label.getBoundingClientRect();
      const before = (e.clientY - rect.top) < rect.height / 2;
      label.classList.add(before ? 'col-drop-before' : 'col-drop-after');
    });
    label.addEventListener('drop', e => {
      if (!dragKey) return;
      e.preventDefault();
      const rect = label.getBoundingClientRect();
      const before = (e.clientY - rect.top) < rect.height / 2;
      moveColumn(dragKey, label.getAttribute('data-col-key'), before);
      dragKey = null;
      list.innerHTML = columnsPopoverListHTML();
      wireColumnsPopoverDrag(pop);
      refreshLibraryView();
    });
  });
}
function openColumnsPopover() {
  closeColumnsPopover();
  const btn = document.getElementById('lib-columns-btn');
  const pop = document.createElement('div');
  pop.className = 'cols-popover';
  const rowHRow = `
    <div class="row-h-row">
      <span class="row-h-label">ROW HEIGHT</span>
      <input type="range" id="row-h-range" class="row-h-slider"
             min="${ROW_H_MIN}" max="${ROW_H_MAX}" step="2" value="${libraryRowHeight}">
      <span class="row-h-val" id="row-h-val">${libraryRowHeight}px</span>
    </div>
    <div class="row-h-row">
      <span class="row-h-label">ALBUM ART</span>
      <select id="art-fit-select" class="row-h-slider" style="flex:1">
        <option value="cover-top" ${libraryArtFit === 'cover-top' ? 'selected' : ''}>Show the upper part of the artwork</option>
        <option value="square"    ${libraryArtFit === 'square'    ? 'selected' : ''}>Show the entire artwork</option>
      </select>
    </div>
    <div class="row-h-row">
      <span class="row-h-label">WIDTH</span>
      <select id="col-mode-select" class="row-h-slider" style="flex:1">
        <option value="fit"    ${libColMode === 'fit'    ? 'selected' : ''}>Fit columns to window</option>
        <option value="scroll" ${libColMode === 'scroll' ? 'selected' : ''}>Allow horizontal scroll</option>
      </select>
    </div>`;
  pop.innerHTML = '<div class="head"><span>COLUMNS</span><button type="button" class="reset" data-action="autofit" title="Size the visible columns to fill the panel width">AUTO FIT</button><button type="button" class="reset" data-action="reset">RESET</button></div>' +
    '<div class="cols-list">' + columnsPopoverListHTML() + '</div>' + rowHRow;
  document.body.appendChild(pop);
  const r = btn.getBoundingClientRect();
  pop.style.left = Math.max(8, r.left) + 'px';
  pop.style.top  = (r.bottom + 4) + 'px';
  btn.classList.add('active');
  columnsPopover = pop;
  wireColumnsPopoverDrag(pop);

  pop.addEventListener('change', e => {
    const cb = e.target.closest('input[data-col]');
    if (!cb) return;
    const key = cb.getAttribute('data-col');
    if (cb.checked) visibleColumns.add(key);
    else            visibleColumns.delete(key);
    saveVisibleColumns();
    refreshLibraryView();
  });
  pop.addEventListener('click', e => {
    if (e.target.matches('[data-action="autofit"]')) {
      autoFitColumns();
      return;
    }
    if (e.target.matches('[data-action="reset"]')) {
      visibleColumns = new Set(LIBRARY_COLUMNS.filter(c => c.default).map(c => c.key));
      saveVisibleColumns();
      columnOrder = LIBRARY_COLUMNS.map(c => c.key);
      saveColumnOrder();
      libColWidths = {};
      saveColWidths();
      libraryRowHeight = 36;
      saveRowHeight(libraryRowHeight);
      applyRowHeight(libraryRowHeight);
      libraryArtFit = 'cover-top';
      saveArtFit(libraryArtFit);
      applyArtFit(libraryArtFit);
      closeColumnsPopover();
      refreshLibraryView();
    }
  });
  const rng = pop.querySelector('#row-h-range');
  const rngVal = pop.querySelector('#row-h-val');
  if (rng) {
    rng.addEventListener('input', () => {
      libraryRowHeight = parseInt(rng.value, 10);
      rngVal.textContent = libraryRowHeight + 'px';
      applyRowHeight(libraryRowHeight);
      applyColWidthsLive(); // square-mode art column tracks row height
    });
    rng.addEventListener('change', () => { saveRowHeight(libraryRowHeight); scheduleWaveRefetch(); });
  }
  const artSel = pop.querySelector('#art-fit-select');
  if (artSel) {
    artSel.addEventListener('change', () => {
      libraryArtFit = artSel.value;
      applyArtFit(libraryArtFit);
      saveArtFit(libraryArtFit);
      applyColWidthsLive(); // art column width depends on the fit mode
    });
  }
  const colModeSel = pop.querySelector('#col-mode-select');
  if (colModeSel) {
    colModeSel.addEventListener('change', () => {
      libColMode = colModeSel.value === 'scroll' ? 'scroll' : 'fit';
      try { localStorage.setItem(COL_MODE_LS_KEY, libColMode); } catch (e) {}
      applyColMode(libColMode);
      applyColWidthsLive();
      syncStickyOffsets();
    });
  }
  // Close on outside click. Defer one frame so the opening click doesn't trip it.
  setTimeout(() => {
    document.addEventListener('click', columnsOutsideHandler);
  }, 0);
}
function columnsOutsideHandler(e) {
  if (!columnsPopover) {
    document.removeEventListener('click', columnsOutsideHandler);
    return;
  }
  if (columnsPopover.contains(e.target)) return;
  if (e.target.closest('#lib-columns-btn')) return;
  closeColumnsPopover();
  document.removeEventListener('click', columnsOutsideHandler);
}
document.getElementById('lib-columns-btn').addEventListener('click', e => {
  e.stopPropagation();
  if (columnsPopover) closeColumnsPopover();
  else openColumnsPopover();
});

function handleLoadClick(e, btn) {
  e.stopPropagation();
  closeLoadMenu();
  if (currentPlayers.length === 0) {
    toast('NO CDJS CONNECTED', 'error');
    return;
  }
  const trackID = parseInt(btn.getAttribute('data-track'), 10);
  if (currentPlayers.length === 1) {
    loadTrack(trackID, currentPlayers[0].device_number);
    return;
  }
  const menu = document.createElement('div');
  menu.className = 'load-menu';
  menu.innerHTML = '<div class="head">SEND TO PLAYER</div>' +
    currentPlayers.map(p => `
      <div class="item" data-dev="${p.device_number}">
        <span class="pnum">${String(p.device_number).padStart(2,'0')}</span>
        <span>${escape(p.name || 'CDJ')}</span>
      </div>`).join('');
  document.body.appendChild(menu);
  // Measure the menu after it's in the DOM so we know its real width — the
  // CSS min-width is 200px but a long player name can push it wider. Then
  // right-align the menu's right edge with the LOAD button's right edge,
  // and clamp inside the viewport so we don't clip off either edge.
  const r = btn.getBoundingClientRect();
  const menuW = menu.offsetWidth;
  const menuH = menu.offsetHeight;
  const margin = 8;
  let left = r.right - menuW;
  if (left < margin) left = margin;
  if (left + menuW > window.innerWidth - margin) left = window.innerWidth - menuW - margin;
  // Prefer below the button. Flip above if it would overflow the bottom edge.
  let top = r.bottom + 4;
  if (top + menuH > window.innerHeight - margin) top = r.top - menuH - 4;
  if (top < margin) top = margin;
  menu.style.left = left + 'px';
  menu.style.top = top + 'px';
  menu.querySelectorAll('div.item').forEach(item => {
    item.addEventListener('click', ev => {
      const dev = parseInt(item.getAttribute('data-dev'), 10);
      loadTrack(trackID, dev);
      closeLoadMenu();
    });
  });
  setTimeout(() => document.addEventListener('click', closeLoadMenu, { once: true }), 0);
}
function closeLoadMenu() {
  document.querySelectorAll('.load-menu').forEach(m => m.remove());
}
async function loadTrack(trackID, deviceNumber) {
  // Guard against clobbering a deck that's mid-playback. currentPlayers is
  // refreshed from /api/players every ~1s, so is_playing is current. Only a
  // live-playing deck triggers the prompt — paused/cued decks load silently.
  const target = (currentPlayers || []).find(p => p.device_number === deviceNumber);
  if (target && target.is_playing) {
    const nowStr = target.track_title
      ? `"${target.track_title}"${target.artist ? ' — ' + target.artist : ''}`
      : (target.track_id ? `TRACK ${String(target.track_id).padStart(4, '0')}` : 'a track');
    const ok = await openConfirmModal({
      title: 'DECK IS PLAYING',
      message: `PLAYER ${String(deviceNumber).padStart(2, '0')} is currently playing ${nowStr}.\n\nLoading a new track will interrupt it. Continue?`,
      okLabel: 'LOAD ANYWAY',
      danger: true,
    });
    if (!ok) return;
  }
  const r = await fetch('/api/load', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ track_id: trackID, device_number: deviceNumber }),
  });
  if (r.ok) toast(`▶ TRACK ${String(trackID).padStart(4,'0')} → PLAYER ${String(deviceNumber).padStart(2,'0')}`, 'success');
  else toast('LOAD FAILED · ' + (await errorText(r)), 'error');
}

