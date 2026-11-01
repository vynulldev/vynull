// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── USB EXPORT ─────────
// Opens a modal asking for the USB destination + copy/symlink choice,
// then POSTs /api/export and disables the form until the response
// arrives. Source is implied by the currently-selected library source
// (COLLECTION when none is selected, otherwise the playlist).
// presetSource/presetLabel override the implied library source — used by the
// bulk-select bar to export just the checked tracks ('selection:<ids>').
function openExportModal(presetSource, presetLabel) {
  // Guard against event-object arguments from direct handler wiring, so a
  // future `el.onclick = openExportModal` can't regress the request body.
  if (typeof presetSource !== 'string') presetSource = null;
  const sel = (typeof selectedSource === 'number')
    ? playlistsCache.find(p => p.id === selectedSource)
    : null;
  const sourceLabel = presetSource ? (presetLabel || 'SELECTION')
    : (sel ? sel.name : 'COLLECTION');
  const apiSource = presetSource || (sel
    ? (sel.is_smart ? `smart:${sel.id}` : `playlist:${sel.id}`)
    : 'all');

  const back = document.createElement('div');
  back.className = 'menu-modal-backdrop show';
  const m = document.createElement('div');
  m.className = 'menu-modal show';
  m.innerHTML = `
    <div class="head">
      <span>EXPORT ${escape(sourceLabel.toUpperCase())} → USB</span>
      <button class="x" type="button" title="Close">×</button>
    </div>
    <div class="body">
      <div class="export-row">
        <label class="export-label">Destination</label>
        <input type="text" id="export-dest" class="input" placeholder="/path/to/usb-root" autocomplete="off" spellcheck="false">
        <div class="export-hint">Absolute path on the server. The directory will be created if it doesn't exist; PIONEER/ and Contents/ go inside.</div>
      </div>
      <div class="export-row">
        <label class="export-label">Audio files</label>
        <div class="export-radios">
          <label><input type="radio" name="copy_files" value="true" checked> Copy (slow, portable)</label>
          <label><input type="radio" name="copy_files" value="false"> Symlink (fast, in-place)</label>
        </div>
      </div>
      <div class="export-row">
        <label class="export-label">If USB has an existing export</label>
        <div class="export-radios">
          <label><input type="radio" name="merge" value="true" checked> Merge (add this on top)</label>
          <label><input type="radio" name="merge" value="false"> Replace (overwrites)</label>
        </div>
      </div>
      <div class="export-row" id="export-preview-row" style="display:none">
        <label class="export-label">Preview</label>
        <div id="export-preview" class="export-hint"></div>
      </div>
      <div class="export-status" id="export-status"></div>
    </div>
    <div class="foot">
      <button type="button" id="export-go">EXPORT</button>
    </div>
  `;
  document.body.appendChild(back);
  document.body.appendChild(m);

  const close = () => { m.remove(); back.remove(); };
  back.onclick = close;
  m.querySelector('.x').onclick = close;

  const destInput = m.querySelector('#export-dest');
  const statusEl = m.querySelector('#export-status');
  const goBtn = m.querySelector('#export-go');
  destInput.focus();

  // Fetch preview (track count) for the chosen source.
  (async () => {
    try {
      const r = await fetch('/api/export/preview?source=' + encodeURIComponent(apiSource));
      if (!r.ok) return;
      const p = await r.json();
      const row = m.querySelector('#export-preview-row');
      const el = m.querySelector('#export-preview');
      el.innerHTML = `${p.total} track${p.total === 1 ? '' : 's'} total`;
      row.style.display = '';
    } catch (e) { /* preview is best-effort */ }
  })();

  goBtn.onclick = async () => {
    const dest = destInput.value.trim();
    if (!dest) { statusEl.textContent = 'Destination is required.'; return; }
    const copyFiles = m.querySelector('input[name="copy_files"]:checked').value === 'true';
    const merge = m.querySelector('input[name="merge"]:checked').value === 'true';
    goBtn.disabled = true;
    destInput.disabled = true;
    statusEl.textContent = 'Exporting… (unchecked tracks are decode-checked first; can take a while for large libraries)';
    try {
      const r = await fetch('/api/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ destination: dest, source: apiSource, copy_files: copyFiles, merge }),
      });
      if (!r.ok) {
        const t = await errorText(r);
        statusEl.textContent = `Failed: ${r.status} ${t}`;
        goBtn.disabled = false;
        destInput.disabled = false;
        return;
      }
      const data = await r.json();
      statusEl.textContent = `Wrote ${data.track_count} track${data.track_count === 1 ? '' : 's'} to ${data.destination}`;
      toast(`EXPORT · ${data.track_count} tracks → ${data.destination}`, 'success');
      goBtn.textContent = 'DONE';
    } catch (err) {
      statusEl.textContent = `Failed: ${err}`;
      goBtn.disabled = false;
      destInput.disabled = false;
    }
  };
}

// wireLibrarySidebar wires the sidebar tree's click handlers, drag
// sources/targets, and the +PL/+SMART/+FOLDER buttons. Called by
// renderLibrarySidebar after replacing innerHTML so listeners apply
// to the freshly-rendered nodes.
function wireLibrarySidebar() {
  // COLLECTION click → switch source to the full library.
  document.querySelectorAll('.lib-src[data-source]').forEach(btn => {
    btn.onclick = () => {
      const src = btn.getAttribute('data-source');
      if (src === 'collection') {
        selectedSource = 'collection';
        plSelectedID = null;
        plSelectedTracks = [];
        refreshLibraryView();
        closeMobilePlaylists();
      }
    };
  });

  // Footer create buttons.
  const newPl = document.getElementById('pl-new-pl');
  if (newPl) newPl.onclick = () => createPlaylistPrompt(false);
  const newFolder = document.getElementById('pl-new-folder');
  if (newFolder) newFolder.onclick = () => createPlaylistPrompt(true);
  const newSmart = document.getElementById('pl-new-smart');
  if (newSmart) newSmart.onclick = () => openSmartEditor(null);

  // Tree node clicks: select playlist (or folder). Same handler for
  // both the regular playlist tree and the History section.
  document.querySelectorAll('#lib-sidebar-tree .pl-node, #lib-sidebar-history .pl-node').forEach(node => {
    node.addEventListener('click', async () => {
      const id = parseInt(node.getAttribute('data-pl-id'), 10);
      const isFolder = node.getAttribute('data-is-folder') === '1';
      selectedSource = id;
      plSelectedID = id;
      if (isFolder) {
        plSelectedTracks = [];
        refreshLibraryView();
        return; // keep the panel open so the user can drill into the folder
      }
      await loadPlaylistTracks(id);
      refreshLibraryView();
      closeMobilePlaylists();
    });
  });

  // Tree-node drag handlers (reparent). Same model as before — drag a
  // playlist or folder onto another folder to nest it, drop on empty
  // tree area to send it to root.
  const treeHost = document.getElementById('lib-sidebar-tree');
  if (treeHost) {
    document.querySelectorAll('#lib-sidebar-tree .pl-node').forEach(node => {
      node.addEventListener('dragstart', e => {
        const id = parseInt(node.getAttribute('data-pl-id'), 10);
        const isFolder = node.getAttribute('data-is-folder') === '1';
        dnd = { type: 'node', id, isFolder };
        e.dataTransfer.effectAllowed = 'move';
        node.classList.add('dragging');
        e.stopPropagation();
      });
      node.addEventListener('dragover', e => {
        if (!dnd || dnd.type !== 'node') return;
        const targetID = parseInt(node.getAttribute('data-pl-id'), 10);
        // Folders only; can't drop onto self or own descendant.
        if (node.getAttribute('data-is-folder') !== '1') return;
        if (targetID === dnd.id) return;
        if (isPlaylistDescendantOf(targetID, dnd.id)) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
        document.querySelectorAll('#lib-sidebar-tree .pl-node').forEach(n => n.classList.remove('drop-target'));
        node.classList.add('drop-target');
      });
      node.addEventListener('drop', e => {
        if (!dnd || dnd.type !== 'node') return;
        const targetID = parseInt(node.getAttribute('data-pl-id'), 10);
        if (node.getAttribute('data-is-folder') !== '1') return;
        if (targetID === dnd.id || isPlaylistDescendantOf(targetID, dnd.id)) return;
        e.preventDefault();
        movePlaylistTo(dnd.id, targetID);
      });
    });
    // Drop on empty tree area → move to root.
    treeHost.addEventListener('dragover', e => {
      if (!dnd || dnd.type !== 'node') return;
      if (!e.target.closest('.pl-node')) {
        e.preventDefault();
        treeHost.classList.add('drop-root');
      }
    });
    treeHost.addEventListener('dragleave', e => {
      // Only clear when leaving the tree itself, not when entering a child.
      if (e.target === treeHost) treeHost.classList.remove('drop-root');
    });
    treeHost.addEventListener('drop', e => {
      if (!dnd || dnd.type !== 'node') return;
      if (!e.target.closest('.pl-node')) {
        e.preventDefault();
        const cur = playlistsCache.find(p => p.id === dnd.id);
        if (cur && cur.parent_id !== 0) movePlaylistTo(dnd.id, 0);
      }
    });
  }
}

