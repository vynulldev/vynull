// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// Two formats cached per track:
//   color_preview = PWV4, 1200 entries of {bass,mid,treble} 0-255. Used for the
//                   big player-card waveform — shows absolute amplitude faithfully.
//   detail        = PWV5, 150 entries/sec of {r,g,b,h} where r/g/b ∈ [0,7] are
//                   per-entry-relative band weights and h ∈ [0,31] is height.
//                   Used for the small library-row thumbnail — vivid colours
//                   regardless of track amplitude.
const waveformCache = new Map();        // `${type}:${trackID}` -> data
// The fetch in progress for a key: a second request for the same data waits on
// it instead of getting nothing.
const inflightWaveforms = new Map();    // same key -> promise of the data

function ensureWaveform(trackID, type) {
  const key = `${type}:${trackID}`;
  if (waveformCache.has(key)) return Promise.resolve(waveformCache.get(key));
  if (!inflightWaveforms.has(key)) {
    inflightWaveforms.set(key, (async () => {
      try {
        const data = await fetchJSON(`/api/analysis/waveform/${trackID}?type=${type}`);
        if (data && data.entries) waveformCache.set(key, data);
        return data;
      } finally {
        inflightWaveforms.delete(key);
      }
    })());
  }
  return inflightWaveforms.get(key);
}

// rekordbox's 3-band bass envelope is smooth, but ours arrives ~5× jaggier
// because the analysis RMS window (6.7 ms) is shorter than a bass cycle, so it
// wobbles with sub-cycle phase. Match rekordbox's look with a per-band moving
// average — widest on bass, none on treble (already smooth). Applied once when
// the data loads; the served analysis (and the CDJ path) is untouched.
const BAND3_SMOOTH = { bass: 9, mid: 5, high: 1 };
function smooth3Band(entries) {
  const n = entries.length;
  if (n < 3) return;
  for (const band of ['bass', 'mid', 'high']) {
    const w = BAND3_SMOOTH[band];
    if (w <= 1) continue;
    const half = w >> 1;
    const src = new Float32Array(n);
    for (let i = 0; i < n; i++) src[i] = entries[i][band];
    for (let i = 0; i < n; i++) {
      const lo = Math.max(0, i - half), hi = Math.min(n - 1, i + half);
      let sum = 0;
      for (let j = lo; j <= hi; j++) sum += src[j];
      entries[i][band] = sum / (hi - lo + 1);
    }
  }
}

// Tracks the server has no 3-band data for (it answered with no entries): they
// draw the spectral wave in 3band mode.
const no3Band = new Set();

// ensureDetailWave fetches the detail waveform in the representation the
// current colour mode needs: 3band pulls {bass,mid,high} entries (style=3band),
// blue/rgb share the spectral {r,g,b,h} data. 3band is cached separately so
// flipping colour modes doesn't refetch the other. A track with no 3-band data
// gets the spectral wave instead.
function ensureDetailWave(trackID) {
  if (detailWaveStyle() !== '3band' || no3Band.has(trackID)) return ensureWaveform(trackID, 'detail');
  const key = 'detail3band:' + trackID;
  if (waveformCache.has(key)) return Promise.resolve(waveformCache.get(key));
  if (!inflightWaveforms.has(key)) {
    inflightWaveforms.set(key, (async () => {
      try {
        const data = await fetchJSON(`/api/analysis/waveform/${trackID}?type=detail&style=3band`);
        if (data && data.entries && data.entries.length) { smooth3Band(data.entries); waveformCache.set(key, data); return data; }
        // No 3-band data (or the fetch failed, which is retried next time):
        // fall back to spectral so the track still draws.
        if (data) no3Band.add(trackID);
        return await ensureWaveform(trackID, 'detail');
      } finally {
        inflightWaveforms.delete(key);
      }
    })());
  }
  return inflightWaveforms.get(key);
}
// getDetailWaveCached returns the cached detail waveform for the current colour
// mode, or undefined when it hasn't been fetched. In 3band mode the spectral
// wave stands in only for a track known to have no 3-band data: anywhere else,
// returning it would hide that the 3-band data still needs fetching.
function getDetailWaveCached(trackID) {
  if (detailWaveStyle() === '3band' && !no3Band.has(trackID)) {
    return waveformCache.get('detail3band:' + trackID);
  }
  return waveformCache.get('detail:' + trackID);
}
// whenDetailWave calls draw with the track's detail waveform once it is loaded,
// unless the colour mode changed meanwhile: that change asked for the new mode's
// data, and drawing this response would show the old mode.
function whenDetailWave(trackID, draw) {
  const style = detailWaveStyle();
  ensureDetailWave(trackID).then(d => { if (d && detailWaveStyle() === style) draw(d); });
}

// Draws a PWV5 detail waveform (~150 entries/sec, {r,g,b,h} ints where r/g/b
// ∈ [0,7] are per-entry-relative band weights and h ∈ [0,31] is height).
// Used for the library-row thumbnails — colours pop because the dominant band
// is always 7 regardless of overall track amplitude. Subsamples to canvas
// width since detail waveforms have many more entries than pixels.
function drawWaveformPWV5(canvas, data, durationMs, mode) {
  mode = mode || effectiveWaveColor();
  const ctx = canvas.getContext('2d');
  const W = canvas.width, H = canvas.height;
  paintBg(ctx, W, H);
  if (!data || !data.entries || data.entries.length === 0) return;
  const entries = data.entries;
  const n = entries.length;
  // 3-band data carries {bass,mid,high} (0-255) instead of PWV5 {r,g,b,h};
  // detect it so we can fall back to the spectral path when a track has none.
  const is3 = data.style === '3band' || entries[0].bass !== undefined;
  // Map pixels to entries on the waveform's true time base (entry i = i/rate
  // seconds), scaled to the track duration the cue markers + playhead use, so
  // features stay aligned with them across the whole track instead of drifting.
  // Fall back to a plain index split when the duration is unknown.
  const rate = data.sample_rate || 150;
  const total = durationMs > 0 ? durationMs : n * 1000 / rate;
  if (is3) {
    // Whole-track 3-band preview → bottom-anchored vertical stack (blue/orange/white).
    drawStacked3Band(ctx, entries, W, H, x => {
      let s = Math.floor((x / W) * total * rate / 1000);
      let e = Math.ceil(((x + 1) / W) * total * rate / 1000);
      if (e > n) e = n;
      if (e <= s) e = s + 1;
      return [s, e];
    });
    return;
  }
  for (let x = 0; x < W; x++) {
    let start = Math.floor((x / W) * total * rate / 1000);
    if (start >= n) continue;
    let end = Math.ceil(((x + 1) / W) * total * rate / 1000);
    if (end > n) end = n;
    if (end <= start) end = start + 1;
    // Bucket entries into W columns; take max-h within each bucket and average
    // the colour weights so peaks pop while colour stays representative.
    let mh = 0, mr = 0, mg = 0, mb = 0, count = 0;
    for (let i = start; i < end; i++) {
      const e = entries[i];
      if (e.h > mh) mh = e.h;
      mr += e.r; mg += e.g; mb += e.b;
      count++;
    }
    if (mh === 0 || count === 0) continue;
    if (mode === 'blue') {
      ctx.fillStyle = blueColor(mr, mg, mb);
    } else {
      // r/g/b are 0-7 → scale to 0-255 with a tiny boost so saturated bands hit full.
      const r = Math.min(255, Math.round((mr / count) * 38));
      const g = Math.min(255, Math.round((mg / count) * 38));
      const b = Math.min(255, Math.round((mb / count) * 38));
      ctx.fillStyle = `rgb(${r},${g},${b})`;
    }
    // h is 0-31 → scale to canvas height; anchor per overview_type.
    const barH = Math.max(1, Math.round((mh / 31) * H));
    ctx.fillRect(x, barTop(barH, H), 1, barH);
  }
}

function paintBg(ctx, W, H) {
  ctx.clearRect(0, 0, W, H);
  const bg = ctx.createLinearGradient(0, 0, 0, H);
  bg.addColorStop(0, '#040303');
  bg.addColorStop(1, '#020102');
  ctx.fillStyle = bg;
  ctx.fillRect(0, 0, W, H);
}

// Hi-DPI helper: size the canvas backing buffer to its CSS width ×
// devicePixelRatio so 1080p / retina displays get crisp waveforms
// instead of an upscaled 960×96 bitmap. Returns the logical (CSS-px)
// width + height the caller should treat as the drawing area; the
// drawing context is pre-scaled so 1 unit == 1 CSS pixel.
function sizeCanvasHiDPI(canvas, cssH) {
  const dpr = window.devicePixelRatio || 1;
  const cssW = canvas.clientWidth || canvas.parentElement.clientWidth || 960;
  canvas.width = Math.round(cssW * dpr);
  canvas.height = Math.round(cssH * dpr);
  canvas.style.height = cssH + 'px';
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  return { W: cssW, H: cssH };
}

// drawWaveformWindow renders a [startMs..endMs] slice of the PWV5 data
// across the canvas, bucketing the same way drawWaveformPWV5 does but
// over a sub-range instead of the whole track. When startMs/endMs
// cover the full track it produces the same output as drawWaveformPWV5.
// A subtle horizontal centerline is drawn first so the waveform bars
// visually overlay it (matches rekordbox / CDJ display convention).
function drawWaveformWindow(canvas, data, durationMs, startMs, endMs, mode, previewAnchor) {
  mode = mode || effectiveWaveColor();
  const ctx = canvas.getContext('2d');
  // Read post-setTransform logical size from canvas style/parent.
  const dpr = window.devicePixelRatio || 1;
  const W = canvas.width / dpr;
  const H = canvas.height / dpr;
  paintBg(ctx, W, H);
  if (!data || !data.entries || data.entries.length === 0 || !durationMs) return;
  const n = data.entries.length;
  const is3 = data.style === '3band' || data.entries[0].bass !== undefined;
  // The detail waveform is a fixed-rate series (sample_rate entries/sec), so
  // entry i represents true audio time i/sample_rate. Map pixels to entries on
  // that true time base — NOT by stretching n entries across durationMs, which
  // is rounded to whole seconds (Track.Duration) and drifts the waveform
  // against the (true-time) beat grid and cue markers as the track goes on.
  const rate = data.sample_rate || 150;
  const waveMs = n * 1000 / rate;
  const windowMs = endMs - startMs;
  if (windowMs <= 0) return;
  if (is3 && previewAnchor) {
    // Whole-track 3-band preview → bottom-anchored vertical stack (no centerline).
    drawStacked3Band(ctx, data.entries, W, H, x => {
      const cStart = Math.max(0, startMs + (x / W) * windowMs);
      const cEnd = Math.min(waveMs, startMs + ((x + 1) / W) * windowMs);
      if (cEnd <= 0 || cStart >= waveMs) return [0, 0];
      const s = Math.floor(cStart * rate / 1000);
      const e = Math.min(n, Math.max(s + 1, Math.ceil(cEnd * rate / 1000)));
      return [s, e];
    });
    return;
  }
  // Center reference line — the waveform bars draw on top.
  ctx.fillStyle = 'rgba(150, 150, 160, 0.35)';
  ctx.fillRect(0, Math.floor(H / 2), W, 1);
  for (let x = 0; x < W; x++) {
    // Per-pixel virtual time range — startMs/endMs can be negative or past
    // the waveform's end (when the cursor approaches either edge); clamp the
    // slice to [0, waveMs] so out-of-range pixels just show the background.
    const pStart = startMs + (x       / W) * windowMs;
    const pEnd   = startMs + ((x + 1) / W) * windowMs;
    if (pEnd <= 0 || pStart >= waveMs) continue;
    const cStart = Math.max(0, pStart);
    const cEnd   = Math.min(waveMs, pEnd);
    const s = Math.floor(cStart * rate / 1000);
    const e = Math.min(n, Math.max(s + 1, Math.ceil(cEnd * rate / 1000)));
    if (is3) {
      // Per-band peak over the bucket, drawn as three additive overlaid bars.
      let mb = 0, mm = 0, mt = 0;
      for (let i = s; i < e; i++) {
        const en = data.entries[i];
        if (en.bass > mb) mb = en.bass;
        if (en.mid  > mm) mm = en.mid;
        if (en.high > mt) mt = en.high;
      }
      if (mb === 0 && mm === 0 && mt === 0) continue;
      drawBands3(ctx, x, 1, H, mb, mm, mt, false); // scrolling zoom → centred/mirrored
      continue;
    }
    let mh = 0, mr = 0, mg = 0, mb = 0, count = 0;
    for (let i = s; i < e; i++) {
      const en = data.entries[i];
      if (en.h > mh) mh = en.h;
      mr += en.r; mg += en.g; mb += en.b;
      count++;
    }
    if (mh === 0 || count === 0) continue;
    if (mode === 'blue') {
      ctx.fillStyle = blueColor(mr, mg, mb);
    } else {
      const r = Math.min(255, Math.round((mr / count) * 38));
      const g = Math.min(255, Math.round((mg / count) * 38));
      const b = Math.min(255, Math.round((mb / count) * 38));
      ctx.fillStyle = `rgb(${r},${g},${b})`;
    }
    const barH = Math.max(1, Math.round((mh / 31) * H));
    ctx.fillRect(x, barTop(barH, H), 1, barH);
  }
}

// drawCueOverlayWindow is the windowed analogue of drawCueMarkersCanvas:
// cues outside [startMs..endMs] are skipped, loops are clipped to the
// visible range, and the label sizes adapt to canvas height. Use for
// the zoom pane (call drawCueMarkersCanvas directly for the overview
// since it shows the whole track at once).
function drawCueOverlayWindow(canvas, cues, durationMs, startMs, endMs, opts) {
  if (!cues || !cues.length || !durationMs) return;
  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const W = canvas.width / dpr;
  const H = canvas.height / dpr;
  const labelH = (opts && typeof opts.labelHeight === 'number') ? opts.labelHeight : 14;
  const showLabels = labelH >= 12 && H >= 40;
  const span = endMs - startMs;
  if (span <= 0) return;
  const toX = ms => Math.round(((ms - startMs) / span) * W);
  ctx.save();
  for (const c of cues) {
    const x = toX(c.time_ms);
    const xEnd = (c.type === 'loop' && c.loop_ms > 0) ? toX(c.loop_ms) : x;
    // Skip entirely-off-screen cues (a loop overlapping the window
    // still draws its visible portion).
    if ((xEnd < 0) || (x > W)) continue;
    const color = CUE_COLORS[c.color_id] || CUE_COLORS[0];
    if (c.type === 'loop' && c.loop_ms > 0) {
      const clipX = Math.max(0, x);
      const clipX2 = Math.min(W, xEnd);
      if (clipX2 > clipX) {
        ctx.fillStyle = color;
        ctx.globalAlpha = 0.22;
        ctx.fillRect(clipX, 0, clipX2 - clipX, H);
        ctx.globalAlpha = 1;
      }
    }
    if (x < 0 || x > W) continue;
    const lineTop = showLabels ? labelH : 0;
    ctx.fillStyle = 'rgba(0,0,0,0.7)';
    ctx.fillRect(x - 1, lineTop, 4, H - lineTop);
    ctx.fillStyle = color;
    ctx.fillRect(x, lineTop, 2, H - lineTop);
    if (showLabels) {
      const label = cueLabel(c.number);
      ctx.font = '700 11px "Geist Mono", monospace';
      const textW = Math.ceil(ctx.measureText(label).width);
      const padX = 4;
      const badgeW = Math.max(14, textW + padX * 2);
      const badgeH = labelH - 2;
      const badgeX = Math.round(x - badgeW / 2 + 1);
      ctx.fillStyle = 'rgba(0,0,0,0.7)';
      ctx.fillRect(badgeX - 1, -1, badgeW + 2, badgeH + 2);
      ctx.fillStyle = color;
      ctx.fillRect(badgeX, 0, badgeW, badgeH);
      ctx.beginPath();
      ctx.moveTo(x - 5, badgeH);
      ctx.lineTo(x + 6, badgeH);
      ctx.lineTo(x + 0.5, badgeH + 4);
      ctx.closePath();
      ctx.fill();
      ctx.fillStyle = isWarmCueColor(c.color_id) ? '#000' : '#fff';
      ctx.textBaseline = 'middle';
      ctx.textAlign = 'center';
      ctx.fillText(label, x + 1, Math.floor(badgeH / 2) + 1);
    }
  }
  ctx.restore();
}

// drawBeatGridOverlay draws beat ticks at top + bottom of the canvas
// for every beat falling within [startMs..endMs]. Downbeats (the first
// beat of each bar — beat_in_bar=1) get taller, brighter ticks so the
// bar boundaries are obvious. Skipped automatically when zoomed out so
// far that ticks would touch each other (>~150 beats visible).
function drawBeatGridOverlay(canvas, beats, startMs, endMs) {
  if (!beats || !beats.length) return;
  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const W = canvas.width / dpr;
  const H = canvas.height / dpr;
  const span = endMs - startMs;
  if (span <= 0) return;
  // Count visible beats; bail when they'd visually merge.
  let visible = 0;
  for (const b of beats) {
    if (b.time_ms >= startMs && b.time_ms <= endMs) visible++;
    if (visible > 150) return;
  }
  ctx.save();
  for (const b of beats) {
    if (b.time_ms < startMs || b.time_ms > endMs) continue;
    const x = Math.round(((b.time_ms - startMs) / span) * W);
    const isDown = b.beat_in_bar === 1;
    if (isDown) {
      // Bar boundary: full-height line + bright top/bottom caps.
      ctx.fillStyle = 'rgba(255, 200, 0, 0.30)';
      ctx.fillRect(x, 0, 1, H);
      ctx.fillStyle = 'rgba(255, 200, 0, 0.95)';
      ctx.fillRect(x - 1, 0, 3, 5);
      ctx.fillRect(x - 1, H - 5, 3, 5);
    } else {
      // Off-beat: full-height faint line.
      ctx.fillStyle = 'rgba(180, 180, 200, 0.22)';
      ctx.fillRect(x, 0, 1, H);
    }
  }
  ctx.restore();
}

// drawOverviewViewport overlays a rectangle showing which slice of the
// track is currently rendered in the zoom pane. Clamped to [0, dur]
// for display so the rect doesn't escape the canvas when the cursor is
// near the start or end of the track.
function drawOverviewViewport(canvas, durationMs, startMs, endMs) {
  if (!durationMs) return;
  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const W = canvas.width / dpr;
  const H = canvas.height / dpr;
  const s = Math.max(0, startMs);
  const e = Math.min(durationMs, endMs);
  if (e <= s) return;
  const x0 = Math.round((s / durationMs) * W);
  const x1 = Math.round((e / durationMs) * W);
  const w  = Math.max(2, x1 - x0);
  ctx.save();
  ctx.fillStyle = 'rgba(255, 140, 0, 0.12)';
  ctx.strokeStyle = '#ff8c00';
  ctx.lineWidth = 1;
  ctx.fillRect(x0, 0, w, H);
  ctx.strokeRect(x0 + 0.5, 0.5, w - 1, H - 1);
  // Cursor marker on the overview too — small orange wedge at the
  // current centerMs position so you can see where the playhead is
  // even when the viewport rect is wide.
  const cx = Math.round((detailWave.centerMs / durationMs) * W);
  ctx.fillStyle = '#ff8c00';
  ctx.beginPath();
  ctx.moveTo(cx - 3, 0);
  ctx.lineTo(cx + 3, 0);
  ctx.lineTo(cx,     4);
  ctx.closePath();
  ctx.fill();
  ctx.restore();
}

// drawZoomCursor paints the playhead line down the middle of the zoom
// canvas + a small triangle indicator at the top. Always at W/2 since
// the zoom window centers on detailWave.centerMs. Also paints a small
// "BEAT N · n/4" badge next to the cursor when beat-grid data is
// available, so you can see exactly which beat you're on while
// scrubbing.
function drawZoomCursor(canvas) {
  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const W = canvas.width / dpr;
  const H = canvas.height / dpr;
  const x = Math.floor(W / 2);
  ctx.save();
  ctx.fillStyle = 'rgba(255, 140, 0, 0.85)';
  ctx.fillRect(x, 0, 1, H);
  ctx.fillStyle = '#ff8c00';
  ctx.beginPath();
  ctx.moveTo(x - 5, 0);
  ctx.lineTo(x + 6, 0);
  ctx.lineTo(x + 0.5, 7);
  ctx.closePath();
  ctx.fill();
  // Beat readout near the cursor: "B73·1/4" + the time. Falls back to
  // just the time when there's no beat-grid data on the track.
  const label = cursorBeatLabel();
  if (label) {
    ctx.font = '700 10px "Geist Mono", monospace';
    const padX = 5;
    const textW = Math.ceil(ctx.measureText(label).width);
    const badgeW = textW + padX * 2;
    const badgeH = 14;
    // Pin to the right of the cursor, just under the triangle marker
    // at the top. Flip to the left if it would overflow the canvas
    // edge.
    let bx = x + 8;
    if (bx + badgeW > W - 2) bx = x - 8 - badgeW;
    const by = 1;
    ctx.fillStyle = 'rgba(0, 0, 0, 0.72)';
    ctx.fillRect(bx - 1, by - 1, badgeW + 2, badgeH + 2);
    ctx.fillStyle = '#ff8c00';
    ctx.fillRect(bx, by, badgeW, badgeH);
    ctx.fillStyle = '#000';
    ctx.textBaseline = 'middle';
    ctx.textAlign = 'left';
    ctx.fillText(label, bx + padX, by + badgeH / 2 + 1);
  }
  ctx.restore();
}

// Find the beat closest to centerMs (the cursor) and format it as
// "B<idx>·<n>/4 <m:ss>". Returns null if there are no beats yet.
function cursorBeatLabel() {
  const beats = detailWave.beats;
  const ms = detailWave.centerMs;
  if (!beats || !beats.length) {
    return ms >= 0 ? fmtCueTimeMs(Math.round(Math.max(0, ms))) : null;
  }
  // Binary search for the latest beat with time_ms <= ms.
  let lo = 0, hi = beats.length - 1, idx = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (beats[mid].time_ms <= ms) { idx = mid; lo = mid + 1; }
    else                          { hi = mid - 1; }
  }
  // If the cursor is before the first beat, fall back to time only.
  if (beats[idx].time_ms > ms) return fmtCueTimeMs(Math.round(Math.max(0, ms)));
  const b = beats[idx];
  return `${fmtCueTimeMs(Math.round(Math.max(0, ms)))} ${idx + 1}·${b.beat_in_bar}/4`;
}

