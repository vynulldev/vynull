// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── boot ─────────
const STARTED = Date.now();

// App version: the status bar and DIAG's RUNTIME card show which build is
// running. A release shows its tag and links to its release page; a build
// between releases shows "dev" plus the commit and links to that commit.
let appVersion = null; // {info, label, href, commitHref} once /api/version answers
(async function showAppVersion() {
  let v;
  try {
    const r = await fetch('/api/version');
    if (!r.ok) return;
    v = await r.json();
  } catch (e) { return; }
  const repo = 'https://github.com/vynulldev/vynull';
  const short = (v.commit || '').slice(0, 7);
  const release = /^v\d+\.\d+\.\d+$/.test(v.version || '');
  let label = release ? v.version : (short ? 'dev · ' + short : 'dev');
  if (v.dirty) label += ' · modified';
  const href = release ? `${repo}/releases/tag/${v.version}` : (v.commit ? `${repo}/commit/${v.commit}` : repo);
  const title = [v.version, v.commit, v.date, v.dirty ? 'uncommitted changes' : '', v.go_version]
    .filter(Boolean).join('\n');
  document.querySelectorAll('.app-version').forEach(a => {
    a.textContent = label;
    a.href = href;
    a.title = title;
  });
  appVersion = { info: v, label, href, commitHref: v.commit ? `${repo}/commit/${v.commit}` : '' };
})();

// tabs
document.querySelectorAll('.tab').forEach(t => {
  t.addEventListener('click', () => {
    // Switching tabs should not leave a library overlay floating over the new
    // view — the detail sheet and playlist panel are position:fixed.
    closeDetail();
    closeMobilePlaylists();
    document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
    document.querySelectorAll('.panel').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    document.getElementById(t.dataset.tab).classList.add('active');
    // Refresh tab-specific data on every activation so the view never
    // shows stale state after mutations elsewhere in the app.
    if (t.dataset.tab === 'library') refreshLibraryView();
    else if (t.dataset.tab === 'tags') renderTagsTab();
    else if (t.dataset.tab === 'diag') refreshDiag();
  });
});

// uptime ticker
setInterval(() => {
  const s = Math.floor((Date.now() - STARTED) / 1000);
  const h = String(Math.floor(s / 3600)).padStart(2, '0');
  const m = String(Math.floor((s % 3600) / 60)).padStart(2, '0');
  const sec = String(s % 60).padStart(2, '0');
  document.getElementById('uptime').textContent = `${h}:${m}:${sec}`;
}, 1000);

// Bottom-bar live status: mirrors what the CLI TUI shows on its status
// line. Export takes precedence over analysis; hidden when both are
// idle so the bar isn't visually noisy.
async function refreshServerStatus() {
  try {
    const r = await fetch('/api/diag/status', { cache: 'no-store' });
    if (!r.ok) return;
    const s = await r.json();
    const el = document.getElementById('server-status');
    if (!s.text) { el.style.display = 'none'; return; }
    el.style.display = '';
    el.className = 'item kind-' + (s.kind || 'idle');
    el.querySelector('.server-status-text').textContent = s.text;
  } catch (e) { /* best-effort */ }
}
setInterval(refreshServerStatus, 1500);
refreshServerStatus();

