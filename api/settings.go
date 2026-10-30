// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"

	"github.com/vynulldev/vynull/analysis"
	"github.com/vynulldev/vynull/device"
	"github.com/vynulldev/vynull/internal/dlog"
)

// syncGridOffset applies the persisted beat-grid offset. No cache
// invalidation: cached detected grids shift to the new offset on their next
// read (analysis.Store.Get), and hand-edited or imported grids never move.
func (s *Server) syncGridOffset() {
	if s.Device == nil || s.Device.Settings == nil {
		return
	}
	ms := s.Device.Settings.GetGridOffset()
	if ms == analysis.GridOffset() {
		return
	}
	analysis.SetGridOffset(ms)
	dlog.Infof("api: beat-grid offset -> %+gms (detected grids shift on next read)", ms)
}

// syncBPMRange applies the persisted Analysis-Setting BPM range to the analyzer
// and, when it actually changed, drops cached analyses so affected tracks
// re-analyze on next access (lazy — nothing is re-analyzed up front). Called
// after a settings write.
func (s *Server) syncBPMRange() {
	if s.Device == nil || s.Device.Settings == nil {
		return
	}
	mn, mx := s.Device.Settings.GetBPMRange()
	if oldMn, oldMx := analysis.TempoRange(); mn == oldMn && mx == oldMx {
		return
	}
	analysis.SetTempoRange(mn, mx)
	if s.Analysis != nil {
		s.Analysis.InvalidateAll()
	}
	dlog.Infof("api: BPM analysis range -> %g-%g; cached analyses invalidated", mn, mx)
}

// handleSettings handles GET/POST /api/settings for CDJ display settings.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.Device.Settings
	if settings == nil {
		jsonError(w, "settings not available", http.StatusServiceUnavailable)
		return
	}

	type SettingsJSON struct {
		WaveformSize     string `json:"waveform_size"`     // "full" or "half"
		WaveformColor    string `json:"waveform_color"`    // "blue", "rgb", or "3band"
		WaveformPosition string `json:"waveform_position"` // "left" or "center"
		KeyDisplay       string `json:"key_display"`       // "classic" or "alphanumeric"
		TrackDetail      string `json:"track_detail"`      // field shown next to track titles
	}

	devSettingToJSON := func() SettingsJSON {
		dev := settings.GetDevSetting()
		var js SettingsJSON
		js.TrackDetail = settings.GetTrackDetail()
		if len(dev) >= 6 {
			switch dev[1] {
			case 0x01:
				js.WaveformSize = "half"
			default:
				js.WaveformSize = "full"
			}
			switch dev[2] {
			case 0x01:
				js.WaveformColor = "blue"
			case 0x03:
				js.WaveformColor = "rgb"
			default:
				js.WaveformColor = "3band"
			}
			switch dev[4] {
			case 0x01:
				js.KeyDisplay = "classic"
			default:
				js.KeyDisplay = "alphanumeric"
			}
			switch dev[5] {
			case 0x01:
				js.WaveformPosition = "center"
			default:
				js.WaveformPosition = "left"
			}
		}
		return js
	}

	if r.Method == http.MethodGet {
		// Backward-compatible: existing 5-field DEVSETTING-derived shape,
		// plus the full YAML config under "full" so the web UI can render
		// every MYSETTING/MYSETTING2/DJMMYSETTING field too.
		full := settings.Config()
		// Report the EFFECTIVE analysis BPM range, not just what's persisted:
		// --bpm-range overrides the range for the session without writing
		// settings.json, so reflect it here or the UI shows Default while a
		// flag-set range is actually in force. Editing it in the UI still
		// persists to settings.json as usual.
		full.BPMRangeMin, full.BPMRangeMax = analysis.TempoRange()
		full.GridOffsetMs = analysis.GridOffset()
		resp := struct {
			SettingsJSON
			Full device.SettingsConfig `json:"full"`
		}{
			SettingsJSON: devSettingToJSON(),
			Full:         full,
		}
		writeJSON(w, resp)
		return
	}

	if !allow(w, r, http.MethodPost) {
		return
	}

	// Two POST shapes accepted:
	//   1. {full: {<entire SettingsConfig>}} — web UI uses this when the
	//      user changes any field; we replace the whole config and persist.
	//   2. {waveform_size: ..., waveform_color: ..., ...} — legacy shape
	//      that only touches the 4 DEVSETTING bytes + track_detail.
	var combined struct {
		SettingsJSON
		Full *device.SettingsConfig `json:"full,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&combined); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if combined.Full != nil {
		settings.SetConfig(*combined.Full)
		dlog.Infof("api: settings: full config replaced")
		s.syncBPMRange() // keep the analyzer + cache in step with the Analysis Setting
		s.syncGridOffset()
		s.Device.NotifySettingsChanged()
		ok(w)
		return
	}
	req := combined.SettingsJSON

	// Start from current settings and apply changes.
	dev := make([]byte, 6)
	copy(dev, settings.GetDevSetting())
	if len(dev) < 6 {
		dev = []byte{0x01, 0x02, 0x03, 0x01, 0x02, 0x01}
	}
	// Ensure required constant bytes are always 0x01.
	dev[0] = 0x01
	dev[3] = 0x01

	if req.WaveformSize != "" {
		switch req.WaveformSize {
		case "half":
			dev[1] = 0x01
		case "full":
			dev[1] = 0x02
		}
	}
	if req.WaveformColor != "" {
		switch req.WaveformColor {
		case "blue":
			dev[2] = 0x01
		case "rgb":
			dev[2] = 0x03
		case "3band":
			dev[2] = 0x04
		}
	}
	if req.KeyDisplay != "" {
		switch req.KeyDisplay {
		case "classic":
			dev[4] = 0x01
		case "alphanumeric":
			dev[4] = 0x02
		}
	}
	if req.WaveformPosition != "" {
		switch req.WaveformPosition {
		case "center":
			dev[5] = 0x01
		case "left":
			dev[5] = 0x02
		}
	}

	settings.SaveDevSetting(dev)
	if req.TrackDetail != "" {
		settings.SetTrackDetail(req.TrackDetail)
	}
	dlog.Infof("api: settings updated: %+v", devSettingToJSON())

	// Notify connected CDJs that settings changed.
	s.Device.NotifySettingsChanged()

	writeJSON(w, devSettingToJSON())
}
