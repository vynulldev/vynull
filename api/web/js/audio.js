// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── DETAIL-DRAWER AUDIO PLAYER ─────────
// Plays the open track through /api/audio/{id} so cues, loops and the beat
// grid can be checked by ear without a CDJ. The zoom cursor (detailWave.centerMs)
// is the playhead: while playing, the audio clock drives it; while paused,
// panning/jumping just moves it and PLAY starts from there. Moving the cursor
// during playback seeks the audio.
const detailPlayer = {
  audio:     null,   // HTMLAudioElement for detailPlayer.trackID
  trackID:   null,
  transcode: false,  // force the ffmpeg WAV path (same decoder as the analysis)
  source:    '',     // 'direct' | 'transcoded', from X-Audio-Source
  metro:     false,
  loop:      null,   // {start,end} in ms while a saved loop is active
  raf:       0,
  driving:   false,  // true while the player itself moves centerMs
  actx:      null,   // AudioContext for the metronome clicks
  lastBeat:  -1,     // index of the last beat already scheduled
  lastDraw:  0,
  padsKey:   '',
};
const PLAYER_LOOKAHEAD_MS = 150;
const PLAYER_DRAW_MS = 33;

function detailPlayerActive() {
  return !!(detailPlayer.audio && !detailPlayer.audio.paused);
}
function detailPlayerEl(id) { return document.getElementById(id); }

// Create (or reuse) the audio element for the open track.
function detailPlayerEnsure() {
  const id = detailWave.trackID;
  if (!id) return null;
  if (detailPlayer.audio && detailPlayer.trackID === id) return detailPlayer.audio;
  detailPlayerStop(true);
  const a = new Audio();
  a.preload = 'auto';
  a.addEventListener('error', () => { toast('AUDIO LOAD FAILED', 'error'); detailPlayerSyncUI(); });
  a.addEventListener('ended', () => { detailPlayerStopLoop(); detailPlayerSyncUI(); });
  a.addEventListener('loadedmetadata', detailPlayerSyncUI);
  detailPlayer.audio = a;
  detailPlayer.trackID = id;
  detailPlayerSetSrc(0);
  return a;
}
function detailPlayerSetSrc(startMs) {
  const a = detailPlayer.audio, id = detailPlayer.trackID;
  const url = '/api/audio/' + id + (detailPlayer.transcode ? '?transcode=1' : '');
  a.src = url;
  a.addEventListener('loadedmetadata', function once() {
    a.removeEventListener('loadedmetadata', once);
    a.currentTime = Math.max(0, startMs) / 1000;
  });
  fetch(url, { method: 'HEAD' }).then(r => {
    detailPlayer.source = r.headers.get('X-Audio-Source') || '';
    detailPlayerSyncUI();
  }).catch(() => {});
}

// Release the audio element and any clicks. keepUI=true skips the repaint
// (used when switching tracks inside detailPlayerEnsure).
function detailPlayerStop(keepUI) {
  cancelAnimationFrame(detailPlayer.raf);
  detailPlayer.raf = 0;
  if (detailPlayer.audio) {
    detailPlayer.audio.pause();
    detailPlayer.audio.removeAttribute('src');
    detailPlayer.audio.load();
  }
  detailPlayer.audio = null;
  detailPlayer.trackID = null;
  detailPlayer.loop = null;
  detailPlayer.source = '';
  detailPlayer.lastBeat = -1;
  if (!keepUI) detailPlayerSyncUI();
}
function detailPlayerStopLoop() { detailPlayer.loop = null; }

function detailPlayerPlay() {
  const a = detailPlayerEnsure();
  if (!a) return;
  if (detailPlayer.metro) detailPlayerMetroInit();
  const ms = Math.max(0, Math.min(detailWave.durationMs, detailWave.centerMs));
  if (a.readyState >= 1) a.currentTime = ms / 1000;
  else a.addEventListener('loadedmetadata', function once() {
    a.removeEventListener('loadedmetadata', once);
    a.currentTime = ms / 1000;
  });
  detailPlayer.lastBeat = -1;
  a.play().then(() => {
    detailPlayerSyncUI();
    if (!detailPlayer.raf) detailPlayer.raf = requestAnimationFrame(detailPlayerTick);
  }).catch(() => toast('PLAYBACK BLOCKED', 'error'));
}
function detailPlayerPause() {
  const a = detailPlayer.audio;
  if (!a) return;
  a.pause();
  cancelAnimationFrame(detailPlayer.raf);
  detailPlayer.raf = 0;
  detailPlayerSyncUI();
}
function detailPlayerToggle() {
  if (detailPlayerActive()) detailPlayerPause(); else detailPlayerPlay();
}

// Called from detailZoomCenterTo for every user-driven cursor move.
function detailPlayerOnUserMove(ms) {
  const a = detailPlayer.audio;
  if (!a || detailPlayer.driving || detailPlayer.trackID !== detailWave.trackID) return;
  if (a.readyState >= 1) a.currentTime = Math.max(0, ms) / 1000;
  detailPlayer.lastBeat = -1;
  // Moving outside the active loop leaves it, as on a CDJ.
  const lp = detailPlayer.loop;
  if (lp && (ms < lp.start || ms >= lp.end)) detailPlayer.loop = null;
}

function detailPlayerTick() {
  detailPlayer.raf = 0;
  const a = detailPlayer.audio;
  if (!a || a.paused || detailPlayer.trackID !== detailWave.trackID) return;
  let ms = a.currentTime * 1000;
  const lp = detailPlayer.loop;
  if (lp && ms >= lp.end) {
    a.currentTime = lp.start / 1000;
    ms = lp.start;
    detailPlayer.lastBeat = -1;
  }
  if (detailPlayer.metro) detailPlayerScheduleClicks(ms);
  const now = performance.now();
  if (now - detailPlayer.lastDraw >= PLAYER_DRAW_MS) {
    detailPlayer.lastDraw = now;
    detailPlayer.driving = true;
    detailWave.centerMs = ms;
    redrawDetailWaveform();
    detailPlayer.driving = false;
    detailPlayerSyncInfo();
  }
  detailPlayer.raf = requestAnimationFrame(detailPlayerTick);
}

// Metronome: a short click per beat of the analysed grid (higher pitch on the
// downbeat), scheduled slightly ahead on the Web Audio clock. If the clicks
// land off the audible beats, the grid (or the decoder alignment, see the WAV
// toggle) is off.
function detailPlayerMetroInit() {
  if (!detailPlayer.actx) detailPlayer.actx = new (window.AudioContext || window.webkitAudioContext)();
  if (detailPlayer.actx.state === 'suspended') detailPlayer.actx.resume();
}
function detailPlayerScheduleClicks(posMs) {
  const beats = detailWave.beats, ctx = detailPlayer.actx;
  if (!beats || !beats.length || !ctx) return;
  let i = detailPlayer.lastBeat + 1;
  if (detailPlayer.lastBeat < 0) {
    // First beat at or after the playhead (binary search).
    let lo = 0, hi = beats.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (beats[mid].time_ms < posMs) lo = mid + 1; else hi = mid;
    }
    i = lo;
  }
  const loopEnd = detailPlayer.loop ? detailPlayer.loop.end : Infinity;
  for (; i < beats.length; i++) {
    const dt = beats[i].time_ms - posMs;
    if (dt > PLAYER_LOOKAHEAD_MS || beats[i].time_ms >= loopEnd) break;
    detailPlayerClick(ctx.currentTime + Math.max(0, dt) / 1000, beats[i].beat_in_bar === 1);
    detailPlayer.lastBeat = i;
  }
}
function detailPlayerClick(when, accent) {
  const ctx = detailPlayer.actx;
  const osc = ctx.createOscillator(), g = ctx.createGain();
  osc.frequency.value = accent ? 1760 : 1100;
  g.gain.setValueAtTime(0.0001, when);
  g.gain.exponentialRampToValueAtTime(0.5, when + 0.002);
  g.gain.exponentialRampToValueAtTime(0.0001, when + 0.05);
  osc.connect(g).connect(ctx.destination);
  osc.start(when);
  osc.stop(when + 0.06);
}

// Jump to a cue: seeks and starts playing from it (like pressing a hot-cue
// pad). A saved loop also becomes the active loop.
function detailPlayerJumpToCue(cue) {
  if (!cue) return;
  const ms = cue.time_ms;
  const loop = (cue.type === 'loop' && cue.loop_ms > ms) ? { start: ms, end: cue.loop_ms } : null;
  const wasPlaying = detailPlayerActive();
  detailZoomCenterTo(ms);            // seeks via detailPlayerOnUserMove
  detailPlayer.loop = loop;
  if (!wasPlaying) detailPlayerPlay();
  detailPlayerSyncUI();
}
function detailPlayerSortedCues() {
  return (detailWave.cues || []).slice().sort((a, b) => a.time_ms - b.time_ms);
}
// Previous / next cue (hot or memory) relative to the playhead.
function detailPlayerStepCue(dir) {
  const cues = detailPlayerSortedCues();
  const ms = detailWave.centerMs;
  const eps = 50;
  const c = dir > 0 ? cues.find(c => c.time_ms > ms + eps)
                    : cues.slice().reverse().find(c => c.time_ms < ms - eps);
  if (c) { detailZoomCenterTo(c.time_ms); }
}
function detailPlayerToggleMetro() {
  detailPlayer.metro = !detailPlayer.metro;
  if (detailPlayer.metro) { detailPlayerMetroInit(); detailPlayer.lastBeat = -1; }
  detailPlayerSyncUI();
}
function detailPlayerToggleTranscode() {
  detailPlayer.transcode = !detailPlayer.transcode;
  const a = detailPlayer.audio;
  if (a) {
    const wasPlaying = !a.paused, pos = a.currentTime * 1000;
    a.pause();
    detailPlayerSetSrc(pos);
    if (wasPlaying) {
      a.addEventListener('loadedmetadata', function once() {
        a.removeEventListener('loadedmetadata', once);
        a.play().catch(() => {});
        if (!detailPlayer.raf) detailPlayer.raf = requestAnimationFrame(detailPlayerTick);
      });
    }
  }
  detailPlayerSyncUI();
}

// ── controls UI ──
function detailPlayerSyncInfo() {
  const el = detailPlayerEl('pl-info');
  if (!el) return;
  const a = detailPlayer.audio;
  let txt = detailPlayer.source ? detailPlayer.source.toUpperCase() : '';
  if (a && isFinite(a.duration) && detailWave.durationMs) {
    // Browser length vs the analysed length: a big gap means the playhead
    // won't line up with the grid and cue times.
    const d = a.duration * 1000 - detailWave.durationMs;
    txt += ' · Δlen ' + (d >= 0 ? '+' : '') + Math.round(d) + 'ms';
  }
  if (detailPlayer.loop) txt += ' · LOOP ' + fmtCueTime(detailPlayer.loop.start) + '–' + fmtCueTime(detailPlayer.loop.end);
  el.textContent = txt;
}
function detailPlayerSyncUI() {
  const play = detailPlayerEl('pl-play');
  if (!play) return;
  const on = detailPlayerActive();
  play.textContent = on ? '❚❚ PAUSE' : '▶ PLAY';
  play.classList.toggle('active', on);
  detailPlayerEl('pl-metro').classList.toggle('active', detailPlayer.metro);
  detailPlayerEl('pl-wav').classList.toggle('active', detailPlayer.transcode);
  detailPlayerSyncInfo();
  detailPlayerRenderPads(true);
}
// One pad per cue (hot cues A–H by number, memory cues as M1…); rebuilt only
// when the cue list changes.
function detailPlayerRenderPads(force) {
  const box = detailPlayerEl('pl-pads');
  if (!box) return;
  const cues = detailPlayerSortedCues();
  const lp = detailPlayer.loop;
  const key = cues.map(c => [c.number, c.type, c.time_ms, c.loop_ms].join(':')).join('|') + '|' + (lp ? lp.start : '');
  if (!force && key === detailPlayer.padsKey && box.childElementCount) return;
  detailPlayer.padsKey = key;
  box.textContent = '';
  for (const c of cues) {
    const b = document.createElement('button');
    b.type = 'button';
    const hot = c.number >= 1 && c.number <= 8;
    b.textContent = hot ? String.fromCharCode(64 + c.number) : 'M';
    b.title = (hot ? 'Hot cue ' + b.textContent : 'Memory cue') + ' · ' + fmtCueTimeMs(c.time_ms) +
              (c.type === 'loop' ? ' · loop' : '') + (hot ? ' · key ' + c.number : '');
    if (c.type === 'loop') b.textContent += '⟲';
    if (lp && lp.start === c.time_ms) b.classList.add('active');
    b.addEventListener('click', () => { b.blur(); detailPlayerJumpToCue(c); });
    box.appendChild(b);
  }
}
// Called from renderDetail once the drawer DOM exists.
function detailPlayerBind(t) {
  if (detailPlayer.trackID && detailPlayer.trackID !== t.id) detailPlayerStop(true);
  const on = (id, fn) => detailPlayerEl(id).addEventListener('click', e => { e.currentTarget.blur(); fn(); });
  on('pl-play', detailPlayerToggle);
  on('pl-metro', detailPlayerToggleMetro);
  on('pl-wav', detailPlayerToggleTranscode);
  detailPlayer.padsKey = '';
  detailPlayerSyncUI();
}

// Keyboard (drawer open, not typing): space play/pause · 1–8 hot cues ·
// [ ] previous/next cue · ←/→ one beat · M metronome · L leave loop.
document.addEventListener('keydown', e => {
  if (selectedTrackID == null || detailWave.trackID !== selectedTrackID) return;
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  const tg = e.target;
  if (tg && (tg.tagName === 'INPUT' || tg.tagName === 'TEXTAREA' || tg.tagName === 'SELECT' || tg.isContentEditable)) return;
  if (!detailPlayerEl('pl-play')) return;
  let handled = true;
  if (e.key === ' ') detailPlayerToggle();
  else if (e.key >= '1' && e.key <= '8') {
    const c = (detailWave.cues || []).find(c => c.number === parseInt(e.key, 10));
    if (c) detailPlayerJumpToCue(c); else handled = false;
  }
  else if (e.key === '[') detailPlayerStepCue(-1);
  else if (e.key === ']') detailPlayerStepCue(+1);
  else if (e.key === 'ArrowLeft')  detailJumpBeats(e.shiftKey ? -4 : -1);
  else if (e.key === 'ArrowRight') detailJumpBeats(e.shiftKey ? 4 : 1);
  else if (e.key === 'm' || e.key === 'M') detailPlayerToggleMetro();
  else if (e.key === 'l' || e.key === 'L') { detailPlayer.loop = null; detailPlayerSyncUI(); }
  else handled = false;
  if (handled) e.preventDefault();
});

// Locate a cue marker within `tolMs` of `ms`. When multiple cues match
// (e.g., overlapping at small zoom levels), the closest one wins.
function findDetailCueNear(ms, tolMs) {
  if (!detailWave.cues) return null;
  let best = null, bestDist = Infinity;
  for (const c of detailWave.cues) {
    const d = Math.abs(c.time_ms - ms);
    if (d <= tolMs && d < bestDist) { best = c; bestDist = d; }
  }
  return best;
}

// Lowest unused number in 1..8 (the hot-cue range). Returns null when
// every slot is taken — caller should warn the user instead of
// silently picking a memory-cue slot.
function nextFreeHotCueNumber() {
  const used = new Set((detailWave.cues || []).map(c => c.number));
  for (let n = 1; n <= 8; n++) if (!used.has(n)) return n;
  return null;
}

// Lowest unused number from 9 up (the memory-cue range, matching how
// imported rekordbox memory cues are numbered). No fixed slot cap.
function nextFreeMemoryCueNumber() {
  const used = new Set((detailWave.cues || []).map(c => c.number));
  let n = 9;
  while (used.has(n)) n++;
  return n;
}

// POST a cue update + patch local caches so the redraw and the CUES
// section pick it up before the 1Hz poll. Adds the cue to
// detailWave.cues if it's new; otherwise the in-place mutation done
// by the drag handler is already reflected.
async function saveDetailCue(cue) {
  const trackID = detailWave.trackID;
  if (!trackID) return;
  // Insert if it's a fresh cue (not already in detailWave.cues by number).
  if (!detailWave.cues.some(c => c.number === cue.number)) {
    detailWave.cues.push(cue);
    detailWave.cues.sort((a, b) => a.time_ms - b.time_ms);
  }
  // Mirror into cuesCache so library row markers + players-tab waveform
  // pick up the change on the next poll-driven re-render.
  cuesCache.set(Number(trackID), detailWave.cues.slice());
  redrawDetailWaveform();
  const r = await fetch(`/api/tracks/${trackID}/cues`, {
    method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify(cue),
  });
  if (!r.ok) {
    toast('SAVE CUE FAILED', 'error');
    return;
  }
  toast(`CUE ${cueLabel(cue.number)} · ${fmtCueTime(cue.time_ms)}`, 'success');
  // Refresh the CUES list block inside the drawer so the row appears
  // (or its time updates). Cheapest path: re-render the whole detail.
  if (selectedTrackID === trackID) openDetail(trackID);
}

function fmtCueTime(ms) {
  const s = Math.floor(ms / 1000);
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0') + '.' +
         String(Math.floor(ms / 100) % 10);
}
// Like fmtCueTime but with millisecond precision (m:ss.mmm) — used for the
// zoom-waveform cursor readout where exact placement matters.
function fmtCueTimeMs(ms) {
  const s = Math.floor(ms / 1000);
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0') + '.' +
         String(Math.floor(ms) % 1000).padStart(3, '0');
}

// POST /api/analysis/beatgrid/adjust with set_downbeat_ms=targetMs.
// The server (handleBeatGridAdjust) shifts the entire grid so the
// nearest existing beat lands at targetMs AND marks that beat as
// beat 1 — so clicking anywhere on the audio's true downbeat will
// realign an off-grid track without the user having to fix the
// phase first. Don't pre-snap on the client: that would destroy the
// click position and the server would compute offset=0.
async function setDetailDownbeat(targetMs) {
  const trackID = detailWave.trackID;
  if (!trackID) return;
  const target = Math.max(0, Math.min(detailWave.durationMs, targetMs));
  const r = await fetch('/api/analysis/beatgrid/adjust', {
    method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ track_id: trackID, set_downbeat_ms: target }),
  });
  if (!r.ok) { toast('SET DOWNBEAT FAILED', 'error'); return; }
  toast('DOWNBEAT · ' + fmtCueTime(Math.round(target)), 'success');
  if (selectedTrackID === trackID) openDetail(trackID);
}

// DELETE /api/tracks/{id}/cues/{number} + patch local caches so the
// removed cue disappears from the waveform + CUES list immediately.
async function deleteDetailCue(cueNumber) {
  const trackID = detailWave.trackID;
  if (!trackID) return;
  const idx = detailWave.cues.findIndex(c => c.number === cueNumber);
  if (idx < 0) return;
  const removed = detailWave.cues.splice(idx, 1)[0];
  cuesCache.set(Number(trackID), detailWave.cues.slice());
  redrawDetailWaveform();
  const r = await fetch(`/api/tracks/${trackID}/cues/${cueNumber}`, { method: 'DELETE' });
  if (!r.ok) {
    // Put it back so the UI doesn't lie about state.
    detailWave.cues.splice(idx, 0, removed);
    cuesCache.set(Number(trackID), detailWave.cues.slice());
    redrawDetailWaveform();
    toast('DELETE CUE FAILED', 'error');
    return;
  }
  toast(`CUE ${cueLabel(cueNumber)} DELETED`, 'success');
  if (selectedTrackID === trackID) openDetail(trackID);
}

// Draws cue markers (and loop bands) on a canvas-based waveform.
// Call after the waveform itself has been drawn so markers sit on top.
//   canvas    — HTMLCanvasElement
//   cues      — [{number, time_ms, loop_ms, color_id, type}]
//   durationMs — track duration in milliseconds; if 0, nothing is drawn
//   opts.labelHeight — if >= 12, draw the cue letter label in a small badge
//                      above the triangle. Default: 14 (auto-show).
function drawCueMarkersCanvas(canvas, cues, durationMs, opts) {
  if (!cues || cues.length === 0 || !durationMs) return;
  const ctx = canvas.getContext('2d');
  const W = canvas.width, H = canvas.height;
  const labelHeight = (opts && typeof opts.labelHeight === 'number') ? opts.labelHeight : 14;
  const showLabels = labelHeight >= 12 && H >= 40;
  ctx.save();
  for (const c of cues) {
    const x = Math.round((c.time_ms / durationMs) * W);
    if (x < 0 || x > W) continue;
    const color = CUE_COLORS[c.color_id] || CUE_COLORS[0];
    // Loops: paint a faint colored band between start and end.
    if (c.type === 'loop' && c.loop_ms > 0) {
      const x2 = Math.min(W, Math.round((c.loop_ms / durationMs) * W));
      if (x2 > x) {
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.18;
        ctx.fillRect(x, 0, x2 - x, H);
        ctx.globalAlpha = 1;
      }
    }
    // Vertical line at the cue position. We paint a black "halo" first
    // (3px wide), then the coloured core (2px wide) on top so the marker
    // stands out against any waveform colour underneath. Top of the line
    // is offset down by the label badge height so the letter sits above
    // the waveform.
    const lineTop = showLabels ? labelHeight : 0;
    ctx.fillStyle = 'rgba(0,0,0,0.7)';
    ctx.fillRect(x - 1, lineTop, 4, H - lineTop);
    ctx.fillStyle = color;
    ctx.fillRect(x, lineTop, 2, H - lineTop);
    if (showLabels) {
      // Filled badge with the letter inside, framed by the same black halo.
      const label = cueLabel(c.number);
      ctx.font = '700 11px "Geist Mono", monospace';
      const textW = Math.ceil(ctx.measureText(label).width);
      const padX = 4;
      const badgeW = Math.max(14, textW + padX * 2);
      const badgeH = labelHeight - 2;
      const badgeX = Math.round(x - badgeW / 2 + 1);
      // Halo
      ctx.fillStyle = 'rgba(0,0,0,0.7)';
      ctx.fillRect(badgeX - 1, -1, badgeW + 2, badgeH + 2);
      // Coloured fill
      ctx.fillStyle = color;
      ctx.fillRect(badgeX, 0, badgeW, badgeH);
      // Pointer triangle below the badge so the eye traces down to the line.
      ctx.beginPath();
      ctx.moveTo(x - 5, badgeH);
      ctx.lineTo(x + 5 + 1, badgeH);
      ctx.lineTo(x + 1, badgeH + 4);
      ctx.closePath();
      ctx.fill();
      // Letter text — black for warm colours (yellow/orange/aqua), white otherwise.
      ctx.fillStyle = isWarmCueColor(c.color_id) ? '#000' : '#fff';
      ctx.textBaseline = 'middle';
      ctx.textAlign = 'center';
      ctx.fillText(label, x + 1, Math.floor(badgeH / 2) + 1);
    } else {
      // Compact mode: larger filled triangle at the top so the cue position
      // is visible even when no letter badge fits.
      ctx.fillStyle = 'rgba(0,0,0,0.7)';
      ctx.beginPath();
      ctx.moveTo(x - 5, -1);
      ctx.lineTo(x + 6, -1);
      ctx.lineTo(x + 0.5, 7);
      ctx.closePath();
      ctx.fill();
      ctx.fillStyle = color;
      ctx.beginPath();
      ctx.moveTo(x - 4, 0);
      ctx.lineTo(x + 5, 0);
      ctx.lineTo(x + 0.5, 6);
      ctx.closePath();
      ctx.fill();
    }
  }
  ctx.restore();
}

// Picks black or white text for a cue badge based on the perceived
// brightness of the swatch — keeps the letter readable across the whole
// rekordbox palette without hard-coding which IDs are "warm". Uses the
// standard W3C-style luminance formula.
function isWarmCueColor(colorID) {
  const hex = CUE_COLORS[colorID] || '';
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})/i.exec(hex);
  if (!m) return false;
  const r = parseInt(m[1], 16), g = parseInt(m[2], 16), b = parseInt(m[3], 16);
  // Perceived brightness 0..255; > 145 ≈ light enough that black text reads better.
  return (r * 0.299 + g * 0.587 + b * 0.114) > 145;
}

// Builds inline HTML for the small library-row thumbnail cue markers.
// Returns an empty string if there are no cues or no known duration so
// the library renderer can drop it straight into the template.
function cueMarkerOverlay(trackID, durationSec) {
  if (!durationSec) return '';
  const cues = getCuesFor(trackID);
  if (cues.length === 0) return '';
  const durMs = durationSec * 1000;
  return cues.map(c => {
    const color = CUE_COLORS[c.color_id] || CUE_COLORS[0];
    const pct = (c.time_ms / durMs) * 100;
    if (pct < 0 || pct > 100) return '';
    let html = '';
    if (c.type === 'loop' && c.loop_ms > 0) {
      const endPct = Math.min(100, (c.loop_ms / durMs) * 100);
      const w = endPct - pct;
      if (w > 0) {
        html += `<span class="cue-loop" style="left:${pct.toFixed(3)}%; width:${w.toFixed(3)}%; color:${color}"></span>`;
      }
    }
    // No letter badge in the library — the 28px-tall thumbnail doesn't
    // have room for it without occluding the waveform. The detail drawer
    // and player canvases show full A/B/C labels in their cue overlays.
    html += `<span class="cue-mark" style="left:${pct.toFixed(3)}%; background:${color}; color:${color}" title="${escape(cueLabel(c.number))}"></span>`;
    return html;
  }).join('');
}

