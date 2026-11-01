// SPDX-License-Identifier: GPL-3.0-or-later
'use strict';

// CDJ "key_display" DEVSETTING: alphanumeric = Camelot (e.g. 8A),
// classic = standard names (e.g. Am). Tracks/players are served in Camelot,
// so classic mode converts via the tables below (mirrors analysis/key.go).
function keyDisplayMode() {
  const m = currentSettings && currentSettings.dev_setting && currentSettings.dev_setting.key_display;
  return m === 'classic' ? 'classic' : 'alphanumeric';
}
const CAMELOT_MAJOR = ["8B","3B","10B","5B","12B","7B","2B","9B","4B","11B","6B","1B"];
const CAMELOT_MINOR = ["5A","12A","7A","2A","9A","4A","11A","6A","1A","8A","3A","10A"];
const KEY_MAJOR_NAMES = ["C","Db","D","Eb","E","F","F#","G","Ab","A","Bb","B"];
const KEY_MINOR_NAMES = ["Cm","C#m","Dm","Ebm","Em","Fm","F#m","Gm","G#m","Am","Bbm","Bm"];
// Map any key spelling (upper-cased) → [camelot, standard].
const KEY_PAIRS = (() => {
  const m = {};
  for (let k = 0; k < 12; k++) {
    m[CAMELOT_MAJOR[k]] = [CAMELOT_MAJOR[k], KEY_MAJOR_NAMES[k]];
    m[KEY_MAJOR_NAMES[k].toUpperCase()] = [CAMELOT_MAJOR[k], KEY_MAJOR_NAMES[k]];
    m[CAMELOT_MINOR[k]] = [CAMELOT_MINOR[k], KEY_MINOR_NAMES[k]];
    m[KEY_MINOR_NAMES[k].toUpperCase()] = [CAMELOT_MINOR[k], KEY_MINOR_NAMES[k]];
  }
  // Enharmonic spellings rekordbox sometimes writes.
  for (const [alias, pc, minor] of [['C#',1,0],['D#',3,0],['GB',6,0],['G#',8,0],['A#',10,0],
                                     ['DBM',1,1],['D#M',3,1],['GBM',6,1],['ABM',8,1],['A#M',10,1]]) {
    m[alias] = minor ? [CAMELOT_MINOR[pc], KEY_MINOR_NAMES[pc]] : [CAMELOT_MAJOR[pc], KEY_MAJOR_NAMES[pc]];
  }
  return m;
})();
// fmtKey re-renders a stored (Camelot) key in the user's chosen notation.
// Unknown spellings pass through unchanged.
function fmtKey(raw) {
  if (!raw) return raw || '';
  const p = KEY_PAIRS[String(raw).trim().toUpperCase()];
  if (!p) return raw;
  return keyDisplayMode() === 'classic' ? p[1] : p[0];
}
// keyBoth shows both notations (Camelot · classic) for the track-detail view,
// leading with the user's chosen key_display. Falls back to the raw string if
// the key isn't recognised.
function keyBoth(raw) {
  if (!raw) return '';
  const p = KEY_PAIRS[String(raw).trim().toUpperCase()];
  if (!p) return raw;
  return keyDisplayMode() === 'classic' ? `${p[1]} · ${p[0]}` : `${p[0]} · ${p[1]}`;
}

