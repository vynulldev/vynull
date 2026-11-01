// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// Library waveform thumbnail sizing. The thumbnail scales with the row height
// and its column width; we request the PNG at device resolution so an enlarged
// thumbnail stays crisp rather than upscaling a small bitmap.
function waveThumbCSS() {
  return { w: Math.max(60, Math.round(libWaveColWidth() - 16)), h: Math.max(14, libraryRowHeight - 8) };
}
// trackIsAnalyzed memoizes "does this track have analysis" per allTracks
// snapshot (the poll swaps in a fresh array whenever anything changes).
let _analyzedMemoSrc = null, _analyzedMemo = null;
function trackIsAnalyzed(tid) {
  if (_analyzedMemoSrc !== allTracks) {
    _analyzedMemo = new Set((allTracks || []).filter(t => t.bpm > 0).map(t => t.id));
    _analyzedMemoSrc = allTracks;
  }
  return _analyzedMemo.has(Number(tid));
}
function waveThumbSrc(tid, bust) {
  const c = waveThumbCSS();
  const dpr = Math.min(3, window.devicePixelRatio || 1);
  const w = Math.min(2400, Math.round(c.w * dpr));
  const h = Math.min(240, Math.round(c.h * dpr));
  // &a=1 once the track is analyzed: the URL must CHANGE when analysis
  // lands, or re-created <img> elements keep resurrecting the placeholder
  // from the browser's per-document image cache (no-store notwithstanding)
  // for as long as the table keeps rebuilding — which during a batch
  // analysis is every poll tick.
  const a = trackIsAnalyzed(tid) ? '&a=1' : '';
  return `/api/analysis/waveform-png/${tid}?type=detail&color=${effectiveWaveColor()}&overview=${overviewType()}&w=${w}&h=${h}${a}` + (bust ? `&_=${Date.now()}` : '');
}
function updateThumbnailSrcs() {
  document.querySelectorAll('img[data-libtrack]').forEach(img => {
    swapImgSrc(img, waveThumbSrc(img.getAttribute('data-libtrack')));
  });
}

// swapImgSrc replaces an <img> src without the black flash: setting .src
// directly blanks the element while an uncached resource loads (exposing the
// dark cell background — most visible on recently-added tracks whose PNG
// isn't disk-cached at the new size yet, so the server renders live). Preload
// through a detached Image and swap only once the bytes are in, so the old
// frame stays up throughout. No-op when the URL is already current.
function swapImgSrc(img, url) {
  if (img.getAttribute('src') === url) return;
  const pre = new Image();
  pre.onload = () => {
    // The row may have been re-rendered or retargeted while preloading.
    if (document.body.contains(img)) {
      img.src = url;
    }
  };
  pre.onerror = () => {}; // keep the old frame on failure
  pre.src = url;
}
// Debounced re-fetch of every visible waveform thumbnail at the new displayed
// size — used when the row height or waveform column width changes.
let waveRefetchTimer = null;
function scheduleWaveRefetch() {
  clearTimeout(waveRefetchTimer);
  waveRefetchTimer = setTimeout(updateThumbnailSrcs, 200);
}
// blueColor maps a bucket's summed band weights to the blue-mode bar colour:
// mostly blue, whitening with the treble fraction. mr/mg/mb are any-scale sums
// — only the ratio matters.
function blueColor(mr, mg, mb) {
  const sum = mr + mg + mb;
  const tf = sum > 0 ? mb / sum : 0;
  const k = tf * 0.85;
  return `rgb(${Math.round(40 + (255-40)*k)},${Math.round(120 + (255-120)*k)},${Math.round(235 + (255-235)*k)})`;
}

// 3-band waveform colours (bass=blue, mid=amber, treble=white), pre-dimmed for
// additive ('lighter') compositing so overlapping bands brighten toward white
// where loud. Treble is dialled back hardest (×0.35) since white saturates the
// additive centre fastest; bass/mid are ×0.85. Mirrors the CDJ-3000 "3Band"
// waveform.
const BAND3_COLORS = ['rgb(26,102,217)', 'rgb(217,145,26)', 'rgb(82,86,89)'];

// drawBands3 paints bass/mid/treble as three overlaid additive bars in the
// column [x, x+w), each at its own height (value/255 × H). `bottom` forces the
// bars to grow from the box bottom instead of centred/mirrored: rekordbox
// always draws the whole-track 3-band PREVIEW as a bottom-anchored half
// waveform, so preview surfaces pass bottom=true while the scrolling detail
// passes false. Additive blending (order-independent) makes overlaps brighten;
// source-over is restored on exit so callers (cue markers, playhead) draw normally.
function drawBands3(ctx, x, w, H, bass, mid, treble, bottom) {
  ctx.globalCompositeOperation = 'lighter';
  drawBand3(ctx, x, w, H, bass,   BAND3_COLORS[0], bottom);
  drawBand3(ctx, x, w, H, mid,    BAND3_COLORS[1], bottom);
  drawBand3(ctx, x, w, H, treble, BAND3_COLORS[2], bottom);
  ctx.globalCompositeOperation = 'source-over';
}
function drawBand3(ctx, x, w, H, v, color, bottom) {
  if (v <= 0) return;
  const barH = Math.max(1, Math.round((v / 255) * H));
  ctx.fillStyle = color;
  ctx.fillRect(x, barTop(barH, H, bottom), w, barH);
}

// STACK3_COLORS: full/vivid band colours for the stacked preview (segments
// don't overlap, so no additive dimming needed). PREVIEW_SMOOTH_COLS adds a
// little column-space smoothing on top of the entry smoothing — rekordbox's
// preview is very smooth.
const STACK3_COLORS = ['rgb(30,120,255)', 'rgb(255,170,30)', 'rgb(235,245,255)'];
const PREVIEW_SMOOTH_COLS = 5;

// drawStacked3Band renders the whole-track 3-band PREVIEW as a bottom-anchored
// VERTICAL STACK: blue bass on the bottom, orange mid stacked above, white
// treble on top — each segment's height is that band's amplitude (not overlaid
// bars). Per-column peaks are smoothed, then the stack is normalised so the
// loudest column fills H (whole track visible). colRange(x) -> [s,e) maps a
// canvas column to its entry index range.
function drawStacked3Band(ctx, entries, W, H, colRange) {
  const n = entries.length;
  const band = [new Float32Array(W), new Float32Array(W), new Float32Array(W)];
  for (let x = 0; x < W; x++) {
    const r = colRange(x);
    let mb = 0, mm = 0, mt = 0;
    for (let i = r[0]; i < r[1]; i++) {
      if (i < 0 || i >= n) continue;
      const en = entries[i];
      if (en.bass > mb) mb = en.bass;
      if (en.mid > mm) mm = en.mid;
      if (en.high > mt) mt = en.high;
    }
    band[0][x] = mb; band[1][x] = mm; band[2][x] = mt;
  }
  // Column-space smoothing per band.
  const half = PREVIEW_SMOOTH_COLS >> 1;
  for (let b = 0; b < 3; b++) {
    const src = band[b].slice();
    for (let x = 0; x < W; x++) {
      let sum = 0, cnt = 0;
      for (let j = x - half; j <= x + half; j++) {
        if (j < 0 || j >= W) continue;
        sum += src[j]; cnt++;
      }
      band[b][x] = sum / cnt;
    }
  }
  // Normalise to the loudest stacked column.
  let maxTot = 1;
  for (let x = 0; x < W; x++) {
    const t = band[0][x] + band[1][x] + band[2][x];
    if (t > maxTot) maxTot = t;
  }
  const scale = H / maxTot;
  for (let x = 0; x < W; x++) {
    let y = H; // bottom
    for (let b = 0; b < 3; b++) {
      const seg = Math.round(band[b][x] * scale);
      if (seg <= 0) continue;
      ctx.fillStyle = STACK3_COLORS[b];
      ctx.fillRect(x, y - seg, 1, seg);
      y -= seg;
    }
  }
}

// CDJ "overview_type" DEVSETTING: full = waveform centred/mirrored about the
// box centreline; half = only the lower half shown, anchored to the bottom of
// the box (same amplitude, not squished). Returns "full" or "half".
function overviewType() {
  const o = currentSettings && currentSettings.dev_setting && currentSettings.dev_setting.overview_type;
  return o === 'half' ? 'half' : 'full';
}
// barTop returns the y of a bar of height barH within a box of height boxH,
// honouring the overview anchor: centred (full) or bottom-aligned (half).
// forceBottom overrides the setting to bottom-align regardless (3-band preview).
function barTop(barH, boxH, forceBottom) {
  return (forceBottom || overviewType() === 'half') ? (boxH - barH) : Math.floor((boxH - barH) / 2);
}

