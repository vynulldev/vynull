// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── playlist drag-and-drop helpers ─────────
// `dnd` is the in-flight drag state shared by every source/target. Types:
//   {type:'track',    idx}                   — reorder within current playlist
//   {type:'node',     id, isFolder}          — reparent in tree
//   {type:'libtrack', trackID}               — drag a library row onto the floating
//                                              playlist drop overlay
let dnd = null;

// dragend fires on the original source after every drag. Clears all
// visual state regardless of whether a drop happened.
document.addEventListener('dragend', () => {
  document.querySelectorAll('.dragging').forEach(el => el.classList.remove('dragging'));
  document.querySelectorAll('.drop-before, .drop-after, .drop-target').forEach(el => {
    el.classList.remove('drop-before', 'drop-after', 'drop-target');
  });
  const tree = document.getElementById('lib-sidebar-tree');
  if (tree) tree.classList.remove('drop-root');
  hidePlaylistDropOverlay();
  dnd = null;
});

// Walks the parent chain in playlistsCache to determine if `candidate`
// is anywhere under `root`. Used to reject self-referential reparenting
// in the tree (otherwise the server's Move would still reject it, but
// the cursor-affordance would lie).
function isPlaylistDescendantOf(candidate, root) {
  let cur = candidate;
  for (let guard = 0; guard < 64; guard++) {
    const p = playlistsCache.find(x => x.id === cur);
    if (!p || p.parent_id === 0) return false;
    if (p.parent_id === root) return true;
    cur = p.parent_id;
  }
  return false;
}

async function movePlaylistTo(id, parentID) {
  const r = await fetch(`/api/playlists/${id}`, {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ parent_id: parentID }),
  });
  if (!r.ok) { toast('MOVE FAILED', 'error'); return; }
  toast('MOVED', 'success');
  await loadPlaylists(true);
}

// Library-row → playlist drop. Shown on dragstart from any library row,
// hidden on dragend. Lists every leaf playlist; folders are dropped from
// the overlay to keep targets unambiguous (you can still drag a track
// in via the Playlists tab and then move it).
let dropOverlayEl = null;
// historyPlaylistIDs returns the set of playlist + folder IDs under
// the auto-managed "History" tree (the folder itself and every
// descendant). Used to exclude these from drop-targets / "add to
// playlist" pickers, since History entries are written automatically
// from CDJ playback and shouldn't be hand-edited.
function historyPlaylistIDs() {
  const ids = new Set();
  if (!Array.isArray(playlistsCache)) return ids;
  const root = playlistsCache.find(p =>
    p.parent_id === 0 && p.is_folder && p.name === 'History');
  if (!root) return ids;
  ids.add(root.id);
  for (let changed = true; changed; ) {
    changed = false;
    for (const p of playlistsCache) {
      if (!ids.has(p.id) && ids.has(p.parent_id)) {
        ids.add(p.id);
        changed = true;
      }
    }
  }
  return ids;
}

function showPlaylistDropOverlay() {
  if (!dropOverlayEl) {
    dropOverlayEl = document.createElement('div');
    dropOverlayEl.id = 'pl-drop-overlay';
    dropOverlayEl.className = 'pl-drop-overlay';
    document.body.appendChild(dropOverlayEl);
  }
  const hist = historyPlaylistIDs();
  const leaves = (playlistsCache || []).filter(p => !p.is_folder && !hist.has(p.id));
  const rows = leaves.length === 0
    ? `<div class="empty">— NO PLAYLISTS YET — CREATE ONE ON THE PLAYLISTS TAB —</div>`
    : leaves.map(p => `
        <div class="pl-node" data-pl-id="${p.id}">
          <span class="icon">♪</span>
          <span class="name">${escape(p.name)}</span>
          <span class="count">${(p.track_ids || []).length}</span>
        </div>
      `).join('');
  dropOverlayEl.innerHTML = `<div class="head">DROP TRACK ON PLAYLIST</div>${rows}`;
  dropOverlayEl.classList.add('show');
  dropOverlayEl.querySelectorAll('.pl-node').forEach(node => {
    node.addEventListener('dragover', e => {
      if (!dnd || dnd.type !== 'libtrack') return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
      dropOverlayEl.querySelectorAll('.pl-node').forEach(n => n.classList.remove('drop-target'));
      node.classList.add('drop-target');
    });
    node.addEventListener('drop', async e => {
      if (!dnd || dnd.type !== 'libtrack') return;
      e.preventDefault();
      const plID = parseInt(node.getAttribute('data-pl-id'), 10);
      const tid = dnd.trackID;
      await appendTrackToPlaylist(plID, tid);
    });
  });
}
function hidePlaylistDropOverlay() {
  if (dropOverlayEl) dropOverlayEl.classList.remove('show');
}

// appendTrackToPlaylist fetches the current track list, appends, and
// posts back. Avoids clobbering concurrent edits (vs. caching the array
// client-side and racing with another tab).
async function appendTrackToPlaylist(playlistID, trackID) {
  let cur;
  try {
    cur = await fetchJSON(`/api/playlists/${playlistID}/tracks`);
  } catch (e) {
    toast('ADD FAILED · ' + e.message.toUpperCase(), 'error');
    return; // writing the membership back now would drop its tracks
  }
  const ids = Array.isArray(cur) ? cur.map(t => t.id) : [];
  if (ids.includes(trackID)) {
    toast('ALREADY IN PLAYLIST', 'info');
    return;
  }
  ids.push(trackID);
  const r = await fetch(`/api/playlists/${playlistID}/tracks`, {
    method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ track_ids: ids }),
  });
  if (!r.ok) { toast('ADD FAILED · ' + (await errorText(r)).toUpperCase(), 'error'); return; }
  const t = (allTracks || []).find(x => x.id === trackID);
  const pl = (playlistsCache || []).find(x => x.id === playlistID);
  toast(`+ ${(t && t.title || 'TRACK').toUpperCase()} → ${(pl && pl.name || 'PLAYLIST').toUpperCase()}`, 'success');
  // Refresh the playlists cache so the count chip in the overlay
  // updates if the user drags another track right after.
  await loadPlaylists(plSelectedID === playlistID);
}

// ───────── SMART PLAYLIST EDITOR ─────────
// Form-builder + raw JSON tabs over the SmartRules tree. Each field's
// available operators and value editor are driven by SMART_FIELDS so
// adding a new field server-side only needs one row here, not new UI
// branches throughout the editor.
const SMART_FIELDS = {
  bpm:             { label: 'BPM',             type: 'number' },
  rating:          { label: 'Rating',          type: 'number' },
  play_count:      { label: 'Play count',      type: 'number' },
  year:            { label: 'Year',            type: 'number' },
  duration_sec:    { label: 'Duration (sec)',  type: 'number' },
  color:           { label: 'Track colour',    type: 'color'  },
  title:           { label: 'Title',           type: 'string' },
  artist:          { label: 'Artist',          type: 'string' },
  album:           { label: 'Album',           type: 'string' },
  genre:           { label: 'Genre',           type: 'string' },
  comment:         { label: 'Comment',         type: 'string' },
  label:           { label: 'Record label',    type: 'string' },
  remixer:         { label: 'Remixer',         type: 'string' },
  original_artist: { label: 'Original artist', type: 'string' },
  file_type:       { label: 'File type',       type: 'string' },
  key:             { label: 'Key',             type: 'key'    },
  tag:             { label: 'Tag',             type: 'tag'    },
  date_added:      { label: 'Date added',      type: 'date'   },
  date_created:    { label: 'Date created',    type: 'date'   },
  release_date:    { label: 'Release date',    type: 'date'   },
};
const SMART_OPS = {
  number: [['eq','equals'], ['ne','is not'], ['lt','<'], ['lte','≤'], ['gt','>'], ['gte','≥'], ['between','between']],
  string: [['is','equals'], ['is_not','is not'], ['contains','contains'], ['not_contains','doesn’t contain'],
           ['starts_with','starts with'], ['ends_with','ends with'], ['is_empty','is empty'], ['not_empty','not empty']],
  // rekordbox's key operators, plus Vynull's harmonic-mixing match.
  key:    [['is','equals'], ['is_not','is not'], ['contains','contains'], ['not_contains','doesn’t contain'],
           ['starts_with','starts with'], ['ends_with','ends with'], ['compatible','is compatible with']],
  tag:    [['has','has tag'], ['not_has','doesn’t have tag'], ['has_any','has any tag'], ['none','has no tags']],
  date:   [['in_last_days','in the last (days)'], ['not_in_last_days','not in the last (days)'],
           ['in_last_months','in the last (months)'], ['not_in_last_months','not in the last (months)'],
           ['on','is'], ['not_on','is not'], ['before','before'], ['after','after'], ['between','between']],
  color:  [['eq','is'], ['ne','is not']],
};
// Key operators that test the key's name as text, so take a typed value.
const SMART_KEY_TEXT_OPS = ['contains', 'not_contains', 'starts_with', 'ends_with'];
// Date operators whose value is a count of days or months, not a date.
const SMART_DATE_COUNT_OPS = ['in_last_days', 'not_in_last_days', 'in_last_months', 'not_in_last_months'];
// Camelot key list for key field dropdowns.
const SMART_KEYS = (function() {
  const out = [];
  for (let n = 1; n <= 12; n++) { out.push(n + 'A'); out.push(n + 'B'); }
  return out;
})();

// Holds the in-flight editor state so tab switches preserve unsaved
// edits. {id, name, rules} where id is null for "create new".
let smartEditorState = null;

async function openSmartEditor(playlistID) {
  // Tag rules pick from the tag catalog; load it (fresh: an import may have
  // added tags) before rendering, or the tag dropdowns come up empty.
  await ensureAllTags(true);
  ensureSmartEditor();
  const existing = playlistID != null ? playlistsCache.find(p => p.id === playlistID) : null;
  smartEditorState = {
    id: playlistID != null ? playlistID : null,
    name: existing ? existing.name : '',
    rules: existing && existing.rules
      ? JSON.parse(JSON.stringify(existing.rules))
      : { combinator: 'all', conditions: [defaultSmartCond()] },
    view: 'form',
  };
  document.getElementById('sm-backdrop').classList.add('show');
  document.getElementById('sm-modal').classList.add('show');
  renderSmartEditor();
}

function ensureSmartEditor() {
  if (document.getElementById('sm-modal')) return;
  const backdrop = document.createElement('div');
  backdrop.id = 'sm-backdrop';
  backdrop.className = 'sm-backdrop';
  backdrop.addEventListener('click', closeSmartEditor);
  document.body.appendChild(backdrop);
  const modal = document.createElement('div');
  modal.id = 'sm-modal';
  modal.className = 'sm-modal';
  document.body.appendChild(modal);
}

function closeSmartEditor() {
  const backdrop = document.getElementById('sm-backdrop');
  const modal = document.getElementById('sm-modal');
  if (backdrop) backdrop.classList.remove('show');
  if (modal) modal.classList.remove('show');
  smartEditorState = null;
}

function defaultSmartCond() {
  return { field: 'bpm', operator: 'between', value: [120, 130] };
}

function renderSmartEditor() {
  const modal = document.getElementById('sm-modal');
  const s = smartEditorState;
  if (!modal || !s) return;
  const title = s.id != null ? 'EDIT SMART PLAYLIST' : 'NEW SMART PLAYLIST';
  modal.innerHTML = `
    <div class="sm-head">
      <span style="color:var(--text2);font-size:10px;letter-spacing:.18em">${title}</span>
      <input type="text" class="name-input" id="sm-name" placeholder="PLAYLIST NAME" value="${escape(s.name || '')}">
      <div class="tabs">
        <button type="button" class="tab-btn ${s.view === 'form' ? 'active' : ''}" data-sm-view="form">FORM</button>
        <button type="button" class="tab-btn ${s.view === 'json' ? 'active' : ''}" data-sm-view="json">JSON</button>
      </div>
    </div>
    <div class="sm-body">${s.view === 'form' ? renderSmartFormBody(s.rules) : renderSmartJsonBody(s.rules)}</div>
    <div class="sm-foot">
      <span class="preview" id="sm-preview">…</span>
      <button type="button" id="sm-cancel">CANCEL</button>
      <button type="button" class="primary" id="sm-save">SAVE</button>
    </div>
  `;
  wireSmartEditor();
  refreshSmartPreview();
}

function renderSmartFormBody(rules) {
  return `<div class="combinator-row">
    Match
    <select data-sm-combinator>
      <option value="all" ${rules.combinator !== 'any' ? 'selected' : ''}>ALL</option>
      <option value="any" ${rules.combinator === 'any' ? 'selected' : ''}>ANY</option>
    </select>
    of the following:
  </div>
  ${renderSmartGroupBody(rules, '0')}`;
}

// renderSmartGroupBody renders the conditions inside one combinator
// level. Path is a "/"-joined index trail like "0" or "0/2/1" used by
// event handlers to navigate the in-memory rules tree.
function renderSmartGroupBody(rules, path) {
  const rows = (rules.conditions || []).map((c, i) => {
    const p = path + '/' + i;
    if (c.group) {
      return `<div class="sm-group" data-sm-path="${p}">
        <div class="combinator-row">
          Match
          <select data-sm-combinator data-sm-group="1">
            <option value="all" ${c.group.combinator !== 'any' ? 'selected' : ''}>ALL</option>
            <option value="any" ${c.group.combinator === 'any' ? 'selected' : ''}>ANY</option>
          </select>
          of the following:
          <button type="button" class="rm" data-sm-rm style="margin-left:auto" title="Remove group">×</button>
        </div>
        ${renderSmartGroupBody(c.group, p)}
      </div>`;
    }
    return `<div class="sm-cond" data-sm-path="${p}">
      ${renderSmartCond(c)}
      <button type="button" class="rm" data-sm-rm title="Remove condition">×</button>
    </div>`;
  }).join('');
  return rows + `<div class="sm-add-row">
    <button type="button" data-sm-add-cond data-sm-parent="${path}">+ CONDITION</button>
    <button type="button" data-sm-add-group data-sm-parent="${path}">+ GROUP</button>
  </div>`;
}

function renderSmartCond(c) {
  const meta = SMART_FIELDS[c.field] || SMART_FIELDS.bpm;
  const ops = SMART_OPS[meta.type] || [];
  const fieldSel = `<select data-sm-field>${Object.entries(SMART_FIELDS).map(([k,m]) =>
    `<option value="${k}" ${k === c.field ? 'selected' : ''}>${escape(m.label)}</option>`
  ).join('')}</select>`;
  const opSel = `<select data-sm-op>${ops.map(([k,l]) =>
    `<option value="${k}" ${k === c.operator ? 'selected' : ''}>${escape(l)}</option>`
  ).join('')}</select>`;
  return fieldSel + opSel + renderSmartValueInput(c, meta);
}

// renderSmartValueInput emits whatever value control fits the
// (type, operator) combination — text/number for plain comparisons,
// two inputs for "between", a Camelot dropdown for keys, a tag picker
// for tag conditions, a colour swatch grid for colours, nothing for
// no-arg operators like is_empty/has_any.
function renderSmartValueInput(c, meta) {
  const op = c.operator || '';
  if (op === 'is_empty' || op === 'not_empty' || op === 'has_any' || op === 'none') {
    return '';
  }
  if (meta.type === 'number' && op === 'between') {
    const lo = Array.isArray(c.value) ? c.value[0] : '';
    const hi = Array.isArray(c.value) ? c.value[1] : '';
    return `<input type="number" data-sm-val data-sm-idx="0" value="${lo}" style="width:70px">
            <span style="color:var(--text3)">…</span>
            <input type="number" data-sm-val data-sm-idx="1" value="${hi}" style="width:70px">`;
  }
  if (meta.type === 'number' || meta.type === 'color') {
    const v = (c.value != null && !Array.isArray(c.value)) ? c.value : '';
    if (meta.type === 'color') {
      return `<select data-sm-val>${[
        [0,'(none)'], [1,'Pink'], [2,'Red'], [3,'Orange'], [4,'Yellow'],
        [5,'Green'], [6,'Aqua'], [7,'Blue'], [8,'Purple'],
      ].map(([n,l]) => `<option value="${n}" ${n == v ? 'selected' : ''}>${l}</option>`).join('')}</select>`;
    }
    return `<input type="number" data-sm-val value="${v}" style="width:100px">`;
  }
  if (meta.type === 'key' && SMART_KEY_TEXT_OPS.includes(op)) {
    return `<input type="text" data-sm-val value="${escape(c.value || '')}" style="min-width:100px">`;
  }
  if (meta.type === 'key') {
    // An imported rekordbox rule may name its key in classic notation
    // ("Am"); keep that value as its own option so opening the editor never
    // swaps the rule's key for the first one listed.
    const v = c.value == null ? '' : String(c.value);
    // Each key is stored in Camelot and labelled in both notations, led by
    // the user's key display setting ("8A · Am" or "Am · 8A").
    const opts = SMART_KEYS.map(k =>
      `<option value="${k}" ${k === v ? 'selected' : ''}>${escape(keyBoth(k))}</option>`);
    if (v && !SMART_KEYS.includes(v)) {
      const p = KEY_PAIRS[v.trim().toUpperCase()];
      opts.unshift(`<option value="${escape(v)}" selected>${escape(v)}${p ? ' · ' + p[0] : ''}</option>`);
    }
    return `<select data-sm-val>${opts.join('')}</select>`;
  }
  if (meta.type === 'tag') {
    const tags = (allTagsCache || []);
    const v = (c.value || '');
    // Rules match tag names case-insensitively; select the rule's tag that
    // way, and keep a name that isn't in the catalog as its own option so
    // saving never swaps the rule's tag for the first one listed.
    const known = tags.some(t => t.name.toLowerCase() === v.toLowerCase());
    const opts = tags.map(t =>
      `<option value="${escape(t.name)}" ${t.name.toLowerCase() === v.toLowerCase() ? 'selected' : ''}>${escape(t.name)}</option>`);
    if (v && !known) opts.unshift(`<option value="${escape(v)}" selected>${escape(v)} (not in tag list)</option>`);
    return `<select data-sm-val>${opts.join('') || '<option value="">— NO TAGS YET —</option>'}</select>`;
  }
  if (meta.type === 'date') {
    if (SMART_DATE_COUNT_OPS.includes(op)) {
      const v = (c.value != null && c.value !== '') ? c.value : 7;
      return `<input type="number" data-sm-val value="${v}" style="width:80px">`;
    }
    if (op === 'between') {
      const day = x => (typeof x === 'string') ? x.substring(0, 10) : '';
      const lo = Array.isArray(c.value) ? day(c.value[0]) : '';
      const hi = Array.isArray(c.value) ? day(c.value[1]) : '';
      return `<input type="date" data-sm-val data-sm-idx="0" value="${lo}">
            <span style="color:var(--text3)">…</span>
            <input type="date" data-sm-val data-sm-idx="1" value="${hi}">`;
    }
    const v = (typeof c.value === 'string') ? c.value.substring(0, 10) : '';
    return `<input type="date" data-sm-val value="${v}">`;
  }
  // string
  return `<input type="text" data-sm-val value="${escape(c.value || '')}" style="min-width:160px">`;
}

function renderSmartJsonBody(rules) {
  return `<textarea class="sm-json" id="sm-json-text">${escape(JSON.stringify(rules, null, 2))}</textarea>
    <div style="color:var(--text3);font-size:10px;margin-top:6px">
      Edit the raw SmartRules JSON. Save parses this back into the form.
    </div>`;
}

function wireSmartEditor() {
  const modal = document.getElementById('sm-modal');
  const s = smartEditorState;
  if (!modal || !s) return;

  modal.querySelector('#sm-cancel').onclick = closeSmartEditor;
  modal.querySelector('#sm-save').onclick = saveSmartEditor;
  modal.querySelectorAll('[data-sm-view]').forEach(btn => {
    btn.onclick = () => switchSmartView(btn.getAttribute('data-sm-view'));
  });
  modal.querySelector('#sm-name').oninput = e => { s.name = e.target.value; };

  if (s.view === 'json') {
    const ta = modal.querySelector('#sm-json-text');
    if (ta) {
      ta.oninput = () => {
        // Don't parse on every keystroke — wait for tab switch or save.
        // But do refresh the preview if it parses.
        try {
          const parsed = JSON.parse(ta.value);
          s.rules = parsed;
          refreshSmartPreview();
        } catch (_) { /* ignore until valid */ }
      };
    }
    return;
  }

  // Form-view wiring: combinator selects, add/remove buttons, value inputs.
  modal.querySelectorAll('[data-sm-combinator]').forEach(sel => {
    sel.onchange = () => {
      const path = sel.closest('[data-sm-path]')?.getAttribute('data-sm-path') || '0';
      const target = sel.hasAttribute('data-sm-group') ? smartAt(path).group : s.rules;
      target.combinator = sel.value;
      refreshSmartPreview();
    };
  });
  modal.querySelectorAll('[data-sm-rm]').forEach(btn => {
    btn.onclick = () => {
      const path = btn.closest('[data-sm-path]').getAttribute('data-sm-path');
      smartRemoveAt(path);
      renderSmartEditor();
    };
  });
  modal.querySelectorAll('[data-sm-add-cond]').forEach(btn => {
    btn.onclick = () => {
      smartAppend(btn.getAttribute('data-sm-parent'), defaultSmartCond());
      renderSmartEditor();
    };
  });
  modal.querySelectorAll('[data-sm-add-group]').forEach(btn => {
    btn.onclick = () => {
      smartAppend(btn.getAttribute('data-sm-parent'), { group: { combinator: 'all', conditions: [defaultSmartCond()] } });
      renderSmartEditor();
    };
  });
  modal.querySelectorAll('[data-sm-field]').forEach(sel => {
    sel.onchange = () => {
      const path = sel.closest('[data-sm-path]').getAttribute('data-sm-path');
      const cond = smartAt(path);
      cond.field = sel.value;
      const meta = SMART_FIELDS[cond.field];
      const ops = SMART_OPS[meta.type];
      cond.operator = ops[0][0]; // reset to first valid op for the new field
      cond.value = '';
      renderSmartEditor();
    };
  });
  modal.querySelectorAll('[data-sm-op]').forEach(sel => {
    sel.onchange = () => {
      const path = sel.closest('[data-sm-path]').getAttribute('data-sm-path');
      const cond = smartAt(path);
      cond.operator = sel.value;
      // Reset value shape when switching to/from "between" or no-arg ops.
      cond.value = sel.value === 'between' ? [0, 0] : SMART_DATE_COUNT_OPS.includes(sel.value) ? 7 : '';
      renderSmartEditor();
    };
  });
  modal.querySelectorAll('[data-sm-val]').forEach(el => {
    el.oninput = el.onchange = () => {
      const path = el.closest('[data-sm-path]').getAttribute('data-sm-path');
      const cond = smartAt(path);
      const meta = SMART_FIELDS[cond.field];
      const idx = el.getAttribute('data-sm-idx');
      let v = el.value;
      if (meta.type === 'number' || meta.type === 'color' ||
          (meta.type === 'date' && SMART_DATE_COUNT_OPS.includes(cond.operator))) {
        v = v === '' ? null : Number(v);
      }
      if (cond.operator === 'between') {
        if (!Array.isArray(cond.value)) cond.value = [0, 0];
        cond.value[parseInt(idx, 10)] = v;
      } else {
        cond.value = v;
      }
      refreshSmartPreview();
    };
  });

  // Sync each select-based value control back to state right after
  // render. Two cases this covers:
  //   1. A field/op change reset cond.value="", but the <select>
  //      auto-displays its first option — no change event fires, so
  //      without this the saved value stays empty.
  //   2. An existing rule's value isn't present in the current option
  //      list (e.g. tag list cold-loaded after the editor opened) —
  //      browser falls back to the first option; sync captures that.
  // Skipped for inputs and the multi-value "between" pair (handled
  // independently by their own change handlers).
  let syncedAny = false;
  modal.querySelectorAll('[data-sm-val]').forEach(el => {
    if (el.tagName !== 'SELECT') return;
    const cond = smartAt(el.closest('[data-sm-path]').getAttribute('data-sm-path'));
    if (cond.operator === 'between') return;
    if (!el.value) return; // empty options list — nothing to sync
    const meta = SMART_FIELDS[cond.field];
    let displayed = el.value;
    if (meta.type === 'color') displayed = Number(displayed);
    if (displayed !== cond.value) {
      cond.value = displayed;
      syncedAny = true;
    }
  });
  if (syncedAny) refreshSmartPreview();
}

// smartAt / smartAppend / smartRemoveAt walk the path "0/2/1" into
// the in-memory rules tree. Root path is "0" (the top-level rules);
// after that each component is the index inside the parent's
// conditions array.
function smartAt(path) {
  const parts = path.split('/').slice(1).map(n => parseInt(n, 10));
  let node = smartEditorState.rules;
  for (let i = 0; i < parts.length - 1; i++) {
    const c = node.conditions[parts[i]];
    node = c.group;
  }
  return node.conditions[parts[parts.length - 1]];
}
function smartAppend(path, item) {
  const parts = path.split('/').slice(1).map(n => parseInt(n, 10));
  let node = smartEditorState.rules;
  for (let i = 0; i < parts.length; i++) {
    const c = node.conditions[parts[i]];
    if (!c) break;
    node = c.group;
  }
  node.conditions.push(item);
}
function smartRemoveAt(path) {
  const parts = path.split('/').slice(1).map(n => parseInt(n, 10));
  let node = smartEditorState.rules;
  for (let i = 0; i < parts.length - 1; i++) {
    const c = node.conditions[parts[i]];
    node = c.group;
  }
  node.conditions.splice(parts[parts.length - 1], 1);
}

function switchSmartView(view) {
  const s = smartEditorState;
  if (!s || s.view === view) return;
  // If leaving JSON, validate parses; if it doesn't, stay put and toast.
  if (s.view === 'json') {
    const ta = document.getElementById('sm-json-text');
    if (ta) {
      try { s.rules = JSON.parse(ta.value); }
      catch (_) { toast('INVALID JSON', 'error'); return; }
    }
  }
  s.view = view;
  renderSmartEditor();
}

// refreshSmartPreview hits the would-be-saved rules against the
// in-memory allTracks via a lightweight client-side evaluator that
// mirrors the server's. Saves us a round-trip per keystroke and works
// even before the playlist exists. Accuracy depends on having the
// fields populated client-side (most do; play_count comes through too).
function refreshSmartPreview() {
  const el = document.getElementById('sm-preview');
  if (!el || !smartEditorState) return;
  const matches = (allTracks || []).filter(t => smartMatchClient(smartEditorState.rules, t)).length;
  el.classList.toggle('matches', matches > 0);
  el.textContent = matches + ' track' + (matches === 1 ? '' : 's') + ' would match';
}

// Client-side mirror of evalLeaf for the live preview counter. Doesn't
// need to be exhaustive — it's a UI affordance, not the source of
// truth. The server re-evaluates on every /tracks read.
function smartMatchClient(rules, t) {
  if (!rules || !rules.conditions || rules.conditions.length === 0) return true;
  const all = rules.combinator !== 'any';
  for (const c of rules.conditions) {
    let ok;
    if (c.group) ok = smartMatchClient(c.group, t);
    else ok = smartMatchLeaf(c, t);
    if (all && !ok) return false;
    if (!all && ok) return true;
  }
  return all;
}
function smartMatchLeaf(c, t) {
  const meta = SMART_FIELDS[c.field];
  if (!meta) return false;
  const op = c.operator;
  const v = c.value;
  if (meta.type === 'number') {
    const x = ({bpm: t.bpm, year: t.year, rating: t.rating, play_count: t.play_count,
                duration_sec: t.duration})[c.field];
    if (x == null) return false;
    if (op === 'between' && Array.isArray(v)) return x >= v[0] && x <= v[1];
    const n = Number(v);
    switch (op) {
      case 'eq': return x === n;
      case 'ne': return x !== n;
      case 'lt': return x < n;
      case 'lte': return x <= n;
      case 'gt': return x > n;
      case 'gte': return x >= n;
    }
    return false;
  }
  if (meta.type === 'color') {
    const x = t.color_id || 0;
    const n = Number(v);
    if (op === 'eq') return x === n;
    if (op === 'ne') return x !== n;
    return false;
  }
  if (meta.type === 'string') {
    const s = (t[c.field] || '').toLowerCase();
    const q = (v || '').toString().toLowerCase();
    switch (op) {
      case 'is':            return s === q;
      case 'is_not':        return s !== q;
      case 'contains':      return s.includes(q);
      case 'not_contains':  return !s.includes(q);
      case 'starts_with':   return s.startsWith(q);
      case 'ends_with':     return s.endsWith(q);
      case 'is_empty':      return s === '';
      case 'not_empty':     return s !== '';
    }
    return false;
  }
  if (meta.type === 'key') {
    // Compare keys in either notation, as the server does.
    const cam = x => { const k = String(x || '').toUpperCase(); return (KEY_PAIRS[k] || [k])[0]; };
    const trackKey = cam(t.key);
    const target = cam(v);
    if (op === 'is') return trackKey === target;
    if (op === 'is_not') return trackKey !== target;
    if (SMART_KEY_TEXT_OPS.includes(op)) {
      // The key's name in each notation, as the server tests it.
      const raw = String(t.key || '');
      const p = KEY_PAIRS[raw.trim().toUpperCase()];
      const names = (p ? [raw, p[0], p[1]] : [raw]).map(n => n.toLowerCase());
      const q = String(v || '').toLowerCase();
      const hit = f => names.some(f);
      if (op === 'contains') return hit(n => n.includes(q));
      if (op === 'not_contains') return raw !== '' && !hit(n => n.includes(q));
      if (op === 'starts_with') return hit(n => n.startsWith(q));
      if (op === 'ends_with') return hit(n => n.endsWith(q));
    }
    // compatible — quick approximation; server does the authoritative check
    return trackKey === target;
  }
  if (meta.type === 'tag') {
    const tags = (t.tags || []).map(n => n.toLowerCase());
    if (op === 'has_any') return tags.length > 0;
    if (op === 'none')    return tags.length === 0;
    const q = (v || '').toString().toLowerCase();
    if (op === 'has')     return tags.includes(q);
    if (op === 'not_has') return !tags.includes(q);
    return false;
  }
  // date_added preview skipped — server-side authoritative
  return true;
}

async function saveSmartEditor() {
  const s = smartEditorState;
  if (!s) return;
  // If we're on the JSON tab, parse it first.
  if (s.view === 'json') {
    const ta = document.getElementById('sm-json-text');
    if (ta) {
      try { s.rules = JSON.parse(ta.value); }
      catch (_) { toast('INVALID JSON', 'error'); return; }
    }
  }
  const name = (s.name || '').trim();
  if (!name) { toast('NAME REQUIRED', 'error'); return; }

  if (s.id == null) {
    // Create new smart playlist. Parent inherits from current
    // selection if it's a folder, else root.
    let parent = 0;
    const sel = plSelectedID != null ? playlistsCache.find(p => p.id === plSelectedID) : null;
    if (sel && sel.is_folder) parent = sel.id;
    const r = await fetch('/api/playlists', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ name, parent_id: parent, is_smart: true, rules: s.rules }),
    });
    if (!r.ok) { toast('CREATE FAILED', 'error'); return; }
    toast(`+ SMART · ${name.toUpperCase()}`, 'success');
  } else {
    // Update existing: rename + replace rules in two requests.
    const cur = playlistsCache.find(p => p.id === s.id);
    if (cur && cur.name !== name) {
      const r1 = await fetch(`/api/playlists/${s.id}`, {
        method: 'PUT', headers: {'Content-Type':'application/json'},
        body: JSON.stringify({ name }),
      });
      if (!r1.ok) { toast('RENAME FAILED', 'error'); return; }
    }
    const r2 = await fetch(`/api/playlists/${s.id}/rules`, {
      method: 'PUT', headers: {'Content-Type':'application/json'},
      body: JSON.stringify(s.rules),
    });
    if (!r2.ok) { toast('UPDATE RULES FAILED', 'error'); return; }
    toast('SAVED', 'success');
  }
  closeSmartEditor();
  await loadPlaylists(true);
}

// openInputModal shows a single-field text-entry modal styled like the rest of
// the app (replacing the browser prompt()). Resolves to the trimmed value, or
// null if cancelled/dismissed. Enter submits, Escape cancels.
function openInputModal({ title, label, placeholder = '', value = '', okLabel = 'CREATE' }) {
  return new Promise((resolve) => {
    const back = document.createElement('div');
    back.className = 'menu-modal-backdrop show';
    const m = document.createElement('div');
    m.className = 'menu-modal show';
    m.style.width = 'min(420px, 96vw)';
    m.innerHTML = `
      <div class="head">
        <span class="title">${escape(title)}</span>
        <button class="x" type="button" title="Close">×</button>
      </div>
      <div class="body">
        <div class="export-row" style="margin-bottom:0">
          <label class="export-label">${escape(label)}</label>
          <input type="text" class="input" id="input-modal-field" placeholder="${escape(placeholder)}" autocomplete="off" spellcheck="false" value="${escape(value)}">
        </div>
      </div>
      <div class="foot">
        <span class="spacer"></span>
        <button type="button" id="input-modal-cancel">CANCEL</button>
        <button type="button" class="primary" id="input-modal-ok">${escape(okLabel)}</button>
      </div>`;
    document.body.appendChild(back);
    document.body.appendChild(m);

    const field = m.querySelector('#input-modal-field');
    let done = false;
    const finish = (val) => {
      if (done) return;
      done = true;
      document.removeEventListener('keydown', onKey);
      m.remove();
      back.remove();
      resolve(val);
    };
    const submit = () => {
      const v = field.value.trim();
      if (!v) { field.focus(); return; }
      finish(v);
    };
    const onKey = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); finish(null); }
      else if (e.key === 'Enter') { e.preventDefault(); submit(); }
    };
    document.addEventListener('keydown', onKey);
    back.onclick = () => finish(null);
    m.querySelector('.x').onclick = () => finish(null);
    m.querySelector('#input-modal-cancel').onclick = () => finish(null);
    m.querySelector('#input-modal-ok').onclick = submit;
    field.focus();
    field.select();
  });
}

// openConfirmModal shows a styled yes/no confirmation (replacing the browser
// confirm()). Resolves to true if confirmed, false if cancelled/dismissed.
// Multi-line messages are honoured (white-space: pre-line). For destructive
// actions pass danger:true to render the action button red. Enter confirms,
// Escape cancels.
function openConfirmModal({ title = 'CONFIRM', message = '', okLabel = 'OK', danger = false }) {
  return new Promise((resolve) => {
    const back = document.createElement('div');
    back.className = 'menu-modal-backdrop show';
    const m = document.createElement('div');
    m.className = 'menu-modal show';
    m.style.width = 'min(460px, 96vw)';
    m.innerHTML = `
      <div class="head">
        <span class="title">${escape(title)}</span>
        <button class="x" type="button" title="Close">×</button>
      </div>
      <div class="body">
        <div class="confirm-msg">${escape(message)}</div>
      </div>
      <div class="foot">
        <span class="spacer"></span>
        <button type="button" id="confirm-cancel">CANCEL</button>
        <button type="button" class="${danger ? 'danger' : 'primary'}" id="confirm-ok">${escape(okLabel)}</button>
      </div>`;
    document.body.appendChild(back);
    document.body.appendChild(m);

    let done = false;
    const finish = (val) => {
      if (done) return;
      done = true;
      document.removeEventListener('keydown', onKey);
      m.remove();
      back.remove();
      resolve(val);
    };
    const onKey = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); finish(false); }
      else if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    };
    document.addEventListener('keydown', onKey);
    back.onclick = () => finish(false);
    m.querySelector('.x').onclick = () => finish(false);
    m.querySelector('#confirm-cancel').onclick = () => finish(false);
    const okBtn = m.querySelector('#confirm-ok');
    okBtn.onclick = () => finish(true);
    okBtn.focus();
  });
}

// openChoiceModal is openConfirmModal with one button per choice. Resolves to
// the chosen value, or null on cancel / Escape / backdrop. Enter picks the
// choice marked primary. A disabled choice shows its title as a tooltip.
function openChoiceModal({ title = 'CHOOSE', message = '', choices = [] }) {
  return new Promise((resolve) => {
    const back = document.createElement('div');
    back.className = 'menu-modal-backdrop show';
    const m = document.createElement('div');
    m.className = 'menu-modal show';
    m.style.width = 'min(520px, 96vw)';
    const btns = choices.map((c, i) =>
      `<button type="button" data-choice="${i}" class="${c.primary ? 'primary' : ''}"` +
      `${c.disabled ? ' disabled' : ''}${c.title ? ` title="${escape(c.title)}"` : ''}>${escape(c.label)}</button>`).join('');
    m.innerHTML = `
      <div class="head">
        <span class="title">${escape(title)}</span>
        <button class="x" type="button" title="Close">×</button>
      </div>
      <div class="body">
        <div class="confirm-msg">${escape(message)}</div>
      </div>
      <div class="foot">
        <button type="button" id="choice-cancel">CANCEL</button>
        <span class="spacer"></span>
        ${btns}
      </div>`;
    document.body.appendChild(back);
    document.body.appendChild(m);

    let done = false;
    const finish = (val) => {
      if (done) return;
      done = true;
      document.removeEventListener('keydown', onKey);
      m.remove();
      back.remove();
      resolve(val);
    };
    const primary = choices.findIndex(c => c.primary && !c.disabled);
    const onKey = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); finish(null); }
      else if (e.key === 'Enter' && primary >= 0) { e.preventDefault(); finish(choices[primary].value); }
    };
    document.addEventListener('keydown', onKey);
    back.onclick = () => finish(null);
    m.querySelector('.x').onclick = () => finish(null);
    m.querySelector('#choice-cancel').onclick = () => finish(null);
    m.querySelectorAll('[data-choice]').forEach(b => {
      b.onclick = () => finish(choices[+b.dataset.choice].value);
    });
    const focus = m.querySelector(`[data-choice="${primary}"]`);
    if (focus) focus.focus();
  });
}

// POST paths to /api/tracks/add and report progress. Small batches return
// synchronously; large ones return a job id we poll until done. statusEl (if
// given) shows live progress; onDone(result) fires once at the end with the
// final counts ({added, ...}).
async function addTracksWithProgress(paths, statusEl, onDone) {
  paths = (paths || []).filter(Boolean);
  if (!paths.length) return;
  const setStatus = (t) => { if (statusEl) { statusEl.style.display = 'block'; statusEl.textContent = t; } };
  setStatus('Adding…');
  let j;
  try {
    const r = await fetch('/api/tracks/add', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paths }),
    });
    if (!r.ok) throw new Error((await errorText(r)) || r.statusText);
    j = await r.json();
  } catch (e) { setStatus('Error: ' + e.message); return; }

  if (!j.async) {
    setStatus(j.added
      ? `Added ${j.added} track${j.added === 1 ? '' : 's'} — library now ${j.total}.`
      : 'No new tracks added (already in library, or no supported audio).');
    if (onDone) onDone(j);
    return j;
  }

  // Large batch: poll the status endpoint until the background job finishes.
  const jobId = j.job_id, total = j.total;
  setStatus(`Adding 0 / ${total}…`);
  return await new Promise((resolve) => {
    const poll = async () => {
      let st;
      try {
        const sr = await fetch('/api/tracks/add/status?id=' + encodeURIComponent(jobId));
        if (!sr.ok) throw new Error('lost track of the add job');
        st = await sr.json();
      } catch (e) { setStatus('Error: ' + e.message); resolve(null); return; }
      if (st.done) {
        setStatus(`Added ${st.added} of ${total}` +
          (st.skipped ? `, ${st.skipped} already present` : '') +
          (st.failed ? `, ${st.failed} failed` : '') + '.');
        if (onDone) onDone(st);
        resolve(st);
        return;
      }
      setStatus(`Adding ${st.added + st.failed + st.skipped} / ${total}…`);
      setTimeout(poll, 700);
    };
    poll();
  });
}

// startImportProgress shows the import progress bar in dialog `m` and polls
// /api/import/status until stopped, driving the phase label + bar. The import
// POST is synchronous, so we poll alongside it and call the returned stop()
// when it resolves. A determinate bar shows for counted phases (done/total),
// an animated indeterminate bar for the rest.
function startImportProgress(m) {
  const prog = m.querySelector('#imp-progress');
  const phaseEl = m.querySelector('#imp-prog-phase');
  const fillEl = m.querySelector('#imp-prog-fill');
  const countEl = m.querySelector('#imp-prog-count');
  if (!prog) return () => {};
  prog.classList.add('show', 'indeterminate');
  phaseEl.textContent = 'Starting…';
  fillEl.style.width = '';
  countEl.textContent = '';
  let running = true;
  (async () => {
    while (running) {
      try {
        const s = await (await fetch('/api/import/status')).json();
        if (running && s.active) {
          phaseEl.textContent = s.phase || 'Working…';
          if (s.total > 0) {
            prog.classList.remove('indeterminate');
            fillEl.style.width = Math.min(100, Math.round((s.done / s.total) * 100)) + '%';
            countEl.textContent = s.done.toLocaleString() + ' / ' + s.total.toLocaleString();
          } else {
            prog.classList.add('indeterminate');
            fillEl.style.width = '';
            countEl.textContent = '';
          }
        }
      } catch (e) { /* transient — keep polling */ }
      await new Promise(r => setTimeout(r, 350));
    }
  })();
  return () => { running = false; prog.classList.remove('show', 'indeterminate'); };
}

// Directory browser for "add files/folders". Navigates the server's filesystem
// (confined server-side to the configured browse roots) and adds a whole folder
// or hand-picked files to the library. onDone(json) fires after each add.
function openAddBrowserDialog(onDone) {
  const back = document.createElement('div');
  back.className = 'menu-modal-backdrop show';
  const m = document.createElement('div');
  m.className = 'menu-modal show';
  m.innerHTML = `
    <div class="head">
      <span>ADD FROM FOLDER</span>
      <button class="x" type="button" title="Close">×</button>
    </div>
    <div class="body">
      <div style="display:flex; gap:6px; margin-bottom:10px;">
        <input type="text" id="fs-path-input" class="input" placeholder="Type or paste a folder path, then Enter" autocomplete="off" spellcheck="false" style="flex:1">
        <button type="button" id="fs-go" class="source-btn">Go</button>
      </div>
      <div class="fs-crumb" id="fs-crumb">Loading…</div>
      <div class="fs-list" id="fs-list"></div>
      <div class="export-status" id="fs-status"></div>
    </div>
    <div class="foot">
      <button type="button" id="fs-up">↑ Up</button>
      <span class="spacer"></span>
      <button type="button" id="fs-add-sel">Add selected (0)</button>
      <button type="button" class="primary" id="fs-add-folder">Add this folder</button>
    </div>
  `;
  document.body.appendChild(back);
  document.body.appendChild(m);
  const close = () => { m.remove(); back.remove(); };
  back.onclick = close;
  m.querySelector('.x').onclick = close;

  const crumb = m.querySelector('#fs-crumb');
  const pathInput = m.querySelector('#fs-path-input');
  const goBtn = m.querySelector('#fs-go');
  const listEl = m.querySelector('#fs-list');
  const statusEl = m.querySelector('#fs-status');
  const upBtn = m.querySelector('#fs-up');
  const addSelBtn = m.querySelector('#fs-add-sel');
  const addFolderBtn = m.querySelector('#fs-add-folder');
  let cur = { path: '', parent: '', roots: true, entries: [] };
  const selected = new Set();
  const updateSel = () => {
    addSelBtn.textContent = `Add selected (${selected.size})`;
    addSelBtn.disabled = selected.size === 0;
  };

  async function load(path) {
    listEl.innerHTML = '<div class="export-hint" style="padding:10px">Loading…</div>';
    statusEl.textContent = '';
    selected.clear(); updateSel();
    try {
      const r = await fetch('/api/fs/list' + (path ? '?path=' + encodeURIComponent(path) : ''));
      if (!r.ok) throw new Error((await errorText(r)) || r.statusText);
      cur = await r.json();
    } catch (e) {
      listEl.innerHTML = `<div class="export-hint" style="padding:10px">Error: ${escape(e.message)}</div>`;
      return;
    }
    crumb.textContent = cur.roots ? 'Choose a location, or type a path:' : cur.path;
    pathInput.value = cur.roots ? '' : cur.path;
    upBtn.style.display = cur.roots ? 'none' : '';
    addFolderBtn.style.display = cur.roots ? 'none' : '';
    if (!cur.entries || !cur.entries.length) {
      listEl.innerHTML = '<div class="export-hint" style="padding:10px">' +
        (cur.roots ? 'No browse locations found.' : 'No subfolders or supported audio here.') + '</div>';
      return;
    }
    listEl.innerHTML = '';
    cur.entries.forEach(e => {
      const row = document.createElement('div');
      row.className = 'fs-row' + (e.is_dir ? '' : ' file');
      if (e.is_dir) {
        row.innerHTML = `<span class="fs-ico">▸</span><span class="fs-name">${escape(e.name)}</span>`;
        row.onclick = () => load(e.path);
      } else {
        const cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.onchange = () => { cb.checked ? selected.add(e.path) : selected.delete(e.path); updateSel(); };
        const ico = document.createElement('span');
        ico.className = 'fs-ico'; ico.textContent = '♪';
        const name = document.createElement('span');
        name.className = 'fs-name'; name.textContent = e.name;
        name.onclick = () => { cb.checked = !cb.checked; cb.onchange(); };
        row.append(cb, ico, name);
      }
      listEl.appendChild(row);
    });
  }

  async function doAdd(paths) {
    paths = paths.filter(Boolean);
    if (!paths.length) return;
    addFolderBtn.disabled = addSelBtn.disabled = true;
    await addTracksWithProgress(paths, statusEl, onDone);
    addFolderBtn.disabled = false;
    updateSel();
  }

  const goTo = () => { const p = pathInput.value.trim(); if (p) load(p); };
  goBtn.onclick = goTo;
  pathInput.addEventListener('keydown', (e) => { if (e.key === 'Enter') goTo(); });
  upBtn.onclick = () => load(cur.parent || '');
  addFolderBtn.onclick = () => doAdd([cur.path]);
  addSelBtn.onclick = () => doAdd([...selected]);
  updateSel();
  load('');
}

function openImportRekordboxDialog() {
  const back = document.createElement('div');
  back.className = 'menu-modal-backdrop show';
  const m = document.createElement('div');
  m.className = 'menu-modal show';
  m.innerHTML = `
    <div class="head">
      <span>IMPORT LIBRARY</span>
      <button class="x" type="button" title="Close">×</button>
    </div>
    <div class="body">
      <div id="imp-form">
        <div class="export-row">
          <label class="export-label">System</label>
          <div class="imp-systems" id="imp-systems"></div>
        </div>
        <div class="export-row" id="imp-format-row" style="display:none">
          <label class="export-label">Format</label>
          <div class="imp-formats" id="imp-formats"></div>
        </div>
        <div class="export-hint" id="imp-src-hint"></div>
        <div class="export-row">
          <label class="export-label">Path</label>
          <input type="text" id="imp-path" class="input" placeholder="/path/to/rekordbox.xml" autocomplete="off" spellcheck="false">
        </div>
        <div class="export-row" id="imp-key-row" style="display:none">
          <label class="export-label">SQLCipher key <span class="export-hint" style="display:inline">(required)</span></label>
          <input type="text" id="imp-key" class="input" placeholder="64-hex SQLCipher key" autocomplete="off" spellcheck="false">
          <div class="export-hint">rekordbox doesn't show this — recover it from your local install, then paste it here.</div>
        </div>
        <details class="imp-advanced">
          <summary>Advanced options</summary>
          <div class="export-row">
            <label class="export-label">Include</label>
            <div id="imp-include" style="display:flex; flex-wrap:wrap; gap:6px 16px;">
              <label data-src="xml,nml,db,zip"><input type="checkbox" id="inc-tracks" checked> Tracks</label>
              <label data-src="xml,nml,db,zip" data-dep="tracks"><input type="checkbox" id="inc-playlists" checked> Playlists</label>
              <label data-src="xml,db,zip" data-dep="tracks"><input type="checkbox" id="inc-tags" checked> Tags</label>
              <label data-src="nml,db,zip" data-dep="tracks"><input type="checkbox" id="inc-cues" checked> Cues</label>
              <label data-src="db,zip" data-dep="tracks"><input type="checkbox" id="inc-analysis" checked> Waveforms &amp; beat grids</label>
              <label data-src="db,zip" data-dep="tracks,analysis" title="Locked tracks keep rekordbox's beat grid: re-analysis and grid edits are refused until you unlock the track in its detail drawer."><input type="checkbox" id="inc-lock" checked> Lock imported beat grids</label>
              <label data-src="db,zip" data-dep="tracks"><input type="checkbox" id="inc-artwork" checked> Artwork</label>
              <label data-src="db,zip"><input type="checkbox" id="inc-settings" checked> Player/mixer settings</label>
            </div>
            <div class="export-hint">Derived options (playlists, tags, cues, analysis, artwork) need Tracks.</div>
          </div>
          <div class="export-row">
            <label class="export-label">Path remap</label>
            <div style="display:flex; gap:6px;">
              <input type="text" id="imp-from" class="input" placeholder="From: Z:/Music/" autocomplete="off" spellcheck="false" style="flex:1">
              <input type="text" id="imp-to"   class="input" placeholder="To: /media/usb/Music/" autocomplete="off" spellcheck="false" style="flex:1">
            </div>
            <div class="export-hint">Rewrites any track path starting with the From prefix.</div>
          </div>
        </details>
      </div>
      <div class="imp-progress" id="imp-progress">
        <div class="imp-progress-phase" id="imp-prog-phase"></div>
        <div class="imp-progress-track"><div class="imp-progress-fill" id="imp-prog-fill"></div></div>
        <div class="imp-progress-count" id="imp-prog-count"></div>
      </div>
      <div class="export-status" id="imp-status"></div>
    </div>
    <div class="foot">
      <button type="button" id="imp-go">IMPORT</button>
    </div>
  `;
  document.body.appendChild(back);
  document.body.appendChild(m);

  const close = () => { m.remove(); back.remove(); };
  back.onclick = close;
  m.querySelector('.x').onclick = close;

  const pathInput  = m.querySelector('#imp-path');
  const keyInput   = m.querySelector('#imp-key');
  const keyRow     = m.querySelector('#imp-key-row');
  const statusEl   = m.querySelector('#imp-status');
  const goBtn      = m.querySelector('#imp-go');
  const srcHint    = m.querySelector('#imp-src-hint');
  const systemsEl  = m.querySelector('#imp-systems');
  const formatRow  = m.querySelector('#imp-format-row');
  const formatsEl  = m.querySelector('#imp-formats');

  // Import sources grouped by DJ system. Each format's id (xml/nml/db/zip) is
  // what the include-filter data-src and the request logic key off; a system
  // with a single format skips the Format row and goes straight to the path.
  const SYSTEMS = [
    { id: 'rekordbox', label: 'Rekordbox', formats: [
      { id: 'xml', label: 'XML',            needsKey: false, placeholder: '/path/to/rekordbox.xml',
        hint: 'rekordbox → File → Export Collection in XML. Metadata + playlists, no key needed.' },
      { id: 'db',  label: 'master.db',      needsKey: true,  placeholder: '/path/to/master.db',
        hint: 'The master.db inside your rekordbox library folder (also pulls in waveforms, artwork, and settings). Needs the SQLCipher key.' },
      { id: 'zip', label: 'backup .zip',    needsKey: true,  placeholder: '/path/to/rekordbox_bak_YYYYMMDD.zip',
        hint: 'rekordbox → File → Library → Backup. Everything in one file. Needs the SQLCipher key.' },
    ]},
    { id: 'traktor', label: 'Traktor', formats: [
      { id: 'nml', label: 'collection.nml', needsKey: false, placeholder: '/path/to/collection.nml',
        hint: "Traktor's collection.nml — tracks, playlists, and cues. No key needed." },
    ]},
  ];
  let curFormat = SYSTEMS[0].formats[0]; // the selected format object

  const incTracksCb = m.querySelector('#inc-tracks');
  // Derived options (everything that needs the track import) are disabled when
  // Tracks is unchecked — they have no data to attach to otherwise.
  const incAnalysisCb = m.querySelector('#inc-analysis');
  const syncIncludeDeps = () => {
    const have = { tracks: incTracksCb.checked, analysis: incAnalysisCb.checked };
    m.querySelectorAll('#imp-include label[data-dep]').forEach(lab => {
      const on = lab.getAttribute('data-dep').split(',').every(d => have[d]);
      const cb = lab.querySelector('input');
      cb.disabled = !on;
      lab.style.opacity = on ? '' : '0.4';
    });
  };
  // Apply the chosen format: key field, path placeholder, hint, and which
  // Include options the format can provide.
  const applyFormat = (fmt) => {
    curFormat = fmt;
    formatsEl.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.fmt === fmt.id));
    keyRow.style.display = fmt.needsKey ? '' : 'none';
    pathInput.placeholder = fmt.placeholder;
    srcHint.textContent = fmt.hint;
    m.querySelectorAll('#imp-include label[data-src]').forEach(lab => {
      lab.style.display = lab.getAttribute('data-src').split(',').includes(fmt.id) ? '' : 'none';
    });
    syncIncludeDeps();
  };
  // Select a system: highlight its tab, render its Format radios (only when it
  // has more than one), and apply its first format.
  const selectSystem = (sys) => {
    systemsEl.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.sys === sys.id));
    const multi = sys.formats.length > 1;
    formatRow.style.display = multi ? '' : 'none';
    formatsEl.innerHTML = multi ? sys.formats.map(f =>
      `<button type="button" data-fmt="${f.id}">${f.label}</button>`).join('') : '';
    formatsEl.querySelectorAll('button').forEach(b => {
      b.onclick = () => applyFormat(sys.formats.find(f => f.id === b.dataset.fmt));
    });
    applyFormat(sys.formats[0]);
  };
  systemsEl.innerHTML = SYSTEMS.map(s => `<button type="button" data-sys="${s.id}">${s.label}</button>`).join('');
  systemsEl.querySelectorAll('button').forEach(b => {
    b.onclick = () => selectSystem(SYSTEMS.find(s => s.id === b.dataset.sys));
  });
  incTracksCb.onchange = syncIncludeDeps;
  incAnalysisCb.onchange = syncIncludeDeps;
  selectSystem(SYSTEMS[0]);
  pathInput.focus();

  goBtn.onclick = async () => {
    const path = pathInput.value.trim();
    if (!path) { statusEl.textContent = 'Path is required.'; return; }
    const body = { path };
    if (curFormat.needsKey) {
      const k = keyInput.value.trim().toLowerCase().replace(/^0x/, '');
      if (!/^[0-9a-f]{64}$/.test(k)) {
        statusEl.textContent = 'A 64-hex SQLCipher key is required.';
        return;
      }
      body.key = k;
    }
    const rFrom = m.querySelector('#imp-from').value.trim();
    const rTo   = m.querySelector('#imp-to').value.trim();
    if ((rFrom && !rTo) || (!rFrom && rTo)) {
      statusEl.textContent = 'Path remap needs both From and To (or leave both blank).';
      return;
    }
    if (rFrom) { body.remap_from = rFrom; body.remap_to = rTo; }
    body.include = {
      tracks:    m.querySelector('#inc-tracks').checked,
      playlists: m.querySelector('#inc-playlists').checked,
      tags:      m.querySelector('#inc-tags').checked,
      cues:      m.querySelector('#inc-cues').checked,
      analysis:  m.querySelector('#inc-analysis').checked,
      artwork:   m.querySelector('#inc-artwork').checked,
      settings:  m.querySelector('#inc-settings').checked,
    };
    const lockCb = m.querySelector('#inc-lock');
    body.lock_analysis = !lockCb.disabled && lockCb.checked; // only tracks that bring a grid get locked
    if (!body.include.tracks && !body.include.settings) {
      statusEl.textContent = 'Nothing selected to import.';
      return;
    }
    goBtn.disabled = true;
    statusEl.textContent = '';
    m.querySelector('#imp-form').style.display = 'none'; // swap the form out for the progress panel
    const stopProgress = startImportProgress(m);
    try {
      const r = await fetch('/api/import/rekordbox', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      stopProgress();
      if (!r.ok) {
        const t = await errorText(r);
        m.querySelector('#imp-form').style.display = '';
        statusEl.textContent = `Failed: ${r.status} ${t}`;
        goBtn.disabled = false;
        return;
      }
      const res = await r.json();
      const parts = [];
      if (res.TracksAdded)   parts.push(`+${res.TracksAdded} new`);
      if (res.TracksUpdated) parts.push(`~${res.TracksUpdated} updated`);
      if (res.TracksSkipped) parts.push(`-${res.TracksSkipped} skipped`);
      if (res.PlaylistsTotal)parts.push(`${res.PlaylistsTotal} playlists`);
      if (res.TagsTotal)     parts.push(`${res.TagsTotal} tags`);
      if (res.AnalysisImported) parts.push(`${res.AnalysisImported} waveforms`);
      if (res.AnalysisLocked)   parts.push(`${res.AnalysisLocked} grids locked`);
      if (res.ArtworkImported)  parts.push(`${res.ArtworkImported} artwork`);
      if (res.CuesImported)     parts.push(`${res.CuesImported} cues`);
      if (res.SettingsImported) parts.push(`${res.SettingsImported} settings`);
      if (res.FilesMissing)     parts.push(`⚠ ${res.FilesMissing} missing files (unplayable)`);
      statusEl.textContent = parts.length ? parts.join(' · ') : 'No changes.';
      // Repurpose the button to close the dialog — import is done, so
      // re-running it would be wrong. Re-enable + rebind to close().
      goBtn.textContent = 'DONE';
      goBtn.disabled = false;
      goBtn.onclick = close;
      toast('IMPORT · ' + (parts.join(' / ').toUpperCase() || 'NO CHANGES'), 'success');
      await loadPlaylists(true);
      refreshLibraryView();
    } catch (err) {
      stopProgress();
      m.querySelector('#imp-form').style.display = '';
      statusEl.textContent = `Failed: ${err}`;
      goBtn.disabled = false;
    }
  };
}

function openFixPathsDialog() {
  const back = document.createElement('div');
  back.className = 'menu-modal-backdrop show';
  const m = document.createElement('div');
  m.className = 'menu-modal show';
  m.innerHTML = `
    <div class="head">
      <span>FIX TRACK PATHS</span>
      <button class="x" type="button" title="Close">×</button>
    </div>
    <div class="body">
      <div class="export-row">
        <label class="export-label">From prefix</label>
        <input type="text" id="fp-from" class="input" placeholder="Z:/Music/" autocomplete="off" spellcheck="false">
      </div>
      <div class="export-row">
        <label class="export-label">To prefix</label>
        <input type="text" id="fp-to" class="input" placeholder="/media/usb/Music/" autocomplete="off" spellcheck="false">
        <div class="export-hint">
          Every track whose path starts with the From prefix is rewritten with the To prefix.
          The on-disk analysis cache is renamed in lockstep so you don't have to re-analyze.
        </div>
      </div>
      <div class="export-status" id="fp-status"></div>
    </div>
    <div class="foot">
      <button type="button" id="fp-go">REWRITE</button>
    </div>
  `;
  document.body.appendChild(back);
  document.body.appendChild(m);
  const close = () => { m.remove(); back.remove(); };
  back.onclick = close;
  m.querySelector('.x').onclick = close;
  const fromEl = m.querySelector('#fp-from');
  const toEl   = m.querySelector('#fp-to');
  const stat   = m.querySelector('#fp-status');
  const go     = m.querySelector('#fp-go');
  fromEl.focus();
  go.onclick = async () => {
    const from = fromEl.value.trim();
    const to   = toEl.value.trim();
    if (!from || !to) { stat.textContent = 'Both prefixes are required.'; return; }
    go.disabled = true;
    stat.textContent = 'Rewriting…';
    try {
      const r = await fetch('/api/library/remap-paths', {
        method: 'POST', headers: {'Content-Type':'application/json'},
        body: JSON.stringify({ from, to }),
      });
      if (!r.ok) {
        const t = await errorText(r);
        stat.textContent = `Failed: ${r.status} ${t}`;
        go.disabled = false;
        return;
      }
      const res = await r.json();
      const miss = res.missing ? ` · ${res.missing} still missing on disk` : '';
      stat.textContent = `Rewrote ${res.changed} track${res.changed === 1 ? '' : 's'}.${miss}`;
      go.textContent = 'DONE';
      toast(`FIX PATHS · ${res.changed} TRACKS`, 'success');
      refreshLibraryView();
    } catch (err) {
      stat.textContent = `Failed: ${err}`;
      go.disabled = false;
    }
  };
}

async function createPlaylistPrompt(isFolder) {
  const name = await openInputModal({
    title: isFolder ? 'NEW FOLDER' : 'NEW PLAYLIST',
    label: isFolder ? 'Folder name' : 'Playlist name',
    placeholder: isFolder ? 'e.g. Sets' : 'e.g. Warm-up',
  });
  if (!name) return;
  // Place new items under the currently-selected folder when one's
  // active, else at the root — matches how rekordbox decides parent.
  let parent = 0;
  const sel = plSelectedID != null ? playlistsCache.find(p => p.id === plSelectedID) : null;
  if (sel && sel.is_folder) parent = sel.id;
  const r = await fetch('/api/playlists', {
    method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ name, parent_id: parent, is_folder: isFolder }),
  });
  if (!r.ok) { toast('CREATE FAILED', 'error'); return; }
  toast(`+ ${(isFolder ? 'FOLDER' : 'PLAYLIST')} · ${name.toUpperCase()}`, 'success');
  await loadPlaylists(true);
}

async function renameSelectedPlaylist() {
  if (plSelectedID == null) return;
  const cur = playlistsCache.find(p => p.id === plSelectedID);
  if (!cur) return;
  const name = await openInputModal({
    title: cur.is_folder ? 'RENAME FOLDER' : 'RENAME PLAYLIST',
    label: 'New name',
    value: cur.name,
    okLabel: 'RENAME',
  });
  if (!name || name === cur.name) return;
  const r = await fetch(`/api/playlists/${plSelectedID}`, {
    method: 'PUT', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ name }),
  });
  if (!r.ok) { toast('RENAME FAILED', 'error'); return; }
  toast(`RENAMED · ${name.toUpperCase()}`, 'success');
  await loadPlaylists(true);
}

async function deleteSelectedPlaylist() {
  if (plSelectedID == null) return;
  const cur = playlistsCache.find(p => p.id === plSelectedID);
  if (!cur) return;
  const noun = cur.is_folder ? 'folder (and everything inside)' : 'playlist';
  if (!await openConfirmModal({
    title: cur.is_folder ? 'DELETE FOLDER' : 'DELETE PLAYLIST',
    message: `Delete ${noun} "${cur.name}"?`,
    okLabel: 'DELETE', danger: true,
  })) return;
  const r = await fetch(`/api/playlists/${plSelectedID}`, { method: 'DELETE' });
  if (!r.ok) { toast('DELETE FAILED', 'error'); return; }
  toast('DELETED', 'success');
  // Drop back to COLLECTION since the active source just disappeared.
  selectedSource = 'collection';
  plSelectedID = null;
  plSelectedTracks = [];
  await loadPlaylists();
}

async function reorderPlaylistTrack(idx, dir) {
  if (plSelectedID == null) return;
  const newIdx = idx + dir;
  if (newIdx < 0 || newIdx >= plSelectedTracks.length) return;
  const ids = plSelectedTracks.map(t => t.id);
  [ids[idx], ids[newIdx]] = [ids[newIdx], ids[idx]];
  await postPlaylistTrackIDs(plSelectedID, ids);
}

async function removePlaylistTrack(idx) {
  if (plSelectedID == null) return;
  const ids = plSelectedTracks.map(t => t.id);
  ids.splice(idx, 1);
  await postPlaylistTrackIDs(plSelectedID, ids);
}

// True when the active library source is a regular (non-smart, non-folder)
// playlist — membership is user-managed, so rows can be reordered/removed.
function activeRegularPlaylist() {
  if (typeof selectedSource !== 'number') return false;
  const sel = playlistsCache.find(p => p.id === selectedSource);
  return !!sel && !sel.is_folder && !sel.is_smart;
}

// Bulk-bar "− PLAYLIST": drop every selected track from the current
// playlist. Library rows, tags, cues, and other playlists are untouched.
async function bulkRemoveFromPlaylist() {
  if (plSelectedID == null) return;
  const drop = new Set(plSelectedTracks.filter(t => bulkSelectedIDs.has(t.id)).map(t => t.id));
  if (drop.size === 0) { toast('SELECTION IS NOT IN THIS PLAYLIST', 'error'); return; }
  const keep = plSelectedTracks.map(t => t.id).filter(id => !drop.has(id));
  const n = plSelectedTracks.length - keep.length;
  if (!await postPlaylistTrackIDs(plSelectedID, keep)) return;
  // Deselect just the removed tracks — a cross-source selection survives.
  drop.forEach(id => bulkSelectedIDs.delete(id));
  updateBulkBar();
  toast(`REMOVED ${n} TRACK${n === 1 ? '' : 'S'} FROM PLAYLIST`, 'success');
}

async function postPlaylistTrackIDs(playlistID, trackIDs) {
  const r = await fetch(`/api/playlists/${playlistID}/tracks`, {
    method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({ track_ids: trackIDs }),
  });
  if (!r.ok) { toast('UPDATE FAILED', 'error'); return false; }
  await loadPlaylistTracks(playlistID);
  // Also refresh the cache so the track count in the tree updates.
  await loadPlaylists(true);
  return true;
}

// Add-tracks picker — a modal that lists every library track, filterable
// by title/artist/album. Clicking a row appends that track to the current
// playlist immediately (so the user can add several without closing the
// modal). Already-included tracks render dim with a "✓ ADDED" tag.
function openPlaylistAddPicker() {
  if (plSelectedID == null) return;
  let backdrop = document.getElementById('pl-picker-backdrop');
  let picker = document.getElementById('pl-picker');
  if (!backdrop) {
    backdrop = document.createElement('div');
    backdrop.id = 'pl-picker-backdrop';
    backdrop.className = 'pl-picker-backdrop';
    document.body.appendChild(backdrop);
    picker = document.createElement('div');
    picker.id = 'pl-picker';
    picker.className = 'pl-picker';
    document.body.appendChild(picker);
  }

  let query = '';
  const close = () => {
    backdrop.classList.remove('show');
    picker.classList.remove('show');
  };
  backdrop.onclick = close;

  const render = () => {
    const inSet = new Set(plSelectedTracks.map(t => t.id));
    const q = query.trim().toLowerCase();
    const filtered = (allTracks || []).filter(t => {
      if (!q) return true;
      return (t.title || '').toLowerCase().includes(q)
          || (t.artist || '').toLowerCase().includes(q)
          || (t.album || '').toLowerCase().includes(q);
    });
    const rows = filtered.slice(0, 500).map(t => `
      <div class="row ${inSet.has(t.id) ? 'added' : ''}" data-tid="${t.id}">
        <div class="t">${escape(t.title || 'Untitled')}</div>
        <div class="a">${escape(t.artist || '—')}</div>
      </div>
    `).join('');
    picker.innerHTML = `
      <div class="head">
        <input type="text" id="pl-pick-q" placeholder="FILTER…" autocomplete="off">
        <span style="color:var(--text3);font-size:10px">${filtered.length} match${filtered.length === 1 ? '' : 'es'}</span>
        <button id="pl-pick-close">DONE</button>
      </div>
      <div class="list" id="pl-pick-list">${rows || '<div class="pl-empty">NO MATCHES</div>'}</div>
    `;
    const input = picker.querySelector('#pl-pick-q');
    input.value = query;
    input.focus();
    input.addEventListener('input', e => { query = e.target.value; render(); });
    input.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });
    picker.querySelector('#pl-pick-close').onclick = close;
    picker.querySelectorAll('.row:not(.added)').forEach(row => {
      row.onclick = async () => {
        const tid = parseInt(row.getAttribute('data-tid'), 10);
        const ids = plSelectedTracks.map(t => t.id);
        ids.push(tid);
        await postPlaylistTrackIDs(plSelectedID, ids);
        render(); // refresh ✓ marks
      };
    });
  };
  render();
  backdrop.classList.add('show');
  picker.classList.add('show');
}

async function renderTagsTab() {
  const el = document.getElementById('tags-tab-content');
  const [cats, tags] = await Promise.all([
    ensureAllCategories(true),
    ensureAllTags(true),
  ]);
  const counts = tagUsageCounts();

  // Categories panel
  const catsHTML = cats.length === 0
    ? '<div class="tags-empty">— NO CATEGORIES —</div>'
    : cats.map(c => `
        <div class="cat-row" data-cat-id="${c.id}">
          <span class="cid">${String(c.id).padStart(2,'0')}</span>
          <div class="name-cell"><span class="name" data-name>${escape(c.name)}</span></div>
          <button class="icon-btn" data-act="rename-cat">RENAME</button>
          <button class="icon-btn danger" data-act="delete-cat">×</button>
        </div>`).join('');

  // Group tags by category for the listing
  const catName = id => {
    if (id === 0) return 'UNCATEGORIZED';
    const c = cats.find(x => x.id === id);
    return c ? c.name.toUpperCase() : `CATEGORY ${id}`;
  };
  const groups = {};
  tags.forEach(t => {
    const k = t.category_id || 0;
    (groups[k] = groups[k] || []).push(t);
  });
  const catIDs = Object.keys(groups).map(n => parseInt(n, 10))
    .sort((a, b) => (a === 0 ? 1 : b === 0 ? -1 : a - b));

  const moveOptions = (selID) => {
    const opts = [`<option value="0" ${selID === 0 ? 'selected' : ''}>UNCATEGORIZED</option>`];
    cats.forEach(c => {
      opts.push(`<option value="${c.id}" ${c.id === selID ? 'selected' : ''}>${escape(c.name.toUpperCase())}</option>`);
    });
    return opts.join('');
  };

  const tagsHTML = tags.length === 0
    ? '<div class="tags-empty">— NO TAGS YET — USE + NEW TAG TO CREATE ONE —</div>'
    : catIDs.map(cid => `
        <div class="cat-group">
          <div class="cat-group-head">${escape(catName(cid))}</div>
          ${groups[cid].map(t => `
            <div class="tag-row" data-tag-id="${t.id}">
              <span class="tid">${String(t.id).padStart(2,'0')}</span>
              <button type="button"
                      class="tag-swatch ${t.color ? '' : 'empty'}"
                      data-act="pick-color"
                      title="${t.color ? 'Change color' : 'Set color'}"
                      style="${t.color ? `background:${TRACK_COLORS[t.color]}` : ''}"></button>
              <div class="name-cell"><span class="name" data-name>${escape(t.name)}</span></div>
              <div class="move-cell">
                <select data-act="move-tag">${moveOptions(t.category_id || 0)}</select>
              </div>
              <span class="uses">${(counts[t.name] || 0)} USED</span>
              <button class="icon-btn danger" data-act="delete-tag">×</button>
            </div>`).join('')}
        </div>`).join('');

  el.innerHTML = `<div class="tags-tab">
    <div class="tags-panel">
      <div class="head">
        <span class="label">CATEGORIES<span class="count">${cats.length}</span></span>
        <button class="add-inline-btn" id="add-cat-btn">+ NEW CATEGORY</button>
      </div>
      <div class="body" id="cats-body">${catsHTML}</div>
    </div>
    <div class="tags-panel">
      <div class="head">
        <span class="label">TAGS<span class="count">${tags.length}</span></span>
        <button class="add-inline-btn" id="add-tag-tab-btn">+ NEW TAG</button>
      </div>
      <div class="body" id="tags-body">${tagsHTML}</div>
    </div>
  </div>`;

  wireTagsTab();
}

function wireTagsTab() {
  // Create category
  const addCat = document.getElementById('add-cat-btn');
  if (addCat) addCat.addEventListener('click', async () => {
    const name = await openInputModal({ title: 'NEW CATEGORY', label: 'Category name', placeholder: 'e.g. Mood' });
    if (!name) return;
    const r = await fetch('/api/tag-categories', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ name }),
    });
    if (r.ok) { allCategoriesCache = null; renderTagsTab(); toast(`+ CATEGORY · ${name.toUpperCase()}`, 'success'); }
    else toast('CREATE FAILED', 'error');
  });

  // Create tag
  const addTag = document.getElementById('add-tag-tab-btn');
  if (addTag) addTag.addEventListener('click', async () => {
    const name = await openInputModal({ title: 'NEW TAG', label: 'Tag name', placeholder: 'e.g. Banger' });
    if (!name) return;
    const r = await fetch('/api/tags', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ name, category_id: 0 }),
    });
    if (r.ok) { allTagsCache = null; renderTagsTab(); toast(`+ TAG · ${name.toUpperCase()}`, 'success'); }
    else toast('CREATE FAILED', 'error');
  });

  // Per-row actions
  document.querySelectorAll('.cat-row').forEach(row => {
    const id = parseInt(row.getAttribute('data-cat-id'), 10);
    const nameEl = row.querySelector('[data-name]');
    row.querySelector('[data-act="rename-cat"]').addEventListener('click', () => startInlineRename(nameEl, async (newName) => {
      const r = await fetch(`/api/tag-categories/${id}`, {
        method: 'PUT', headers: {'Content-Type':'application/json'},
        body: JSON.stringify({ name: newName }),
      });
      if (r.ok) { allCategoriesCache = null; renderTagsTab(); toast(`CATEGORY · ${newName.toUpperCase()}`, 'success'); }
      else toast('RENAME FAILED', 'error');
    }));
    row.querySelector('[data-act="delete-cat"]').addEventListener('click', async () => {
      if (!await openConfirmModal({
        title: 'DELETE CATEGORY',
        message: `Delete category "${nameEl.textContent}"?\n\nTags within will move to Uncategorized.`,
        okLabel: 'DELETE', danger: true,
      })) return;
      const r = await fetch(`/api/tag-categories/${id}`, { method: 'DELETE' });
      if (r.ok) { allCategoriesCache = null; allTagsCache = null; renderTagsTab(); toast('CATEGORY DELETED', 'success'); }
      else toast('DELETE FAILED', 'error');
    });
    nameEl.addEventListener('dblclick', () => row.querySelector('[data-act="rename-cat"]').click());
  });

  document.querySelectorAll('.tag-row').forEach(row => {
    const id = parseInt(row.getAttribute('data-tag-id'), 10);
    const nameEl = row.querySelector('[data-name]');
    row.querySelector('[data-act="move-tag"]').addEventListener('change', async (e) => {
      const newCat = parseInt(e.target.value, 10);
      const r = await fetch(`/api/tags/${id}`, {
        method: 'PUT', headers: {'Content-Type':'application/json'},
        body: JSON.stringify({ category_id: newCat }),
      });
      if (r.ok) { allTagsCache = null; renderTagsTab(); toast('TAG MOVED', 'success'); }
      else toast('MOVE FAILED', 'error');
    });
    const swatch = row.querySelector('[data-act="pick-color"]');
    if (swatch) swatch.addEventListener('click', (e) => {
      e.stopPropagation();
      const cur = (allTagsCache || []).find(x => x.id === id);
      openTagColorPicker(swatch, id, (cur && cur.color) || 0);
    });
    row.querySelector('[data-act="delete-tag"]').addEventListener('click', async () => {
      const oldName = nameEl.textContent;
      if (!await openConfirmModal({
        title: 'DELETE TAG',
        message: `Delete tag "${oldName}"?\n\nIt will be removed from all tracks.`,
        okLabel: 'DELETE', danger: true,
      })) return;
      const r = await fetch(`/api/tags/${id}`, { method: 'DELETE' });
      if (r.ok) {
        allTagsCache = null; renderTagsTab(); toast('TAG DELETED', 'success');
        // Also strip from any allTracks rows so the library + open drawer reflect it.
        allTracks.forEach(t => { if (t.tags) t.tags = t.tags.filter(n => n !== oldName); });
        if (selectedTrackID != null) {
          drawerTrackTags = drawerTrackTags.filter(t => t.id !== id);
          renderTagsSection();
        }
        refreshLibraryView();
      } else toast('DELETE FAILED', 'error');
    });
    nameEl.addEventListener('dblclick', () => {
      startInlineRename(nameEl, async (newName) => {
        const r = await fetch(`/api/tags/${id}`, {
          method: 'PUT', headers: {'Content-Type':'application/json'},
          body: JSON.stringify({ name: newName }),
        });
        if (r.ok) {
          allTagsCache = null;
          // Update allTracks since track listings reference tag by name.
          const oldName = nameEl.textContent;
          allTracks.forEach(t => { if (t.tags) t.tags = t.tags.map(n => n === oldName ? newName : n); });
          if (selectedTrackID != null) {
            drawerTrackTags = drawerTrackTags.map(t => t.id === id ? { ...t, name: newName } : t);
            renderTagsSection();
          }
          renderTagsTab();
          toast(`RENAMED · ${newName.toUpperCase()}`, 'success');
        } else toast('RENAME FAILED', 'error');
      });
    });
  });
}

// Replace a name-cell span with an input, save on Enter / blur, cancel on Esc.
function startInlineRename(nameEl, onSave) {
  const old = nameEl.textContent;
  const input = document.createElement('input');
  input.className = 'name-input';
  input.value = old;
  nameEl.replaceWith(input);
  input.focus();
  input.select();
  let done = false;
  const finish = (commit) => {
    if (done) return;
    done = true;
    const newVal = input.value.trim();
    if (commit && newVal && newVal !== old) {
      onSave(newVal);
    } else {
      // restore the original span if cancelled / unchanged
      const span = document.createElement('span');
      span.className = 'name';
      span.setAttribute('data-name', '');
      span.textContent = old;
      input.replaceWith(span);
    }
  };
  input.addEventListener('keydown', e => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') finish(false);
  });
  input.addEventListener('blur', () => finish(true));
}

// Render the tab when the user switches to it.
document.querySelector('.tab[data-tab="tags"]').addEventListener('click', () => {
  renderTagsTab();
});

