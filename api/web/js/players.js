// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── PLAYERS ─────────
// Cache of the compat filter's last-resolved reference, so we only
// re-apply the filter (which re-renders the whole library table, and
// would otherwise flicker the per-row waveform <img>s every poll tick)
// when the deck's key or BPM actually changes.
let lastCompatRefKey = null;
// Per-deck track ID snapshot from the previous poll. Used to detect
// "deck N just loaded a new track" so the COMPAT filter can auto-
// follow whichever deck the user is currently cueing into.
const lastDeckTrackIDs = new Map();
// Show a one-line strip at the top of the PLAYERS tab whenever any
// peer reports DeviceType=Mixer. Channel-level state (on-air, master,
// per-channel BPM) is not parsed yet — the mixer packet layout varies
// between DJM models and we'd need verified offsets per model.
// For now this just signals "yes, a DJM is on the network" so the
// user knows their setup is detected.
function renderMixerStrip(peers) {
  const host = document.getElementById('mixer-strip');
  if (!host) return;
  const mixers = (peers || []).filter(p => p.device_type === 'mixer');
  if (mixers.length === 0) { host.innerHTML = ''; return; }
  host.innerHTML = mixers.map(m => {
    const mx = m.mixer;
    // Has at least one fielded value? A presence-only mixer (a
    // stripped type 0x30) gives us a populated `mixer` object with all zero
    // fields — distinguish that from "rich" 0x29 / 0x03 mixer state
    // by the channel_state_known flag (set after we've parsed at
    // least one channel-bearing packet, even if all 4 channels are
    // currently off-air).
    const hasRichState = mx && (
      mx.channel_state_known ||
      mx.master_bpm > 0 ||
      mx.master_device > 0
    );
    let detail = '<span class="dim" title="No 0x29 status received yet, or this DJM only broadcasts the stripped 0x30 packet. Channel/on-air/master state would require the mixer\'s separate TCP control protocol, not just listening to broadcasts.">presence only</span>';
    if (hasRichState) {
      const chips = (mx.channel_on_air || [false,false,false,false]).map((on, i) =>
        `<span class="ch ${on ? 'on' : 'off'}">CH${i+1}</span>`).join('');
      const bpm = mx.master_bpm > 0 ? `${mx.master_bpm.toFixed(2)} BPM` : '—';
      const bib = mx.beat_in_bar ? `${mx.beat_in_bar}/4` : '—';
      const md  = mx.master_device ? `dev ${mx.master_device}` : 'no master';
      detail = `<span class="ch-row">${chips}</span>` +
               `<span class="mxr-master">MASTER · ${md} · ${bpm} · ${bib}</span>`;
    }
    return `
      <div class="mixer-card">
        <span class="led amber"></span>
        <span class="lbl">MIXER</span>
        <span class="name">${escape(m.name || 'DJM')}</span>
        <span class="meta">device ${m.device_number} · ${m.ip}</span>
        ${detail}
      </div>
    `;
  }).join('');
}

// updateLinkStatus drives the bottom status-bar DJ-LINK chip from the live
// peer list: grey "NO LINK" when nothing's on the network, green "LINKED · N"
// (N = CDJs/mixers/rekordbox instances detected) otherwise.
function updateLinkStatus(peers) {
  const chip = document.getElementById('link-status');
  if (!chip) return;
  const led = chip.querySelector('.led');
  const txt = document.getElementById('link-status-text');
  const n = Array.isArray(peers) ? peers.length : 0;
  if (n > 0) {
    led.className = 'led green';
    txt.textContent = `LINKED · ${n}`;
    chip.title = `${n} device${n === 1 ? '' : 's'} on the Pro DJ Link network`;
  } else {
    led.className = 'led';
    txt.textContent = 'NO LINK';
    chip.title = 'No DJ Link devices detected on the network';
  }
}

// Persistent cover-art <img> per deck (device_number -> HTMLImageElement). The
// PLAYERS grid is rebuilt every 1s poll; reusing these nodes (rather than
// recreating the <img> in the template) is what stops the art from blinking.
const deckArtImgs = new Map();

// External-track waveform + cues (from /api/analysis/ext), cached by URL so the
// 1s grid rebuild doesn't refetch. The URL is unique per (player, slot, track),
// so a new track naturally misses and refetches.
const extAnalysisCache = new Map();
const extAnalysisInflight = new Set();
function drawExtWave(canvas, d) {
  const durMs = d.duration_ms || 0;
  drawWaveformPWV5(canvas, { entries: d.waveform || [] }, durMs);
  drawCueMarkersCanvas(canvas, d.cues || [], durMs);
}

function renderPlayers(players) {
  const prev = currentPlayers;
  currentPlayers = players;
  document.getElementById('peer-count').textContent = String(players.length);
  // Live-update the open detail drawer's LOAD buttons when the player roster
  // changes (a device connected or disconnected).
  const prevDevs = (prev || []).map(p => p.device_number).sort().join(',');
  const curDevs = players.map(p => p.device_number).sort().join(',');
  if (prevDevs !== curDevs) refreshDetailLoadButtons();
  // Compat filter in deck-mode follows whatever's loaded on that deck —
  // when its key/bpm changes, refresh the readout and re-apply the
  // filter. Also rebuild the bar if the deck roster itself changed
  // (added/removed deck) so the dropdown options stay accurate.
  if (typeof libraryFilters !== 'undefined') {
    // Auto-switch COMPAT to whichever deck just loaded a new track —
    // matches the DJ workflow where each load directs attention to the
    // newly cued-up deck. Only fires when COMPAT is already engaged
    // (deck:N or custom): if the user has it OFF, loading a track
    // doesn't turn it on. Skipped for the first sighting of a deck (we
    // only react to transitions), and only fires when a non-zero
    // track_id appears (track unload → 0 doesn't trigger).
    let autoSwitchedTo = null;
    for (const p of players) {
      if (!p.device_number || !p.track_id) continue;
      const wasLoaded = lastDeckTrackIDs.get(p.device_number);
      if (wasLoaded !== undefined && wasLoaded !== p.track_id &&
          libraryFilters.compat.mode !== 'off') {
        autoSwitchedTo = p.device_number;
      }
      lastDeckTrackIDs.set(p.device_number, p.track_id);
    }
    for (const dn of Array.from(lastDeckTrackIDs.keys())) {
      if (!players.find(p => p.device_number === dn)) lastDeckTrackIDs.delete(dn);
    }

    const rosterChanged = prev.length !== players.length ||
      prev.some((p, i) => !players[i] || p.device_number !== players[i].device_number);

    if (autoSwitchedTo !== null) {
      const newMode = `deck:${autoSwitchedTo}`;
      if (libraryFilters.compat.mode !== newMode) {
        libraryFilters.compat.mode = newMode;
        keepLibraryPlace(() => { renderFilterBar(); refreshLibraryView(); });
        lastCompatRefKey = JSON.stringify(resolveCompatReference());
        return;
      }
    }

    if (rosterChanged && document.getElementById('lib-fb-compat-mode')) {
      keepLibraryPlace(() => { renderFilterBar(); refreshLibraryView(); });
      lastCompatRefKey = JSON.stringify(resolveCompatReference());
    } else if (libraryFilters.compat.mode.startsWith('deck:')) {
      const ref = resolveCompatReference();
      const refKey = JSON.stringify(ref);
      if (refKey !== lastCompatRefKey) {
        lastCompatRefKey = refKey;
        updateCompatReferenceText();
        keepLibraryPlace(refreshLibraryView);
      }
    }
  }
  const el = document.getElementById('players-content');
  if (!players || players.length === 0) {
    el.innerHTML = '<div class="empty-state">AWAITING DEVICE LINK</div>';
    return;
  }
  el.innerHTML = '<div class="player-grid">' + players.map(p => {
    const isPlaying = p.is_playing;
    const isPaused = !isPlaying && (p.play_state === 5 || p.play_state === 6);
    const stateClass = isPlaying ? 'playing' : isPaused ? 'paused' : '';
    const ledClass = isPlaying ? 'green pulse' : isPaused ? 'amber' : '';
    const pitch = (typeof p.pitch_pct === 'number') ? p.pitch_pct : 0;
    const pitchStr = (pitch > 0 ? '+' : '') + pitch.toFixed(2) + '%';
    const trackHeader = p.external
      ? `<div class="track-header">
          <div class="track-title" title="Playing from ${escape(p.source || 'another source')}">${escape(p.track_title || p.source || 'External source')}</div>
        </div>
        <div class="track-meta-line">${p.track_title ? escape(p.artist || '—') + ' · ' : ''}<span style="color:var(--text3)">${escape(p.source || 'external source')}</span></div>`
      : `<div class="track-header">
          <div class="track-title" data-open-detail="${p.track_id}" title="Click to view track details">${escape(p.track_title || ('TRACK #' + p.track_id))}</div>
          <div class="actions">
            <button type="button" class="action-btn" data-find-track="${p.track_id}" title="Jump to this track in the library">FIND</button>
          </div>
        </div>
        <div class="track-meta-line">${escape(p.artist || '—')}</div>`;
    // Cover art: render an empty slot here; a persistent <img> (kept in
    // deckArtImgs, keyed by deck) is moved into it after render so the 1Hz grid
    // rebuild doesn't recreate — and blink — the image. See the post-render pass.
    const artSlot = p.artwork_url
      ? `<div class="deck-art-slot" data-art-dev="${p.device_number}" data-art-url="${escape(p.artwork_url)}"></div>`
      : '';
    const trackLead = `<div class="track-lead">${artSlot}<div class="track-lead-text">${trackHeader}</div></div>`;
    const trackBody = p.track_id > 0 ? `
      ${trackLead}
      <div class="readouts">
        <div class="bpm-display">
          <div class="label">BPM</div>
          <div class="value">${(p.bpm || 0).toFixed(2)}</div>
        </div>
        <dl class="meta-stack">
          <dt>PITCH</dt><dd>${pitchStr}</dd>
          <dt>KEY</dt><dd>${escape(fmtKey(p.key) || '—')}</dd>
          <dt>BEAT</dt><dd>${p.beat_in_bar || '—'} / 4</dd>
        </dl>
      </div>
      ${p.external ? (p.analysis_url ? `<div class="wave-wrap"><canvas data-ext-wave="${escape(p.analysis_url)}" width="1200" height="58"></canvas>${initialPlayheadMarkup(p)}</div>` : `<div class="wave-wrap" style="display:flex;align-items:center;justify-content:center;color:var(--text3);font-size:10px;letter-spacing:0.14em">WAVEFORM UNAVAILABLE · EXTERNAL SOURCE</div>`) : `<div class="wave-wrap">
        <canvas data-track="${p.track_id}" width="1200" height="58"></canvas>
        ${initialPlayheadMarkup(p)}
      </div>
      <div class="player-phrases" data-phrases-track="${p.track_id}"></div>`}
      <div class="player-time" data-dev="${p.device_number}">
        <span class="elapsed">${initialTimeText(p, 'elapsed')}</span>
        <span class="sep">/</span>
        <span class="remaining">${initialTimeText(p, 'remaining')}</span>
      </div>` : '<div style="padding: 16px 0; color: var(--text3); font-size: 11px; letter-spacing: 0.12em;">— NO TRACK LOADED —</div>';
    return `<div class="player">
      <div class="player-head">
        <span><span class="id">PLAYER ${String(p.device_number).padStart(2,'0')}</span>&nbsp;·&nbsp;<span class="name">${escape(p.name || 'CDJ')}</span></span>
        ${p.is_master ? '<span class="master"><span class="led amber"></span>MASTER</span>' : '<span style="color:var(--text3)">·</span>'}
      </div>
      <div class="player-body">
        <div class="led-row">
          <div class="item ${stateClass ? 'on ' + stateClass : ''}">
            <span class="led ${ledClass}"></span>${escape((p.play_state_name || 'IDLE').toUpperCase())}
          </div>
          <div class="item ${p.is_sync ? 'on sync' : ''}">
            <span class="led ${p.is_sync ? 'cyan' : ''}"></span>SYNC
          </div>
          <div class="item ${p.on_air ? 'on onair' : ''}">
            <span class="led ${p.on_air ? 'red pulse' : ''}"></span>ON-AIR
          </div>
        </div>
        ${trackBody}
      </div>
    </div>`;
  }).join('') + '</div>';
  // Big player-card waveform: PWV5 detail (vivid per-entry colour) with cue
  // markers overlaid. Duration_ms comes from the player JSON (server pulls
  // it from the library track); without it we skip cue markers since we
  // can't compute proportional positions.
  el.querySelectorAll('canvas[data-track]').forEach(canvas => {
    const tid = parseInt(canvas.getAttribute('data-track'), 10);
    const p = players.find(pp => pp.track_id === tid);
    const durMs = (p && p.duration_ms) || 0;
    const cues = getCuesFor(tid);
    const drawAll = data => {
      drawWaveformPWV5(canvas, data, durMs);
      drawCueMarkersCanvas(canvas, cues, durMs);
    };
    const cached = getDetailWaveCached(tid);
    if (cached) drawAll(cached);
    else whenDetailWave(tid, drawAll);
  });
  // External tracks: waveform + cues from the source player's ANLZ over NFS.
  // The endpoint 404s until the download lands, so a miss just retries next poll.
  el.querySelectorAll('canvas[data-ext-wave]').forEach(canvas => {
    const url = canvas.getAttribute('data-ext-wave');
    if (!url) return;
    if (extAnalysisCache.has(url)) { drawExtWave(canvas, extAnalysisCache.get(url)); return; }
    if (extAnalysisInflight.has(url)) return;
    extAnalysisInflight.add(url);
    fetchJSON(url).then(d => {
      extAnalysisInflight.delete(url);
      if (!d) return; // not downloaded yet — retry on the next poll
      extAnalysisCache.set(url, d);
      const cur = document.querySelector(`canvas[data-ext-wave="${url}"]`);
      if (cur) drawExtWave(cur, d);
    });
  });
  // Phrase strip under each loaded deck's waveform. Phrases are fetched per
  // track and cached (the 1Hz poll re-creates these elements every second, so
  // re-fetching each time would hammer the endpoint); a new load changes the
  // track_id, which misses the cache and fetches fresh.
  el.querySelectorAll('[data-phrases-track]').forEach(host => {
    const tid = parseInt(host.getAttribute('data-phrases-track'), 10);
    if (!tid) return;
    const p = players.find(pp => pp.track_id === tid);
    const durMs = (p && p.duration_ms) || 0;
    if (playerPhrasesCache.has(tid)) {
      renderPlayerPhraseStrip(host, playerPhrasesCache.get(tid), durMs);
    } else {
      fetchJSON('/api/tracks/' + tid + '/phrases').then(ph => {
        const arr = Array.isArray(ph) ? ph : [];
        playerPhrasesCache.set(tid, arr);
        // The poll may have re-rendered the grid since the fetch began — find
        // the current strip for this track (if any deck still shows it).
        const cur = document.querySelector(`[data-phrases-track="${tid}"]`);
        if (cur) renderPlayerPhraseStrip(cur, arr, durMs);
      });
    }
  });
  // Cover art: reuse one persistent <img> per deck, moving it into the freshly
  // rendered slot (re-parenting a loaded <img> doesn't reload or blink it). Only
  // change src when the track changes. External art may 404 until its NFS
  // download lands, so retry those (and only those) on error each tick.
  el.querySelectorAll('.deck-art-slot').forEach(slot => {
    const dev = slot.getAttribute('data-art-dev');
    const url = slot.getAttribute('data-art-url');
    let img = deckArtImgs.get(dev);
    if (!img) {
      img = new Image();
      img.className = 'deck-art';
      img.alt = '';
      img.onload = () => { img.dataset.state = 'ok'; img.style.display = ''; };
      img.onerror = () => { img.dataset.state = 'error'; img.style.display = 'none'; };
      deckArtImgs.set(dev, img);
    }
    if (img.dataset.url !== url) {
      img.dataset.url = url;
      img.dataset.state = 'loading';
      img.src = url;
    } else if (img.dataset.state === 'error' && url.indexOf('/artwork/ext/') >= 0) {
      img.dataset.state = 'loading';
      img.src = url + (url.indexOf('?') >= 0 ? '&' : '?') + '_r=' + Date.now();
    }
    slot.appendChild(img);
  });
  for (const dev of Array.from(deckArtImgs.keys())) {
    if (!players.find(p => String(p.device_number) === String(dev))) deckArtImgs.delete(dev);
  }
  // Snapshot each player's position from the poll and (re)start the
  // playhead-interpolation loop. Sampling at poll time + rAF interpolation
  // between samples keeps the playhead visually smooth despite 1Hz polling.
  players.forEach(samplePlayerPosition);
  schedulePlayheadFrame();
  // Wire title click → open the detail drawer, FIND button → jump to library.
  el.querySelectorAll('[data-open-detail]').forEach(t => {
    t.addEventListener('click', () => {
      const tid = parseInt(t.getAttribute('data-open-detail'), 10);
      openDetail(tid);
    });
  });
  el.querySelectorAll('[data-find-track]').forEach(btn => {
    btn.addEventListener('click', () => {
      const tid = parseInt(btn.getAttribute('data-find-track'), 10);
      findTrackInLibrary(tid);
    });
  });
}

// Switches to the Library tab, clears any active filter, scrolls the
// matching row into view and briefly highlights it. Called from the FIND
// button on each player card.
function findTrackInLibrary(trackID) {
  // Clear any filter that could be hiding the track row.
  // FIND from a player card — clear filter and force-jump to COLLECTION
  // so the matching track is reachable even if a playlist was active.
  const search = document.getElementById('lib-search');
  if (search && search.value) search.value = '';
  selectedSource = 'collection';
  plSelectedID = null;
  plSelectedTracks = [];
  refreshLibraryView();
  // Activate the library tab via the existing tab-switch wiring.
  const tab = document.querySelector('.tab[data-tab="library"]');
  if (tab) tab.click();
  // Defer the scroll a tick so the panel is rendered after the tab switch.
  setTimeout(() => {
    const row = document.querySelector(`tr[data-track-row="${trackID}"]`);
    if (!row) {
      toast(`TRACK ${String(trackID).padStart(4, '0')} NOT IN VISIBLE ROWS`, 'error');
      return;
    }
    row.scrollIntoView({ behavior: 'smooth', block: 'center' });
    row.classList.remove('flash-highlight');
    // Re-trigger the CSS animation by forcing a reflow.
    void row.offsetWidth;
    row.classList.add('flash-highlight');
  }, 30);
}

// keyed by device_number → {posMs, durationMs, isPlaying, rate, sampledAt}
const playerPosState = new Map();
let playheadRafPending = false;

// fmtMMSS formats a millisecond count as M:SS (no leading minute zero,
// always two-digit seconds). Negative input is treated as 0.
function fmtMMSS(ms) {
  if (!ms || ms < 0) ms = 0;
  const s = Math.floor(ms / 1000);
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}

// initialTimeText computes the elapsed or remaining string for a
// player at render time, so the time row paints correct on first
// frame instead of flashing "0:00 / 0:00" until the rAF loop ticks.
function initialTimeText(p, which) {
  const s = playerPosState.get(p.device_number);
  if (!s || !s.durationMs) return which === 'remaining' ? '-0:00' : '0:00';
  let pos = s.posMs;
  if (s.isPlaying) pos += (performance.now() - s.sampledAt) * s.rate;
  if (pos < 0) pos = 0;
  if (pos > s.durationMs) pos = s.durationMs;
  return which === 'remaining' ? '-' + fmtMMSS(s.durationMs - pos) : fmtMMSS(pos);
}

// Renders the playhead element with its initial position already baked in,
// so when renderPlayers re-creates the element each 1s poll it doesn't
// flash from hidden → visible. Falls back to .hidden when we don't yet
// have a sampled position (first render after fresh page load).
function initialPlayheadMarkup(p) {
  const s = playerPosState.get(p.device_number);
  if (!s || !s.durationMs) {
    return `<div class="playhead hidden" data-dev="${p.device_number}"></div>`;
  }
  let pos = s.posMs;
  if (s.isPlaying) {
    pos += (performance.now() - s.sampledAt) * s.rate;
  }
  pos = Math.max(0, Math.min(s.durationMs, pos));
  const pct = ((pos / s.durationMs) * 100).toFixed(3);
  return `<div class="playhead" data-dev="${p.device_number}" style="left: ${pct}%"></div>`;
}

function samplePlayerPosition(p) {
  // CDJ reports beats elapsed since track start (beat_in_track, 1-based).
  // External tracks carry no library duration and the status BPM is the pitched
  // playback tempo, so prefer the ANLZ we fetched for the waveform: it gives the
  // real beat grid (exact beat times) plus the nominal tempo and duration.
  let bpm = p.bpm, durationMs = p.duration_ms || 0, beats = null;
  if (p.external && p.analysis_url) {
    const d = extAnalysisCache.get(p.analysis_url);
    if (d) {
      if (d.bpm > 0) bpm = d.bpm;
      if (d.duration_ms) durationMs = d.duration_ms;
      if (d.beats && d.beats.length) beats = d.beats;
    }
  }
  let posMs = 0;
  if (beats && p.beat_in_track > 0 && p.beat_in_track <= beats.length) {
    posMs = beats[p.beat_in_track - 1]; // exact beat time from the grid
  } else if (p.beat_in_track > 0 && bpm > 0) {
    posMs = (p.beat_in_track - 1) * 60000 / bpm;
  }
  // Pitch_pct is what % above/below nominal speed the deck is set to.
  const rate = 1 + ((p.pitch_pct || 0) / 100);
  // beat_age_ms is the server's sub-beat phase (time since that beat fired);
  // without it the playhead quantizes to whole beats. Capped at one beat so
  // stalled packets hold at the next beat instead of running ahead.
  let age = p.beat_age_ms || 0;
  if (bpm > 0) age = Math.min(age, 60000 / bpm);
  posMs += age * rate;
  playerPosState.set(p.device_number, {
    posMs,
    durationMs,
    isPlaying: !!p.is_playing,
    rate,
    sampledAt: performance.now(),
  });
}

function schedulePlayheadFrame() {
  if (playheadRafPending) return;
  playheadRafPending = true;
  requestAnimationFrame(tickPlayheads);
}

function tickPlayheads() {
  playheadRafPending = false;
  const now = performance.now();
  let anyPlaying = false;
  // Per-device interpolated position, computed once and reused for the
  // playhead element and the elapsed/remaining text below the wave.
  const positions = new Map();
  document.querySelectorAll('.wave-wrap .playhead').forEach(el => {
    const dev = parseInt(el.getAttribute('data-dev'), 10);
    const s = playerPosState.get(dev);
    if (!s || !s.durationMs) {
      el.classList.add('hidden');
      return;
    }
    let pos = s.posMs;
    if (s.isPlaying) {
      pos += (now - s.sampledAt) * s.rate;
      anyPlaying = true;
    }
    if (pos < 0) pos = 0;
    if (pos > s.durationMs) pos = s.durationMs;
    el.style.left = ((pos / s.durationMs) * 100).toFixed(3) + '%';
    el.classList.remove('hidden');
    positions.set(dev, { pos, durationMs: s.durationMs });
  });
  document.querySelectorAll('.player-time').forEach(el => {
    const dev = parseInt(el.getAttribute('data-dev'), 10);
    const p = positions.get(dev);
    if (!p) return;
    const elapsed = el.querySelector('.elapsed');
    const remaining = el.querySelector('.remaining');
    if (elapsed) elapsed.textContent = fmtMMSS(p.pos);
    if (remaining) remaining.textContent = '-' + fmtMMSS(p.durationMs - p.pos);
  });
  // Keep animating while at least one player is moving; paused / cued
  // playheads stay frozen at their last sampled position until the next poll.
  if (anyPlaying) schedulePlayheadFrame();
}

