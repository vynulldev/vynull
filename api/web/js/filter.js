// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── LIBRARY FILTER BAR ─────────
// Per-tab filter state for the LIBRARY view, layered on top of the
// search-text filter. Colors and ratings are Sets of selected values
// (multi-select OR); BPM is an inclusive range with null = unbounded.
// Filters apply to whichever source is in the toolbar — collection,
// playlist, history entry, etc.
const libraryFilters = {
  colors: new Set(),  // 0 = no color, 1-8 = palette
  ratings: new Set(), // 0 = unrated, 1-5 = star count
  tags: new Set(),    // tag NAMES (matches t.tags shape); empty Set = untagged
  keys: new Set(),    // canonical Camelot key codes (e.g. "8A"); empty = no key filter
  bpmMin: null,
  bpmMax: null,
  // Harmonic / tempo compatibility against a reference key+bpm.
  // mode: 'off' | 'deck:<N>' | 'custom'
  // tolerance: BPM percent window (±%, e.g. 6 → ±6%)
  compat: {
    mode: 'off',
    customKey: '',
    customBpm: null,
    tolerance: 6,
  },
};

// Resolve the compat filter's current reference {key, bpm} based on
// mode. Returns null when no usable reference (mode=off, deck not
// connected, or custom fields both blank).
function resolveCompatReference() {
  const c = libraryFilters.compat;
  if (c.mode === 'off') return null;
  if (c.mode === 'custom') {
    if (!c.customKey && !c.customBpm) return null;
    return { key: c.customKey || '', bpm: c.customBpm || 0 };
  }
  if (c.mode.startsWith('deck:')) {
    const num = parseInt(c.mode.slice(5), 10);
    const p = (currentPlayers || []).find(pl => pl.device_number === num);
    if (!p || (!p.key && !p.bpm)) return null;
    return { key: p.key || '', bpm: p.bpm || 0 };
  }
  return null;
}

// JS port of api/smart_playlist.go camelotCompatible: same key, ±1 on
// the wheel, or the relative major/minor (same number, opposite
// letter). Non-Camelot strings fall back to case-insensitive equality.
function parseCamelot(s) {
  s = (s || '').trim().toUpperCase();
  if (s.length < 2 || s.length > 3) return null;
  const last = s[s.length - 1];
  if (last !== 'A' && last !== 'B') return null;
  const n = parseInt(s.slice(0, -1), 10);
  if (!Number.isFinite(n) || n < 1 || n > 12) return null;
  return { n, letter: last };
}
function camelotCompatible(a, b) {
  const pa = parseCamelot(a);
  const pb = parseCamelot(b);
  if (!pa || !pb) return (a || '').toUpperCase() === (b || '').toUpperCase();
  if (pa.n === pb.n) return true; // same or relative major/minor
  const diff = (pa.n - pb.n + 12) % 12;
  if ((diff === 1 || diff === 11) && pa.letter === pb.letter) return true;
  return false;
}
// Sentinel for "no tags at all" in the tag filter set, since tag names
// are arbitrary strings — using a non-string marker can't collide.
const UNTAGGED = Symbol('untagged');

function libraryFiltersActive() {
  return libraryFilters.colors.size > 0
      || libraryFilters.ratings.size > 0
      || libraryFilters.tags.size > 0
      || libraryFilters.keys.size > 0
      || libraryFilters.bpmMin !== null
      || libraryFilters.bpmMax !== null
      || resolveCompatReference() !== null;
}

// syncClearButton enables/disables the always-present Clear button to match
// whether any filter is active — used by edits that don't re-render the whole
// bar (BPM typing, the tag/key combos) so the button reflects state instantly.
function syncClearButton() {
  const b = document.getElementById('lib-fb-clear');
  if (b) b.disabled = !libraryFiltersActive();
}

function applyLibraryFilters(rows) {
  if (!libraryFiltersActive()) return rows;
  return rows.filter(t => {
    if (libraryFilters.colors.size > 0 && !libraryFilters.colors.has(t.color_id || 0)) {
      return false;
    }
    if (libraryFilters.ratings.size > 0 && !libraryFilters.ratings.has(t.rating || 0)) {
      return false;
    }
    if (libraryFilters.tags.size > 0) {
      const trackTags = Array.isArray(t.tags) ? t.tags : [];
      // Match if any selected tag is on the track, OR if the user selected
      // the "untagged" sentinel and this track has zero tags.
      let match = false;
      if (libraryFilters.tags.has(UNTAGGED) && trackTags.length === 0) match = true;
      if (!match) {
        for (const tag of trackTags) {
          if (libraryFilters.tags.has(tag)) { match = true; break; }
        }
      }
      if (!match) return false;
    }
    if (libraryFilters.keys.size > 0 && !libraryFilters.keys.has(canonicalCamelot(t.key))) {
      return false;
    }
    const bpm = t.bpm || 0;
    if (libraryFilters.bpmMin !== null && bpm < libraryFilters.bpmMin) return false;
    if (libraryFilters.bpmMax !== null && bpm > libraryFilters.bpmMax) return false;
    const ref = resolveCompatReference();
    if (ref) {
      // Key compat: only enforced when a reference key is set.
      if (ref.key && !camelotCompatible(t.key || '', ref.key)) return false;
      // BPM compat: only enforced when a reference BPM is set.
      if (ref.bpm && libraryFilters.compat.tolerance > 0) {
        const tol = ref.bpm * libraryFilters.compat.tolerance / 100;
        if (!bpm || Math.abs(bpm - ref.bpm) > tol) return false;
      }
    }
    return true;
  });
}

// getFilterTagNames returns the sorted tag catalog used by the library tag
// filter — the global catalog (allTagsCache) when loaded, else the union of
// tags actually present on tracks so the control isn't empty on first paint.
function getFilterTagNames() {
  let names = [];
  if (Array.isArray(allTagsCache) && allTagsCache.length > 0) {
    names = allTagsCache.map(t => t.name);
  } else if (Array.isArray(allTracks)) {
    const seen = new Set();
    for (const t of allTracks) {
      if (Array.isArray(t.tags)) for (const n of t.tags) seen.add(n);
    }
    names = Array.from(seen);
  }
  names.sort((a, b) => a.localeCompare(b));
  return names;
}

// ── Library multi-select filter combo box ─────────────────────────────────
// A searchable dropdown shared by the Tags and Keys filters (inline chips
// overflowed the bar once a library had many of either). cfg supplies the
// title, item list, and selection callbacks; selection state lives in the
// caller's libraryFilters.* Set so applyLibraryFilters is unchanged. Items use
// a data-idx into cfg.items so values can be non-strings (e.g. the UNTAGGED
// Symbol).
let filterComboAnchorId = null;
function openFilterCombo(anchor, cfg) {
  closeFilterCombo();
  filterComboAnchorId = anchor.id;
  const pop = document.createElement('div');
  pop.className = 'lib-fb-tagpop';
  pop.innerHTML = `
    <div class="head">${escape(cfg.title)}</div>
    <input type="text" class="tagpop-search" placeholder="${escape(cfg.searchPlaceholder || 'Search…')}" autocomplete="off">
    <div class="tagpop-list"></div>
    <div class="tagpop-foot"><button type="button" class="tagpop-clear">${escape(cfg.clearLabel || 'Clear')}</button></div>`;
  document.body.appendChild(pop);

  // Anchor below the trigger, clamped to the viewport so it never runs off
  // the right edge or the bottom.
  const r = anchor.getBoundingClientRect();
  const pw = pop.offsetWidth || 260;
  let left = r.left;
  if (left + pw > window.innerWidth - 8) left = window.innerWidth - pw - 8;
  if (left < 8) left = 8;
  pop.style.left = left + 'px';
  pop.style.top = (r.bottom + 4) + 'px';
  const listEl = pop.querySelector('.tagpop-list');
  const avail = window.innerHeight - r.bottom - 96;
  if (avail > 120 && avail < 260) listEl.style.maxHeight = avail + 'px';

  const searchEl = pop.querySelector('.tagpop-search');
  function renderList() {
    const q = searchEl.value.trim().toLowerCase();
    let html = '';
    let lastGroup = null;
    cfg.items.forEach((it, i) => {
      const hay = (it.search != null ? it.search : it.label).toLowerCase();
      if (q && !hay.includes(q)) return;
      // Category header when an item's group differs from the previous visible
      // item's. Items must be pre-sorted by group (see openTagFilterCombo);
      // groups with no visible items get no header. Ungrouped items reset it.
      if (it.group != null) {
        if (it.group !== lastGroup) {
          html += `<div class="tagpop-cat-head">${escape(it.group)}</div>`;
          lastGroup = it.group;
        }
      } else {
        lastGroup = null;
      }
      const s = cfg.isSelected(it.value);
      html += `<div class="tagpop-item ${s ? 'sel' : ''}" data-idx="${i}"><span class="cb">${s ? '☑' : '☐'}</span><span class="nm" title="${escape(it.label)}">${escape(it.label)}</span></div>`;
    });
    listEl.innerHTML = html || '<div class="tagpop-empty">No matches</div>';
  }
  renderList();
  searchEl.addEventListener('input', renderList);
  listEl.addEventListener('click', (e) => {
    const item = e.target.closest('.tagpop-item');
    if (!item) return;
    cfg.toggle(cfg.items[parseInt(item.getAttribute('data-idx'), 10)].value);
    renderList();
    cfg.onChange();
  });
  pop.querySelector('.tagpop-clear').addEventListener('click', () => {
    cfg.clear();
    renderList();
    cfg.onChange();
  });

  searchEl.focus();
  // Defer binding the dismissers so the click that opened the popover
  // doesn't immediately close it.
  setTimeout(() => {
    document.addEventListener('click', outsideFilterComboClick);
    document.addEventListener('keydown', filterComboKeydown);
  }, 0);
}
function filterComboOpen() { return !!document.querySelector('.lib-fb-tagpop'); }
function outsideFilterComboClick(e) {
  if (e.target.closest('.lib-fb-tagpop')) return;
  if (filterComboAnchorId && e.target.closest('#' + filterComboAnchorId)) return;
  closeFilterCombo();
}
function filterComboKeydown(e) { if (e.key === 'Escape') closeFilterCombo(); }
function closeFilterCombo() {
  document.querySelectorAll('.lib-fb-tagpop').forEach(p => p.remove());
  document.removeEventListener('click', outsideFilterComboClick);
  document.removeEventListener('keydown', filterComboKeydown);
  filterComboAnchorId = null;
}

// Tags filter combo. Selection lives in libraryFilters.tags (names + the
// UNTAGGED sentinel).
function updateTagFilterButton() {
  const txt = document.getElementById('lib-fb-tagbtn-text');
  const btn = document.getElementById('lib-fb-tagbtn');
  if (!txt || !btn) return;
  const n = libraryFilters.tags.size;
  txt.textContent = n > 0 ? `Tags (${n})` : 'Tags';
  btn.classList.toggle('has-sel', n > 0);
}
async function openTagFilterCombo(anchor) {
  const cats = await ensureAllCategories();
  const catName = id => {
    if (!id) return 'UNCATEGORIZED';
    const c = (cats || []).find(x => x.id === id);
    return c ? c.name.toUpperCase() : `CATEGORY ${id}`;
  };
  const names = getFilterTagNames();
  // Map tag name → category id (only available when the catalog is loaded).
  const catOf = {};
  if (Array.isArray(allTagsCache)) {
    for (const t of allTagsCache) catOf[t.name] = t.category_id || 0;
  }
  // Group only when it adds signal (more than one category, or a real one).
  const groupIDs = new Set(names.map(n => catOf[n] || 0));
  const useGroups = groupIDs.size > 1 || (groupIDs.size === 1 && !groupIDs.has(0));
  let tagItems;
  if (useGroups) {
    // Sort by category (uncategorized last, else by name) then tag name, so
    // the combo can emit a header on each group change.
    tagItems = names.slice().sort((a, b) => {
      const ca = catOf[a] || 0, cb = catOf[b] || 0;
      if (ca !== cb) {
        if (ca === 0) return 1;
        if (cb === 0) return -1;
        return catName(ca).localeCompare(catName(cb));
      }
      return a.localeCompare(b);
    }).map(n => ({ value: n, label: n, group: catName(catOf[n] || 0) }));
  } else {
    tagItems = names.map(n => ({ value: n, label: n }));
  }
  const items = [{ value: UNTAGGED, label: '— Untagged', search: 'untagged' }].concat(tagItems);
  openFilterCombo(anchor, {
    title: 'Filter by tag',
    searchPlaceholder: 'Search tags…',
    clearLabel: 'Clear tag filter',
    items,
    isSelected: v => libraryFilters.tags.has(v),
    toggle: v => { libraryFilters.tags.has(v) ? libraryFilters.tags.delete(v) : libraryFilters.tags.add(v); },
    clear: () => libraryFilters.tags.clear(),
    onChange: () => { updateTagFilterButton(); syncClearButton(); refreshLibraryView(); },
  });
}

// Keys filter combo. Selection lives in libraryFilters.keys (canonical Camelot
// codes); items are the distinct keys present in the library, labelled in the
// user's current key_display notation and sorted around the Camelot wheel.
function canonicalCamelot(raw) {
  if (!raw) return '';
  const p = KEY_PAIRS[String(raw).trim().toUpperCase()];
  return p ? p[0] : String(raw).trim().toUpperCase();
}
function getFilterKeys() {
  const seen = new Set();
  if (Array.isArray(allTracks)) {
    for (const t of allTracks) {
      const c = canonicalCamelot(t.key);
      if (c) seen.add(c);
    }
  }
  return Array.from(seen).sort((a, b) => {
    const pa = parseCamelot(a), pb = parseCamelot(b);
    if (pa && pb) return (pa.n - pb.n) || a.slice(-1).localeCompare(b.slice(-1));
    return a.localeCompare(b);
  });
}
function updateKeyFilterButton() {
  const txt = document.getElementById('lib-fb-keybtn-text');
  const btn = document.getElementById('lib-fb-keybtn');
  if (!txt || !btn) return;
  const n = libraryFilters.keys.size;
  txt.textContent = n > 0 ? `Key (${n})` : 'Key';
  btn.classList.toggle('has-sel', n > 0);
}
function openKeyFilterCombo(anchor) {
  const items = getFilterKeys().map(k => ({ value: k, label: fmtKey(k), search: keyBoth(k) }));
  openFilterCombo(anchor, {
    title: 'Filter by key',
    searchPlaceholder: 'Search keys…',
    clearLabel: 'Clear key filter',
    items,
    isSelected: v => libraryFilters.keys.has(v),
    toggle: v => { libraryFilters.keys.has(v) ? libraryFilters.keys.delete(v) : libraryFilters.keys.add(v); },
    clear: () => libraryFilters.keys.clear(),
    onChange: () => { updateKeyFilterButton(); syncClearButton(); refreshLibraryView(); },
  });
}

// Rating filter combo. Selection lives in libraryFilters.ratings (ints 0–5,
// where 0 = unrated). Same multi-select machinery as Tags/Key.
function updateRatingFilterButton() {
  const txt = document.getElementById('lib-fb-ratingbtn-text');
  const btn = document.getElementById('lib-fb-ratingbtn');
  if (!txt || !btn) return;
  const n = libraryFilters.ratings.size;
  txt.textContent = n > 0 ? `Rating (${n})` : 'Rating';
  btn.classList.toggle('has-sel', n > 0);
}
function openRatingFilterCombo(anchor) {
  const items = [0, 1, 2, 3, 4, 5].map(v => ({
    value: v,
    label: v === 0 ? '— Unrated' : '★'.repeat(v),
    search: v === 0 ? 'unrated 0' : `${v} star${v > 1 ? 's' : ''}`,
  }));
  openFilterCombo(anchor, {
    title: 'Filter by rating',
    searchPlaceholder: 'Search ratings…',
    clearLabel: 'Clear rating filter',
    items,
    isSelected: v => libraryFilters.ratings.has(v),
    toggle: v => { libraryFilters.ratings.has(v) ? libraryFilters.ratings.delete(v) : libraryFilters.ratings.add(v); },
    clear: () => libraryFilters.ratings.clear(),
    onChange: () => { updateRatingFilterButton(); syncClearButton(); refreshLibraryView(); },
  });
}

function renderFilterBar() {
  const bar = document.getElementById('lib-filterbar');
  if (!bar) return;
  // A bar re-render destroys the trigger button, so drop any open popover.
  closeFilterCombo();
  const colorIDs = [0, 1, 2, 3, 4, 5, 6, 7, 8];

  const colorChips = colorIDs.map(id => {
    const sel = libraryFilters.colors.has(id) ? 'selected' : '';
    if (id === 0) {
      return `<button type="button" class="lib-fb-color empty ${sel}" data-color="0" title="No colour"></button>`;
    }
    const name = (window.COLOR_NAMES && window.COLOR_NAMES[id]) || `Color ${id}`;
    return `<button type="button" class="lib-fb-color ${sel}" data-color="${id}" style="background:${TRACK_COLORS[id]}" title="${name}"></button>`;
  }).join('');

  // Rating: a combo-box trigger like Tags/Key (multi-select over 0–5 stars).
  const ratingSelCount = libraryFilters.ratings.size;
  const ratingBtnText = ratingSelCount > 0 ? `Rating (${ratingSelCount})` : 'Rating';

  const minVal = libraryFilters.bpmMin !== null ? libraryFilters.bpmMin : '';
  const maxVal = libraryFilters.bpmMax !== null ? libraryFilters.bpmMax : '';

  // Tags: a combo-box trigger that opens a searchable multi-select popover.
  // (Inline chips overflowed the bar once a library had many tags.)
  const tagNames = getFilterTagNames();
  const tagSelCount = libraryFilters.tags.size;
  const tagBtnText = tagSelCount > 0 ? `Tags (${tagSelCount})` : 'Tags';
  const tagRow = (tagNames.length > 0 || tagSelCount > 0)
    ? `<div class="lib-fb-group">
        <span class="lib-fb-name">Tags</span>
        <button type="button" class="lib-fb-tagbtn ${tagSelCount > 0 ? 'has-sel' : ''}" id="lib-fb-tagbtn" title="Filter by tag">
          <span id="lib-fb-tagbtn-text">${escape(tagBtnText)}</span><span class="caret">▾</span>
        </button>
      </div>`
    : '';

  // Key: a combo-box trigger like Tags — pick one or more specific keys
  // (distinct from the Compat row, which matches harmonically against a deck).
  const keyVals = getFilterKeys();
  const keySelCount = libraryFilters.keys.size;
  const keyBtnText = keySelCount > 0 ? `Key (${keySelCount})` : 'Key';
  const keyRow = (keyVals.length > 0 || keySelCount > 0)
    ? `<div class="lib-fb-group">
        <span class="lib-fb-name">Key</span>
        <button type="button" class="lib-fb-tagbtn ${keySelCount > 0 ? 'has-sel' : ''}" id="lib-fb-keybtn" title="Filter by key">
          <span id="lib-fb-keybtn-text">${escape(keyBtnText)}</span><span class="caret">▾</span>
        </button>
      </div>`
    : '';

  // Compat row: mode dropdown (OFF / DECK n / CUSTOM), custom inputs
  // when CUSTOM, tolerance selector, and a live reference readout.
  const compat = libraryFilters.compat;
  const deckOpts = (currentPlayers || [])
    .filter(p => p.device_number)
    .sort((a, b) => a.device_number - b.device_number)
    .map(p => {
      const val = `deck:${p.device_number}`;
      const sel = compat.mode === val ? 'selected' : '';
      return `<option value="${val}" ${sel}>DECK ${p.device_number}</option>`;
    }).join('');
  const customSel = compat.mode === 'custom' ? 'selected' : '';
  const offSel = compat.mode === 'off' ? 'selected' : '';
  const customRow = compat.mode === 'custom'
    ? `<input type="text" id="lib-fb-compat-key" class="lib-fb-num" style="width:54px" placeholder="key" value="${escape(compat.customKey || '')}">
       <input type="number" id="lib-fb-compat-bpm" class="lib-fb-num" min="0" step="0.1" placeholder="bpm" value="${compat.customBpm !== null ? compat.customBpm : ''}">`
    : '';
  const tolOpts = [3, 6, 10].map(v => {
    const sel = compat.tolerance === v ? 'selected' : '';
    return `<option value="${v}" ${sel}>±${v}%</option>`;
  }).join('');
  const ref = resolveCompatReference();
  const refText = ref
    ? '→ ' + [fmtKey(ref.key), ref.bpm ? ref.bpm.toFixed(1) + ' BPM' : ''].filter(Boolean).join(' · ')
    : '';
  const compatRow = `
    <div class="lib-fb-group">
      <span class="lib-fb-name">Compat</span>
      <select id="lib-fb-compat-mode" class="lib-fb-select">
        <option value="off" ${offSel}>OFF</option>
        ${deckOpts}
        <option value="custom" ${customSel}>CUSTOM…</option>
      </select>
      ${customRow}
      <select id="lib-fb-compat-tol" class="lib-fb-select">${tolOpts}</select>
      <span class="lib-fb-ref" id="lib-fb-compat-ref">${escape(refText)}</span>
    </div>
  `;

  bar.innerHTML = `
    <span class="lib-fb-label">FILTERS</span>
    <div class="lib-fb-group">
      <span class="lib-fb-name">Colour</span>${colorChips}
    </div>
    <div class="lib-fb-group">
      <span class="lib-fb-name">Rating</span>
      <button type="button" class="lib-fb-tagbtn ${ratingSelCount > 0 ? 'has-sel' : ''}" id="lib-fb-ratingbtn" title="Filter by rating">
        <span id="lib-fb-ratingbtn-text">${escape(ratingBtnText)}</span><span class="caret">▾</span>
      </button>
    </div>
    <div class="lib-fb-group">
      <span class="lib-fb-name">BPM</span>
      <input type="number" id="lib-fb-bpm-min" class="lib-fb-num" min="0" step="1" placeholder="min" value="${minVal}">
      <span class="lib-fb-dash">–</span>
      <input type="number" id="lib-fb-bpm-max" class="lib-fb-num" min="0" step="1" placeholder="max" value="${maxVal}">
    </div>
    ${tagRow}
    ${keyRow}
    ${compatRow}
    <button type="button" class="lib-fb-clear" id="lib-fb-clear" ${libraryFiltersActive() ? '' : 'disabled'}>✕ Clear</button>
  `;

  bar.querySelectorAll('[data-color]').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = parseInt(btn.getAttribute('data-color'), 10);
      libraryFilters.colors.has(id) ? libraryFilters.colors.delete(id) : libraryFilters.colors.add(id);
      renderFilterBar();
      refreshLibraryView();
    });
  });
  const ratingBtn = document.getElementById('lib-fb-ratingbtn');
  if (ratingBtn) {
    ratingBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      if (filterComboAnchorId === 'lib-fb-ratingbtn') closeFilterCombo();
      else openRatingFilterCombo(ratingBtn);
    });
  }
  const tagBtn = document.getElementById('lib-fb-tagbtn');
  if (tagBtn) {
    tagBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      if (filterComboAnchorId === 'lib-fb-tagbtn') closeFilterCombo();
      else openTagFilterCombo(tagBtn);
    });
  }
  const keyBtn = document.getElementById('lib-fb-keybtn');
  if (keyBtn) {
    keyBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      if (filterComboAnchorId === 'lib-fb-keybtn') closeFilterCombo();
      else openKeyFilterCombo(keyBtn);
    });
  }
  const compatMode = document.getElementById('lib-fb-compat-mode');
  if (compatMode) {
    compatMode.addEventListener('change', () => {
      libraryFilters.compat.mode = compatMode.value;
      lastCompatRefKey = JSON.stringify(resolveCompatReference());
      renderFilterBar();
      refreshLibraryView();
    });
  }
  const compatTol = document.getElementById('lib-fb-compat-tol');
  if (compatTol) {
    compatTol.addEventListener('change', () => {
      libraryFilters.compat.tolerance = parseInt(compatTol.value, 10);
      updateCompatReferenceText();
      refreshLibraryView();
    });
  }
  const compatKey = document.getElementById('lib-fb-compat-key');
  if (compatKey) {
    compatKey.addEventListener('input', () => {
      libraryFilters.compat.customKey = compatKey.value.trim();
      updateCompatReferenceText();
      refreshLibraryView();
    });
  }
  const compatBpm = document.getElementById('lib-fb-compat-bpm');
  if (compatBpm) {
    compatBpm.addEventListener('input', () => {
      const v = compatBpm.value.trim();
      libraryFilters.compat.customBpm = v === '' ? null : Math.max(0, parseFloat(v) || 0);
      updateCompatReferenceText();
      refreshLibraryView();
    });
  }
  const bindBpm = (id, key) => {
    const inp = document.getElementById(id);
    if (!inp) return;
    inp.addEventListener('input', () => {
      const v = inp.value.trim();
      libraryFilters[key] = v === '' ? null : Math.max(0, parseInt(v, 10) || 0);
      // Don't re-render the bar on every keystroke (would steal focus); the
      // Clear button is always present, so just toggle its enabled state.
      syncClearButton();
      refreshLibraryView();
    });
  };
  bindBpm('lib-fb-bpm-min', 'bpmMin');
  bindBpm('lib-fb-bpm-max', 'bpmMax');
  const clearBtn = document.getElementById('lib-fb-clear');
  if (clearBtn) {
    clearBtn.addEventListener('click', () => {
      libraryFilters.colors.clear();
      libraryFilters.ratings.clear();
      libraryFilters.tags.clear();
      libraryFilters.keys.clear();
      libraryFilters.bpmMin = null;
      libraryFilters.bpmMax = null;
      libraryFilters.compat.mode = 'off';
      libraryFilters.compat.customKey = '';
      libraryFilters.compat.customBpm = null;
      renderFilterBar();
      refreshLibraryView();
    });
  }

  // Record the option signature so refreshLibraryView re-renders the bar when
  // the available keys/tags change (e.g. once tracks/tags finish loading).
  lastFilterOptionsSig = keyVals.length + ':' + tagNames.length;
}

// Update just the compat reference readout span without rebuilding the
// whole bar — avoids stealing focus from the custom key/bpm inputs and
// lets renderPlayers() refresh live as deck data changes.
function updateCompatReferenceText() {
  const span = document.getElementById('lib-fb-compat-ref');
  if (!span) return;
  const ref = resolveCompatReference();
  span.textContent = ref
    ? '→ ' + [ref.key, ref.bpm ? ref.bpm.toFixed(1) + ' BPM' : ''].filter(Boolean).join(' · ')
    : '';
}

// keepLibraryPlace runs render, a re-render of the library the user didn't ask
// for (the poll saw the tracks change, a deck changed the compatibility
// filter), without moving them. The table is rebuilt from scratch, in scroll
// mode with its scrolling wrap, which starts again at the top. So note the
// first row in view and where it sits on screen, then scroll it back to the
// same spot. If that track is no longer listed, the wrap's old offsets stand.
function keepLibraryPlace(render) {
  const el = document.getElementById('library-content');
  const place = el && libraryPlace(el);
  render();
  if (place) restoreLibraryPlace(el, place);
}

// libraryPlace finds the first row (or card) in view: the first whose bottom
// is below the sticky column header (its cells are what stick, not the thead).
// Rows are in document order, so a binary search reads only a few positions.
function libraryPlace(el) {
  const rows = el.querySelectorAll('[data-track-row], [data-track-card]');
  if (!rows.length) return null;
  const head = el.querySelector('table.lib thead th');
  const top = head ? head.getBoundingClientRect().bottom : 0;
  let lo = 0, hi = rows.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (rows[mid].getBoundingClientRect().bottom <= top) lo = mid + 1; else hi = mid;
  }
  const wrap = el.querySelector('.lib-table-wrap');
  return {
    id: rows[lo].getAttribute('data-track-row') || rows[lo].getAttribute('data-track-card'),
    y: rows[lo].getBoundingClientRect().top,
    wrapTop: wrap ? wrap.scrollTop : 0,
    wrapLeft: wrap ? wrap.scrollLeft : 0,
  };
}

function restoreLibraryPlace(el, place) {
  const wrap = el.querySelector('.lib-table-wrap');
  if (wrap) {
    wrap.scrollTop = place.wrapTop;
    wrap.scrollLeft = place.wrapLeft;
  }
  const row = el.querySelector(`[data-track-row="${place.id}"], [data-track-card="${place.id}"]`);
  if (!row) return;
  const dy = row.getBoundingClientRect().top - place.y;
  if (dy) scrollParent(row).scrollTop += dy;
}

// scrollParent returns the element that scrolls node into view: the nearest
// ancestor that overflows and scrolls (the table wrap in scroll mode), else the
// page.
function scrollParent(node) {
  for (let n = node.parentElement; n; n = n.parentElement) {
    if (/auto|scroll/.test(getComputedStyle(n).overflowY) && n.scrollHeight > n.clientHeight) return n;
  }
  return document.scrollingElement || document.documentElement;
}

// refreshLibraryView is the orchestrator: it picks the tracks to show
// based on selectedSource, updates the toolbar (source label +
// playlist actions), and delegates to renderLibrary for the actual
// table render. Every code path that used to call renderLibrary(allTracks)
// directly now goes through here so the view always reflects the
// current sidebar selection.
let lastFilterOptionsSig = '';
function refreshLibraryView() {
  if (isHotCueBankSource()) {
    renderHotCueBankView();
    return;
  }
  const colsBtn = document.getElementById('lib-columns-btn');
  if (colsBtn) colsBtn.style.display = '';
  const bar = document.getElementById('lib-filterbar');
  if (bar) bar.style.display = '';
  if (bar) {
    // The Tags/Key rows only render when their options exist, but the bar is
    // first drawn before tracks/tags have loaded — so re-render once the
    // available option set changes (initial load, import, tag CRUD). Guard
    // against clobbering an open combo popover or a focused input.
    const sig = getFilterKeys().length + ':' + getFilterTagNames().length;
    const busy = bar.contains(document.activeElement) || document.querySelector('.lib-fb-tagpop');
    if (bar.innerHTML === '' || (sig !== lastFilterOptionsSig && !busy)) {
      renderFilterBar(); // records lastFilterOptionsSig itself
    }
  }
  const sel = (typeof selectedSource === 'number')
    ? playlistsCache.find(p => p.id === selectedSource)
    : null;
  const isFolder = !!(sel && sel.is_folder);
  const sourceName = document.getElementById('lib-source-name');
  const actions = document.getElementById('lib-playlist-actions');
  if (sourceName) {
    sourceName.textContent = sel ? sel.name.toUpperCase() : 'COLLECTION';
  }
  const toggleName = document.getElementById('lib-src-toggle-name');
  if (toggleName) toggleName.textContent = sel ? sel.name.toUpperCase() : 'COLLECTION';
  if (actions) {
    if (!sel) {
      // COLLECTION — import new audio from disk, or export it all. Labelled
      // "ADD FILES" (not "ADD TRACKS") to distinguish it from the playlist
      // "+ ADD TRACKS" button, which only adds *existing* library tracks.
      actions.innerHTML = `
        <button id="col-add" title="Browse for folders or audio files to import into the library">+ ADD FILES</button>
        <button id="pl-export">EXPORT…</button>`;
    } else if (sel.read_only) {
      // A served rekordbox USB's playlist — no mutation, but EXPORT only
      // reads tracks, so re-exporting a stick's playlist is fine.
      actions.innerHTML = sel.is_folder ? '' : `<button id="pl-export">EXPORT…</button>`;
    } else if (isFolder) {
      // Folder selected — only rename/delete makes sense (no tracks).
      actions.innerHTML = `
        <button id="pl-rename">RENAME</button>
        <button id="pl-delete" class="danger">DELETE</button>`;
    } else if (sel.is_smart) {
      actions.innerHTML = `
        <button id="pl-edit-rules">EDIT RULES</button>
        <button id="pl-export">EXPORT…</button>
        <button id="pl-rename">RENAME</button>
        <button id="pl-delete" class="danger">DELETE</button>`;
    } else {
      actions.innerHTML = `
        <button id="pl-add">+ ADD TRACKS</button>
        <button id="pl-export">EXPORT…</button>
        <button id="pl-rename">RENAME</button>
        <button id="pl-delete" class="danger">DELETE</button>`;
    }
    wireLibraryToolbarActions();
  }

  // Pick tracks for the table.
  if (selectedSource === 'collection') {
    renderLibrary(allTracks);
  } else if (isFolder) {
    const el = document.getElementById('library-content');
    if (el) el.innerHTML = `<div class="empty-state">— FOLDER — SELECT A PLAYLIST WITHIN —</div>`;
    const stats = document.getElementById('lib-stats');
    if (stats) stats.innerHTML = '';
  } else {
    renderLibrary(plSelectedTracks);
  }

  // Sidebar selection highlight stays in sync without a full re-render.
  // History rows live in their own container alongside the main tree.
  document.querySelectorAll('#lib-sidebar-tree .pl-node, #lib-sidebar-history .pl-node').forEach(n => {
    const id = parseInt(n.getAttribute('data-pl-id'), 10);
    n.classList.toggle('selected', id === selectedSource);
  });
  document.querySelectorAll('.lib-src[data-source="collection"]').forEach(b => {
    b.classList.toggle('active', selectedSource === 'collection');
  });
  document.querySelectorAll('#lib-sidebar-hcb .hcb-node').forEach(n => n.classList.remove('selected'));
  // Collection count chip.
  const collCount = document.getElementById('lib-coll-count');
  if (collCount) collCount.textContent = (allTracks || []).length;

  // Sticky offsets — toolbar height varies based on which playlist
  // action buttons are active, so the thead's sticky top has to track
  // it instead of being a hardcoded number that gaps or overlaps.
  syncStickyOffsets();
}

// syncStickyOffsets pins the table's column-header row directly below
// the sticky toolbar by reading the toolbar's intrinsic height. Called
// from refreshLibraryView (after every selection change) and after
// each renderLibrary (since the table itself is re-created).
// Also called once on load and on window resize to handle the
// initial paint + responsive reflow.
function syncStickyOffsets() {
  const STICKY_TOP = 96; // header + tabs height — must match the .lib-toolbar CSS
  const toolbar = document.querySelector('#library .lib-toolbar');
  if (!toolbar) return;
  const offset = STICKY_TOP + toolbar.offsetHeight;
  const scroll = libColMode === 'scroll';
  // Fit mode: header sticks below the toolbar (viewport). Scroll mode: the wrap
  // is the scroll container, so the header sticks to its top (0).
  const top = scroll ? 0 : offset;
  document.querySelectorAll('#library table.lib thead th').forEach(th => {
    th.style.top = top + 'px';
  });
  // Scroll mode: size the bounded viewport to fill from its actual top down to
  // the bottom of the window (measured, so there's no gap), and scroll inside.
  const wrap = document.querySelector('#library .lib-table-wrap');
  if (wrap) {
    if (scroll) {
      const naturalTop = wrap.getBoundingClientRect().top + window.scrollY;
      wrap.style.maxHeight = Math.max(160, Math.floor(window.innerHeight - naturalTop - 8)) + 'px';
    } else {
      wrap.style.removeProperty('max-height');
    }
  }
}
window.addEventListener('resize', syncStickyOffsets);
// Re-fit the library columns to the panel when the window/panel width changes,
// so a resized window never leaves the table over- or under-bounds.
window.addEventListener('resize', () => { applyColWidthsLive(); scheduleWaveRefetch(); });

// renderLibrarySidebar paints the COLLECTION button + playlist tree
// into the lib-sidebar-tree element. Called after loadPlaylists or
// any tree-mutating action (create/rename/move/delete) so the tree
// shape reflects the current state.
function renderLibrarySidebar() {
  const host = document.getElementById('lib-sidebar-tree');
  if (!host) return;
  host.innerHTML = renderPlaylistTree() || '<div class="pl-empty">— NO PLAYLISTS YET —</div>';
  renderHistorySection();
  renderHotCueBankSection();
  wireLibrarySidebar();
}

// renderHistorySection promotes the auto-generated "History" folder
// to its own labelled section in the sidebar (mirroring rekordbox's
// dedicated HISTORY nav). Children render flat — every dated playlist
// is one click away rather than buried inside the folder. Sorted
// newest-first so the most recent session is at the top.
function renderHistorySection() {
  const head = document.getElementById('lib-history-head');
  const host = document.getElementById('lib-sidebar-history');
  if (!head || !host) return;
  const folder = playlistsCache.find(p =>
    p.parent_id === 0 && p.is_folder && p.name === 'History');
  if (!folder) {
    head.style.display = 'none';
    host.innerHTML = '';
    return;
  }
  const entries = playlistsCache
    .filter(p => p.parent_id === folder.id && !p.is_folder)
    .sort((a, b) => b.name.localeCompare(a.name)); // ISO date names sort newest-first descending
  if (entries.length === 0) {
    head.style.display = 'none';
    host.innerHTML = '';
    return;
  }
  head.style.display = '';
  host.innerHTML = entries.map(p => {
    const isSel = p.id === selectedSource;
    const display = p.name.startsWith('History · ') ? p.name.slice(10) : p.name;
    return `<div class="pl-node ${isSel ? 'selected' : ''}"
                 draggable="true"
                 data-pl-id="${p.id}" data-is-folder="0" data-is-smart="0"
                 style="padding-left: 14px">
      <span class="icon">◷</span>
      <span class="name">${escape(display)}</span>
      <span class="count">${(p.track_ids || []).length}</span>
    </div>`;
  }).join('');
}

// Builds the indented tree from the flat playlistsCache. Roots first;
// each node recurses into its children. Folders are clickable to expand
// (currently always-expanded — simpler than tracking collapsed state for v1).
function renderPlaylistTree() {
  if (playlistsCache.length === 0) {
    return '';
  }
  // Hide the auto-generated History folder + its descendants from the
  // main tree — they appear in their own HISTORY section below to
  // avoid double-listing.
  const historyFolder = playlistsCache.find(p =>
    p.parent_id === 0 && p.is_folder && p.name === 'History');
  const historyIDs = new Set();
  if (historyFolder) {
    historyIDs.add(historyFolder.id);
    let grew = true;
    while (grew) {
      grew = false;
      for (const p of playlistsCache) {
        if (historyIDs.has(p.parent_id) && !historyIDs.has(p.id)) {
          historyIDs.add(p.id);
          grew = true;
        }
      }
    }
  }
  const byParent = new Map();
  for (const p of playlistsCache) {
    if (historyIDs.has(p.id)) continue;
    if (!byParent.has(p.parent_id)) byParent.set(p.parent_id, []);
    byParent.get(p.parent_id).push(p);
  }
  const render = (parentID, depth) => {
    const kids = byParent.get(parentID) || [];
    return kids.map(p => {
      const isSel = p.id === selectedSource;
      const icon = p.is_folder ? '▸' : (p.is_smart ? '✦' : '♪');
      // Smart playlists evaluate at read time so we don't know their
      // size without a fetch; render "~" instead of a stale stored
      // count to avoid lying.
      const count = p.is_folder
        ? ''
        : `<span class="count">${p.is_smart ? '~' : (p.track_ids || []).length}</span>`;
      // read_only = a served rekordbox USB's playlist: shown like the rest
      // (it's what the decks browse) but not draggable, with a USB badge.
      return `<div class="pl-node ${p.is_folder ? 'folder' : ''} ${p.is_smart ? 'smart' : ''} ${isSel ? 'selected' : ''}"
                   draggable="${p.read_only ? 'false' : 'true'}"
                   data-pl-id="${p.id}" data-is-folder="${p.is_folder ? 1 : 0}" data-is-smart="${p.is_smart ? 1 : 0}"
                   style="padding-left: ${14 + depth * 14}px">
        <span class="icon">${icon}</span>
        <span class="name">${escape(p.name)}</span>
        ${p.read_only ? '<span class="count">USB</span>' : ''}${count}
      </div>` + (p.is_folder ? render(p.id, depth + 1) : '');
    }).join('');
  };
  return render(0, 0);
}

// wireLibraryToolbarActions binds the per-playlist action buttons in
// the library toolbar (rename / delete / add tracks / edit rules).
// Buttons are re-rendered every refreshLibraryView so the wiring
// re-applies each time.
function wireLibraryToolbarActions() {
  const renameBtn = document.getElementById('pl-rename');
  if (renameBtn) renameBtn.onclick = renameSelectedPlaylist;
  const deleteBtn = document.getElementById('pl-delete');
  if (deleteBtn) deleteBtn.onclick = deleteSelectedPlaylist;
  const addBtn = document.getElementById('pl-add');
  if (addBtn) addBtn.onclick = openPlaylistAddPicker;
  const colAdd = document.getElementById('col-add');
  // COLLECTION view: browse for folders/files to import into the library.
  // Force an immediate refetch when done rather than waiting for the poll.
  if (colAdd) colAdd.onclick = () => openAddBrowserDialog(() => { lastTracksRev = -1; refresh(); });
  const editRules = document.getElementById('pl-edit-rules');
  if (editRules) editRules.onclick = () => openSmartEditor(plSelectedID);
  // Wrap in a closure: assigning openExportModal directly as the handler
  // passed the click EVENT as presetSource, which then went out as the
  // export request's "source" field (an object where the API expects a
  // string) — toolbar exports failed with a 400 while the bulk-select
  // path, which passes a real string, worked.
  const exportBtn = document.getElementById('pl-export');
  if (exportBtn) exportBtn.onclick = () => openExportModal();
}

