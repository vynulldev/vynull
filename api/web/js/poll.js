// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── polling ─────────
async function refresh() {
  const [players, trackRev, settings, enums, cueRev, peers] = await Promise.all([
    fetchJSON('/api/players'),
    fetchJSON('/api/tracks/rev'),
    fetchJSON('/api/settings'),
    settingsEnums ? Promise.resolve(settingsEnums) : fetchJSON('/api/settings/enums'),
    fetchJSON('/api/cues/rev'),
    fetchJSON('/api/peers'),
  ]);
  if (enums && !settingsEnums) settingsEnums = enums;
  // The track list and every cue are fetched only when their revision moved,
  // and revalidated against their ETag (no-cache), so an unchanged one costs a
  // 304. A low-frequency forced refetch self-heals if any change ever forgot
  // to move a revision.
  pollCount++;
  const forced = pollCount % 120 === 0;
  // Cues first — both renderPlayers and renderLibrary read from cuesCache.
  // Snapshot-skip so we only mutate the cache + trigger a re-render when
  // the CDJ (or our own API) has actually saved/deleted a cue.
  let cuesChanged = false;
  const cuesRev = (cueRev && typeof cueRev.rev === 'number') ? cueRev.rev : null;
  if (cuesRev === null || cuesRev !== lastCuesRev || forced) {
    const cues = await fetchJSON('/api/cues', { cache: 'no-cache' });
    if (cues && typeof cues === 'object') {
      if (cuesRev !== null) lastCuesRev = cuesRev;
      const key = JSON.stringify(cues);
      if (key !== lastCuesKey) {
        lastCuesKey = key;
        cuesCache.clear();
        for (const [tid, list] of Object.entries(cues)) {
          cuesCache.set(Number(tid), list);
        }
        cuesChanged = true;
      }
    }
  }
  updateLinkStatus(peers);
  if (Array.isArray(peers))   renderMixerStrip(peers);
  if (Array.isArray(players)) renderPlayers(players);
  // Only refetch the (potentially large) track list when its revision moved
  // — otherwise the per-second poll costs one tiny /api/tracks/rev fetch
  // instead of transferring + parsing + diffing the whole library every tick.
  const rev = (trackRev && typeof trackRev.rev === 'number') ? trackRev.rev : null;
  const revMoved = rev === null || rev !== lastTracksRev;
  if (revMoved || forced) {
    const tracks = await fetchJSON('/api/tracks', { cache: 'no-cache' });
    if (Array.isArray(tracks)) {
      if (rev !== null) lastTracksRev = rev;
      // Overlay any locally-pending mutations the server may not have
      // committed yet (race between PUT and this GET), then diff so an
      // edit we already rendered optimistically doesn't re-render the
      // whole table (the <img> teardown flashes every waveform thumbnail).
      applyPendingTrackEdits(tracks);
      const key = JSON.stringify(tracks);
      if (key !== lastTracksKey || cuesChanged) {
        allTracks = tracks;
        lastTracksKey = key;
        keepLibraryPlace(refreshLibraryView);
      }
    }
  } else if (cuesChanged) {
    // Track list unchanged but cue markers did — re-render existing rows.
    keepLibraryPlace(refreshLibraryView);
  }
  if (settings && settings.full) renderSettings(settings.full);
  // Repaint when any display-affecting DEVSETTING changes: waveform colour
  // (also covers AUTO mode), overview height, or key notation. redrawAllWaveforms
  // repaints canvases + thumbnails cheaply; only a key-notation change needs a
  // full library re-render (the KEY column), so do that one selectively.
  const sig = effectiveWaveColor() + '|' + overviewType() + '|' + keyDisplayMode();
  if (sig !== lastDisplaySig) {
    const keyChanged = keyDisplayMode() !== lastKeyDisplay;
    lastDisplaySig = sig;
    lastKeyDisplay = keyDisplayMode();
    renderWaveColorButton();
    redrawAllWaveforms();
    if (keyChanged && typeof refreshLibraryView === 'function') keepLibraryPlace(refreshLibraryView);
  }
}
// Theme switcher: reflect the early-applied theme into the select, persist on change.
(function initTheme() {
  const sel = document.getElementById('theme-select');
  if (!sel) return;
  sel.value = document.documentElement.dataset.theme || 'dark';
  sel.addEventListener('change', () => {
    document.documentElement.dataset.theme = sel.value;
    try { localStorage.setItem('theme', sel.value); } catch (e) {}
    const s = document.querySelector('select[data-webui="theme"]');
    if (s) s.value = sel.value;
  });
})();
document.getElementById('wave-color-btn').addEventListener('click', cycleWaveColor);
renderWaveColorButton();
document.getElementById('vocal-det-btn').addEventListener('click', cycleVocalDetect);
renderVocalButton();

// Detail drawer is resizable — drag the top handle up/down to change its height,
// persisted across sessions.
(function setupDetailResize() {
  const drawer = document.getElementById('detail-drawer');
  const handle = document.getElementById('detail-resize');
  if (!drawer || !handle) return;
  const KEY = 'detailDrawerHeight';
  const clamp = h => Math.max(220, Math.min(Math.round(window.innerHeight * 0.92), h));
  // On mobile the drawer is a full-screen sheet sized by CSS (height:100dvh)
  // and the resize handle is hidden. A desktop-persisted inline height would
  // override that and shrink the open sheet to ~half screen — so only apply
  // (and honour) the saved height on the desktop layout, and clear the inline
  // height when we cross into the mobile layout.
  const applyHeight = () => {
    if (isMobileLayout()) { drawer.style.height = ''; return; }
    try { const s = parseInt(localStorage.getItem(KEY), 10); if (s > 0) drawer.style.height = clamp(s) + 'px'; } catch (e) {}
  };
  applyHeight();
  mobileMQ.addEventListener('change', applyHeight);
  let dragging = false;
  handle.addEventListener('pointerdown', e => {
    dragging = true;
    handle.setPointerCapture(e.pointerId);
    document.body.style.userSelect = 'none';
    e.preventDefault();
  });
  handle.addEventListener('pointermove', e => {
    if (dragging) drawer.style.height = clamp(window.innerHeight - e.clientY) + 'px';
  });
  const stop = e => {
    if (!dragging) return;
    dragging = false;
    try { handle.releasePointerCapture(e.pointerId); } catch (_) {}
    document.body.style.userSelect = '';
    try { localStorage.setItem(KEY, parseInt(drawer.style.height, 10) || ''); } catch (_) {}
    if (typeof redrawDetailWaveform === 'function' && drawer.classList.contains('open')) redrawDetailWaveform();
  };
  handle.addEventListener('pointerup', stop);
  handle.addEventListener('pointercancel', stop);
})();

refresh();
// Load playlists once at boot so the library-row → playlist drag
// overlay has data the first time the user drags a row, even before
// they've opened the Playlists tab.
loadPlaylists();
// Same for tags — the smart-playlist editor's tag dropdown and the
// in-drawer tag picker both read allTagsCache, and without this
// pre-fetch they'd be empty until the user happens to open the Tags
// tab first.
ensureAllTags();

// Visibility-aware polling. When the tab is hidden (minimised, another
// workspace, another tab), the 1 Hz poll + canvas/cue/playhead redraw
// loop keeps burning CPU pointlessly — measured ~30-40% of one core on
// the user's Firefox + RTX 2060 + driver 470. Pause while hidden, resume
// (with an immediate refresh so the visible state matches reality
// instantly) when the tab returns.
let pollTimer = setInterval(refresh, 1000);
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  } else if (!pollTimer) {
    refresh();
    pollTimer = setInterval(refresh, 1000);
  }
});
