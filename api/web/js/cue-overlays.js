// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── CUE OVERLAYS ─────────
// cuesCache: trackID (Number) → [{number,type,time_ms,loop_ms,color_id}]
// Populated by the 1s poll fetching /api/cues; library + drawer + players
// all read from it. Refreshes when a cue is saved/deleted on the CDJ or
// via our API.
const cuesCache = new Map();
let lastCuesKey = '';

// Looks up cues for a track by either number or string key.
function getCuesFor(trackID) {
  return cuesCache.get(Number(trackID)) || [];
}

// Detail-drawer waveform state: overview canvas + zoom canvas with
// shared cue/wave data. Setting up via setDetailWaveform() initializes;
// redrawDetailWaveform() repaints from the cached state (used by the
// cue-color callback so a color change updates both panes).
const ZOOM_WINDOW_MS_LEVELS = [4000, 8000, 16000, 32000, 64000, 128000];
const detailWave = {
  trackID:    null,
  wave:       null,
  cues:       null,
  beats:      null, // [{time_ms, beat_in_bar}] from /api/analysis/{id}
  durationMs: 0,
  bpm:        0,    // track tempo, for beat-jump when no grid is present
  zoomIdx:    2,    // ZOOM_WINDOW_MS_LEVELS[2] = 16s
  centerMs:   8000, // center of zoom window
};
function detailZoomBounds() {
  // No clamping — the window always extends ±windowMs/2 around the
  // cursor (centerMs), even when that puts start<0 or end>durationMs.
  // The waveform renderer just leaves those pixels blank, which lets
  // the cursor reach the genuine start and end of the track.
  const win = ZOOM_WINDOW_MS_LEVELS[detailWave.zoomIdx];
  return {
    start: detailWave.centerMs - win / 2,
    end:   detailWave.centerMs + win / 2,
  };
}
function setDetailWaveform(t, wave, cues, beats) {
  // Preserve zoom + cursor position when re-rendering the same track
  // (e.g., after saving a cue) so the user's view doesn't jump back
  // to the beginning. Only reset on a track switch.
  const sameTrack = detailWave.trackID === t.id;
  detailWave.trackID    = t.id;
  detailWave.wave       = wave;
  detailWave.cues       = cues || [];
  detailWave.beats      = beats || null;
  detailWave.durationMs = (t.duration || 0) * 1000;
  detailWave.bpm        = t.bpm || 0;
  if (!sameTrack) {
    detailWave.zoomIdx = 2;
    // Default cursor position: the first downbeat (beat 1 of bar 1).
    // Falls back to the first cue, then to half the zoom window.
    const firstDownbeat = (beats || []).find(b => b.beat_in_bar === 1);
    if (firstDownbeat) {
      detailWave.centerMs = firstDownbeat.time_ms;
    } else {
      const firstCue = (cues || []).filter(c => c.time_ms >= 0).sort((a,b)=>a.time_ms-b.time_ms)[0];
      detailWave.centerMs = firstCue ? firstCue.time_ms : (ZOOM_WINDOW_MS_LEVELS[detailWave.zoomIdx] / 2);
    }
  }
  redrawDetailWaveform();
}
function redrawDetailWaveform() {
  const ov = document.getElementById('detail-overview');
  const zm = document.getElementById('detail-zoom');
  if (!ov || !zm) return;
  // Hi-DPI sizing — must happen every render in case the drawer was
  // hidden when the canvas size was 0.
  sizeCanvasHiDPI(ov, 40);
  sizeCanvasHiDPI(zm, 96);
  // Overview = whole track, with cue markers + viewport rect. previewAnchor=true
  // so 3-band draws as a bottom-anchored half waveform (rekordbox convention).
  drawWaveformWindow(ov, detailWave.wave, detailWave.durationMs, 0, detailWave.durationMs, undefined, true);
  drawCueOverlayWindow(ov, detailWave.cues, detailWave.durationMs, 0, detailWave.durationMs, { labelHeight: 0 });
  const { start, end } = detailZoomBounds();
  drawOverviewViewport(ov, detailWave.durationMs, start, end);
  // Zoom pane — beat grid first (under waveform/cues), then waveform,
  // then cues so loops/markers stay readable on top of everything.
  drawWaveformWindow(zm, detailWave.wave, detailWave.durationMs, start, end);
  drawBeatGridOverlay(zm, detailWave.beats, start, end);
  drawCueOverlayWindow(zm, detailWave.cues, detailWave.durationMs, start, end);
  drawZoomCursor(zm);
  // Mirror the visible slice of the phrase strip below the zoom pane
  // so the user can see which sections fall under the current cursor.
  renderPhraseStripZoom(detailPhrasesCache, start, end);
  // Update the zoom label.
  const lbl = document.getElementById('zoom-label');
  if (lbl) lbl.textContent = (ZOOM_WINDOW_MS_LEVELS[detailWave.zoomIdx] / 1000) + 's';
}
function detailZoomStep(delta) {
  detailWave.zoomIdx = Math.max(0, Math.min(ZOOM_WINDOW_MS_LEVELS.length - 1, detailWave.zoomIdx + delta));
  redrawDetailWaveform();
}
function detailZoomCenterTo(ms) {
  detailWave.centerMs = Math.max(0, Math.min(detailWave.durationMs, ms));
  detailPlayerOnUserMove(detailWave.centerMs);
  redrawDetailWaveform();
}
// detailJumpBeats moves the cursor by `delta` beats (±1/2/4/8/16/32). With a
// beat grid it snaps to the nearest beat then steps by index; without one it
// approximates from the track tempo.
function detailJumpBeats(delta) {
  const beats = detailWave.beats;
  if (beats && beats.length) {
    const ms = detailWave.centerMs;
    // Latest beat with time_ms <= cursor.
    let lo = 0, hi = beats.length - 1, idx = 0;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (beats[mid].time_ms <= ms) { idx = mid; lo = mid + 1; }
      else                          { hi = mid - 1; }
    }
    // Snap to whichever of idx / idx+1 is actually closest to the cursor.
    if (idx + 1 < beats.length &&
        Math.abs(beats[idx + 1].time_ms - ms) < Math.abs(beats[idx].time_ms - ms)) {
      idx++;
    }
    const target = Math.max(0, Math.min(beats.length - 1, idx + delta));
    detailZoomCenterTo(beats[target].time_ms);
    return;
  }
  if (detailWave.bpm > 0) {
    detailZoomCenterTo(detailWave.centerMs + delta * (60000 / detailWave.bpm));
  }
}

