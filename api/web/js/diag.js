// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// ───────── DIAG tab ─────────
let diagLastSeq = 0;
let diagInterval = null;

function fmtBytes(n) {
  if (!n) return '0';
  const units = ['B','KB','MB','GB','TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n.toString() : n.toFixed(1)) + ' ' + units[i];
}

function fmtDuration(sec) {
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.floor(sec/60) + 'm ' + (sec%60) + 's';
  const h = Math.floor(sec/3600), m = Math.floor((sec%3600)/60);
  return h + 'h ' + m + 'm';
}

async function refreshDiag() {
  const r = await fetch('/api/diag', { cache: 'no-store' });
  if (!r.ok) return;
  const d = await r.json();
  const fill = (id, pairs) => {
    document.getElementById(id).innerHTML = pairs
      .map(([k,v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('');
  };
  const ver = appVersion ? [
    ['VERSION', `<a href="${escape(appVersion.href)}" target="_blank" rel="noopener">${escape(appVersion.info.version)}</a>`],
    ['COMMIT',  appVersion.info.commit
      ? `<a href="${escape(appVersion.commitHref)}" target="_blank" rel="noopener">${escape(appVersion.info.commit.slice(0, 7))}</a>${appVersion.info.dirty ? ' (modified)' : ''}`
      : '—'],
    ['BUILT',   escape(appVersion.info.date || '—')],
  ] : [];
  fill('diag-runtime', [
    ...ver,
    ['LINK',       d.linked ? 'ON (advertising)' : 'OFF (silent)'],
    ['UPTIME',     fmtDuration(d.uptime_sec)],
    ['GO',         d.go_version],
    ['GOROUTINES', d.goroutines],
    ['HEAP',       d.heap_mb + ' MB'],
    ['SYS',        d.sys_mb + ' MB'],
    ['GC PAUSE',   d.gc_pause_us + ' µs'],
    ['GC COUNT',   d.gc_count],
  ]);
  fill('diag-library', [
    ['TRACKS',          d.library_tracks],
    ['CACHE ON DISK',   fmtBytes(d.cache_dir_bytes)],
  ]);
  fill('diag-analysis', [
    ['PENDING',  d.analysis_pending],
    ['ANALYZED', d.analysis_analyzed],
    ['CACHED',   d.analysis_cached],
  ]);
  await refreshDiagLog();
}

// All log entries held client-side so the user can filter without
// re-fetching from the server. Bounded to the same cap the server's
// ring uses (500); incremental fetches append, then the filter is
// re-applied to the DOM.
const diagLogBuf = [];
const DIAG_LOG_MAX = 500;

// "Source" of a log line = the prefix before the first colon, e.g.
// "dbserver" in "dbserver: unlink closed 2 session(s)". Used both for
// the source-pill filter row and for grouping discovery (we surface
// pills for whatever prefixes have actually been seen in this session).
function logSource(line) {
  const m = /^(?:\d{4}\/\d{2}\/\d{2}\s\d{2}:\d{2}:\d{2}\s)?([a-z][a-z0-9_-]*):/i.exec(line);
  return m ? m[1].toLowerCase() : 'misc';
}
const ERROR_RE = /\b(error|err|fail(?:ed|ure)?|fatal|panic|warn(?:ing)?)\b/i;

const diagFilter = { text: '', errorsOnly: false, mutedSources: new Set() };
const diagKnownSources = new Set();

async function refreshDiagLog() {
  const r = await fetch('/api/diag/logs?since=' + diagLastSeq, { cache: 'no-store' });
  if (!r.ok) return;
  const entries = await r.json();
  if (!entries.length) return;
  for (const e of entries) {
    e.source = logSource(e.line);
    diagKnownSources.add(e.source);
    diagLogBuf.push(e);
    if (e.seq > diagLastSeq) diagLastSeq = e.seq;
  }
  // Bound buffer
  if (diagLogBuf.length > DIAG_LOG_MAX) {
    diagLogBuf.splice(0, diagLogBuf.length - DIAG_LOG_MAX);
  }
  renderDiagSources();
  renderDiagLog();
}

// The sources that matter while debugging a live deck stay as always-
// visible pills; everything else (mostly one-shot startup prefixes like
// settings/portmapper/mpris) folds into a "more" menu so the filter row
// stays one line. Since the DIAG ring started capturing startup lines,
// a fresh session discovers 14+ prefixes.
const DIAG_PRIMARY_SOURCES = ['dbserver', 'nfs', 'pdb', 'lazy-analysis', 'status', 'misc'];

function renderDiagSources() {
  const wrap = document.getElementById('diag-log-sources');
  if (!wrap) return;
  const sources = Array.from(diagKnownSources).sort();
  // Only rebuild when the set of known sources changed (avoid losing
  // click focus, the visual transition, and the open menu on every poll).
  const key = sources.join(',');
  if (wrap.dataset.key === key) return;
  wrap.dataset.key = key;
  const primary = DIAG_PRIMARY_SOURCES.filter(s => diagKnownSources.has(s));
  const overflow = sources.filter(s => !DIAG_PRIMARY_SOURCES.includes(s));
  const pill = s =>
    `<button type="button" class="src-pill ${diagFilter.mutedSources.has(s) ? '' : 'on'}" data-src="${s}">${s}</button>`;
  wrap.innerHTML = primary.map(pill).join('') + (overflow.length
    ? `<button type="button" class="src-pill src-more" id="src-more"></button>
       <div class="src-menu hidden" id="src-menu">${overflow.map(pill).join('')}</div>`
    : '');
  wrap.querySelectorAll('.src-pill[data-src]').forEach(b => {
    b.addEventListener('click', () => {
      const s = b.dataset.src;
      if (diagFilter.mutedSources.has(s)) diagFilter.mutedSources.delete(s);
      else diagFilter.mutedSources.add(s);
      b.classList.toggle('on');
      renderDiagMoreBadge();
      renderDiagLog();
    });
  });
  const more = document.getElementById('src-more');
  const menu = document.getElementById('src-menu');
  if (more && menu) {
    more.addEventListener('click', e => { e.stopPropagation(); menu.classList.toggle('hidden'); });
    menu.addEventListener('click', e => e.stopPropagation()); // keep open while toggling several
  }
  renderDiagMoreBadge();
}

// Label the "more" button with the overflow count and how many of those
// are muted — muted state must stay visible even when its pills are
// folded away, or a hidden filter silently eats log lines.
function renderDiagMoreBadge() {
  const more = document.getElementById('src-more');
  if (!more) return;
  const overflow = Array.from(diagKnownSources).filter(s => !DIAG_PRIMARY_SOURCES.includes(s));
  const muted = overflow.filter(s => diagFilter.mutedSources.has(s)).length;
  more.textContent = muted ? `more (${overflow.length} · ${muted} off) ▾` : `more (${overflow.length}) ▾`;
  more.classList.toggle('muted-some', muted > 0);
}

// Close the overflow menu on any outside click (registered once).
document.addEventListener('click', () => {
  const menu = document.getElementById('src-menu');
  if (menu) menu.classList.add('hidden');
});

function renderDiagLog() {
  const el = document.getElementById('diag-log');
  if (!el) return;
  const wasAtBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 8;
  const needle = diagFilter.text.toLowerCase();
  const out = [];
  for (const e of diagLogBuf) {
    if (diagFilter.mutedSources.has(e.source)) continue;
    if (diagFilter.errorsOnly && !ERROR_RE.test(e.line)) continue;
    if (needle && !e.line.toLowerCase().includes(needle)) continue;
    const ts = new Date(e.time).toISOString().slice(11, 19);
    out.push(ts + '  ' + e.line);
  }
  el.textContent = out.length ? out.join('\n') + '\n' : '(no matches)';
  if (wasAtBottom) el.scrollTop = el.scrollHeight;
}

// Wire filter controls once on first DOM hit (the input/checkbox
// exist from page load, even before the user opens the DIAG tab).
(function wireDiagFilters() {
  const txt = document.getElementById('diag-log-filter');
  const err = document.getElementById('diag-log-errors');
  if (!txt || !err) return;
  txt.addEventListener('input', () => { diagFilter.text = txt.value; renderDiagLog(); });
  err.addEventListener('change', () => { diagFilter.errorsOnly = err.checked; renderDiagLog(); });
})();

// Auto-refresh DIAG only when its tab is active.
function diagTabActive() {
  return document.getElementById('diag').classList.contains('active');
}
setInterval(() => { if (diagTabActive()) refreshDiag(); }, 2000);

