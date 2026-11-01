// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── TRACK DETAIL DRAWER ─────────
let selectedTrackID = null;

// Pioneer / rekordbox cue colour palette (color_id from API).
// Rekordbox hot-cue colour palette (62 entries, IDs 0x01..0x3e). NXS2+
// players pick from this palette when assigning a hot-cue colour; 0x00 =
// default. IDs ≥ 0x40 on CDJs are "hot-cue picker" entries stored as
// 0x30 + palette_index (e.g. user-set GREEN comes through as 0x42 → 0x12).
const CUE_COLORS = (() => {
  const p = {
    0x00: '#28e214', // unset — rekordbox renders a cue with no colour as green, not orange
    0x01: '#305aff', 0x02: '#5073ff', 0x03: '#508cff', 0x04: '#50a0ff',
    0x05: '#50b4ff', 0x06: '#50b0f2', 0x07: '#50aee8', 0x08: '#45acdb',
    0x09: '#00e0ff', 0x0a: '#19daf0', 0x0b: '#32d2e6', 0x0c: '#21b4b9',
    0x0d: '#20aaa0', 0x0e: '#1fa392', 0x0f: '#19a08c', 0x10: '#14a584',
    0x11: '#14aa7d', 0x12: '#10b176', 0x13: '#30d26e', 0x14: '#37de5a',
    0x15: '#3ceb50', 0x16: '#28e214', 0x17: '#7dc13d', 0x18: '#8cc832',
    0x19: '#9bd723', 0x1a: '#a5e116', 0x1b: '#a5dc0a', 0x1c: '#aad208',
    0x1d: '#b4c805', 0x1e: '#b4be04', 0x1f: '#bab404', 0x20: '#c3af04',
    0x21: '#e1aa00', 0x22: '#ffa000', 0x23: '#ff9600', 0x24: '#ff8c00',
    0x25: '#ff7500', 0x26: '#e0641b', 0x27: '#e0461e', 0x28: '#e0301e',
    0x29: '#e02823', 0x2a: '#e62828', 0x2b: '#ff376f', 0x2c: '#ff2d6f',
    0x2d: '#ff127b', 0x2e: '#f51e8c', 0x2f: '#eb2da0', 0x30: '#e637b4',
    0x31: '#de44cf', 0x32: '#de448d', 0x33: '#e630b4', 0x34: '#e619dc',
    0x35: '#e600ff', 0x36: '#dc00ff', 0x37: '#cc00ff', 0x38: '#b432ff',
    0x39: '#b93cff', 0x3a: '#c542ff', 0x3b: '#aa5aff', 0x3c: '#aa72ff',
    0x3d: '#8272ff', 0x3e: '#6473ff',
  };
  // TRACK_COLORS is the 8-entry rekordbox track/tag colour palette: the same
  // set the rekordbox app uses for "Track colour" and the CDJ track-row
  // accents, keyed by the colour IDs in api.colorNames (1=Pink … 8=Purple).
  // Kept separate from CUE_COLORS because that palette's IDs 1-8 are all
  // consecutive blue shades from the 62-entry hot-cue rainbow — which made
  // the track/tag pickers look monochrome. 0 = "no colour".
  window.TRACK_COLORS = {
    0: '',
    1: '#FF7FE8', // pink
    2: '#FF4D52', // red
    3: '#FF9853', // orange
    4: '#FFE25C', // yellow
    5: '#66E078', // green
    6: '#64DCEA', // aqua
    7: '#6391FF', // blue
    8: '#BB6FFF', // purple
  };
  // Proxy-style accessor: handle the NXS2 hot-cue picker which stores its
  // colours as 0x30 + palette_index (so user-picked GREEN comes through as
  // 0x42 = 0x30 + 0x12). Unknown IDs fall back to the default orange.
  return new Proxy(p, {
    get(target, key) {
      if (key in target) return target[key];
      const n = Number(key);
      if (!Number.isFinite(n)) return target[0x00];
      if (n > 0x3e && (n - 0x30) in target) return target[n - 0x30];
      return target[0x00];
    },
  });
})();

function formatTime(ms) {
  if (typeof ms !== 'number') return '—';
  const totalCs = Math.round(ms / 10);
  const cs = totalCs % 100;
  const totalSec = Math.floor(totalCs / 100);
  const sec = totalSec % 60;
  const min = Math.floor(totalSec / 60);
  return `${min}:${String(sec).padStart(2,'0')}.${String(cs).padStart(2,'0')}`;
}

function formatBytes(n) {
  if (!n) return '—';
  const u = ['B','KB','MB','GB'];
  let i = 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return n.toFixed(i === 0 ? 0 : 1) + ' ' + u[i];
}

// All-tags catalog, lazy-loaded on first drawer open and refreshed after
// any catalog change (e.g. creating a new tag via the picker).
let allTagsCache = null;
async function ensureAllTags(forceRefresh) {
  if (allTagsCache && !forceRefresh) return allTagsCache;
  const r = await fetchJSON('/api/tags');
  if (Array.isArray(r)) allTagsCache = r;
  return allTagsCache || [];
}
// Tag objects (with IDs) currently assigned to the open drawer's track.
// We need IDs (not just names) to mutate via POST /api/tracks/{id}/tags.
let drawerTrackTags = [];

// loadRelated fills the drawer's RELATED section with tracks that mix well
// with this one (GET /api/tracks/{id}/related), best first, each with why it
// was suggested. Clicking a row opens that track. Ignored if the drawer has
// moved on to another track by the time the request returns.
async function loadRelated(trackID) {
  let list = [];
  try {
    const r = await fetch(`/api/tracks/${trackID}/related?limit=10`);
    if (r.ok) list = await r.json();
  } catch (e) { /* leave the section empty */ }
  if (selectedTrackID !== trackID) return;
  const el = document.getElementById('related-list');
  const count = document.getElementById('related-count');
  if (!el) return;
  if (count) count.textContent = list.length;
  if (!list.length) {
    el.innerHTML = '<div class="related-empty">No related tracks in the library yet.</div>';
    return;
  }
  el.innerHTML = list.map(m => {
    const t = m.track;
    const bpm = t.bpm ? t.bpm.toFixed(1) : '—';
    return `<button type="button" class="related-row" data-id="${t.id}" title="${escape(m.reasons.join(' · '))}">
      ${artHTML(t, 'related-art', 'related-art empty', '♪')}
      <span class="related-title">${escape(t.title || '—')}<span class="related-artist">${escape(t.artist || '')}</span></span>
      <span class="related-meta">${escape(t.key || '—')} · ${bpm}</span>
      <span class="related-why">${escape(m.reasons.join(' · '))}</span>
    </button>`;
  }).join('');
  el.querySelectorAll('.related-row').forEach(b => {
    b.onclick = () => openDetail(Number(b.dataset.id));
  });
}

async function openDetail(trackID) {
  selectedTrackID = trackID;
  document.querySelectorAll('tr[data-track-row]').forEach(r => {
    r.classList.toggle('selected', parseInt(r.getAttribute('data-track-row'), 10) === trackID);
  });
  document.getElementById('detail-id').textContent = ' · ' + String(trackID).padStart(4, '0');
  document.getElementById('detail-body').innerHTML =
    '<div class="detail-empty">LOADING TRACK…</div>';
  document.getElementById('detail-drawer').classList.add('open');
  document.getElementById('detail-drawer').setAttribute('aria-hidden', 'false');
  document.getElementById('detail-backdrop').classList.add('open');
  document.body.classList.add('drawer-open');

  // Fetch everything in parallel.
  const waveStyle = detailWaveStyle();
  const [track, cues, trackTags, _allTags, loadedWave, phrases, analysis] = await Promise.all([
    fetchJSON('/api/tracks/' + trackID),
    fetchJSON('/api/tracks/' + trackID + '/cues'),
    fetchJSON('/api/tracks/' + trackID + '/tags'),
    ensureAllTags(),
    getDetailWaveCached(trackID) || ensureDetailWave(trackID),
    fetchJSON('/api/tracks/' + trackID + '/phrases'),
    fetchJSON('/api/analysis/' + trackID),
  ]);
  if (selectedTrackID !== trackID) return; // user closed / switched while loading
  // If the colour mode changed while loading, drop the old mode's wave:
  // renderDetail then fetches the current one.
  const wave = detailWaveStyle() === waveStyle ? loadedWave : null;
  drawerTrackTags = Array.isArray(trackTags) ? trackTags : [];
  renderDetail(track, cues || [], wave, phrases || [], analysis || null);
}

function closeDetail() {
  detailPlayerStop();
  selectedTrackID = null;
  document.querySelectorAll('tr[data-track-row].selected').forEach(r => r.classList.remove('selected'));
  document.getElementById('detail-drawer').classList.remove('open');
  document.getElementById('detail-drawer').setAttribute('aria-hidden', 'true');
  document.getElementById('detail-backdrop').classList.remove('open');
  document.body.classList.remove('drawer-open');
}
document.getElementById('detail-close').addEventListener('click', closeDetail);
document.addEventListener('keydown', e => {
  if (e.key === 'Escape' && selectedTrackID != null) closeDetail();
});

// LINK / UNLINK toggle — flips the device's outbound-advertising gate.
// Unlinked, the virtual device stops emitting keep-alives + status
// broadcasts, so CDJs time us out and we vanish from their LINK menu
// (matches rekordbox UX). Server boots unlinked by default.
let linkedState = null;
function applyLinkUI(linked) {
  linkedState = linked;
  const btn = document.getElementById('unlink-btn');
  const led = document.getElementById('link-led');
  if (linked) {
    btn.textContent = 'UNLINK';
    btn.title = 'Currently linked — click to disconnect (CDJs vanish from this device, dbserver sessions are dropped)';
    btn.classList.remove('is-unlinked');
    led.className = 'led green pulse';
  } else {
    btn.textContent = 'LINK';
    btn.title = 'Currently unlinked — click to advertise this device so CDJs see it in their LINK menu';
    btn.classList.add('is-unlinked');
    led.className = 'led';
  }
}
async function fetchLinkState() {
  try {
    const r = await fetch('/api/link', { cache: 'no-store' });
    if (r.ok) applyLinkUI((await r.json()).linked);
  } catch (e) { /* leave UI as-is */ }
}
fetchLinkState();
setInterval(fetchLinkState, 5000); // catch out-of-band toggles

document.getElementById('unlink-btn').addEventListener('click', async () => {
  const btn = document.getElementById('unlink-btn');
  const want = !linkedState;
  btn.disabled = true;
  try {
    const r = await fetch('/api/link', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ linked: want }),
    });
    if (!r.ok) { toast('LINK TOGGLE FAILED', 'error'); return; }
    const data = await r.json();
    applyLinkUI(data.linked);
    toast(data.linked ? 'LINKED · ADVERTISING' : 'UNLINKED · CDJS WILL TIME OUT', 'success');
  } catch (e) {
    toast('LINK TOGGLE FAILED', 'error');
  } finally {
    setTimeout(() => { btn.disabled = false; }, 800);
  }
});

// buildByline renders the artist · album · year · genre row under the detail
// title. Each part appears only when its field is set. Extracted so the head
// can be re-rendered in place after an inline metadata edit (see
// refreshDetailHead).
function buildByline(t) {
  // Inline SVG icons next to each field. Single colour via currentColor so
  // they pick up the byline's grey and any hover style.
  const META_ICONS = {
    artist: '<svg class="meta-icon" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><circle cx="8" cy="5" r="2.5"/><path d="M8 9c-3 0-5 1.8-5 4v1h10v-1c0-2.2-2-4-5-4z"/></svg>',
    album:  '<svg class="meta-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true"><circle cx="8" cy="8" r="5.8"/><circle cx="8" cy="8" r="1.2" fill="currentColor" stroke="none"/></svg>',
    year:   '<svg class="meta-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" aria-hidden="true"><rect x="2.5" y="4" width="11" height="9.5"/><line x1="2.5" y1="7" x2="13.5" y2="7"/><line x1="6" y1="2.5" x2="6" y2="4.5"/><line x1="10" y1="2.5" x2="10" y2="4.5"/></svg>',
    genre:  '<svg class="meta-icon" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><path d="M12 2.2L6.5 3.5v7.7a2.1 2.1 0 1 0 1.3 1.95V5.5L12 4.5v5.5a2.1 2.1 0 1 0 1.3 1.95z"/></svg>',
  };
  const parts = [];
  if (t.artist) parts.push(`<span class="meta-cell" title="Artist">${META_ICONS.artist}<span class="meta-val">${escape(t.artist)}</span></span>`);
  if (t.album)  parts.push(`<span class="meta-cell" title="Album">${META_ICONS.album}<span class="meta-val">${escape(t.album)}</span></span>`);
  if (t.year)   parts.push(`<span class="meta-cell" title="Year">${META_ICONS.year}<span class="meta-val">${t.year}</span></span>`);
  if (t.genre)  parts.push(`<span class="meta-cell" title="Genre">${META_ICONS.genre}<span class="meta-val">${escape(t.genre)}</span></span>`);
  return parts.join('');
}

// refreshDetailHead updates the drawer's title + byline in place after an
// inline edit to one of the identity fields (title/artist/album/genre/year),
// so the header reflects the change without a full drawer reload.
function refreshDetailHead(t) {
  const titleText = document.getElementById('detail-title-text');
  if (titleText) titleText.textContent = t.title || 'Untitled';
  const byEl = document.querySelector('#detail-body .detail-byline');
  if (byEl) byEl.innerHTML = buildByline(t) || '<span style="color:var(--text3)">—</span>';
}

// The detail drawer's LOAD buttons (one per connected player). Reused by
// renderDetail (initial render) and refreshDetailLoadButtons (live update when a
// device connects/disconnects).
function buildLoadActions() {
  return currentPlayers.length === 0
    ? '<span class="none">— NO PLAYERS CONNECTED —</span>'
    : currentPlayers.map(p =>
        `<button class="load-btn" data-load-dev="${p.device_number}">
          <span class="pnum">${String(p.device_number).padStart(2, '0')}</span>▶ LOAD
        </button>`).join('');
}
// Re-render the open drawer's LOAD buttons in place (and re-wire them) so they
// track the current players. No-op when the drawer is closed.
function refreshDetailLoadButtons() {
  const drawer = document.getElementById('detail-drawer');
  const host = document.querySelector('#detail-body .detail-actions');
  if (!host || !selectedTrackID || !drawer || !drawer.classList.contains('open')) return;
  host.innerHTML = buildLoadActions();
  host.querySelectorAll('[data-load-dev]').forEach(btn => {
    btn.addEventListener('click', () =>
      loadTrack(selectedTrackID, parseInt(btn.getAttribute('data-load-dev'), 10)));
  });
}

function renderDetail(t, cues, wave, phrases, analysis) {
  if (!t) {
    document.getElementById('detail-body').innerHTML =
      '<div class="detail-empty">FAILED TO LOAD TRACK</div>';
    return;
  }
  // Always render a color chip; clicking opens the swatch picker.
  // color_id == 0 renders as the hatched "none" chip.
  const colorBg = t.color_id > 0 ? `background:${TRACK_COLORS[t.color_id]}` : '';
  const colorChip = `<span class="color-chip ${t.color_id > 0 ? '' : 'empty'}" id="track-color-chip" title="Click to set color" style="${colorBg}"></span>`;
  const byline = buildByline(t);

  const loadActions = buildLoadActions();

  const durStr = t.duration ? `${Math.floor(t.duration/60)}:${String(t.duration%60).padStart(2,'0')}` : '—';
  const metaRows = [
    ['TITLE', t.title || '', 'title'],
    ['ARTIST', t.artist || '', 'artist'],
    ['ALBUM', t.album || '', 'album'],
    ['GENRE', t.genre || '', 'genre'],
    ['YEAR', t.year ? String(t.year) : '', 'year', 'number'],
    ['BPM', (t.bpm || 0).toFixed(2)],
    ['KEY', fmtKey(t.key) || '', 'key'],
    ['DURATION', durStr],
    ['BITRATE', t.bitrate ? t.bitrate + ' kbps' : '—'],
    ['TYPE', (t.file_type || '—').toUpperCase()],
    ['SIZE', formatBytes(t.file_size)],
    ['TRACK', t.track_num || '—'],
    ['RATING', fmtStars(t.rating, t.id)],
    ['COLOR', t.color_name || '—'],
    ['PLAY COUNT', t.play_count || 0],
    ['ADDED', t.date_added || '—'],
    ['CREATED', t.date_created || '—'],
    ['RELEASED', t.release_date || '—'],
    ['LABEL', t.label || '', 'label'],
    ['REMIXER', t.remixer || '', 'remixer'],
    ['ORIG. ARTIST', t.original_artist || '', 'original_artist'],
    ['MIX NAME', t.mix_name || '', 'mix_name'],
    ['COMMENT', t.comment || '', 'comment'],
    ['PATH', t.file_path || '', 'path'],
  ];

  // Sort by cue letter (number 1=A, 2=B, ...) so the list always shows A, B,
  // C, D in order. CDJs save cues in whatever order the user creates them,
  // so the raw array can be e.g. B, C, A, D. Copy first — getCuesFor returns
  // the cached array, and we don't want to mutate cuesCache here.
  const sortedCues = cues ? cues.slice().sort((a, b) => a.number - b.number) : cues;
  const cueList = sortedCues && sortedCues.length > 0
    ? sortedCues.map(c => {
        const color = CUE_COLORS[c.color_id] || CUE_COLORS[0];
        const isLoop = c.type === 'loop';
        return `<div class="cue-row" data-cue-number="${c.number}">
          <span class="num">${cueLabel(c.number)}</span>
          <button type="button" class="dot-color" data-act="pick-cue-color" title="Change cue color" style="background:${color};color:${color}"></button>
          <span><span class="time">${formatTime(c.time_ms)}</span>${c.label ? ' · <span class="label">' + escape(c.label) + '</span>' : ''}</span>
          ${isLoop ? '<span class="loop-tag">LOOP</span>' : ''}
          <button type="button" class="cue-delete" data-act="delete-cue" title="Delete this cue">×</button>
        </div>`;
      }).join('')
    : '<div class="detail-empty">— NO CUE POINTS —</div>';

  // Tags section is re-rendered into <div id="tags-host"> after mutations.
  const tagsHTML = '<div id="tags-host"></div>';

  const detailArt = artHTML(t, 'detail-art', 'detail-art empty', '');
  document.getElementById('detail-body').innerHTML = `
    <div class="detail-track-head">
      ${detailArt}
      <div class="left">
        <div class="detail-title">${colorChip}<span id="detail-title-text">${escape(t.title || 'Untitled')}</span>${t.file_missing ? '<span class="missing-badge" title="File not found at this path — playback and analysis will fail until the file is restored or the path remapped">⚠ FILE MISSING</span>' : ''}</div>
        <div class="detail-byline">${byline || '<span style="color:var(--text3)">—</span>'}</div>
      </div>
      <div class="right">
        <div class="detail-actions">${loadActions}</div>
      </div>
    </div>

    <div class="detail-wave">
      <canvas id="detail-overview" class="overview"></canvas>
      <div id="detail-phrases"></div>
      <div class="zoom-controls player-controls" style="justify-content:flex-start">
        <button type="button" id="pl-play" title="Play / pause from the cursor (Space)">▶ PLAY</button>
        <button type="button" id="pl-metro" title="Click on every beat of the analysed grid (M)">METRO</button>
        <button type="button" id="pl-wav" title="Play the ffmpeg-decoded WAV, the same decoder the analysis used, to rule out browser decoder offset">WAV</button>
        <span id="pl-pads" class="pl-pads" title="Click or press 1–8 to jump to a cue · [ ] previous/next cue"></span>
        <span style="flex:1"></span>
        <span id="pl-info" class="pl-info"></span>
      </div>
      <div class="zoom-controls">
        <button type="button" id="add-cue-at-cursor" title="Add a hot cue at the cursor position">+ CUE</button>
        <button type="button" id="add-memcue-at-cursor" title="Add a memory cue at the cursor position">+ MEM</button>
        <button type="button" id="set-downbeat-at-cursor" title="Mark the nearest beat to the cursor as the downbeat (beat 1 of the bar)">↓ DBEAT</button>
        <button type="button" id="jump-downbeat" title="Jump the view to the first downbeat (1.1)">▸ 1.1</button>
        <span style="flex:1"></span>
        <button type="button" id="zoom-out" title="Zoom out (show more time)">−</button>
        <span class="zoom-label" id="zoom-label">16s</span>
        <button type="button" id="zoom-in"  title="Zoom in (show less time)">+</button>
      </div>
      <div class="beat-jump">
        <button type="button" data-jump="-32" title="Jump back 32 beats">◀32</button>
        <button type="button" data-jump="-16" title="Jump back 16 beats">◀16</button>
        <button type="button" data-jump="-8"  title="Jump back 8 beats">◀8</button>
        <button type="button" data-jump="-4"  title="Jump back 4 beats">◀4</button>
        <button type="button" data-jump="-2"  title="Jump back 2 beats">◀2</button>
        <button type="button" data-jump="-1"  title="Jump back 1 beat">◀1</button>
        <span class="bj-sep">BEATS</span>
        <button type="button" data-jump="1"   title="Jump forward 1 beat">1▶</button>
        <button type="button" data-jump="2"   title="Jump forward 2 beats">2▶</button>
        <button type="button" data-jump="4"   title="Jump forward 4 beats">4▶</button>
        <button type="button" data-jump="8"   title="Jump forward 8 beats">8▶</button>
        <button type="button" data-jump="16"  title="Jump forward 16 beats">16▶</button>
        <button type="button" data-jump="32"  title="Jump forward 32 beats">32▶</button>
      </div>
      <canvas id="detail-zoom" class="zoom"></canvas>
      <div id="detail-phrases-zoom"></div>
      <div class="timeline">
        <span>0:00</span>
        <span>${durStr}</span>
      </div>
    </div>

    <div class="detail-sections">
      <div class="detail-col-meta">
        <div class="detail-section">
          <h4>METADATA</h4>
          <div class="meta-table">
            ${metaRows.map(([k,v,field,inputType]) => {
              if (field === 'path') {
                // Editable file path — repoints the track (library only, never
                // moves the file); red while the file is missing.
                const miss = t.file_missing ? ' missing' : '';
                return `<div class="k">${k}</div><div class="v"><input class="meta-edit path-edit${miss}" data-field="path" value="${escape(v)}" placeholder="—" title="Update the file path (saved to the library, never moves the file)"></div>`;
              }
              if (field) {
                // Editable text field — saves to the library DB only, never the file.
                const type = inputType === 'number' ? ' type="number" min="0" step="1" inputmode="numeric"' : '';
                return `<div class="k">${k}</div><div class="v"><input class="meta-edit" data-field="${field}"${type} value="${escape(v)}" placeholder="—" title="Edit ${k} (saved to the library, not the audio file)"></div>`;
              }
              const cls = (v === '—' ? 'v dim' : 'v');
              return `<div class="k">${k}</div><div class="${cls}">${v}</div>`;
            }).join('')}
          </div>
        </div>
      </div>

      <div class="detail-col-rest">
        <div class="detail-section" id="beats-section">
          <h4>BEAT GRID</h4>
          ${renderBeatGridEditor(t)}
        </div>

        <div class="detail-section">
          <h4>CUES <span class="count">${cues ? cues.length : 0}</span></h4>
          <div class="cue-list">${cueList}</div>
        </div>

        <div class="detail-section" id="tags-section">
          <h4>TAGS <span class="count" id="tags-count">${drawerTrackTags.length}</span></h4>
          ${tagsHTML}
        </div>

        <div class="detail-section" id="related-section">
          <h4>RELATED <span class="count" id="related-count"></span></h4>
          <div class="related-list" id="related-list"><div class="related-empty">Loading…</div></div>
        </div>
      </div>
    </div>
  `;
  loadRelated(t.id);

  // Seed the phrase cache BEFORE setDetailWaveform: redrawDetailWaveform()
  // renders the zoom phrase strip from detailPhrasesCache, and setDetailWaveform
  // triggers that redraw — so without this the zoom strip would paint the
  // previous track's phrases (or none on first open) until renderPhraseStrip
  // ran further below. (The overview strip is rendered directly, hence it was
  // already correct on first open.)
  detailPhrasesCache = Array.isArray(phrases) ? phrases : [];
  detailPlayerBind(t);

  // Draw the rekordbox-style overview + zoom waveforms now that the
  // canvases are in the DOM. setDetailWaveform populates the shared
  // detailWave state; redrawDetailWaveform repaints both panes on
  // every change (zoom, scroll, cue color edit).
  const beats = (analysis && Array.isArray(analysis.beats)) ? analysis.beats : null;
  const ov = document.getElementById('detail-overview');
  if (ov) {
    if (wave) setDetailWaveform(t, wave, cues, beats);
    else whenDetailWave(t.id, d => {
      // Drawer may have been closed/switched while the wave fetch was
      // in flight — only apply if we're still showing this track.
      if (selectedTrackID === t.id) setDetailWaveform(t, d, cues, beats);
    });
    // Click or drag anywhere on the overview to scrub the zoom pane.
    // Window-level move + up are bound only for the duration of the
    // drag so reopening the drawer doesn't leak listeners.
    const scrubTo = clientX => {
      const rect = ov.getBoundingClientRect();
      const frac = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      detailZoomCenterTo(frac * detailWave.durationMs);
    };
    ov.addEventListener('mousedown', e => {
      e.preventDefault();
      scrubTo(e.clientX);
      const onMove = ev => scrubTo(ev.clientX);
      const onEnd = () => {
        window.removeEventListener('mousemove', onMove);
        window.removeEventListener('mouseup', onEnd);
      };
      window.addEventListener('mousemove', onMove);
      window.addEventListener('mouseup', onEnd);
    });

    // Zoom canvas: three interactions, dispatched at mousedown by
    // hit-testing the click against any cue marker:
    //   - shift+click empty space → add a new hot cue at that time
    //   - drag a cue marker        → move that cue (POSTed on mouseup)
    //   - drag empty space         → pan the zoom window ("grab paper")
    const zm = document.getElementById('detail-zoom');
    if (zm) {
      zm.addEventListener('mousedown', e => {
        e.preventDefault();
        const rect     = zm.getBoundingClientRect();
        const windowMs = ZOOM_WINDOW_MS_LEVELS[detailWave.zoomIdx];
        const msPerPx  = windowMs / rect.width;
        const clickMs  = detailZoomBounds().start + (e.clientX - rect.left) * msPerPx;

        // ctrl/cmd+click → mark nearest beat to clickMs as the downbeat.
        if (e.ctrlKey || e.metaKey) {
          setDetailDownbeat(clickMs);
          return;
        }

        // alt+click on a cue marker → delete it (no drag).
        const hitForAlt = findDetailCueNear(clickMs, 6 * msPerPx);
        if (e.altKey && hitForAlt) {
          deleteDetailCue(hitForAlt.number);
          return;
        }

        // shift+click on empty space → add a new hot cue at clickMs.
        if (e.shiftKey && !findDetailCueNear(clickMs, 6 * msPerPx)) {
          const num = nextFreeHotCueNumber();
          if (num == null) { toast('ALL HOT CUE SLOTS USED', 'error'); return; }
          const cue = { number: num, type: 'cue', time_ms: Math.round(clickMs), loop_ms: -1, color_id: defaultCueColorFor(num), label: '' };
          saveDetailCue(cue);
          return;
        }

        // Hit-test cue markers (±6px). If we hit one, drag it.
        const hit = findDetailCueNear(clickMs, 6 * msPerPx);
        if (hit) {
          const startMs = hit.time_ms;
          zm.style.cursor = 'ew-resize';
          const onMove = ev => {
            const dx = ev.clientX - e.clientX;
            const newMs = Math.max(0, Math.min(detailWave.durationMs, startMs + dx * msPerPx));
            hit.time_ms = Math.round(newMs);
            redrawDetailWaveform();
          };
          const onEnd = () => {
            window.removeEventListener('mousemove', onMove);
            window.removeEventListener('mouseup', onEnd);
            zm.style.cursor = 'grab';
            if (hit.time_ms !== startMs) saveDetailCue(hit);
          };
          window.addEventListener('mousemove', onMove);
          window.addEventListener('mouseup', onEnd);
          return;
        }

        // No cue under cursor → pan the window.
        const startCenterMs = detailWave.centerMs;
        const onMove = ev => {
          const dx = ev.clientX - e.clientX;
          detailZoomCenterTo(startCenterMs - dx * msPerPx);
        };
        const onEnd = () => {
          window.removeEventListener('mousemove', onMove);
          window.removeEventListener('mouseup', onEnd);
          zm.style.cursor = 'grab';
        };
        zm.style.cursor = 'grabbing';
        window.addEventListener('mousemove', onMove);
        window.addEventListener('mouseup', onEnd);
      });
      zm.style.cursor = 'grab';
      zm.title = 'Drag to pan · drag a cue marker to move it · shift+click to add a hot cue · alt+click a cue to delete it · ctrl/⌘+click to set the downbeat';
    }

    document.getElementById('zoom-in').addEventListener('click',  () => detailZoomStep(-1));
    document.getElementById('zoom-out').addEventListener('click', () => detailZoomStep(+1));
    document.querySelectorAll('.beat-jump button[data-jump]').forEach(btn => {
      btn.addEventListener('click', () => detailJumpBeats(parseInt(btn.getAttribute('data-jump'), 10)));
    });
    document.getElementById('add-cue-at-cursor').addEventListener('click', () => {
      const num = nextFreeHotCueNumber();
      if (num == null) { toast('ALL HOT CUE SLOTS USED', 'error'); return; }
      saveDetailCue({
        number:   num,
        type:     'cue',
        time_ms:  Math.round(Math.max(0, Math.min(detailWave.durationMs, detailWave.centerMs))),
        loop_ms:  -1,
        color_id: defaultCueColorFor(num),
        label:    '',
      });
    });
    document.getElementById('add-memcue-at-cursor').addEventListener('click', () => {
      saveDetailCue({
        number:   nextFreeMemoryCueNumber(),
        type:     'cue',
        time_ms:  Math.round(Math.max(0, Math.min(detailWave.durationMs, detailWave.centerMs))),
        loop_ms:  -1,
        color_id: 0, // default colour — the deck paints memory cues its standard green
        label:    '',
      });
    });
    document.getElementById('set-downbeat-at-cursor').addEventListener('click', () => {
      setDetailDownbeat(detailWave.centerMs);
    });
    document.getElementById('jump-downbeat').addEventListener('click', () => {
      const db = (detailWave.beats || []).find(b => b.beat_in_bar === 1);
      if (db) detailZoomCenterTo(db.time_ms);
      else toast('NO BEAT GRID', 'error');
    });
  }

  // Phrase strip under the waveform — sections like INTRO / UP / CHORUS / OUTRO.
  renderPhraseStrip(phrases, t.duration);

  // Wire load buttons inside the drawer.
  document.querySelectorAll('[data-load-dev]').forEach(btn => {
    btn.addEventListener('click', () => {
      const dev = parseInt(btn.getAttribute('data-load-dev'), 10);
      loadTrack(t.id, dev);
    });
  });

  // Editable metadata fields. Commit on blur or Enter; PUT updates the library
  // DB only (never the audio file). Esc reverts to the last saved value.
  document.querySelectorAll('#detail-body .meta-edit').forEach(inp => {
    let saved = inp.value;
    const commit = async () => {
      const val = inp.value.trim();
      if (val === saved) return;
      const field = inp.getAttribute('data-field');
      // The file path has its own endpoint (rekeys analysis, rechecks presence)
      // and changes the missing/size indicators, so refresh the drawer after.
      if (field === 'path') {
        if (val === '') { inp.value = saved; return; }
        const r = await fetch('/api/tracks/' + t.id + '/path', {
          method: 'PUT', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ path: val }),
        });
        if (!r.ok) {
          const why = await errorText(r);
          toast('PATH SAVE FAILED' + (why ? ': ' + why.toUpperCase() : ''), 'error');
          inp.value = saved;
          return;
        }
        saved = val;
        toast('PATH UPDATED', 'success');
        openDetail(t.id); // re-render so the FILE MISSING badge / size / type update
        return;
      }
      // Key is normalized server-side (any notation → canonical Camelot), so
      // adopt the returned value and re-display it in the user's notation
      // rather than the raw typed text.
      if (field === 'key') {
        const r = await fetch('/api/tracks/' + t.id + '/metadata', {
          method: 'PUT', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ key: val }),
        });
        if (!r.ok) { toast('KEY SAVE FAILED — use Camelot (8A) or classic (Am)', 'error'); inp.value = saved; return; }
        const updated = await r.json();
        t.key = updated.key || '';
        saved = fmtKey(t.key);
        inp.value = saved;
        toast('KEY SAVED', 'success');
        return;
      }
      const r = await fetch('/api/tracks/' + t.id + '/metadata', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ [field]: val }),
      });
      if (!r.ok) { toast('SAVE FAILED', 'error'); inp.value = saved; return; }
      saved = val;
      inp.value = val;
      // Keep the open drawer's track object in sync (year is numeric).
      t[field] = field === 'year' ? (parseInt(val, 10) || 0) : val;
      // Identity fields show in the title/byline above — refresh it in place.
      if (field === 'title' || field === 'artist' || field === 'album' || field === 'genre' || field === 'year') {
        refreshDetailHead(t);
      }
      toast(field.replace(/_/g, ' ').toUpperCase() + ' SAVED', 'success');
    };
    inp.addEventListener('blur', commit);
    inp.addEventListener('keydown', e => {
      if (e.key === 'Enter') { e.preventDefault(); inp.blur(); }
      else if (e.key === 'Escape') { inp.value = saved; inp.blur(); }
    });
  });

  renderTagsSection();

  // Wire the track-color chip → swatch picker popover.
  const chip = document.getElementById('track-color-chip');
  if (chip) chip.addEventListener('click', (e) => {
    e.stopPropagation();
    openColorPicker(chip, t.id, t.color_id || 0);
  });
  // Wire each cue dot → cue color picker.
  document.querySelectorAll('#detail-body .cue-row [data-act="pick-cue-color"]').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const row = btn.closest('.cue-row');
      const cueNumber = parseInt(row.getAttribute('data-cue-number'), 10);
      const cur = getCuesFor(t.id).find(c => c.number === cueNumber);
      openCueColorPicker(btn, t.id, cueNumber, (cur && cur.color_id) || 0);
    });
  });
  // Wire each cue × button → delete this cue.
  document.querySelectorAll('#detail-body .cue-row [data-act="delete-cue"]').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const row = btn.closest('.cue-row');
      const cueNumber = parseInt(row.getAttribute('data-cue-number'), 10);
      deleteDetailCue(cueNumber);
    });
  });
  // Click a cue row (outside its colour/delete buttons, which stopPropagation)
  // → jump the zoom waveform to that cue and scroll it into view.
  document.querySelectorAll('#detail-body .cue-row').forEach(row => {
    row.title = 'Jump to this cue on the waveform';
    row.addEventListener('click', () => {
      const cueNumber = parseInt(row.getAttribute('data-cue-number'), 10);
      const cue = getCuesFor(t.id).find(c => c.number === cueNumber);
      if (!cue) return;
      detailZoomCenterTo(cue.time_ms);
      const wave = document.querySelector('#detail-body .detail-wave');
      if (wave) wave.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    });
  });
  // Star clicks (RATING row in METADATA) → setTrackRating.
  wireStarClicks(document.getElementById('detail-body'));
  // Beat grid editor — BPM input, ×2/÷2 quick buttons, phase-shift
  // chips, clear-override button.
  wireBeatGridEditor(t.id);
}

// Renders the coloured phrase strip under the waveform. `phrases` is
// the array returned by /api/tracks/{id}/phrases; durationSec is the
// track length used to scale widths. Segments narrower than ~42px hide
// their label so it doesn't overflow.
// Renders the full-track phrase strip under the overview canvas.
// Cached so the per-redraw zoom strip can reuse them without re-fetching.
let detailPhrasesCache = [];
function renderPhraseStrip(phrases, durationSec) {
  detailPhrasesCache = phrases || [];
  const host = document.getElementById('detail-phrases');
  if (!host) return;
  if (!phrases || phrases.length === 0) {
    host.className = 'detail-phrases-empty';
    host.innerHTML = '— NO PHRASES DETECTED —';
    return;
  }
  // Absolutely position each segment at its real start time so the strip lines
  // up with the overview waveform (zoom-aligned gives the container relative
  // positioning); flex tiling would drop the lead-in and shift everything left.
  host.className = 'detail-phrases zoom-aligned';
  const totalMs = durationSec > 0 ? durationSec * 1000 : (phrases[phrases.length - 1].end_ms || 1);
  host.innerHTML = phraseAbsSegmentsHTML(phrases, totalMs);
}

// Player-card phrase strips: phrases keyed by track ID so the 1Hz player poll
// (which rebuilds the cards each tick) reuses them instead of re-fetching.
const playerPhrasesCache = new Map();
function renderPlayerPhraseStrip(host, phrases, durMs) {
  if (!host) return;
  if (!phrases || phrases.length === 0) {
    host.className = 'player-phrases empty';
    host.innerHTML = '';
    return;
  }
  host.className = 'player-phrases detail-phrases zoom-aligned';
  const totalMs = durMs > 0 ? durMs : (phrases[phrases.length - 1].end_ms || 1);
  host.innerHTML = phraseAbsSegmentsHTML(phrases, totalMs);
}

// phraseAbsSegmentsHTML builds absolutely-positioned phrase segments for a
// full-track strip — each at left = start_ms/totalMs so the strip lines up with
// the waveform (which maps time→x the same way). Flex tiling would drop the
// lead-in before the first beat and shift everything left. Shared by the detail
// overview strip and the player-card strips; the container needs the
// zoom-aligned class (display:block; position:relative).
function phraseAbsSegmentsHTML(phrases, totalMs) {
  return phrases.map(p => {
    const leftPct = (p.start_ms / totalMs) * 100;
    const widthPct = (Math.max(0, p.end_ms - p.start_ms) / totalMs) * 100;
    const voc = phraseHasVocal(p) ? ' has-vocal' : '';
    const title = `${p.name} · vocal ${(p.vocal_score || 0).toFixed(2)} · ${fmtPhraseTime(p.start_ms)} – ${fmtPhraseTime(p.end_ms)}`;
    const narrow = widthPct < 4.5 ? 'too-narrow' : '';
    return `<div class="phrase kind-${p.kind}${voc} ${narrow}" data-vscore="${(p.vocal_score || 0).toFixed(4)}" style="position:absolute; left:${leftPct.toFixed(3)}%; width:${widthPct.toFixed(3)}%" title="${escape(title)}">${escape(p.name)}</div>`;
  }).join('');
}

// Renders a windowed phrase strip aligned to the zoom canvas — only
// phrases overlapping [startMs..endMs] show, positioned absolutely so
// the segments line up with the same time on the waveform above.
function renderPhraseStripZoom(phrases, startMs, endMs) {
  const host = document.getElementById('detail-phrases-zoom');
  if (!host) return;
  if (!phrases || phrases.length === 0) {
    host.className = '';
    host.innerHTML = '';
    return;
  }
  const windowMs = endMs - startMs;
  if (windowMs <= 0) { host.innerHTML = ''; return; }
  host.className = 'detail-phrases zoom-aligned';
  const visible = phrases.filter(p => p.end_ms > startMs && p.start_ms < endMs);
  host.innerHTML = visible.map(p => {
    const segStart = Math.max(p.start_ms, startMs);
    const segEnd   = Math.min(p.end_ms,   endMs);
    const leftPct  = ((segStart - startMs) / windowMs) * 100;
    const widthPct = ((segEnd   - segStart) / windowMs) * 100;
    const voc = phraseHasVocal(p) ? ' has-vocal' : '';
    const title = `${p.name} · vocal ${(p.vocal_score || 0).toFixed(2)} · ${fmtPhraseTime(p.start_ms)} – ${fmtPhraseTime(p.end_ms)}`;
    const narrow = widthPct < 8 ? 'too-narrow' : '';
    return `<div class="phrase kind-${p.kind}${voc} ${narrow}" data-vscore="${(p.vocal_score || 0).toFixed(4)}" style="position:absolute; left:${leftPct.toFixed(3)}%; width:${widthPct.toFixed(3)}%" title="${escape(title)}">${escape(p.name)}</div>`;
  }).join('');
}

function fmtPhraseTime(ms) {
  const s = Math.floor(ms / 1000);
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}

// ───────── BEAT GRID EDITOR ─────────
// Renders the BEAT GRID section of the detail drawer. Effective BPM
// + phase shift live on the library Track; the API endpoint persists
// and rebroadcasts so the CDJ re-fetches its beat grid.
function renderBeatGridEditor(t) {
  const eff = (t.bpm || 0).toFixed(2);
  const det = (t.detected_bpm || 0) > 0 ? (t.detected_bpm).toFixed(2) : null;
  const phase = ((t.beat_phase_shift || 0) % 4 + 4) % 4;
  // Locked tracks protect their hand-corrected grid: grid edits and grid/all
  // re-analysis are disabled; only a waveform-only re-analysis is allowed.
  const locked = !!t.analysis_locked;
  const dis = locked ? 'disabled' : '';
  // Show CLEAR when either BPM differs from detected OR phase ≠ 0;
  // either kind of override is reversible via clear.
  const overridden = det !== null || phase !== 0;
  const phaseBtn = n => {
    const active = phase === n ? 'active' : '';
    const label = n === 0 ? '0' : '+' + n;
    return `<button type="button" class="bg-phase ${active}" data-phase="${n}" ${dis}>${label}</button>`;
  };
  let detLine;
  if (locked) {
    detLine = `<span class="bg-det">🔒 locked</span>`;
  } else if (det !== null) {
    detLine = `<span class="bg-det">detected: ${det}</span>`;
  } else if (phase !== 0) {
    detLine = `<span class="bg-det">phase shifted +${phase}</span>`;
  } else {
    detLine = '<span class="bg-det dim">(no override)</span>';
  }
  if (overridden && !locked) {
    detLine += `<button type="button" class="bg-clear" id="bg-clear">CLEAR OVERRIDE</button>`;
  }
  // Re-analyze asks what to recompute (everything / grid / waveform) in its
  // confirmation dialog; while locked only waveform is offered.
  detLine += `
    <button type="button" class="bg-clear" id="bg-reanalyze" title="Re-run analysis from the audio file: everything, the grid only, or the waveform only">RE-ANALYZE</button>
    <button type="button" class="bg-clear bg-lock ${locked ? 'active' : ''}" id="bg-lock" title="${locked ? 'Unlock to allow re-analysis and grid edits' : 'Lock to protect this grid from re-analysis and edits'}">${locked ? '🔒 LOCKED' : 'LOCK'}</button>`;
  return `
    <div class="bg-row">
      <span class="bg-label">BPM</span>
      <input type="number" id="bg-bpm" class="bg-num" min="20" max="400" step="0.01" value="${eff}" ${dis}>
      <button type="button" class="bg-quick" id="bg-half" title="Halve BPM" ${dis}>÷2</button>
      <button type="button" class="bg-quick" id="bg-double" title="Double BPM" ${dis}>×2</button>
      <button type="button" class="bg-tap" id="bg-tap" title="Tap to set BPM (Space). Resets after 2s of silence." ${dis}>
        TAP <span class="bg-tap-hint" id="bg-tap-hint"></span>
      </button>
      <button type="button" class="bg-save" id="bg-save" ${dis}>SAVE</button>
    </div>
    <div class="bg-row">
      <span class="bg-label">Phase</span>
      <div class="bg-phase-group">${[0,1,2,3].map(phaseBtn).join('')}</div>
    </div>
    <div class="bg-row bg-foot">${detLine}</div>
  `;
}

async function saveBeatOverrides(trackID, body) {
  const r = await fetch(`/api/tracks/${trackID}/beats`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    toast(`SAVE FAILED · ${r.status}`, 'error');
    return null;
  }
  const data = await r.json();
  // Mirror onto allTracks so library rows / pickers see the new BPM
  // without waiting for the next /api/tracks poll.
  const t = (allTracks || []).find(x => x.id === trackID);
  if (t) {
    t.bpm = data.bpm;
    t.detected_bpm = data.detected_bpm;
    t.beat_phase_shift = data.beat_phase_shift;
  }
  return data;
}

function wireBeatGridEditor(trackID) {
  const bpmInput = document.getElementById('bg-bpm');
  const sec = document.getElementById('beats-section');
  if (!bpmInput || !sec) return;
  const refresh = async (patch) => {
    const data = await saveBeatOverrides(trackID, patch);
    if (!data) return;
    sec.innerHTML = `<h4>BEAT GRID</h4>` + renderBeatGridEditor({
      id: trackID,
      bpm: data.bpm,
      detected_bpm: data.detected_bpm,
      beat_phase_shift: data.beat_phase_shift,
      analysis_locked: (allTracks || []).find(x => x.id === trackID)?.analysis_locked || false,
    });
    wireBeatGridEditor(trackID);
    toast(`BEAT GRID · BPM ${data.bpm.toFixed(2)}`, 'success');
    // Refetch the analysis so the zoom waveform's beat-grid overlay
    // picks up the new grid. Without this the ticks keep showing the
    // pre-save tempo/phase until the drawer is closed and reopened.
    if (detailWave.trackID === trackID) {
      const fresh = await fetchJSON('/api/analysis/' + trackID);
      if (fresh && Array.isArray(fresh.beats) && detailWave.trackID === trackID) {
        detailWave.beats = fresh.beats;
        redrawDetailWaveform();
      }
    }
  };
  document.getElementById('bg-save').onclick = () => {
    const v = parseFloat(bpmInput.value);
    if (!isFinite(v) || v <= 0) { toast('INVALID BPM', 'error'); return; }
    refresh({ bpm: v });
  };
  document.getElementById('bg-half').onclick = () => {
    const v = parseFloat(bpmInput.value);
    if (isFinite(v) && v > 0) refresh({ bpm: v / 2 });
  };
  document.getElementById('bg-double').onclick = () => {
    const v = parseFloat(bpmInput.value);
    if (isFinite(v) && v > 0) refresh({ bpm: v * 2 });
  };
  sec.querySelectorAll('[data-phase]').forEach(btn => {
    btn.onclick = () => refresh({ beat_phase_shift: parseInt(btn.getAttribute('data-phase'), 10) });
  });
  const clr = document.getElementById('bg-clear');
  if (clr) clr.onclick = () => refresh({ clear: true });
  const rean = document.getElementById('bg-reanalyze');
  if (rean) rean.onclick = async () => {
    const locked = (allTracks || []).find(x => x.id === trackID)?.analysis_locked || false;
    const lockedTip = 'Locked: unlock the track to change its grid';
    const parts = await openChoiceModal({
      title: 'RE-ANALYZE TRACK',
      message: (locked ? 'This track is locked, so only the waveform can be re-analyzed.\n\n' : '') +
        'EVERYTHING · beats, BPM, downbeat, key and waveform, from the audio file.\n' +
        'GRID ONLY · beats, BPM, downbeat and key; keeps the waveform.\n' +
        'WAVEFORM ONLY · the colour waveform; keeps the grid exactly as it is.\n\n' +
        'Everything and grid only replace any manual grid correction.',
      choices: [
        { value: 'waveform', label: 'WAVEFORM ONLY', primary: locked },
        { value: 'grid', label: 'GRID ONLY', disabled: locked, title: locked ? lockedTip : '' },
        { value: 'all', label: 'EVERYTHING', primary: !locked, disabled: locked, title: locked ? lockedTip : '' },
      ],
    });
    if (!parts) return;
    rean.disabled = true;
    rean.textContent = 'RE-ANALYZING…';
    try {
      const r = await fetch(`/api/analysis/reanalyze/${trackID}?parts=${parts}`, { method: 'POST' });
      if (!r.ok) { toast(`RE-ANALYSIS FAILED · ${r.status}`, 'error'); return; }
      const data = await r.json();
      toast(`RE-ANALYZED (${parts}) · BPM ${data.bpm.toFixed(2)} · ${data.beat_count} BEATS`, 'success');
      // Drop the cached waveform so the detail drawer pulls a fresh
      // PWV5 (the bytes may have changed after re-analysis), and
      // reopen so beats + waveform + cues all re-fetch. Also drop the
      // player-card phrase cache so a deck showing this track re-fetches
      // the recomputed phrases on the next poll.
      waveformCache.delete('detail:' + trackID);
      playerPhrasesCache.delete(trackID);
      if (selectedTrackID === trackID) openDetail(trackID);
    } catch (e) {
      toast('RE-ANALYSIS FAILED · ' + e, 'error');
    } finally {
      rean.disabled = false;
    }
  };

  // Lock toggle: protects the grid daemon-side (issue #70) so no client or the
  // daemon itself can re-analyze or rewrite it. Re-renders so the grid-edit
  // controls enable/disable to match.
  const lockBtn = document.getElementById('bg-lock');
  if (lockBtn) lockBtn.onclick = async () => {
    const willLock = !lockBtn.classList.contains('active');
    lockBtn.disabled = true;
    try {
      const r = await fetch(`/api/tracks/${trackID}/lock`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ locked: willLock }),
      });
      if (!r.ok) { toast(`LOCK FAILED · ${r.status}`, 'error'); return; }
      const cur = (allTracks || []).find(x => x.id === trackID);
      if (cur) cur.analysis_locked = willLock;
      toast(willLock ? 'GRID LOCKED' : 'GRID UNLOCKED', 'success');
      sec.innerHTML = `<h4>BEAT GRID</h4>` + renderBeatGridEditor({
        id: trackID,
        bpm: parseFloat(bpmInput.value) || (cur ? cur.bpm : 0),
        detected_bpm: cur ? cur.detected_bpm : 0,
        beat_phase_shift: cur ? cur.beat_phase_shift : 0,
        analysis_locked: willLock,
      });
      wireBeatGridEditor(trackID);
    } catch (e) {
      toast('LOCK FAILED · ' + e, 'error');
    } finally {
      lockBtn.disabled = false;
    }
  };

  // Tap tempo: keep a sliding window of tap timestamps, compute BPM
  // from the median interval. Resets after 2s of silence so a fresh
  // tap sequence doesn't blend with the previous one. SAVE still has
  // to be pressed to commit — TAP just updates the input.
  const tap = document.getElementById('bg-tap');
  const tapHint = document.getElementById('bg-tap-hint');
  let taps = [];
  let tapTimer = null;
  const resetTaps = () => {
    taps = [];
    if (tapHint) tapHint.textContent = '';
    tap.classList.remove('on');
  };
  const onTap = () => {
    const now = performance.now();
    if (taps.length > 0 && now - taps[taps.length - 1] > 2000) taps = [];
    taps.push(now);
    if (taps.length > 12) taps.shift();
    tap.classList.add('on');
    setTimeout(() => tap.classList.remove('on'), 80);
    if (tapTimer) clearTimeout(tapTimer);
    tapTimer = setTimeout(resetTaps, 2200);
    if (taps.length >= 2) {
      const intervals = [];
      for (let i = 1; i < taps.length; i++) intervals.push(taps[i] - taps[i - 1]);
      intervals.sort((a, b) => a - b);
      const median = intervals[Math.floor(intervals.length / 2)];
      const bpm = 60000 / median;
      bpmInput.value = bpm.toFixed(2);
      if (tapHint) tapHint.textContent = `(${taps.length})`;
    } else if (tapHint) {
      tapHint.textContent = '(1)';
    }
  };
  tap.onclick = onTap;
  // Space bar taps while the BPM input has focus, so a user can rest
  // a finger on Space and tap to the beat.
  bpmInput.addEventListener('keydown', e => {
    if (e.key === ' ') { e.preventDefault(); onTap(); }
  });
}

// ───────── TRACK COLOR PICKER ─────────
function openColorPicker(anchorEl, trackID, currentColorID) {
  closeColorPicker();
  const menu = document.createElement('div');
  menu.className = 'color-picker';
  const swatches = ['none','1','2','3','4','5','6','7','8'];
  menu.innerHTML = swatches.map(s => {
    if (s === 'none') {
      return `<div class="swatch none ${currentColorID === 0 ? 'selected' : ''}" data-color="0" title="No color"></div>`;
    }
    const id = parseInt(s, 10);
    const name = (typeof COLOR_NAMES !== 'undefined' && COLOR_NAMES[id]) || ('Color ' + id);
    return `<div class="swatch ${currentColorID === id ? 'selected' : ''}" data-color="${id}" style="background:${TRACK_COLORS[id]}" title="${name}"></div>`;
  }).join('');
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  let left = r.left;
  if (left + menu.offsetWidth > window.innerWidth - 10) left = window.innerWidth - menu.offsetWidth - 10;
  menu.style.left = Math.max(8, left) + 'px';
  menu.style.top = (r.bottom + 6) + 'px';
  menu.querySelectorAll('.swatch').forEach(sw => {
    sw.addEventListener('click', async (ev) => {
      ev.stopPropagation();
      const cid = parseInt(sw.getAttribute('data-color'), 10);
      await setTrackColor(trackID, cid);
      closeColorPicker();
    });
  });
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}
function outsideColorClick(e) {
  // Covers the bulk colour/tag/playlist pickers, which share this lifecycle.
  // The bulk tag/playlist pickers reuse the .tag-picker styling but carry the
  // .bulk-picker marker so this doesn't disturb the detail-drawer tag picker.
  if (e.target.closest('.color-picker, .bulk-picker')) return;
  closeColorPicker();
}
function closeColorPicker() {
  document.querySelectorAll('.color-picker, .bulk-picker').forEach(m => m.remove());
  document.removeEventListener('click', outsideColorClick);
}

// ───────── CUE COLOR PICKER ─────────
// Reuses the swatch popover. POSTs the *full* CueInfo back to /api/tracks/{id}/cues
// — that endpoint does a full replace, so we have to send everything the cue
// already had (time, type, label, loop) with only color_id swapped.
//
// CDJ_CUE_PALETTE: the 16 palette indices the on-CDJ hot-cue picker exposes,
// spaced every 4 through the 62-entry CUE_COLORS rainbow so users get the
// full spectrum (blues, greens, yellows, oranges, reds, pinks, purples)
// instead of the consecutive shades-of-blue 0x01..0x08. Includes 0x12
// (GREEN), which we already know the on-CDJ picker exposes from the
// 0x42 → 0x12 mapping documented above CUE_COLORS.
const CDJ_CUE_PALETTE = [
  0x02, 0x06, 0x0a, 0x0e, 0x12, 0x16, 0x1a, 0x1e,
  0x22, 0x26, 0x2a, 0x2e, 0x32, 0x36, 0x3a, 0x3e,
];
// Shared swatch popover for picking a hot-cue colour from CDJ_CUE_PALETTE
// (plus a "none"/0 swatch). Anchored under anchorEl; onPick(colorID) fires
// with the chosen palette index (0 = none). Used by both the per-track cue
// colour picker and the per-cue default-colour picker.
function openCuePalettePopover(anchorEl, currentColorID, onPick) {
  closeColorPicker();
  const menu = document.createElement('div');
  menu.className = 'color-picker wrap';
  const noneHTML = `<div class="swatch none ${currentColorID === 0 ? 'selected' : ''}" data-color="0" title="No color"></div>`;
  const swatchesHTML = CDJ_CUE_PALETTE.map(id =>
    `<div class="swatch ${currentColorID === id ? 'selected' : ''}" data-color="${id}" style="background:${CUE_COLORS[id]}" title="Color 0x${id.toString(16).padStart(2,'0')}"></div>`
  ).join('');
  menu.innerHTML = noneHTML + swatchesHTML;
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  let left = r.left;
  if (left + menu.offsetWidth > window.innerWidth - 10) left = window.innerWidth - menu.offsetWidth - 10;
  menu.style.left = Math.max(8, left) + 'px';
  menu.style.top = (r.bottom + 6) + 'px';
  menu.querySelectorAll('.swatch').forEach(sw => {
    sw.addEventListener('click', (ev) => {
      ev.stopPropagation();
      const cid = parseInt(sw.getAttribute('data-color'), 10);
      closeColorPicker();
      onPick(cid);
    });
  });
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}
function openCueColorPicker(anchorEl, trackID, cueNumber, currentColorID) {
  openCuePalettePopover(anchorEl, currentColorID, cid => setCueColor(trackID, cueNumber, cid));
}

// ───────── PER-CUE DEFAULT COLOURS (browser-local) ─────────
// Default colour applied to a NEW hot cue (A–H = cue numbers 1–8) when it's
// created from the web UI. Browser-local (like the other WEB UI prefs); not
// sent to the CDJ and not applied to cues the CDJ itself creates. 0 = no
// colour (the historical default).
let defaultCueColors = (() => {
  const out = {};
  let raw = {};
  try { raw = JSON.parse(localStorage.getItem('defaultCueColors') || '{}'); } catch (e) {}
  for (let n = 1; n <= 8; n++) out[n] = Number(raw[n]) || 0;
  return out;
})();
function defaultCueColorFor(num) { return defaultCueColors[num] || 0; }
function setDefaultCueColor(num, colorID) {
  defaultCueColors[num] = colorID;
  try { localStorage.setItem('defaultCueColors', JSON.stringify(defaultCueColors)); } catch (e) {}
}
// Opens the cue palette popover bound to a hot-cue slot's default colour and
// repaints the settings swatch in place.
function openDefaultCueColorPicker(anchorEl, cueNumber) {
  openCuePalettePopover(anchorEl, defaultCueColorFor(cueNumber), cid => {
    setDefaultCueColor(cueNumber, cid);
    anchorEl.style.background = cid === 0 ? '' : (CUE_COLORS[cid] || CUE_COLORS[0]);
    anchorEl.style.color = cid === 0 ? '' : (isWarmCueColor(cid) ? '#000' : '#fff');
    anchorEl.classList.toggle('none', cid === 0);
    toast(`CUE ${cueLabel(cueNumber)} DEFAULT → ${cid === 0 ? 'NONE' : '0x' + cid.toString(16).padStart(2, '0')}`, 'success');
  });
}

async function setCueColor(trackID, cueNumber, colorID) {
  const existing = getCuesFor(trackID).find(c => c.number === cueNumber);
  if (!existing) { toast('CUE NOT FOUND', 'error'); return; }
  const body = {
    number:   existing.number,
    type:     existing.type || 'cue',
    time_ms:  existing.time_ms,
    loop_ms:  existing.loop_ms != null ? existing.loop_ms : -1,
    color_id: colorID,
    label:    existing.label || '',
  };
  const r = await fetch(`/api/tracks/${trackID}/cues`, {
    method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify(body),
  });
  if (!r.ok) { toast('SET CUE COLOR FAILED', 'error'); return; }
  // Patch the cache so library row, drawer, and waveform overlay re-render
  // with the new colour on the next paint without waiting for the 1s poll.
  existing.color_id = colorID;
  // Patch the visible dot in place so it updates instantly even before any
  // re-render happens.
  const dot = document.querySelector(`#detail-body .cue-row[data-cue-number="${cueNumber}"] [data-act="pick-cue-color"]`);
  if (dot) {
    const hex = CUE_COLORS[colorID] || CUE_COLORS[0];
    dot.style.background = hex;
    dot.style.color = hex;
  }
  // Redraw the waveform + cue markers using cached data, so the marker on
  // the big waveform reflects the new colour instantly.
  if (detailWave.trackID === trackID) {
    detailWave.cues = getCuesFor(trackID);
    redrawDetailWaveform();
  }
  toast(colorID ? 'CUE COLOR SET' : 'CUE COLOR CLEARED', 'success');
}

// ───────── TAG COLOR PICKER ─────────
// Mirrors openColorPicker but PUTs to /api/tags/{id} and patches local caches
// so the Tags tab, drawer chips, and tag picker reflect the new colour without
// a full refresh.
function openTagColorPicker(anchorEl, tagID, currentColorID) {
  closeColorPicker();
  const menu = document.createElement('div');
  menu.className = 'color-picker';
  const swatches = ['none','1','2','3','4','5','6','7','8'];
  menu.innerHTML = swatches.map(s => {
    if (s === 'none') {
      return `<div class="swatch none ${currentColorID === 0 ? 'selected' : ''}" data-color="0" title="No color"></div>`;
    }
    const id = parseInt(s, 10);
    const name = (typeof COLOR_NAMES !== 'undefined' && COLOR_NAMES[id]) || ('Color ' + id);
    return `<div class="swatch ${currentColorID === id ? 'selected' : ''}" data-color="${id}" style="background:${TRACK_COLORS[id]}" title="${name}"></div>`;
  }).join('');
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  let left = r.left;
  if (left + menu.offsetWidth > window.innerWidth - 10) left = window.innerWidth - menu.offsetWidth - 10;
  menu.style.left = Math.max(8, left) + 'px';
  menu.style.top = (r.bottom + 6) + 'px';
  menu.querySelectorAll('.swatch').forEach(sw => {
    sw.addEventListener('click', async (ev) => {
      ev.stopPropagation();
      const cid = parseInt(sw.getAttribute('data-color'), 10);
      await setTagColor(tagID, cid);
      closeColorPicker();
    });
  });
  setTimeout(() => document.addEventListener('click', outsideColorClick), 0);
}

async function setTagColor(tagID, colorID) {
  const r = await fetch(`/api/tags/${tagID}`, {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ color: colorID }),
  });
  if (!r.ok) { toast('SET COLOR FAILED', 'error'); return; }
  // Patch the local caches so the next render uses the new colour without
  // re-fetching /api/tags.
  if (allTagsCache) {
    const t = allTagsCache.find(x => x.id === tagID);
    if (t) t.color = colorID;
  }
  if (Array.isArray(drawerTrackTags)) {
    const t = drawerTrackTags.find(x => x.id === tagID);
    if (t) t.color = colorID;
  }
  renderTagsTab();
  renderTagsSection();
  toast(colorID ? 'TAG COLOR SET' : 'TAG COLOR CLEARED', 'success');
}

async function setTrackColor(trackID, colorID) {
  const r = await fetch(`/api/tracks/${trackID}/color`, {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ color_id: colorID }),
  });
  if (!r.ok) { toast('SET COLOR FAILED', 'error'); return; }
  // Update local state so UI reflects immediately, and register a
  // pending edit so a racing poll can't clobber the new color.
  const patch = { color_id: colorID, color_name: COLOR_NAMES[colorID] || '' };
  markPendingTrackEdit(trackID, patch);
  const t = allTracks.find(x => x.id === trackID);
  if (t) {
    Object.assign(t, patch);
    rememberTracksSnapshot();
  }
  // Patch the drawer's chip in place.
  const chip = document.getElementById('track-color-chip');
  if (chip) {
    chip.classList.toggle('empty', colorID === 0);
    chip.style.background = colorID > 0 ? TRACK_COLORS[colorID] : '';
  }
  // Patch the library row's chip (if the COLOR column is visible).
  document.querySelectorAll(`[data-color-pick="${trackID}"]`).forEach(libChip => {
    libChip.classList.toggle('empty', colorID === 0);
    libChip.style.background = colorID > 0 ? TRACK_COLORS[colorID] : '';
    libChip.title = colorID > 0 ? (COLOR_NAMES[colorID] || 'Set color') : 'Set color';
  });
  toast(colorID === 0 ? 'COLOR CLEARED' : `COLOR · ${COLOR_NAMES[colorID]}`, 'success');
}

// Set a track's rating (0-5). Click on the currently-set value clears it.
// Used by the star widgets in the library RATING column and the detail
// drawer METADATA section.
async function setTrackRating(trackID, rating) {
  const t = allTracks.find(x => x.id === trackID);
  // Click on currently-set rating clears (toggles back to unset).
  if (t && (t.rating || 0) === rating) rating = 0;
  const r = await fetch(`/api/tracks/${trackID}/rating`, {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ rating }),
  });
  if (!r.ok) { toast('SET RATING FAILED', 'error'); return; }
  markPendingTrackEdit(trackID, { rating });
  if (t) {
    t.rating = rating;
    rememberTracksSnapshot();
  }
  // Patch every visible star widget for this track in place (library row +
  // detail drawer) so the user sees the change without a re-render flash.
  document.querySelectorAll(`[data-rating-track="${trackID}"]`).forEach(el => {
    el.setAttribute('data-rating-current', String(rating));
    el.querySelectorAll('.star').forEach(btn => {
      const set = parseInt(btn.getAttribute('data-rating-set'), 10);
      btn.classList.toggle('filled', set <= rating);
    });
  });
  toast(rating === 0 ? 'RATING CLEARED' : `RATING · ${rating}★`, 'success');
}

// Wires star click handlers within a root element. Used by renderLibrary
// and renderDetail post-render. Stops propagation so star clicks in the
// library don't also open the detail drawer.
function wireStarClicks(root) {
  root.querySelectorAll('[data-rating-track] .star').forEach(btn => {
    btn.addEventListener('click', e => {
      e.stopPropagation();
      const parent = btn.closest('[data-rating-track]');
      const tid = parseInt(parent.getAttribute('data-rating-track'), 10);
      const rating = parseInt(btn.getAttribute('data-rating-set'), 10);
      setTrackRating(tid, rating);
    });
  });
}

const COLOR_NAMES = {
  0: 'NONE', 1: 'PINK', 2: 'RED', 3: 'ORANGE', 4: 'YELLOW',
  5: 'GREEN', 6: 'AQUA', 7: 'BLUE', 8: 'PURPLE',
};

// ───────── TAG EDITING (drawer) ─────────

function renderTagsSection() {
  const host = document.getElementById('tags-host');
  if (!host) return;
  document.getElementById('tags-count').textContent = drawerTrackTags.length;

  let chips = drawerTrackTags.map(t => {
    const style = t.color ? ` style="--chip-color:${TRACK_COLORS[t.color]}"` : '';
    return `<span class="tag-chip removable" data-tag-id="${t.id}"${style}>
      ${escape(t.name)}
      <button class="chip-x" title="Remove tag" aria-label="Remove tag">×</button>
    </span>`;
  }).join('');
  chips += `<button class="tag-chip add" id="add-tag-btn" type="button">+ ADD</button>`;
  host.innerHTML = `<div class="tag-chips">${chips}</div>`;

  // Wire remove buttons.
  host.querySelectorAll('.chip-x').forEach(btn => {
    btn.addEventListener('click', e => {
      e.stopPropagation();
      const tagID = parseInt(btn.closest('.tag-chip').getAttribute('data-tag-id'), 10);
      removeTagFromCurrent(tagID);
    });
  });
  // Wire add button.
  const addBtn = document.getElementById('add-tag-btn');
  if (addBtn) addBtn.addEventListener('click', e => {
    e.stopPropagation();
    openTagPicker(addBtn);
  });
}

async function saveTrackTags(tagIDs) {
  if (selectedTrackID == null) return false;
  const r = await fetch(`/api/tracks/${selectedTrackID}/tags`, {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ tag_ids: tagIDs }),
  });
  return r.ok;
}

async function removeTagFromCurrent(tagID) {
  const tag = drawerTrackTags.find(t => t.id === tagID);
  const newList = drawerTrackTags.filter(t => t.id !== tagID);
  const newIDs = newList.map(t => t.id);
  // Optimistic update.
  drawerTrackTags = newList;
  renderTagsSection();
  // Also reflect in the library row's allTracks cache (so a refresh doesn't
  // briefly show the removed tag back).
  patchTrackTagsLocal(selectedTrackID, newList.map(t => t.name));
  const ok = await saveTrackTags(newIDs);
  if (ok) toast(`REMOVED · ${(tag ? tag.name : 'TAG').toUpperCase()}`, 'success');
  else { toast('REMOVE FAILED', 'error'); }
}

async function addTagToCurrent(tagID) {
  if (drawerTrackTags.some(t => t.id === tagID)) return;
  const tag = (allTagsCache || []).find(t => t.id === tagID);
  if (!tag) return;
  const newList = [...drawerTrackTags, tag];
  drawerTrackTags = newList;
  renderTagsSection();
  patchTrackTagsLocal(selectedTrackID, newList.map(t => t.name));
  const ok = await saveTrackTags(newList.map(t => t.id));
  if (ok) toast(`+ ${tag.name.toUpperCase()}`, 'success');
  else { toast('ASSIGN FAILED', 'error'); }
}

async function createAndAssignTag(name) {
  name = (name || '').trim();
  if (!name) return;
  // POST creates (or returns existing-by-name) → {id}
  const r = await fetch('/api/tags', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ name, category_id: 0 }),
  });
  if (!r.ok) { toast('CREATE FAILED', 'error'); return; }
  const created = await r.json();
  if (!created || !created.id) return;
  await ensureAllTags(true); // refresh catalog so subsequent picker opens see it
  await addTagToCurrent(created.id);
}

// Keep the in-memory library-track list in sync with tag edits so the next
// poll cycle doesn't briefly revert what the user just did.
function patchTrackTagsLocal(trackID, tagNames) {
  const t = allTracks.find(x => x.id === trackID);
  if (t) t.tags = tagNames;
  rememberTracksSnapshot();
}

// After any in-place mutation of allTracks, sync the snapshot so the next
// 1s poll (which will return the same data) doesn't trigger a re-render.
function rememberTracksSnapshot() {
  lastTracksKey = JSON.stringify(allTracks);
}

// Pending-edits buffer for race-proofing local mutations against the
// 1Hz library poll. Without this, the sequence
//   click swatch → PUT issued → poll GET races against PUT on server →
//   poll's stale response replaces allTracks → re-render loses the
//   local color
// happens any time the GET arrives at the server before the PUT
// handler ran. Each entry overrides matching server fields for up to
// PENDING_EDIT_TTL_MS, then auto-expires (the server has surely caught
// up by then).
const pendingTrackEdits = new Map(); // trackID -> { fields:{...}, expiresAt:ms }
const PENDING_EDIT_TTL_MS = 3000;
function markPendingTrackEdit(trackID, patch) {
  pendingTrackEdits.set(trackID, { fields: patch, expiresAt: Date.now() + PENDING_EDIT_TTL_MS });
}
function applyPendingTrackEdits(tracks) {
  if (pendingTrackEdits.size === 0) return;
  const now = Date.now();
  for (const t of tracks) {
    const p = pendingTrackEdits.get(t.id);
    if (!p) continue;
    if (p.expiresAt < now) { pendingTrackEdits.delete(t.id); continue; }
    // If the server has caught up (all patched fields already match), drop
    // the pending entry — the patch is now part of the canonical state.
    let allMatch = true;
    for (const [k, v] of Object.entries(p.fields)) {
      if (t[k] !== v) { allMatch = false; break; }
    }
    if (allMatch) pendingTrackEdits.delete(t.id);
    else          Object.assign(t, p.fields);
  }
}

// ───────── TAG PICKER POPOVER ─────────
let tagPickerOpen = false;

// groupedTagPickerHTML builds the inner-list markup for a tag picker, grouping
// tags under their category (matching the Tags tab). cats is the category list;
// opts.assignedIDs marks already-applied tags; opts.showDot prefixes the colour
// dot. Category headers are omitted when every tag is uncategorized (no signal).
function groupedTagPickerHTML(tags, cats, opts = {}) {
  const { assignedIDs, showDot } = opts;
  const catName = id => {
    if (!id) return 'UNCATEGORIZED';
    const c = (cats || []).find(x => x.id === id);
    return c ? c.name.toUpperCase() : `CATEGORY ${id}`;
  };
  const groups = {};
  tags.forEach(t => { const k = t.category_id || 0; (groups[k] = groups[k] || []).push(t); });
  // Sort categories by name, uncategorized (0) last.
  const catIDs = Object.keys(groups).map(n => parseInt(n, 10)).sort((a, b) =>
    (a === 0 ? 1 : b === 0 ? -1 : catName(a).localeCompare(catName(b))));
  const showHeads = catIDs.length > 1 || (catIDs.length === 1 && catIDs[0] !== 0);
  return catIDs.map(cid => {
    const head = showHeads ? `<div class="picker-cat-head">${escape(catName(cid))}</div>` : '';
    const items = groups[cid].map(t => {
      const assigned = assignedIDs && assignedIDs.has(t.id) ? ' assigned' : '';
      const dot = showDot
        ? (t.color ? `<span class="picker-dot" style="background:${TRACK_COLORS[t.color]}"></span>`
                   : `<span class="picker-dot empty"></span>`)
        : '';
      return `<div class="picker-item${assigned}" data-tag-id="${t.id}"><span class="picker-name">${dot}${escape(t.name)}</span></div>`;
    }).join('');
    return head + items;
  }).join('');
}

async function openTagPicker(anchorEl) {
  closeTagPicker();
  const cats = await ensureAllCategories();
  const assignedIDs = new Set(drawerTrackTags.map(t => t.id));
  const available = (allTagsCache || []).slice().sort((a, b) => a.name.localeCompare(b.name));

  const menu = document.createElement('div');
  menu.className = 'tag-picker';
  menu.innerHTML = `
    <div class="head">SELECT TAG</div>
    <div class="list" id="tag-picker-list">
      ${available.length === 0
        ? '<div class="picker-empty">NO TAGS YET</div>'
        : groupedTagPickerHTML(available, cats, { assignedIDs, showDot: true })}
    </div>
    <div class="head">OR CREATE NEW</div>
    <div class="picker-create">
      <input type="text" id="new-tag-input" placeholder="TAG NAME…" autocomplete="off">
      <button id="create-tag-btn" disabled>CREATE</button>
    </div>
  `;

  // Position anchored under the + ADD button, clamped to viewport.
  document.body.appendChild(menu);
  const r = anchorEl.getBoundingClientRect();
  const mw = menu.offsetWidth, mh = menu.offsetHeight;
  let left = r.left;
  if (left + mw > window.innerWidth - 10) left = window.innerWidth - mw - 10;
  let top = r.bottom + 4;
  if (top + mh > window.innerHeight - 10) top = r.top - mh - 4;
  menu.style.left = Math.max(8, left) + 'px';
  menu.style.top = Math.max(8, top) + 'px';

  // The picker stays open on selection so you can tag a bunch at once — each
  // click toggles the tag on/off for the track. Click outside or press Escape
  // to dismiss. Items re-wire after a rebuild (e.g. when a new tag is created).
  const listEl = menu.querySelector('#tag-picker-list');
  const wireItems = () => {
    listEl.querySelectorAll('.picker-item').forEach(item => {
      item.addEventListener('click', () => {
        const id = parseInt(item.getAttribute('data-tag-id'), 10);
        if (drawerTrackTags.some(t => t.id === id)) {
          removeTagFromCurrent(id);
          item.classList.remove('assigned');
        } else {
          addTagToCurrent(id);
          item.classList.add('assigned');
        }
      });
    });
  };
  const rebuildList = () => {
    const avail = (allTagsCache || []).slice().sort((a, b) => a.name.localeCompare(b.name));
    const assigned = new Set(drawerTrackTags.map(t => t.id));
    listEl.innerHTML = avail.length === 0
      ? '<div class="picker-empty">NO TAGS YET</div>'
      : groupedTagPickerHTML(avail, cats, { assignedIDs: assigned, showDot: true });
    wireItems();
  };
  wireItems();

  // Create-new input + button. On create the popup stays open and the list
  // refreshes so the new (now-assigned) tag appears in place.
  const input = document.getElementById('new-tag-input');
  const createBtn = document.getElementById('create-tag-btn');
  const enableBtn = () => { createBtn.disabled = input.value.trim().length === 0; };
  const doCreate = async () => {
    if (createBtn.disabled) return;
    await createAndAssignTag(input.value);
    input.value = '';
    enableBtn();
    rebuildList();
    input.focus({ preventScroll: true });
  };
  input.addEventListener('input', enableBtn);
  input.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); doCreate(); }
    else if (e.key === 'Escape') { closeTagPicker(); }
  });
  createBtn.addEventListener('click', doCreate);
  input.focus({ preventScroll: true });

  tagPickerOpen = true;
  setTimeout(() => document.addEventListener('click', outsidePickerClick), 0);
}
function outsidePickerClick(e) {
  if (e.target.closest('.tag-picker')) return;
  closeTagPicker();
}
function closeTagPicker() {
  document.querySelectorAll('.tag-picker').forEach(m => m.remove());
  document.removeEventListener('click', outsidePickerClick);
  tagPickerOpen = false;
}

// Cue numbers: 1-8 → A-H, 9-16 → 1-8 (memory cues in rekordbox parlance).
function cueLabel(n) {
  if (n >= 1 && n <= 8) return String.fromCharCode('A'.charCodeAt(0) + n - 1);
  if (n >= 9 && n <= 16) return String(n - 8);
  return String(n);
}

