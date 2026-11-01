// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// --- Resizable library columns -------------------------------------------
// Per-column widths (px) persisted in localStorage; missing keys fall back to
// DEFAULT_COL_WIDTHS. The art column is excluded (its width derives from the
// album-art mode + row height). Applied via a <colgroup> under
// table-layout:fixed, with a trailing auto column absorbing any slack.
const COL_W_LS_KEY = 'pdj-library-col-widths';
const COL_W_MIN = 44, COL_W_MAX = 900;
const DEFAULT_COL_WIDTHS = {
  waveform: 156, id: 58, title: 280, artist: 180, album: 180, genre: 150,
  bpm: 74, key: 66, duration: 74, year: 60, rating: 96, color: 70,
  date_added: 110, date_created: 110, release_date: 110, play_count: 64, bitrate: 74, file_type: 70, comment: 200,
  label: 150, remixer: 160, tags: 180, file_path: 300, webui: 70, source: 90,
  cdj_menu: 90, my_setting: 110, djm_my_setting: 120, dev_setting: 110,
};
function loadColWidths() {
  try { const v = JSON.parse(localStorage.getItem(COL_W_LS_KEY)); if (v && typeof v === 'object') return v; } catch (e) {}
  return {};
}
let libColWidths = loadColWidths();
function saveColWidths() { try { localStorage.setItem(COL_W_LS_KEY, JSON.stringify(libColWidths)); } catch (e) {} }
// Column sizing mode: 'fit' squishes columns to fit the panel (no horizontal
// scroll); 'scroll' keeps their exact widths and lets the table scroll
// horizontally. Scroll mode turns the table into its own bounded scroll
// viewport (both axes internal) so the sticky header keeps working — see the
// html[data-lib-colmode="scroll"] CSS + syncStickyOffsets.
const COL_MODE_LS_KEY = 'pdj-library-col-mode';
function loadColMode() { try { return localStorage.getItem(COL_MODE_LS_KEY) === 'scroll' ? 'scroll' : 'fit'; } catch (e) { return 'fit'; } }
let libColMode = loadColMode();
function applyColMode(m) { document.documentElement.setAttribute('data-lib-colmode', m); }
applyColMode(libColMode);
function colWidth(key) {
  const w = libColWidths[key];
  const base = Number.isFinite(w) ? w : (DEFAULT_COL_WIDTHS[key] || 120);
  return Math.max(COL_W_MIN, Math.min(COL_W_MAX, base));
}
function libWaveColWidth() { return colWidth('waveform'); }
function artColWidth() { return libraryArtFit === 'square' ? Math.max(24, libraryRowHeight) : 88; }
// Push every <col> width from the store and recompute the table's min-width, so
// the wrap (no overflow → page horizontal scroll) reserves the full width and
// columns aren't squished when they overflow the viewport.
const LIB_SEL_W = 34, LIB_LOAD_W = 96;
function libWrapAvail(table) {
  const wrap = table.closest('.lib-table-wrap');
  return wrap ? wrap.clientWidth : 0;
}
function applyColWidthsLive(root) {
  const table = (root || document).querySelector('table.lib');
  if (!table) return;
  const cols = [...table.querySelectorAll('colgroup col[data-col-key]')];
  const want = cols.map(col => {
    const k = col.getAttribute('data-col-key');
    return (k === 'art') ? artColWidth() : colWidth(k);
  });
  const loadCol = table.querySelector('colgroup col.c-load');
  const avail = libWrapAvail(table);
  const dataSum = want.reduce((a, b) => a + b, 0);
  const total = LIB_SEL_W + dataSum + LIB_LOAD_W;

  if (libColMode === 'scroll' && avail > 0 && total > avail) {
    // Overflow → keep exact widths and let the (bounded) wrap scroll
    // horizontally. Load column is fixed so the sum is exact.
    cols.forEach((col, i) => { col.style.width = want[i] + 'px'; });
    if (loadCol) loadCol.style.width = LIB_LOAD_W + 'px';
    table.style.width = total + 'px';
    table.style.removeProperty('min-width');
    return;
  }

  // Fits (either mode) or fit-mode overflow: the trailing load column is auto and
  // absorbs slack so data columns keep exact widths and the table fills the
  // panel. In fit mode, if columns would still overflow, squish proportionally.
  let scale = 1;
  if (libColMode === 'fit' && avail > 0 && total > avail) {
    scale = Math.max(0.15, (avail - LIB_SEL_W - LIB_LOAD_W) / dataSum);
  }
  cols.forEach((col, i) => { col.style.width = Math.max(20, Math.round(want[i] * scale)) + 'px'; });
  if (loadCol) loadCol.style.removeProperty('width');
  table.style.width = '100%';
  table.style.removeProperty('min-width');
}
// Attach drag-to-resize handles to each resizable column header.
function attachColResizers(root) {
  root.querySelectorAll('th[data-col-key]').forEach(th => {
    const key = th.getAttribute('data-col-key');
    if (key === 'art') return; // derived width, not user-resizable
    const handle = document.createElement('div');
    handle.className = 'col-resize';
    handle.title = 'Drag to resize';
    th.appendChild(handle);
    handle.addEventListener('click', e => e.stopPropagation()); // don't trigger sort
    handle.addEventListener('mousedown', e => {
      e.preventDefault(); e.stopPropagation();
      const startX = e.clientX, startW = colWidth(key);
      th.draggable = false; // suspend column-reorder drag while resizing
      handle.classList.add('dragging');
      document.body.style.cursor = 'col-resize';
      const onMove = ev => {
        let w = Math.max(COL_W_MIN, Math.min(COL_W_MAX, startW + (ev.clientX - startX)));
        // In fit mode, clamp so the table can't grow past the panel (the load
        // column absorbs the rest). In scroll mode, allow free growth → scroll.
        const table = root.querySelector('table.lib');
        const avail = table ? libWrapAvail(table) : 0;
        if (libColMode === 'fit' && avail > 0) {
          let others = 0;
          root.querySelectorAll('colgroup col[data-col-key]').forEach(col => {
            const k = col.getAttribute('data-col-key');
            if (k !== key) others += (k === 'art') ? artColWidth() : colWidth(k);
          });
          const maxThis = avail - LIB_SEL_W - LIB_LOAD_W - others;
          if (maxThis > COL_W_MIN) w = Math.min(w, maxThis);
        }
        libColWidths[key] = w;
        applyColWidthsLive(root);
      };
      const onUp = () => {
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
        handle.classList.remove('dragging');
        document.body.style.cursor = '';
        setTimeout(() => { th.draggable = true; }, 0);
        saveColWidths();
        if (key === 'waveform') scheduleWaveRefetch();
      };
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
  });
}

// autoFitColumns scales the visible resizable columns so they exactly fill the
// panel width (one-click). The art column (derived width), the sel checkbox and
// the load column are reserved; the rest are scaled from their current widths.
function autoFitColumns() {
  const table = document.querySelector('table.lib');
  if (!table) return;
  const avail = libWrapAvail(table);
  if (avail <= 0) return;
  const keys = [...table.querySelectorAll('colgroup col[data-col-key]')].map(c => c.getAttribute('data-col-key'));
  const hasArt = keys.includes('art');
  const dataKeys = keys.filter(k => k !== 'art');
  if (!dataKeys.length) return;
  const reserved = LIB_SEL_W + LIB_LOAD_W + (hasArt ? artColWidth() : 0);
  const target = Math.max(dataKeys.length * COL_W_MIN, avail - reserved);
  const cur = dataKeys.map(k => colWidth(k));
  const sum = cur.reduce((a, b) => a + b, 0) || 1;
  const scale = target / sum;
  dataKeys.forEach((k, i) => { libColWidths[k] = Math.max(COL_W_MIN, Math.round(cur[i] * scale)); });
  saveColWidths();
  applyColWidthsLive();
  scheduleWaveRefetch();
}

