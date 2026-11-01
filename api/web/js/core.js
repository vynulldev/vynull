// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── helpers ─────────
// fetchJSON fetches url and returns its JSON. A failure throws an Error saying
// why (the API's {"error": "..."}, an HTTP status, or the network's reason),
// with err.status the HTTP status, 0 when no answer came. Callers that would
// go on to write after a read must not mistake a failure for an empty answer.
async function fetchJSON(url, opts) {
  let r;
  try {
    // Most endpoints send no cache headers, so without no-store the browser
    // would answer a poll from its cache, and an edit would seem to revert a
    // second after it saved. The track list and cues revalidate (no-cache).
    r = await fetch(url, { cache: 'no-store', ...(opts || {}) });
  } catch (e) {
    throw Object.assign(new Error(e.message || 'network error'), { status: 0 });
  }
  if (!r.ok) throw Object.assign(new Error((await errorText(r)) || `HTTP ${r.status}`), { status: r.status });
  return await r.json();
}
// maybeJSON is fetchJSON answering null instead of throwing, for polls and
// loads that simply try again later.
function maybeJSON(url, opts) {
  return fetchJSON(url, opts).catch(() => null);
}
// errorText is why a failed request failed: the API's {"error": "..."}, or
// the body as it came from anything answering otherwise.
async function errorText(r) {
  const t = (await r.text().catch(() => '')).trim();
  try { return JSON.parse(t).error || t; } catch (e) { return t; }
}
function escape(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

// toast
let toastTimer;
function toast(msg, kind) {
  const el = document.getElementById('toast');
  el.textContent = msg;
  el.className = 'show ' + (kind || '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.classList.remove('show'); }, 2800);
}

// ───────── state ─────────
let allTracks = [];
let currentPlayers = [];
let settingsEnums = null;
let currentSettings = null;

// Waveform colour for the web UI. By default it follows the saved DEVSETTING
// "waveform_color" (blue/rgb/3band); the statusbar WAVE button can override it
// to any explicit mode (or back to AUTO) without touching the CDJ setting.
// null override == AUTO (mirror the setting).
let waveColorOverride = (() => {
  try { return localStorage.getItem('waveColorOverride') || null; } catch (e) { return null; }
})();
// Tracks the last-applied display signature (waveform colour + overview height
// + key notation) so the poll can repaint when any of those DEVSETTINGs change
// — e.g. the CDJ "waveform color" while in AUTO, or "overview"/"key display".
let lastDisplaySig = null;
let lastKeyDisplay = null; // only a key-notation change needs a library re-render
function settingWaveColor() {
  const c = currentSettings && currentSettings.dev_setting && currentSettings.dev_setting.waveform_color;
  return (c === 'blue' || c === 'rgb' || c === '3band') ? c : 'rgb';
}
function effectiveWaveColor() {
  return waveColorOverride || settingWaveColor();
}
// Detail-waveform fetch style: blue and rgb both render from the spectral PWV5
// data (recoloured client-side), so only 3band needs a different fetch.
function detailWaveStyle() {
  return effectiveWaveColor() === '3band' ? '3band' : 'spectral';
}
// Statusbar WAVE button: cycle AUTO → BLUE → RGB → 3BAND → AUTO.
const WAVE_COLOR_CYCLE = [null, 'blue', 'rgb', '3band'];
function setWaveColorOverride(mode) {
  waveColorOverride = mode; // null = AUTO (follow DEVSETTING)
  try { if (mode) localStorage.setItem('waveColorOverride', mode); else localStorage.removeItem('waveColorOverride'); } catch (e) {}
  renderWaveColorButton();
  redrawAllWaveforms();
}
function cycleWaveColor() {
  const i = WAVE_COLOR_CYCLE.indexOf(waveColorOverride);
  setWaveColorOverride(WAVE_COLOR_CYCLE[(i + 1) % WAVE_COLOR_CYCLE.length]);
}
function renderWaveColorButton() {
  const lbl = document.getElementById('wave-color-label');
  if (!lbl) return;
  const eff = effectiveWaveColor();
  lbl.textContent = 'WAVE: ' + (waveColorOverride ? waveColorOverride.toUpperCase() : 'AUTO·' + eff.toUpperCase());
  const led = document.querySelector('#wave-color-btn .led');
  if (led) led.className = 'led ' + (eff === 'blue' ? 'cyan' : eff === '3band' ? 'amber' : 'green');
  syncWebUISelects();
}

// Vocal-detection display: a 🎤 marker on phrases whose raw vocal score clears
// the current sensitivity threshold. The detector runs server-side; this is a
// pure client toggle (off / low / med / high) — phrase divs carry data-vscore,
// so cycling just re-applies the .has-vocal class without rebuilding strips.
const VOCAL_LEVELS = [
  { name: 'OFF',  thr: Infinity },
  { name: 'LOW',  thr: 0.15 },   // strict — only strong vocals
  { name: 'MED',  thr: 0.12 },   // default
  { name: 'HIGH', thr: 0.085 },  // lenient — catch more (more false positives)
];
let vocalLevel = (() => {
  try { const v = parseInt(localStorage.getItem('vocalLevel'), 10); return (v >= 0 && v <= 3) ? v : 2; }
  catch (e) { return 2; }
})();
function vocalThreshold() { return VOCAL_LEVELS[vocalLevel].thr; }
function phraseHasVocal(p) { return (p.vocal_score || 0) > vocalThreshold(); }
function reapplyVocalMarkers() {
  const thr = vocalThreshold();
  document.querySelectorAll('.phrase[data-vscore]').forEach(el => {
    el.classList.toggle('has-vocal', parseFloat(el.dataset.vscore) > thr);
  });
}
function cycleVocalDetect() {
  vocalLevel = (vocalLevel + 1) % VOCAL_LEVELS.length;
  try { localStorage.setItem('vocalLevel', vocalLevel); } catch (e) {}
  renderVocalButton();
  reapplyVocalMarkers();
}
function renderVocalButton() {
  const lbl = document.getElementById('vocal-det-label');
  if (lbl) lbl.textContent = 'VOCAL: ' + VOCAL_LEVELS[vocalLevel].name;
  const led = document.querySelector('#vocal-det-btn .led');
  if (led) led.className = 'led ' + (vocalLevel === 0 ? '' : 'green');
  syncWebUISelects();
}
// Repaint every waveform surface after a colour change: player-card canvases,
// the open detail drawer, and the library-row PNG thumbnails (re-rendered so
// their <img> src picks up the new &color=).
function redrawAllWaveforms() {
  document.querySelectorAll('canvas[data-track]').forEach(canvas => {
    const tid = parseInt(canvas.getAttribute('data-track'), 10);
    const cues = getCuesFor(tid);
    const p = (currentPlayers || []).find(pp => pp.track_id === tid);
    const durMs = (p && p.duration_ms) || 0;
    const draw = d => { drawWaveformPWV5(canvas, d, durMs); drawCueMarkersCanvas(canvas, cues, durMs); };
    const c = getDetailWaveCached(tid);
    if (c) draw(c); else whenDetailWave(tid, draw);
  });
  // Repoint the library-row thumbnail <img>s at the newly-keyed PNG in place
  // instead of rebuilding the whole table (which flashes every row). The KEY
  // column, which depends on key notation, is re-rendered separately by the
  // poll only when key_display actually changes.
  updateThumbnailSrcs();
  const shown = selectedTrackID;
  if (shown) {
    whenDetailWave(shown, d => {
      if (selectedTrackID === shown) { detailWave.wave = d; redrawDetailWaveform(); }
    });
  }
}
// updateThumbnailSrcs rewrites each library-row thumbnail src so a waveform
// colour/overview change re-fetches the newly-keyed PNG without a full table
// rebuild. Same URL when nothing changed → browser cache hit (no fetch).
