// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── SETTINGS ─────────
let lastSettingsKey = '';
let lastTracksKey = '';
let lastTracksRev = -1; // last track list revision seen; gates full track refetch
let lastCuesRev = -1;   // last cue revision seen; gates the full cue refetch
let pollCount = 0;      // poll ticks, for the periodic forced-refetch fallback

// ───────── CDJ MENU CONFIG (modal) ─────────
// User-configurable visibility + order of the CDJ root menu. Lives in
// a modal opened from the Settings tab (low-frequency config — modal
// keeps the Settings page focused on CDJ settings). PUT to
// /api/menu-items on every change so the deck picks up changes on its
// next root-menu poll (~1s).
let menuItemsCache = [];

let trackDetailCache = 'bpm';
let trackDetailFieldsCache = [];

// menuLoaded is whether the menus came from the server: they are saved whole,
// so a save waits for a successful load.
let menuLoaded = false;

async function loadMenuItems() {
  let r;
  try {
    r = await fetchJSON('/api/menu-items');
  } catch (e) {
    toast('CDJ MENU NOT LOADED · ' + e.message.toUpperCase(), 'error');
    return;
  }
  menuLoaded = true;
  menuItemsCache = (r && Array.isArray(r.items)) ? r.items : [];
  trackDetailCache = (r && r.track_detail) ? r.track_detail : 'bpm';
  trackDetailFieldsCache = (r && Array.isArray(r.track_detail_fields)) ? r.track_detail_fields : [];
  sortItemsCache = (r && Array.isArray(r.sorts)) ? r.sorts : [];
  renderMenuItems();
  renderSortItems();
}

function renderMenuItems() {
  const host = document.getElementById('cdj-menu-config');
  if (!host) return;
  // Track-detail dropdown: the field rendered in the second column of
  // every CDJ track list (BPM / Key / Artist / etc.). Falls back to
  // a hardcoded list if the server didn't ship one.
  const detailOpts = (trackDetailFieldsCache.length ? trackDetailFieldsCache : [
    {key:'bpm',label:'BPM'}, {key:'artist',label:'Artist'}, {key:'key',label:'Key'},
  ]).map(f => `<option value="${escape(f.key)}" ${f.key === trackDetailCache ? 'selected' : ''}>${escape(f.label)}</option>`).join('');
  // Two-list layout mirrors rekordbox's category configuration UI
  // (see docs/reference/cdj-menu-config.md in vynull-tools). Inactive items live on the
  // left; active items on the right, in CDJ render order. Locked items
  // (TRACK / PLAYLIST / HISTORY / SEARCH / FOLDER) stay greyed in the
  // active list and can't be moved out.
  const active = menuItemsCache.filter(m => m.visible);
  const inactive = menuItemsCache.filter(m => !m.visible);
  const renderCol = (items, side) => items.map(m => {
    const cls = ['menu-row'];
    if (m.locked) cls.push('locked');
    if (menuSelected[side] && menuSelected[side].has(m.key)) cls.push('selected');
    return `<div class="${cls.join(' ')}" data-side="${side}" data-key="${escape(m.key)}">${escape(m.label)}</div>`;
  }).join('');
  host.innerHTML = `
    <div class="menu-detail-row">
      <label for="menu-track-detail">Track-list detail column:</label>
      <select id="menu-track-detail">${detailOpts}</select>
    </div>
    <div class="hint">Move categories between Inactive and Active with → / ← · order with Up/Down · changes reach the CDJ on its next root-menu poll (~1s).</div>
    <div class="menu-twolist">
      <div class="col">
        <div class="col-head">INACTIVE CATEGORIES</div>
        <div class="col-body" id="menu-inactive">${renderCol(inactive, 'inactive') || '<div class="empty-state" style="padding:18px">— ALL ACTIVE —</div>'}</div>
      </div>
      <div class="actions">
        <button type="button" id="menu-activate"   title="Activate selected">→</button>
        <button type="button" id="menu-deactivate" title="Deactivate selected">←</button>
      </div>
      <div class="col">
        <div class="col-head">ACTIVE CATEGORIES</div>
        <div class="col-body" id="menu-active">${renderCol(active, 'active') || '<div class="empty-state" style="padding:18px">— NONE ACTIVE —</div>'}</div>
        <div class="menu-updown" style="padding: 0 12px 10px">
          <button type="button" id="menu-up">UP</button>
          <button type="button" id="menu-down">DOWN</button>
        </div>
      </div>
    </div>
    <div class="menu-config-foot">
      <button type="button" id="menu-reset">RESET TO DEFAULTS</button>
    </div>`;
  wireMenuItems();
}

// Selection state for the two lists, keyed by item.key. Persisting it
// across re-renders means a row stays highlighted after activation /
// reordering until the user clicks elsewhere.
const menuSelected = { active: new Set(), inactive: new Set() };
// Manual double-click detection: the single-click handler re-renders the rows,
// which replaces the DOM between the two clicks so the native dblclick event
// never fires. Track the last-clicked key + time in JS state (survives the
// re-render) instead.
let menuLastClick = { key: null, t: 0 };

function wireMenuItems() {
  const modal = document.getElementById('cdj-menu-config');
  if (!modal) return;

  // Row click toggles selection within that column. Locked rows can be
  // visually highlighted but won't drive any action.
  modal.querySelectorAll('.menu-row').forEach(row => {
    row.onclick = (e) => {
      const side = row.getAttribute('data-side');
      const key = row.getAttribute('data-key');

      // Manual double-click: same row clicked twice in quick succession flips
      // it to the other side (same as selecting it and pressing → / ←). The
      // native dblclick can't be used because the single-click re-render
      // replaces the row between the two clicks.
      const now = Date.now();
      const dbl = menuLastClick.key === key && (now - menuLastClick.t) < 400;
      menuLastClick = { key, t: now };
      if (dbl) {
        menuLastClick = { key: null, t: 0 };
        const item = menuItemsCache.find(m => m.key === key);
        if (!item || item.locked) return;
        menuSelected.active.clear();
        menuSelected.inactive.clear();
        menuSelected[side].add(key);
        const btn = modal.querySelector(side === 'inactive' ? '#menu-activate' : '#menu-deactivate');
        if (btn) btn.click();
        return;
      }

      const set = menuSelected[side];
      // Cmd/Ctrl-click extends selection; plain click replaces it.
      if (!(e.metaKey || e.ctrlKey)) {
        menuSelected[side === 'active' ? 'inactive' : 'active'].clear();
        if (set.has(key) && set.size === 1) {
          set.clear();
        } else {
          set.clear();
          set.add(key);
        }
      } else {
        if (set.has(key)) set.delete(key);
        else              set.add(key);
      }
      // Re-render just for the highlight; cheap since the list is small.
      renderMenuItems();
    };
  });

  const tdSel = modal.querySelector('#menu-track-detail');
  if (tdSel) tdSel.onchange = async () => {
    trackDetailCache = tdSel.value;
    const r = await fetch('/api/menu-items', {
      method: 'PUT', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ track_detail: tdSel.value }),
    });
    if (!r.ok) { toast('SAVE FAILED', 'error'); return; }
    toast('TRACK DETAIL · ' + tdSel.value.toUpperCase(), 'success');
  };

  const reset = modal.querySelector('#menu-reset');
  if (reset) reset.onclick = async () => {
    if (!await openConfirmModal({
      title: 'RESET CDJ MENU',
      message: 'Reset CDJ menu to defaults?\n\nOrder and visibility will be restored.',
      okLabel: 'RESET', danger: true,
    })) return;
    const r = await fetch('/api/menu-items/reset', { method: 'POST' });
    if (!r.ok) { toast('RESET FAILED', 'error'); return; }
    toast('MENU RESET', 'success');
    menuSelected.active.clear();
    menuSelected.inactive.clear();
    await loadMenuItems();
  };

  // Activate: move selected inactive items to the bottom of the active
  // list (matches rekordbox UX) and clear the inactive selection.
  modal.querySelector('#menu-activate').onclick = () => {
    const keys = Array.from(menuSelected.inactive);
    if (keys.length === 0) return;
    const moved = [];
    menuItemsCache = menuItemsCache.filter(m => {
      if (keys.includes(m.key) && !m.locked) {
        m.visible = true;
        moved.push(m);
        return false;
      }
      return true;
    });
    menuItemsCache.push(...moved);
    menuSelected.inactive.clear();
    moved.forEach(m => menuSelected.active.add(m.key));
    renderMenuItems();
    saveMenuItems();
  };

  // Deactivate: move selected active items to the inactive list.
  // Locked items are silently skipped — they can be selected but not
  // moved (matches rekordbox's greyed-out behaviour).
  modal.querySelector('#menu-deactivate').onclick = () => {
    const keys = Array.from(menuSelected.active);
    if (keys.length === 0) return;
    const moved = [];
    menuItemsCache = menuItemsCache.filter(m => {
      if (keys.includes(m.key) && !m.locked) {
        m.visible = false;
        moved.push(m);
        return false;
      }
      return true;
    });
    menuItemsCache.unshift(...moved); // inactive items appear at the top of left list
    menuSelected.active.clear();
    moved.forEach(m => menuSelected.inactive.add(m.key));
    renderMenuItems();
    saveMenuItems();
  };

  // Up/Down reorder within the active list. Operates on a single
  // selected active item (multi-select reorder gets ambiguous fast).
  const reorderActive = (delta) => {
    const keys = Array.from(menuSelected.active);
    if (keys.length !== 1) return;
    const key = keys[0];
    const activeKeys = menuItemsCache.filter(m => m.visible).map(m => m.key);
    const pos = activeKeys.indexOf(key);
    const newPos = pos + delta;
    if (pos < 0 || newPos < 0 || newPos >= activeKeys.length) return;
    // Swap in activeKeys and rebuild menuItemsCache: inactive items
    // first (preserving their order), then active items in new order.
    activeKeys.splice(pos, 1);
    activeKeys.splice(newPos, 0, key);
    const inactiveItems = menuItemsCache.filter(m => !m.visible);
    const activeItems = activeKeys.map(k => menuItemsCache.find(m => m.key === k));
    menuItemsCache = [...inactiveItems, ...activeItems];
    renderMenuItems();
    saveMenuItems();
  };
  modal.querySelector('#menu-up').onclick   = () => reorderActive(-1);
  modal.querySelector('#menu-down').onclick = () => reorderActive(+1);
}

async function saveMenuItems() {
  if (!menuLoaded) { toast('CDJ MENU NOT LOADED · NOT SAVED', 'error'); return; }
  const r = await fetch('/api/menu-items', {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ items: menuItemsCache }),
  });
  if (!r.ok) { toast('SAVE FAILED', 'error'); return; }
}

// ───────── CDJ SORT MENU ─────────
// The deck's SORT menu (the sorts its sort button offers for a track list),
// in the same two-list layout as the root menu: hidden sorts on the left,
// shown ones on the right in menu order. Saved with PUT /api/menu-items
// {sorts}; the deck reads it the next time its sort button is pressed.
let sortItemsCache = [];
const sortSelected = { active: new Set(), inactive: new Set() };
// Double-click moves a sort across, as in the root-menu editor. Detected by
// hand (see menuLastClick): the single-click re-render replaces the row
// between the two clicks, so the native dblclick never fires.
let sortLastClick = { id: null, t: 0 };

function renderSortItems() {
  const host = document.getElementById('cdj-sort-config');
  if (!host) return;
  const shown = sortItemsCache.filter(s => s.visible);
  const hidden = sortItemsCache.filter(s => !s.visible);
  const col = (items, side) => items.map(s => {
    const sel = sortSelected[side].has(s.id) ? ' selected' : '';
    return `<div class="menu-row${sel}" data-side="${side}" data-id="${s.id}">${escape(s.label || String(s.id))}</div>`;
  }).join('');
  host.innerHTML = `
    <div class="hint">The sorts the deck's sort button offers in a track list. Move sorts between Hidden and Shown with → / ← or a double-click · order with Up/Down. DEFAULT keeps the track-list detail column above; every other sort shows its own field. A rekordbox import with settings brings in your library's sort menu.</div>
    <div class="menu-twolist">
      <div class="col">
        <div class="col-head">HIDDEN SORTS</div>
        <div class="col-body">${col(hidden, 'inactive') || '<div class="empty-state" style="padding:18px">— ALL SHOWN —</div>'}</div>
      </div>
      <div class="actions">
        <button type="button" id="sort-show" title="Show selected">→</button>
        <button type="button" id="sort-hide" title="Hide selected">←</button>
      </div>
      <div class="col">
        <div class="col-head">SHOWN SORTS</div>
        <div class="col-body">${col(shown, 'active') || '<div class="empty-state" style="padding:18px">— NONE SHOWN —</div>'}</div>
        <div class="menu-updown" style="padding: 0 12px 10px">
          <button type="button" id="sort-up">UP</button>
          <button type="button" id="sort-down">DOWN</button>
        </div>
      </div>
    </div>
    <div class="menu-config-foot">
      <button type="button" id="sort-reset">RESET TO DEFAULTS</button>
    </div>`;
  wireSortItems(host);
}

function wireSortItems(host) {
  host.querySelectorAll('.menu-row').forEach(row => {
    row.onclick = (e) => {
      const side = row.getAttribute('data-side');
      const id = Number(row.getAttribute('data-id'));
      const now = Date.now();
      const dbl = sortLastClick.id === id && (now - sortLastClick.t) < 400;
      sortLastClick = dbl ? { id: null, t: 0 } : { id, t: now };
      if (dbl) {
        sortSelected.active.clear();
        sortSelected.inactive.clear();
        sortSelected[side].add(id);
        host.querySelector(side === 'inactive' ? '#sort-show' : '#sort-hide').click();
        return;
      }
      const set = sortSelected[side];
      if (!(e.metaKey || e.ctrlKey)) {
        sortSelected[side === 'active' ? 'inactive' : 'active'].clear();
        const only = set.has(id) && set.size === 1;
        set.clear();
        if (!only) set.add(id);
      } else if (set.has(id)) {
        set.delete(id);
      } else {
        set.add(id);
      }
      renderSortItems();
    };
  });
  // Show / hide the selected sorts; shown ones join the end of the menu.
  const move = (from, to, visible) => {
    const ids = Array.from(sortSelected[from]);
    if (!ids.length) return;
    if (!visible && sortItemsCache.filter(s => s.visible && !ids.includes(s.id)).length === 0) {
      toast('THE SORT MENU NEEDS ONE SORT', 'error');
      return;
    }
    const moved = sortItemsCache.filter(s => ids.includes(s.id));
    sortItemsCache = sortItemsCache.filter(s => !ids.includes(s.id));
    moved.forEach(s => { s.visible = visible; });
    sortItemsCache = visible ? [...sortItemsCache, ...moved] : [...moved, ...sortItemsCache];
    sortSelected[from].clear();
    moved.forEach(s => sortSelected[to].add(s.id));
    renderSortItems();
    saveSortItems();
  };
  host.querySelector('#sort-show').onclick = () => move('inactive', 'active', true);
  host.querySelector('#sort-hide').onclick = () => move('active', 'inactive', false);
  const reorder = (delta) => {
    const ids = Array.from(sortSelected.active);
    if (ids.length !== 1) return;
    const shown = sortItemsCache.filter(s => s.visible);
    const pos = shown.findIndex(s => s.id === ids[0]);
    const to = pos + delta;
    if (pos < 0 || to < 0 || to >= shown.length) return;
    const [it] = shown.splice(pos, 1);
    shown.splice(to, 0, it);
    sortItemsCache = [...shown, ...sortItemsCache.filter(s => !s.visible)];
    renderSortItems();
    saveSortItems();
  };
  host.querySelector('#sort-up').onclick = () => reorder(-1);
  host.querySelector('#sort-down').onclick = () => reorder(+1);
  host.querySelector('#sort-reset').onclick = async () => {
    if (!await openConfirmModal({
      title: 'RESET SORT MENU',
      message: "Reset the CDJ sort menu to rekordbox's defaults?",
      okLabel: 'RESET', danger: true,
    })) return;
    const r = await fetch('/api/menu-items/reset?sorts=1', { method: 'POST' });
    if (!r.ok) { toast('RESET FAILED', 'error'); return; }
    toast('SORT MENU RESET', 'success');
    sortSelected.active.clear();
    sortSelected.inactive.clear();
    await loadMenuItems();
  };
}

async function saveSortItems() {
  if (!menuLoaded) { toast('CDJ MENU NOT LOADED · NOT SAVED', 'error'); return; }
  const r = await fetch('/api/menu-items', {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ sorts: sortItemsCache.map(s => ({ id: s.id, visible: s.visible })) }),
  });
  if (!r.ok) { toast('SAVE FAILED', 'error'); return; }
}

// Section catalog for the SETTINGS tab sidebar.
// Our beat detector searches 60-200 BPM (autocorrelation + peak spacing); it
// can't see outside that, so the presets and the custom inputs stay inside it —
// unlike rekordbox's list, whose 48-95 and 108-255 entries would have dead
// zones for us. Custom values are clamped to this range.
const BPM_DETECT_MIN = 60;
const BPM_DETECT_MAX = 200;

// BPM analysis-range presets. "Default" (0,0) leaves the analyzer's own
// detection window in place; every other entry is a one-octave-wide window
// inside the detector's 60-200 range that forces the octave.
const BPM_RANGE_PRESETS = [
  { label: 'Default (80–170)', min: 0,   max: 0   },
  { label: '60–120',           min: 60,  max: 120 },
  { label: '70–140',           min: 70,  max: 140 },
  { label: '80–160',           min: 80,  max: 160 },
  { label: '90–180',           min: 90,  max: 180 },
  { label: '100–200',          min: 100, max: 200 },
];

const SETTINGS_SECTIONS = [
  { key: 'webui',          label: 'WEB UI'         }, // browser-local display prefs (not a CDJ setting)
  { key: 'source',         label: 'LIBRARY SOURCE' }, // rekordbox import + path fix (not a CDJ setting)
  { key: 'overlay',        label: 'STREAM OVERLAY' }, // now-playing overlay config (not a CDJ setting)
  { key: 'cdj_menu',       label: 'CDJ MENU'       },
  { key: 'my_setting',     label: 'MYSETTING'    },
  { key: 'my_setting_2',   label: 'MYSETTING2'   },
  { key: 'djm_my_setting', label: 'DJMMYSETTING' },
  { key: 'dev_setting',    label: 'DEVSETTING'   },
];
const SETTINGS_ACTIVE_LS_KEY = 'pdj-settings-active-section';
let settingsActiveSection = (() => {
  try {
    const v = localStorage.getItem(SETTINGS_ACTIVE_LS_KEY);
    if (SETTINGS_SECTIONS.some(s => s.key === v)) return v;
  } catch (e) {}
  return SETTINGS_SECTIONS[0].key;
})();
function setSettingsActiveSection(key) {
  if (settingsActiveSection === key) return;
  settingsActiveSection = key;
  try { localStorage.setItem(SETTINGS_ACTIVE_LS_KEY, key); } catch (e) {}
  // Force a re-render — bypass the "no change" snapshot since the data
  // is the same but the visible section needs to swap.
  lastSettingsKey = '';
  if (currentSettings) renderSettings(currentSettings);
}

function renderSettings(cfg) {
  const el = document.getElementById('settings-content');
  const nav = document.getElementById('settings-nav');
  if (!cfg || !settingsEnums) {
    if (el) el.innerHTML = '<div class="empty-state">LOADING</div>';
    return;
  }
  // Skip re-render if (a) nothing actually changed from the prior poll, or
  // (b) the user is mid-interaction (a select is focused or open within the
  // settings panel). Either case would otherwise wipe their open dropdown
  // mid-click — the 1Hz poll made the panel unusable. We still update
  // currentSettings so subsequent edits POST from up-to-date state.
  currentSettings = cfg;
  const cfgKey = JSON.stringify(cfg) + '|' + settingsActiveSection;
  if (cfgKey === lastSettingsKey) return;
  const focused = document.activeElement;
  if (focused && el && el.contains(focused)) {
    // Don't disturb the user; pick this change up on the next poll once
    // their focus moves elsewhere.
    return;
  }
  lastSettingsKey = cfgKey;

  // CDJ MENU and WEB UI are virtual sections (not part of the settings config);
  // they're always shown, the others appear only when present in cfg.
  const sectionAvailable = s => s.key === 'cdj_menu' || s.key === 'webui' || s.key === 'source' || s.key === 'overlay' || !!cfg[s.key];

  // Render the sidebar with all sections; the body only shows the active one.
  if (nav) {
    nav.innerHTML = SETTINGS_SECTIONS
      .filter(sectionAvailable)
      .map(s => `<button type="button" data-section="${s.key}" class="${s.key === settingsActiveSection ? 'active' : ''}">${s.label}</button>`)
      .join('');
    nav.querySelectorAll('button[data-section]').forEach(btn => {
      btn.addEventListener('click', () => setSettingsActiveSection(btn.getAttribute('data-section')));
    });
  }

  // If the persisted active section isn't available in the current cfg
  // (e.g., a fresh install with only some settings loaded), fall back to
  // the first one that is.
  let section = SETTINGS_SECTIONS.find(s => s.key === settingsActiveSection && sectionAvailable(s));
  if (!section) {
    section = SETTINGS_SECTIONS.find(sectionAvailable);
  }
  if (!section) {
    el.innerHTML = '<div class="empty-state">NO SETTINGS LOADED</div>';
    return;
  }

  // The top note describes pushing changes to the CDJ — hide it for the
  // browser-local WEB UI section so it doesn't imply those sync to the device.
  const topNote = document.querySelector('#settings .settings-note');
  if (topNote) topNote.style.display = (section.key === 'webui' || section.key === 'source' || section.key === 'overlay') ? 'none' : '';

  // WEB UI: browser-local display preferences (rendered from localStorage, not cfg).
  if (section.key === 'webui') {
    renderWebUISettings(el);
    return;
  }

  // STREAM OVERLAY: server-side now-playing overlay config (fetched, not from cfg).
  if (section.key === 'overlay') {
    renderOverlaySettings(el);
    return;
  }

  // CDJ MENU: render the category-configuration UI inline (was a modal).
  if (section.key === 'cdj_menu') {
    el.innerHTML = `<div class="settings-block">
      <div class="head"><span class="inner">CDJ MENU</span></div>
      <div class="body"><div id="cdj-menu-config" class="menu-config"><div class="empty-state">LOADING</div></div></div>
    </div>
    <div class="settings-block">
      <div class="head"><span class="inner">CDJ SORT MENU</span></div>
      <div class="body"><div id="cdj-sort-config" class="menu-config"><div class="empty-state">LOADING</div></div></div>
    </div>`;
    if (menuItemsCache.length) { renderMenuItems(); renderSortItems(); } else loadMenuItems();
    return;
  }

  // LIBRARY SOURCE: import from rekordbox + bulk path fix (not a CDJ setting).
  if (section.key === 'source') {
    // renderSettings receives the full config itself on the 1 Hz poll
    // (renderSettings(settings.full)) and the API response (has .full) on
    // other paths — accept both shapes, or the dropdown always shows Default.
    const bpmFull = (cfg && (cfg.full || cfg)) || {};
    const bpmCurMin = Number(bpmFull.bpm_range_min) || 0;
    const bpmCurMax = Number(bpmFull.bpm_range_max) || 0;
    const bpmCustomActive = bpmCurMin > 0 && bpmCurMax > bpmCurMin &&
      !BPM_RANGE_PRESETS.some(p => p.min === bpmCurMin && p.max === bpmCurMax);
    el.innerHTML = `<div class="settings-block">
      <div class="head"><span class="inner">LIBRARY SOURCE</span></div>
      <div class="body">
        <div class="source-action">
          <label class="export-label">Add files or folders</label>
          <div style="display:flex; gap:6px;">
            <input type="text" id="add-path-input" class="input" placeholder="/media/usb/Music  ·  or  ·  /path/to/track.flac" autocomplete="off" spellcheck="false" style="flex:1">
            <button type="button" id="add-browse-btn" class="source-btn">Browse…</button>
            <button type="button" id="add-path-btn" class="source-btn">+ Add</button>
          </div>
          <div class="export-hint">Add a file or a folder from this machine to the library. Folders are scanned recursively for MP3, M4A, FLAC, WAV, and AIFF; tracks are analyzed in the background.</div>
          <div id="add-path-status" class="export-hint" style="display:none; margin-top:8px;"></div>
        </div>
        <div class="source-action">
          <button type="button" id="set-import-rb" class="source-btn">↓ Import a library…</button>
          <div class="export-hint">Bring in tracks, playlists, tags, cues, and analysis from a rekordbox <b>XML</b> export, an encrypted <b>master.db</b>, or a library backup <b>.zip</b> — or a Traktor <b>collection.nml</b>.</div>
        </div>
        <div class="source-action">
          <button type="button" id="set-fix-paths" class="source-btn">↪ Fix track paths…</button>
          <div class="export-hint">Search-and-replace a path prefix across the whole library — e.g. after moving your music, or importing from another machine.</div>
        </div>
        <div class="source-action">
          <label class="export-label">Analysis · BPM range</label>
          <div class="selector"><select id="bpm-range-select">${bpmRangeOptionsHTML(cfg)}</select></div>
          <div id="bpm-range-custom" style="display:${bpmCustomActive ? 'flex' : 'none'}; gap:6px; margin-top:6px; align-items:center;">
            <input type="number" id="bpm-range-min" class="input" placeholder="${BPM_DETECT_MIN}" min="${BPM_DETECT_MIN}" max="${BPM_DETECT_MAX}" step="1" value="${bpmCustomActive ? bpmCurMin : ''}" style="width:74px">
            <span style="opacity:.6">–</span>
            <input type="number" id="bpm-range-max" class="input" placeholder="${BPM_DETECT_MAX}" min="${BPM_DETECT_MIN}" max="${BPM_DETECT_MAX}" step="1" value="${bpmCustomActive ? bpmCurMax : ''}" style="width:74px">
            <button type="button" id="bpm-range-apply" class="source-btn">Apply</button>
          </div>
          <div class="export-hint">Constrain BPM detection to a tempo range so fast genres (e.g. drum &amp; bass) stop analyzing at half or a third of their tempo. Pick a preset or Custom…. Our detector supports ${BPM_DETECT_MIN}–${BPM_DETECT_MAX} BPM; values outside that are clamped. Changing it re-analyzes affected tracks as they're next loaded.</div>
        </div>
        <div class="source-action">
          <label class="export-label">Analysis · Beat-grid offset</label>
          <div style="display:flex; gap:6px; align-items:center; flex-wrap:wrap;">
            <input type="number" id="grid-offset-input" class="input" min="-200" max="200" step="1" value="${Number(bpmFull.grid_offset_ms) || 0}" style="width:74px">
            <span style="opacity:.6">ms</span>
            <button type="button" id="grid-offset-apply" class="source-btn">Apply</button>
          </div>
          <div class="export-hint">Shift every detected beat grid by this many milliseconds (negative = earlier). 0 matches rekordbox. Applies instantly; hand-corrected and imported grids are not moved.</div>
        </div>
      </div>
    </div>`;
    const bpmSel = el.querySelector('#bpm-range-select');
    const bpmCustomRow = el.querySelector('#bpm-range-custom');
    if (bpmSel) bpmSel.onchange = () => {
      if (bpmSel.value === 'custom') {
        // Reveal the inputs and wait for Apply — don't POST a half-typed range.
        if (bpmCustomRow) bpmCustomRow.style.display = 'flex';
        const mi = el.querySelector('#bpm-range-min');
        if (mi) mi.focus();
      } else {
        if (bpmCustomRow) bpmCustomRow.style.display = 'none';
        onBPMRangeChange(bpmSel);
      }
    };
    const bpmApply = el.querySelector('#bpm-range-apply');
    if (bpmApply) bpmApply.onclick = () => applyCustomBPMRange(el);
    const goApply = el.querySelector('#grid-offset-apply');
    if (goApply) goApply.onclick = () => applyGridOffset(el);
    const imp = el.querySelector('#set-import-rb');
    if (imp) imp.onclick = () => openImportRekordboxDialog();
    const fix = el.querySelector('#set-fix-paths');
    if (fix) fix.onclick = () => openFixPathsDialog();

    const addBtn = el.querySelector('#add-path-btn');
    const addInput = el.querySelector('#add-path-input');
    const addStatus = el.querySelector('#add-path-status');
    if (addBtn && addInput) {
      const doAdd = async () => {
        const p = addInput.value.trim();
        if (!p) return;
        addBtn.disabled = true;
        await addTracksWithProgress([p], addStatus, (res) => { if (res.added) addInput.value = ''; });
        addBtn.disabled = false;
      };
      addBtn.onclick = doAdd;
      addInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') doAdd(); });
    }
    const browseBtn = el.querySelector('#add-browse-btn');
    if (browseBtn) browseBtn.onclick = () => openAddBrowserDialog((res) => {
      if (addStatus) {
        addStatus.style.display = 'block';
        addStatus.textContent = `Added ${res.added} track${res.added === 1 ? '' : 's'} from the browser.`;
      }
    });
    return;
  }

  const obj = cfg[section.key];
  el.innerHTML = `<div class="settings-block">
    <div class="head"><span class="inner">${section.label}</span></div>
    <div class="body">
      ${Object.entries(obj).map(([k, v]) => settingsRow(section.key, k, v)).join('')}
    </div>
  </div>`;
  el.querySelectorAll('select[data-section]').forEach(sel => {
    sel.addEventListener('change', () => handleSettingChange(sel));
  });
}
function settingsRow(sectionKey, fieldKey, value) {
  const opts = settingsEnums[fieldKey];
  if (!opts) {
    return `<div class="settings-row">
      <div class="key">${escape(fieldKey)}</div>
      <div style="color: var(--text); font-size: 11px;">${escape(String(value))}</div>
    </div>`;
  }
  const optionsHTML = opts.map(o =>
    `<option value="${escape(o)}" ${o === value ? 'selected' : ''}>${escape(o)}</option>`).join('');
  const extra = opts.includes(value) ? '' :
    `<option value="${escape(value)}" selected>${escape(value)} ⚑</option>`;
  return `<div class="settings-row">
    <div class="key">${escape(fieldKey)}</div>
    <div class="selector">
      <select data-section="${sectionKey}" data-field="${fieldKey}">
        ${extra}${optionsHTML}
      </select>
    </div>
  </div>`;
}
async function handleSettingChange(select) {
  const section = select.getAttribute('data-section');
  const field = select.getAttribute('data-field');
  const newValue = select.value;
  if (!currentSettings || !currentSettings[section]) return;
  const updated = JSON.parse(JSON.stringify(currentSettings));
  updated[section][field] = newValue;
  const r = await fetch('/api/settings', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ full: updated }),
  });
  if (r.ok) {
    currentSettings = updated;
    // Keep the "last seen" snapshot in sync so the next poll doesn't trigger
    // a redundant re-render comparing against the pre-edit value.
    lastSettingsKey = JSON.stringify(updated);
    toast(`${field.replace(/_/g, ' ').toUpperCase()} → ${newValue.toUpperCase()}`, 'success');
  } else {
    toast('SAVE FAILED', 'error');
    refresh();
  }
}

// bpmRangeOptionsHTML builds the <option> list for the BPM-range dropdown: the
// presets plus a "Custom…" entry. Selects the matching preset, or "Custom…"
// when the persisted range matches no preset (e.g. a custom value entered here
// or via --bpm-range on the CLI).
function bpmRangeOptionsHTML(cfg) {
  const full = (cfg && (cfg.full || cfg)) || {};
  const curMin = Number(full.bpm_range_min) || 0;
  const curMax = Number(full.bpm_range_max) || 0;
  let matched = false;
  let opts = BPM_RANGE_PRESETS.map(p => {
    const isSel = p.min === curMin && p.max === curMax;
    if (isSel) matched = true;
    return `<option value="${p.min}-${p.max}"${isSel ? ' selected' : ''}>${p.label}</option>`;
  }).join('');
  const customActive = !matched && curMin > 0 && curMax > curMin;
  opts += `<option value="custom"${customActive ? ' selected' : ''}>Custom…</option>`;
  return opts;
}

// postBPMRange persists a range via the full-config POST (the same path every
// settings field uses) and reflects it immediately.
async function postBPMRange(min, max, label) {
  // currentSettings is the full config itself on the poll path, or the API
  // response (has .full) otherwise. Base the POST on the whole config — the
  // full-config POST does a SetConfig REPLACE, so sending only the two BPM
  // fields would wipe every other setting (my_setting, dev_setting, ...).
  const base = (currentSettings && (currentSettings.full || currentSettings)) || {};
  const full = JSON.parse(JSON.stringify(base));
  full.bpm_range_min = min;
  full.bpm_range_max = max;
  const r = await fetch('/api/settings', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ full }),
  });
  if (!r.ok) { toast('SAVE FAILED', 'error'); return; }
  // Reflect immediately into whichever shape currentSettings holds.
  const cur = (currentSettings && currentSettings.full) || currentSettings;
  if (cur) { cur.bpm_range_min = min; cur.bpm_range_max = max; lastSettingsKey = ''; }
  toast('BPM RANGE · ' + label, 'success');
}

// applyGridOffset validates and saves the beat-grid offset via the
// full-config POST (whole config, since the POST replaces it).
async function applyGridOffset(scope) {
  const ms = Number(scope.querySelector('#grid-offset-input').value);
  if (!isFinite(ms) || Math.abs(ms) > 200) { toast('OFFSET MUST BE −200…200 MS', 'error'); return; }
  const base = (currentSettings && (currentSettings.full || currentSettings)) || {};
  const full = JSON.parse(JSON.stringify(base));
  full.grid_offset_ms = ms;
  const r = await fetch('/api/settings', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ full }),
  });
  if (!r.ok) { toast('SAVE FAILED', 'error'); return; }
  const cur = (currentSettings && currentSettings.full) || currentSettings;
  if (cur) { cur.grid_offset_ms = ms; lastSettingsKey = ''; }
  // Detected grids shift server-side on their next read; drop cached
  // waveforms/beats so an open drawer refetches the moved grid.
  if (typeof waveformCache !== 'undefined') waveformCache.clear();
  if (selectedTrackID) openDetail(selectedTrackID);
  toast(`BEAT-GRID OFFSET · ${ms > 0 ? '+' : ''}${ms} MS`, 'success');
}

// onBPMRangeChange handles a preset selection (Default or an octave preset).
async function onBPMRangeChange(sel) {
  const [min, max] = sel.value.split('-').map(Number);
  postBPMRange(min, max, sel.options[sel.selectedIndex].text);
}

// applyCustomBPMRange validates, clamps to the detector's range, and saves the
// min/max from the custom inputs.
async function applyCustomBPMRange(scope) {
  let min = Number(scope.querySelector('#bpm-range-min').value);
  let max = Number(scope.querySelector('#bpm-range-max').value);
  if (!(min > 0 && max > 0 && max > min)) {
    toast('RANGE MUST BE MIN < MAX, BOTH ABOVE 0', 'error');
    return;
  }
  const cMin = Math.max(BPM_DETECT_MIN, Math.min(BPM_DETECT_MAX, Math.round(min)));
  const cMax = Math.max(BPM_DETECT_MIN, Math.min(BPM_DETECT_MAX, Math.round(max)));
  if (cMax <= cMin) {
    toast(`RANGE OUTSIDE ${BPM_DETECT_MIN}–${BPM_DETECT_MAX} BPM`, 'error');
    return;
  }
  const clamped = cMin !== Math.round(min) || cMax !== Math.round(max);
  postBPMRange(cMin, cMax, `${cMin}–${cMax}${clamped ? ' (clamped)' : ''}`);
}

// WEB UI section — browser-local display preferences. These mirror the statusbar
// quick-toggles (same localStorage); changing one updates the other via
// syncWebUISelects(). Nothing here is sent to the CDJ.
// Single source of truth for UI themes — data-theme value ↔ display label.
// Mirrors the top-bar #theme-select so the WEB UI settings dropdown stays
// consistent with it (was offering only DARK/LIGHT and conflating label
// with value).
const THEMES = [
  { value: 'dark',  label: 'TERMINAL' },
  { value: 'slate', label: 'SLATE' },
  { value: 'light', label: 'LIGHT' },
];
function renderWebUISettings(el) {
  // opts entries may be plain strings (value === label) or {v, t} pairs
  // when the stored value differs from the displayed label (e.g. themes).
  const row = (id, label, opts, cur, hint) => `<div class="settings-row">
      <div class="key">${label}${hint ? `<div class="row-hint">${hint}</div>` : ''}</div>
      <div class="selector"><select data-webui="${id}">${
        opts.map(o => {
          const v = (typeof o === 'object') ? o.v : o;
          const t = (typeof o === 'object') ? o.t : o;
          return `<option value="${v}" ${v === cur ? 'selected' : ''}>${t}</option>`;
        }).join('')
      }</select></div>
    </div>`;
  const waveCur = waveColorOverride ? waveColorOverride.toUpperCase() : 'AUTO';
  const themeCur = document.documentElement.dataset.theme || 'dark';
  el.innerHTML = `<div class="settings-block">
    <div class="head"><span class="inner">WEB UI</span></div>
    <div class="settings-note" style="margin: 6px 8px; border: 0">
      BROWSER-LOCAL DISPLAY PREFERENCES · STORED IN THIS BROWSER · NOT SENT TO THE CDJ
    </div>
    <div class="body">
      ${row('waveColor', 'waveform colour', ['AUTO', 'BLUE', 'RGB', '3BAND'], waveCur, 'AUTO follows the CDJ waveform-colour setting')}
      ${row('vocalLevel', 'vocal markers', VOCAL_LEVELS.map(l => l.name), VOCAL_LEVELS[vocalLevel].name, '🎤 on sung phrases — sensitivity: higher catches more, with more false positives')}
      ${row('theme', 'theme', THEMES.map(t => ({ v: t.value, t: t.label })), themeCur)}
      <div class="settings-row">
        <div class="key">cue default colours<div class="row-hint">colour applied to new hot cues (A–H) you add from the web UI · click a slot to change</div></div>
        <div class="selector">
          <div class="cue-default-grid">${
            [1, 2, 3, 4, 5, 6, 7, 8].map(n => {
              const cid = defaultCueColorFor(n);
              const bg = cid === 0 ? '' : `background:${CUE_COLORS[cid] || CUE_COLORS[0]}`;
              const fg = cid === 0 ? '' : `color:${isWarmCueColor(cid) ? '#000' : '#fff'}`;
              return `<button type="button" class="cue-default-swatch ${cid === 0 ? 'none' : ''}" data-cue-default="${n}" style="${[bg, fg].filter(Boolean).join(';')}" title="Hot cue ${cueLabel(n)} default colour"><span>${cueLabel(n)}</span></button>`;
            }).join('')
          }</div>
        </div>
      </div>
    </div>
  </div>`;
  el.querySelectorAll('select[data-webui]').forEach(s =>
    s.addEventListener('change', () => handleWebUIChange(s)));
  el.querySelectorAll('.cue-default-swatch').forEach(sw =>
    sw.addEventListener('click', () => openDefaultCueColorPicker(sw, parseInt(sw.getAttribute('data-cue-default'), 10))));
}

// STREAM OVERLAY: edits the server-side now-playing overlay config
// (/api/overlay/config), which the /overlay page (e.g. in OBS) reads.
async function renderOverlaySettings(el) {
  el.innerHTML = '<div class="empty-state">LOADING</div>';
  let cfg;
  try {
    cfg = await (await fetch('/api/overlay/config', { cache: 'no-store' })).json();
  } catch (e) {
    el.innerHTML = '<div class="empty-state">OVERLAY CONFIG UNAVAILABLE</div>';
    return;
  }
  const sel = (id, opts, cur) => `<select data-overlay="${id}">${
    opts.map(([v, t]) => `<option value="${v}" ${v === cur ? 'selected' : ''}>${t}</option>`).join('')
  }</select>`;
  const url = location.origin + '/overlay';
  const labelVal = (cfg.label || '').replace(/&/g, '&amp;').replace(/"/g, '&quot;');
  el.innerHTML = `<div class="settings-block">
    <div class="head"><span class="inner">STREAM OVERLAY</span></div>
    <div class="settings-note" style="margin: 6px 8px; border: 0">
      NOW-PLAYING OVERLAY FOR STREAMING · ADD AN OBS BROWSER SOURCE AT <b>${url}</b> WITH A TRANSPARENT BACKGROUND · CHANGES APPLY LIVE
    </div>
    <div class="body">
      <div class="settings-row"><div class="key">position</div><div class="selector">${sel('position',
        [['bottom-left', 'Bottom left'], ['bottom-right', 'Bottom right'], ['top-left', 'Top left'],
         ['top-right', 'Top right'], ['bottom-center', 'Bottom centre'], ['top-center', 'Top centre']], cfg.position)}</div></div>
      <div class="settings-row"><div class="key">style<div class="row-hint">spinning vinyl, or a square album cover</div></div>
        <div class="selector">${sel('style', [['vinyl', 'Vinyl (spinning)'], ['cover', 'Cover (square)']], cfg.style)}</div></div>
      <div class="settings-row"><div class="key">accent colour</div>
        <div class="selector"><input type="color" data-overlay="accent" value="${cfg.accent}"></div></div>
      <div class="settings-row"><div class="key">width<div class="row-hint">card width in px (280–1000)</div></div>
        <div class="selector"><input type="number" class="input" data-overlay="width" value="${cfg.width || 440}" min="280" max="1000" step="10" style="width:88px;padding-left:10px;padding-right:6px"></div></div>
      <div class="settings-row"><div class="key">label<div class="row-hint">header text · leave blank to hide</div></div>
        <div class="selector"><input type="text" class="input" data-overlay="label" value="${labelVal}" maxlength="40" placeholder="Now Playing"></div></div>
      <div class="settings-row"><div class="key">show BPM / key</div>
        <div class="selector"><input type="checkbox" data-overlay="show_meta" ${cfg.show_meta !== false ? 'checked' : ''}></div></div>
      <div class="settings-row"><div class="key">now-playing card<div class="row-hint">the audible deck's track</div></div>
        <div class="selector"><input type="checkbox" data-overlay="show_nowplaying" ${cfg.show_nowplaying !== false ? 'checked' : ''}></div></div>
      <div class="settings-row"><div class="key">waveform<div class="row-hint">scrolling waveform under the card</div></div>
        <div class="selector"><input type="checkbox" data-overlay="show_waveform" ${cfg.show_waveform !== false ? 'checked' : ''}></div></div>
      <div class="settings-row"><div class="key">waveform style<div class="row-hint">half (bottom bars) or full (mirrored both sides of centre)</div></div>
        <div class="selector">${sel('waveform_style', [['half', 'Half'], ['full', 'Full (mirrored)']], cfg.waveform_style || 'full')}</div></div>
      <div class="settings-row"><div class="key">full-height band<div class="row-hint">tall waveform across the edge · replaces the small one</div></div>
        <div class="selector"><input type="checkbox" data-overlay="waveform_band" ${cfg.waveform_band ? 'checked' : ''}></div></div>
      <div class="settings-row"><div class="key">band height<div class="row-hint">band height in px (60–600)</div></div>
        <div class="selector"><input type="number" class="input" data-overlay="waveform_height" value="${cfg.waveform_height || 180}" min="60" max="600" step="10" style="width:88px;padding-left:10px;padding-right:6px"></div></div>
      <div class="settings-row"><div class="key">decks<div class="row-hint">which deck is playing what, with waveforms</div></div>
        <div class="selector"><input type="checkbox" data-overlay="show_decks" ${cfg.show_decks ? 'checked' : ''}></div></div>
      <div class="settings-row"><div class="key">recently played</div>
        <div class="selector"><input type="checkbox" data-overlay="show_history" ${cfg.show_history ? 'checked' : ''}></div></div>
      <div class="settings-row"><div class="key">recent count<div class="row-hint">how many tracks in the recently-played list</div></div>
        <div class="selector"><input type="number" class="input" data-overlay="history_count" value="${cfg.history_count || 5}" min="0" max="20" style="width:74px;padding-left:10px;padding-right:6px"></div></div>
      <div class="settings-row"><div class="key"></div><div class="selector">
        <a href="/overlay" target="_blank" rel="noopener" class="source-btn" style="text-decoration:none">&#8599; Open overlay preview</a></div></div>
    </div>
  </div>`;
  const save = () => {
    const v = id => el.querySelector(`[data-overlay="${id}"]`);
    fetch('/api/overlay/config', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        position: v('position').value,
        style: v('style').value,
        accent: v('accent').value,
        label: v('label').value,
        show_meta: v('show_meta').checked,
        show_nowplaying: v('show_nowplaying').checked,
        show_waveform: v('show_waveform').checked,
        waveform_style: v('waveform_style').value,
        waveform_band: v('waveform_band').checked,
        waveform_height: parseInt(v('waveform_height').value, 10) || 0,
        show_decks: v('show_decks').checked,
        show_history: v('show_history').checked,
        history_count: parseInt(v('history_count').value, 10) || 0,
        width: parseInt(v('width').value, 10) || 0,
      }),
    }).then(() => toast('OVERLAY UPDATED', 'success'));
  };
  el.querySelectorAll('[data-overlay]').forEach(c => c.addEventListener('change', save));
}
function handleWebUIChange(sel) {
  const id = sel.getAttribute('data-webui'), v = sel.value;
  if (id === 'waveColor') {
    setWaveColorOverride(v === 'AUTO' ? null : v.toLowerCase());
  } else if (id === 'vocalLevel') {
    const i = VOCAL_LEVELS.findIndex(l => l.name === v);
    if (i >= 0) {
      vocalLevel = i;
      try { localStorage.setItem('vocalLevel', vocalLevel); } catch (e) {}
      renderVocalButton();
      reapplyVocalMarkers();
    }
  } else if (id === 'theme') {
    // v is the data-theme value ('dark' | 'slate' | 'light').
    document.documentElement.dataset.theme = v;
    try { localStorage.setItem('theme', v); } catch (e) {}
    const top = document.getElementById('theme-select');
    if (top) top.value = v;
    const label = (THEMES.find(t => t.value === v) || {}).label || v;
    toast('THEME → ' + label, 'success');
    return;
  }
  toast(id.replace(/([A-Z])/g, ' $1').toUpperCase() + ' → ' + v, 'success');
}
// Keep the WEB UI section's selects in sync when a statusbar quick-toggle
// changes the same preference (no-op when the section isn't rendered).
function syncWebUISelects() {
  const wc = document.querySelector('select[data-webui="waveColor"]');
  if (wc) wc.value = waveColorOverride ? waveColorOverride.toUpperCase() : 'AUTO';
  const vl = document.querySelector('select[data-webui="vocalLevel"]');
  if (vl) vl.value = VOCAL_LEVELS[vocalLevel].name;
  const th = document.querySelector('select[data-webui="theme"]');
  if (th) th.value = document.documentElement.dataset.theme || 'dark';
}

