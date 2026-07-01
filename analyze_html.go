// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"vynull/analysis"
	"vynull/pdb"
)

func writeAnalysisHTML(outPath, audioPath string, result *analysis.Result, beats *analysis.BeatResult, rbTrack *pdb.Track, rbDATPath, rbEXTPath, rb2EXPath string) error {
	var b strings.Builder

	trackName := filepath.Base(audioPath)

	// Read rekordbox ANLZ sections if available.
	var rbPQTZ, rbPWAV, rbPWV4, rbPWV5, rbPWV6, rbPWV7 []byte
	var rbPQT2Section []byte // full section (header + body)
	var rbBeats []rbBeat
	if rbDATPath != "" {
		rbPQTZ = readANLZBody(rbDATPath, "PQTZ")
		rbPWAV = readANLZBody(rbDATPath, "PWAV")
	}
	if rbEXTPath != "" {
		rbPWV4 = readANLZBody(rbEXTPath, "PWV4")
		rbPWV5 = readANLZBody(rbEXTPath, "PWV5")
		rbPQT2Section = readANLZSection(rbEXTPath, "PQT2")
	}
	if rb2EXPath != "" {
		rbPWV6 = readANLZBody(rb2EXPath, "PWV6")
		rbPWV7 = readANLZBody(rb2EXPath, "PWV7")
	}
	if rbPQTZ != nil {
		rbBeats = parsePQTZBeats(rbPQTZ)
	}

	b.WriteString(`<!DOCTYPE html>
<html><head><meta charset="utf-8">
<title>Analysis: ` + htmlEscape(trackName) + `</title>
<style>
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #1a1a2e; color: #e0e0e0; padding: 20px; }
h1 { color: #fff; margin-bottom: 4px; font-size: 20px; }
h2 { color: #8888cc; margin: 20px 0 10px; font-size: 16px; border-bottom: 1px solid #333; padding-bottom: 4px; }
h3 { color: #aaa; margin: 12px 0 6px; font-size: 13px; }
.subtitle { color: #888; font-size: 12px; margin-bottom: 16px; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; }
.panel { background: #16213e; border-radius: 8px; padding: 16px; }
.panel h3 { color: #6c9bff; margin-top: 0; }
table { border-collapse: collapse; width: 100%; font-size: 12px; font-family: 'SF Mono', 'Consolas', monospace; }
th { background: #1a1a3e; color: #8888cc; text-align: left; padding: 4px 8px; position: sticky; top: 0; }
td { padding: 3px 8px; border-bottom: 1px solid #222; }
tr:hover { background: #1e2a4a; }
.diff { background: #3a2020; }
.good { color: #5c5; }
.warn { color: #cc5; }
.bad { color: #c55; }
.summary-table td:first-child { color: #888; width: 140px; }
.summary-table td:nth-child(2) { color: #6c9bff; font-weight: bold; }
.summary-table td:nth-child(3) { color: #5c5; font-weight: bold; }
.summary-table td:nth-child(4) { color: #cc5; }
canvas { border-radius: 4px; display: block; margin: 4px 0; }
.wave-label { font-size: 11px; color: #666; margin-top: 8px; }
.beat-table { max-height: 500px; overflow-y: auto; display: block; }
.beat-table table { display: table; }
</style></head><body>
`)

	b.WriteString(`<h1>` + htmlEscape(trackName) + `</h1>`)
	b.WriteString(`<div class="subtitle">` + htmlEscape(audioPath) + `</div>`)

	// Summary comparison table.
	b.WriteString(`<h2>Summary</h2>`)
	b.WriteString(`<table class="summary-table"><tr><th></th><th>Rekordbox</th><th>Ours</th><th>Delta</th></tr>`)

	rbBPM := 0.0
	rbFirstBeat := 0.0
	rbBeatCount := len(rbBeats)
	if len(rbBeats) > 0 {
		rbBPM = rbBeats[0].tempo
		rbFirstBeat = float64(rbBeats[0].timeMs)
	}
	ourBPM := beats.BPM
	ourFirstBeat := 0.0
	if len(beats.Beats) > 0 {
		ourFirstBeat = beats.Beats[0]
	}

	bpmDelta := ourBPM - rbBPM
	beatDelta := ourFirstBeat - rbFirstBeat
	bpmClass := "good"
	if math.Abs(bpmDelta) > 0.1 {
		bpmClass = "warn"
	}
	if math.Abs(bpmDelta) > 1.0 {
		bpmClass = "bad"
	}
	beatClass := "good"
	if math.Abs(beatDelta) > 20 {
		beatClass = "warn"
	}
	if math.Abs(beatDelta) > 50 {
		beatClass = "bad"
	}

	summaryRow := func(label, rbVal, ourVal, delta, class string) {
		b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td><td class="%s">%s</td></tr>`, label, rbVal, ourVal, class, delta))
	}

	if rbBPM > 0 {
		summaryRow("BPM", fmt.Sprintf("%.2f", rbBPM), fmt.Sprintf("%.2f", ourBPM), fmt.Sprintf("%+.3f", bpmDelta), bpmClass)
	} else {
		summaryRow("BPM", "-", fmt.Sprintf("%.2f", ourBPM), "", "")
	}
	summaryRow("Beats", fmt.Sprintf("%d", rbBeatCount), fmt.Sprintf("%d", len(beats.Beats)), fmt.Sprintf("%+d", len(beats.Beats)-rbBeatCount), "")
	if rbFirstBeat > 0 {
		summaryRow("First beat", fmt.Sprintf("%.1f ms", rbFirstBeat), fmt.Sprintf("%.1f ms", ourFirstBeat), fmt.Sprintf("%+.1f ms", beatDelta), beatClass)
	} else {
		summaryRow("First beat", "-", fmt.Sprintf("%.1f ms", ourFirstBeat), "", "")
	}
	// Downbeat: rekordbox encodes it in PQTZ beat#1 position
	rbDownbeat := "-"
	if len(rbBeats) > 0 {
		// Find first beat#1 — that's the downbeat
		for _, rb := range rbBeats {
			if rb.beatNum == 1 {
				rbDownbeat = fmt.Sprintf("%.1f ms", float64(rb.timeMs))
				break
			}
		}
	}
	summaryRow("Downbeat", rbDownbeat, fmt.Sprintf("%.1f ms", beats.Downbeat), "", "")

	// Key from PDB
	rbKey := "-"
	if rbTrack != nil && rbTrack.Key != "" {
		rbKey = rbTrack.Key
	}
	keyDelta := ""
	if rbKey != "-" && rbKey != result.KeyCamelot && rbKey != result.KeyStandard {
		keyDelta = "mismatch"
	}
	summaryRow("Key", rbKey, result.KeyCamelot+" ("+result.KeyStandard+")", keyDelta, func() string {
		if keyDelta != "" {
			return "warn"
		}
		return "good"
	}())

	// Duration from PDB
	rbDur := "-"
	if rbTrack != nil && rbTrack.Duration > 0 {
		rbDur = fmt.Sprintf("%ds", rbTrack.Duration)
	}
	summaryRow("Duration", rbDur, fmt.Sprintf("%ds", result.Duration), "", "")
	b.WriteString(`</table>`)

	// Waveform visualizations.
	b.WriteString(`<h2>Waveforms</h2>`)
	b.WriteString(`<div class="grid">`)

	// PWAV comparison.
	b.WriteString(`<div class="panel"><h3>PWAV (Mono Preview) — Rekordbox</h3>`)
	if rbPWAV != nil {
		writeWaveCanvas(&b, "rb_pwav", rbPWAV, 1, false)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class="panel"><h3>PWAV (Mono Preview) — Ours</h3>`)
	if result.WavePreview != nil {
		writeWaveCanvas(&b, "our_pwav", result.WavePreview, 1, false)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div>`)

	// PWV4 comparison.
	b.WriteString(`<div class="panel"><h3>PWV4 (Color Preview) — Rekordbox</h3>`)
	if rbPWV4 != nil {
		writeColorPreviewCanvas(&b, "rb_pwv4", rbPWV4)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class="panel"><h3>PWV4 (Color Preview) — Ours</h3>`)
	if result.WaveColorPreview != nil {
		writeColorPreviewCanvas(&b, "our_pwv4", result.WaveColorPreview)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div>`)

	// PWV5 comparison.
	b.WriteString(`<div class="panel"><h3>PWV5 (Color Detail) — Rekordbox</h3>`)
	if rbPWV5 != nil {
		writeColorDetailCanvas(&b, "rb_pwv5", rbPWV5)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<div class="panel"><h3>PWV5 (Color Detail) — Ours</h3>`)
	if result.WaveDetail != nil {
		writeColorDetailCanvas(&b, "our_pwv5", result.WaveDetail)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div>`)

	// PWV6 (3-band preview) — from .2EX file.
	if rbPWV6 != nil {
		b.WriteString(`<div class="panel" style="grid-column: span 2"><h3>PWV6 (3-Band Preview) — Rekordbox</h3>`)
		writeThreeBandPreviewCanvas(&b, "rb_pwv6", rbPWV6)
		b.WriteString(`</div>`)
	}

	// PWV7 (3-band detail) — from .2EX file.
	if rbPWV7 != nil {
		b.WriteString(`<div class="panel" style="grid-column: span 2"><h3>PWV7 (3-Band Detail) — Rekordbox</h3>`)
		writeThreeBandDetailCanvas(&b, "rb_pwv7", rbPWV7)
		b.WriteString(`</div>`)
	}

	b.WriteString(`</div>`) // end grid

	// Waveform with beat grid overlay.
	b.WriteString(`<h2>Waveform + Beat Grid</h2>`)
	if result.WaveDetail != nil && len(beats.Beats) > 0 {
		writeColorDetailWithBeats(&b, "wave_beats", result.WaveDetail, beats.Beats, beats.Downbeat, float64(result.Duration))
	}
	if rbPWV5 != nil && len(rbBeats) > 0 {
		rbBeatPositions := make([]float64, len(rbBeats))
		for i, rb := range rbBeats {
			rbBeatPositions[i] = float64(rb.timeMs)
		}
		rbDownbeat := 0.0
		for _, rb := range rbBeats {
			if rb.beatNum == 1 {
				rbDownbeat = float64(rb.timeMs)
				break
			}
		}
		writeColorDetailWithBeats(&b, "rb_wave_beats", rbPWV5, rbBeatPositions, rbDownbeat, float64(result.Duration))
		b.WriteString(`<div class="wave-label">Top: ours, Bottom: rekordbox. Yellow = downbeat, white = beats 2-4.</div>`)
	}

	// PQT2 header comparison.
	b.WriteString(`<h2>PQT2 Header (Extended Beat Grid)</h2>`)
	// Our PQT2 is in result.BeatGridPQT2 which includes a 4-byte LE prefix.
	var ourPQT2Section []byte
	if result.BeatGridPQT2 != nil && len(result.BeatGridPQT2) >= 60 {
		ourPQT2Section = result.BeatGridPQT2[4:] // skip LE prefix
	}
	writePQT2HeaderTable(&b, rbPQT2Section, ourPQT2Section)

	// Beat grid comparison.
	b.WriteString(`<h2>Beat Grid</h2>`)
	b.WriteString(`<div class="grid">`)

	b.WriteString(`<div class="panel"><h3>PQTZ — Rekordbox</h3>`)
	b.WriteString(`<div class="beat-table">`)
	if len(rbBeats) > 0 {
		writeBeatTable(&b, rbBeats, nil)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div></div>`)

	b.WriteString(`<div class="panel"><h3>Detected — Ours</h3>`)
	b.WriteString(`<div class="beat-table">`)
	if len(beats.Beats) > 0 {
		writeOurBeatTable(&b, beats, rbBeats)
	} else {
		b.WriteString(`<p style="color:#666">No data</p>`)
	}
	b.WriteString(`</div></div>`)

	b.WriteString(`</div>`) // end grid

	b.WriteString(`</body></html>`)

	return os.WriteFile(outPath, []byte(b.String()), 0644)
}

type rbBeat struct {
	beatNum uint16
	tempo   float64
	timeMs  uint32
}

func parsePQTZBeats(body []byte) []rbBeat {
	n := len(body) / 8
	beats := make([]rbBeat, n)
	for i := 0; i < n; i++ {
		off := i * 8
		beats[i] = rbBeat{
			beatNum: binary.BigEndian.Uint16(body[off:]),
			tempo:   float64(binary.BigEndian.Uint16(body[off+2:])) / 100.0,
			timeMs:  binary.BigEndian.Uint32(body[off+4:]),
		}
	}
	return beats
}

// readANLZSection reads a full section (header + body) from an ANLZ file.
func readANLZSection(filePath, tag string) []byte {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 28 {
		return nil
	}
	hdrLen := int(binary.BigEndian.Uint32(data[4:8]))
	pos := hdrLen
	for pos+12 <= len(data) {
		fourcc := string(data[pos : pos+4])
		secLen := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		if secLen <= 0 || pos+secLen > len(data) {
			break
		}
		if fourcc == tag {
			return data[pos : pos+secLen]
		}
		pos += secLen
	}
	return nil
}

// readANLZBody reads a section's body (after the header) from an ANLZ file.
func readANLZBody(filePath, tag string) []byte {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 28 {
		return nil
	}
	hdrLen := int(binary.BigEndian.Uint32(data[4:8]))
	pos := hdrLen
	for pos+12 <= len(data) {
		fourcc := string(data[pos : pos+4])
		secHdrLen := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		secLen := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		if secLen <= 0 || pos+secLen > len(data) {
			break
		}
		if fourcc == tag {
			return data[pos+secHdrLen : pos+secLen]
		}
		pos += secLen
	}
	return nil
}

func writeWaveCanvas(b *strings.Builder, id string, data []byte, bytesPerEntry int, color bool) {
	n := len(data) / bytesPerEntry
	width := n
	if width > 1200 {
		width = 1200
	}
	height := 64

	// Encode data as base64 for JS.
	encoded := base64.StdEncoding.EncodeToString(data)

	b.WriteString(fmt.Sprintf(`<canvas id="%s" width="%d" height="%d"></canvas>`, id, width, height))
	b.WriteString(fmt.Sprintf(`<div class="wave-label">%d entries</div>`, n))
	b.WriteString(fmt.Sprintf(`<script>
(function() {
  var c = document.getElementById('%s');
  var ctx = c.getContext('2d');
  var raw = atob('%s');
  var n = raw.length;
  ctx.fillStyle = '#16213e';
  ctx.fillRect(0, 0, c.width, c.height);
  var step = Math.max(1, n / c.width);
  for (var i = 0; i < c.width; i++) {
    var idx = Math.floor(i * step);
    if (idx >= n) break;
    var h = (raw.charCodeAt(idx) & 0x1f);
    var barH = h / 31 * c.height;
    ctx.fillStyle = '#4466aa';
    ctx.fillRect(i, c.height - barH, 1, barH);
  }
})();
</script>`, id, encoded))
}

func writeColorPreviewCanvas(b *strings.Builder, id string, data []byte) {
	n := len(data) / 6
	width := n
	if width > 1200 {
		width = 1200
	}
	height := 64

	encoded := base64.StdEncoding.EncodeToString(data)

	b.WriteString(fmt.Sprintf(`<canvas id="%s" width="%d" height="%d"></canvas>`, id, width, height))
	b.WriteString(fmt.Sprintf(`<div class="wave-label">%d entries (6 bytes each)</div>`, n))
	b.WriteString(fmt.Sprintf(`<script>
(function() {
  var c = document.getElementById('%s');
  var ctx = c.getContext('2d');
  var raw = atob('%s');
  var n = raw.length / 6;
  ctx.fillStyle = '#16213e';
  ctx.fillRect(0, 0, c.width, c.height);
  var step = Math.max(1, n / c.width);
  for (var i = 0; i < c.width; i++) {
    var idx = Math.floor(i * step) * 6;
    if (idx + 5 >= raw.length) break;
    var b0 = raw.charCodeAt(idx);
    var h = (b0 & 0x1f);
    var r = raw.charCodeAt(idx+3);
    var g = raw.charCodeAt(idx+4);
    var bl = raw.charCodeAt(idx+5);
    var barH = h / 31 * c.height;
    ctx.fillStyle = 'rgb(' + Math.min(255,r*2) + ',' + Math.min(255,g*2) + ',' + Math.min(255,bl*2) + ')';
    ctx.fillRect(i, c.height - barH, 1, barH);
  }
})();
</script>`, id, encoded))
}

func writeColorDetailCanvas(b *strings.Builder, id string, data []byte) {
	n := len(data) / 2
	width := 1200
	height := 64

	encoded := base64.StdEncoding.EncodeToString(data)

	// Count padding.
	padding := 0
	for i := 0; i < len(data)-1; i += 2 {
		if data[i] == 0xff && data[i+1] == 0x80 {
			padding++
		}
	}

	b.WriteString(fmt.Sprintf(`<canvas id="%s" width="%d" height="%d"></canvas>`, id, width, height))
	b.WriteString(fmt.Sprintf(`<div class="wave-label">%d entries, %d padding (%.1f%%)</div>`, n, padding, float64(padding)/float64(max(n, 1))*100))
	b.WriteString(fmt.Sprintf(`<script>
(function() {
  var c = document.getElementById('%s');
  var ctx = c.getContext('2d');
  var raw = atob('%s');
  var n = raw.length / 2;
  ctx.fillStyle = '#16213e';
  ctx.fillRect(0, 0, c.width, c.height);
  var colorMap = [
    [0,0,255], [0,80,255], [0,160,200], [0,200,100],
    [180,200,0], [255,160,0], [255,60,0], [255,255,255]
  ];
  var step = Math.max(1, n / c.width);
  for (var i = 0; i < c.width; i++) {
    var idx = Math.floor(i * step) * 2;
    if (idx + 1 >= raw.length) break;
    var hi = raw.charCodeAt(idx);
    var lo = raw.charCodeAt(idx+1);
    var word = (hi << 8) | lo;
    var r = (word >> 13) & 7;
    var g = (word >> 10) & 7;
    var b = (word >> 7) & 7;
    var h = (word >> 2) & 0x1f;
    if (hi == 0xff && lo == 0x80) continue;
    var barH = h / 31 * c.height;
    ctx.fillStyle = 'rgb(' + Math.round(r*36) + ',' + Math.round(g*36) + ',' + Math.round(b*36) + ')';
    ctx.fillRect(i, c.height - barH, 1, barH);
  }
})();
</script>`, id, encoded))
}

// writeThreeBandPreviewCanvas renders a PWV6 3-band preview waveform.
// Format: 1200 entries × 3 bytes each: [mid, high, low].
// Colors: low=dark blue (#0046bf), mid=amber (#db9316), high=white.
func writeThreeBandPreviewCanvas(b *strings.Builder, id string, data []byte) {
	n := len(data) / 3
	width := 1200
	height := 64

	encoded := base64.StdEncoding.EncodeToString(data)

	b.WriteString(fmt.Sprintf(`<canvas id="%s" width="%d" height="%d"></canvas>`, id, width, height))
	b.WriteString(fmt.Sprintf(`<div class="wave-label">%d entries (3 bytes/entry)</div>`, n))
	b.WriteString(fmt.Sprintf(`<script>
(function() {
  var c = document.getElementById('%s');
  var ctx = c.getContext('2d');
  var raw = atob('%s');
  var n = raw.length / 3;
  ctx.fillStyle = '#16213e';
  ctx.fillRect(0, 0, c.width, c.height);
  var step = Math.max(1, n / c.width);
  for (var i = 0; i < c.width; i++) {
    var idx = Math.floor(i * step) * 3;
    if (idx + 2 >= raw.length) break;
    var mid  = raw.charCodeAt(idx);
    var high = raw.charCodeAt(idx + 1);
    var low  = raw.charCodeAt(idx + 2);
    // Stack: low at bottom (dark blue), mid above (amber), high on top (white).
    var scale = c.height / 255;
    var lowH  = low * scale;
    var midH  = mid * scale;
    var highH = high * scale;
    var y = c.height;
    ctx.fillStyle = '#0046bf';
    ctx.fillRect(i, y - lowH, 1, lowH);
    y -= lowH;
    ctx.fillStyle = '#db9316';
    ctx.fillRect(i, y - midH, 1, midH);
    y -= midH;
    ctx.fillStyle = '#e0e0e0';
    ctx.fillRect(i, y - highH, 1, highH);
  }
})();
</script>`, id, encoded))
}

// writeThreeBandDetailCanvas renders a PWV7 3-band detail waveform.
// Format: ~150 entries/sec × 3 bytes each: [mid, high, low].
// Colors: low=dark blue, mid=amber, high=white. Overlapping (not stacked).
func writeThreeBandDetailCanvas(b *strings.Builder, id string, data []byte) {
	n := len(data) / 3
	width := 1200
	if n < width {
		width = n
	}
	height := 64

	encoded := base64.StdEncoding.EncodeToString(data)

	b.WriteString(fmt.Sprintf(`<canvas id="%s" width="%d" height="%d"></canvas>`, id, width, height))
	b.WriteString(fmt.Sprintf(`<div class="wave-label">%d entries (3 bytes/entry, %.1fs at 150/sec)</div>`, n, float64(n)/150.0))
	b.WriteString(fmt.Sprintf(`<script>
(function() {
  var c = document.getElementById('%s');
  var ctx = c.getContext('2d');
  var raw = atob('%s');
  var n = raw.length / 3;
  ctx.fillStyle = '#16213e';
  ctx.fillRect(0, 0, c.width, c.height);
  var step = Math.max(1, n / c.width);
  for (var i = 0; i < c.width; i++) {
    var idx = Math.floor(i * step) * 3;
    if (idx + 2 >= raw.length) break;
    var mid  = raw.charCodeAt(idx);
    var high = raw.charCodeAt(idx + 1);
    var low  = raw.charCodeAt(idx + 2);
    // Overlapping bars: low behind (tallest), mid in front, high on top.
    var scale = c.height / 255;
    ctx.fillStyle = '#0046bf';
    ctx.fillRect(i, c.height - low * scale, 1, low * scale);
    ctx.fillStyle = '#db9316';
    ctx.fillRect(i, c.height - mid * scale, 1, mid * scale);
    ctx.fillStyle = '#e0e0e0';
    ctx.fillRect(i, c.height - high * scale, 1, high * scale);
  }
})();
</script>`, id, encoded))
}

// writeColorDetailWithBeats renders the PWV5 color detail waveform with beat grid lines overlaid.
// beatPositions are in milliseconds, duration is track duration in seconds.
func writeColorDetailWithBeats(b *strings.Builder, id string, data []byte, beatPositions []float64, downbeat float64, durationSec float64) {
	n := len(data) / 2
	width := n // 1 pixel per waveform entry — full resolution
	if width > 20000 {
		width = 20000
	}
	height := 128

	encoded := base64.StdEncoding.EncodeToString(data)

	// Encode beat positions as comma-separated values.
	var beatsStr strings.Builder
	for i, bp := range beatPositions {
		if i > 0 {
			beatsStr.WriteString(",")
		}
		beatsStr.WriteString(fmt.Sprintf("%.1f", bp))
	}

	b.WriteString(fmt.Sprintf(`<div style="overflow-x:auto;max-width:100%%;border:1px solid #333;"><canvas id="%s" width="%d" height="%d" style="image-rendering:pixelated;"></canvas></div>`, id, width, height))
	b.WriteString(fmt.Sprintf(`<div class="wave-label">%d waveform entries, %d beats, %.1fs duration</div>`, n, len(beatPositions), durationSec))
	b.WriteString(fmt.Sprintf(`<script>
(function() {
  var c = document.getElementById('%s');
  var ctx = c.getContext('2d');
  var raw = atob('%s');
  var n = raw.length / 2;
  var durationMs = %f;
  var downbeatMs = %f;
  var beats = [%s];

  ctx.fillStyle = '#16213e';
  ctx.fillRect(0, 0, c.width, c.height);

  // Draw waveform
  var step = Math.max(1, n / c.width);
  for (var i = 0; i < c.width; i++) {
    var idx = Math.floor(i * step) * 2;
    if (idx + 1 >= raw.length) break;
    var hi = raw.charCodeAt(idx);
    var lo = raw.charCodeAt(idx+1);
    var word = (hi << 8) | lo;
    var r = (word >> 13) & 7;
    var g = (word >> 10) & 7;
    var b = (word >> 7) & 7;
    var h = (word >> 2) & 0x1f;
    if (hi == 0xff && lo == 0x80) continue;
    var barH = h / 31 * (c.height / 2);
    var mid = c.height / 2;
    ctx.fillStyle = 'rgb(' + Math.round(r*36) + ',' + Math.round(g*36) + ',' + Math.round(b*36) + ')';
    ctx.fillRect(i, mid - barH, 1, barH * 2);
  }

  // Draw beat grid lines
  for (var bi = 0; bi < beats.length; bi++) {
    var x = Math.round(beats[bi] / durationMs * c.width);
    if (x < 0 || x >= c.width) continue;

    // Find downbeat index
    var dbIdx = 0;
    for (var di = 0; di < beats.length; di++) {
      if (beats[di] >= downbeatMs - 0.5) { dbIdx = di; break; }
    }

    var beatInBar = ((bi - dbIdx) %% 4);
    if (beatInBar < 0) beatInBar += 4;

    if (beatInBar === 0) {
      // Beat 1 (downbeat) — bright yellow line
      ctx.fillStyle = 'rgba(255, 255, 0, 0.9)';
      ctx.fillRect(x, 0, 2, c.height);
    } else {
      // Beats 2-4 — dim white line
      ctx.fillStyle = 'rgba(255, 255, 255, 0.3)';
      ctx.fillRect(x, 0, 1, c.height);
    }
  }
})();
</script>`, id, encoded, durationSec*1000, downbeat, beatsStr.String()))
}

func writeBeatTable(b *strings.Builder, beats []rbBeat, _ []float64) {
	b.WriteString(`<table><tr><th>#</th><th>Time (ms)</th><th>Interval</th><th>BPM</th><th>Bar</th></tr>`)
	limit := len(beats)
	if limit > 200 {
		limit = 200
	}
	for i := 0; i < limit; i++ {
		interval := "-"
		if i > 0 {
			iv := float64(beats[i].timeMs) - float64(beats[i-1].timeMs)
			interval = fmt.Sprintf("%.1f", iv)
		}
		bar := (i / 4) + 1
		bib := (i % 4) + 1
		b.WriteString(fmt.Sprintf(`<tr><td>%d</td><td>%d</td><td>%s</td><td>%.2f</td><td>%d.%d</td></tr>`,
			i+1, beats[i].timeMs, interval, beats[i].tempo, bar, bib))
	}
	if len(beats) > 200 {
		b.WriteString(fmt.Sprintf(`<tr><td colspan="5" style="color:#666">... %d more</td></tr>`, len(beats)-200))
	}
	b.WriteString(`</table>`)
}

func writeOurBeatTable(b *strings.Builder, beats *analysis.BeatResult, rbBeats []rbBeat) {
	b.WriteString(`<table><tr><th>#</th><th>Time (ms)</th><th>Interval</th><th>BPM</th><th>Bar</th><th>Δ rb</th></tr>`)

	dbIdx := 0
	for j, bt := range beats.Beats {
		if bt >= beats.Downbeat-0.5 {
			dbIdx = j
			break
		}
	}

	limit := len(beats.Beats)
	if limit > 200 {
		limit = 200
	}
	for i := 0; i < limit; i++ {
		ms := beats.Beats[i]
		interval := "-"
		localBPM := fmt.Sprintf("%.2f", beats.BPM)
		if i > 0 {
			iv := beats.Beats[i] - beats.Beats[i-1]
			interval = fmt.Sprintf("%.1f", iv)
			if iv > 0 {
				localBPM = fmt.Sprintf("%.2f", 60000.0/iv)
			}
		}
		beatInBar := ((i - dbIdx) % 4)
		if beatInBar < 0 {
			beatInBar += 4
		}
		beatInBar++
		barNum := (i-dbIdx)/4 + 1
		barStr := fmt.Sprintf("%d.%d", barNum, beatInBar)
		if i < dbIdx {
			barStr = "(pre)"
		}

		// Delta vs rekordbox.
		delta := ""
		rowClass := ""
		if i < len(rbBeats) {
			d := ms - float64(rbBeats[i].timeMs)
			delta = fmt.Sprintf("%+.1f", d)
			if math.Abs(d) > 50 {
				rowClass = ` class="diff"`
			}
		}

		b.WriteString(fmt.Sprintf(`<tr%s><td>%d</td><td>%.1f</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			rowClass, i+1, ms, interval, localBPM, barStr, delta))
	}
	if len(beats.Beats) > 200 {
		b.WriteString(fmt.Sprintf(`<tr><td colspan="6" style="color:#666">... %d more</td></tr>`, len(beats.Beats)-200))
	}
	b.WriteString(`</table>`)
}

func writePQT2HeaderTable(b *strings.Builder, rbSection, ourSection []byte) {
	type field struct {
		offset int
		size   int
		name   string
		format string // "hex", "u32", "u16", "tempo", "time", "beatnum"
	}
	fields := []field{
		{0, 4, "FourCC", "hex"},
		{4, 4, "len_header", "u32"},
		{8, 4, "len_tag", "u32"},
		{12, 4, "(zero)", "hex"},
		{16, 4, "constant", "hex"},
		{20, 4, "(zero)", "hex"},
		{24, 2, "first_beat: beat#", "u16"},
		{26, 2, "first_beat: tempo", "tempo"},
		{28, 4, "first_beat: time_ms", "u32"},
		{32, 2, "last_beat: beat#", "u16"},
		{34, 2, "last_beat: tempo", "tempo"},
		{36, 4, "last_beat: time_ms", "u32"},
		{40, 4, "entry_count", "u32"},
		{44, 4, "(zero)", "hex"},
		{48, 4, "(reserved)", "hex"},
		{52, 4, "(reserved)", "hex"},
	}

	readField := func(data []byte, f field) string {
		if data == nil || f.offset+f.size > len(data) {
			return "-"
		}
		switch f.format {
		case "hex":
			h := ""
			for _, b := range data[f.offset : f.offset+f.size] {
				h += fmt.Sprintf("%02x", b)
			}
			return "0x" + h
		case "u32":
			return fmt.Sprintf("%d", binary.BigEndian.Uint32(data[f.offset:]))
		case "u16":
			return fmt.Sprintf("%d", binary.BigEndian.Uint16(data[f.offset:]))
		case "tempo":
			v := binary.BigEndian.Uint16(data[f.offset:])
			return fmt.Sprintf("%d (%.2f BPM)", v, float64(v)/100)
		default:
			return fmt.Sprintf("%d", binary.BigEndian.Uint32(data[f.offset:]))
		}
	}

	b.WriteString(`<table><tr><th>Offset</th><th>Size</th><th>Field</th><th>Rekordbox</th><th>Ours</th><th>Match</th></tr>`)
	for _, f := range fields {
		rbVal := readField(rbSection, f)
		ourVal := readField(ourSection, f)
		match := ""
		matchClass := ""
		if rbVal != "-" && ourVal != "-" {
			if rbVal == ourVal {
				match = "✓"
				matchClass = "good"
			} else {
				match = "✗"
				matchClass = "bad"
			}
		}
		b.WriteString(fmt.Sprintf(`<tr><td>%d (0x%02x)</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td class="%s">%s</td></tr>`,
			f.offset, f.offset, f.size, f.name, rbVal, ourVal, matchClass, match))
	}
	b.WriteString(`</table>`)
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
