// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"log"
	"path/filepath"
	"sync"
)

// CDJSettings holds the runtime settings state. Backed by a JSON config
// file (last-write wins) — config seeds at startup, CDJ pushes via 0x37
// (MYSETTING) / 0x48 (DEVSETTING) decode back into the same struct and
// re-persist, so changes made via the CDJ UI survive restarts.
type CDJSettings struct {
	mu   sync.RWMutex
	path string
	cfg  SettingsConfig
}

// NewCDJSettings loads settings from dir/settings.json (or writes
// defaults there if absent). The path can be overridden via the
// --settings CLI flag (see NewCDJSettingsAt). Legacy settings.yaml at
// the same path is migrated automatically.
func NewCDJSettings(dir string) *CDJSettings {
	return NewCDJSettingsAt(filepath.Join(dir, "settings.json"))
}

// NewCDJSettingsAt loads settings from a specific path.
func NewCDJSettingsAt(path string) *CDJSettings {
	cfg, err := LoadConfig(path)
	if err != nil {
		log.Printf("settings: %v (using defaults)", err)
		cfg = DefaultSettingsConfig()
	}
	return &CDJSettings{path: path, cfg: cfg}
}

// Config returns a copy of the current config (for inspection / API).
func (s *CDJSettings) Config() SettingsConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetConfig replaces the entire config with new and persists to YAML.
// Used by the web UI when the user edits settings.
func (s *CDJSettings) SetConfig(cfg SettingsConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.persist()
	log.Printf("settings: full config replaced → %s", s.path)
}

// SaveMySetting stores MYSETTING data from a 0x37 packet. Decodes the
// 40-byte body back into the YAML config and persists.
func (s *CDJSettings) SaveMySetting(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.MySetting = DecodeMySetting(body)
	s.persist()
	log.Printf("settings: saved MYSETTING (%d bytes) → %s", len(body), s.path)
}

// SaveDevSetting stores DEVSETTING data from a 0x48 packet. Decodes the
// 6-byte body back into the YAML config and persists.
func (s *CDJSettings) SaveDevSetting(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.DevSetting = DecodeDevSetting(body)
	s.persist()
	log.Printf("settings: saved DEVSETTING (%d bytes) → %s", len(body), s.path)
}

// SaveMySetting2 stores MYSETTING2 data from a CDJ push.
func (s *CDJSettings) SaveMySetting2(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.MySetting2 = DecodeMySetting2(body)
	s.persist()
	log.Printf("settings: saved MYSETTING2 (%d bytes) → %s", len(body), s.path)
}

// GetMySetting returns the encoded MYSETTING body (40 bytes).
func (s *CDJSettings) GetMySetting() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MySetting.Encode()
}

// GetMySetting2 returns the encoded MYSETTING2 body (40 bytes).
func (s *CDJSettings) GetMySetting2() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MySetting2.Encode()
}

// GetDjmMySetting returns the encoded DJMMYSETTING body (52 bytes).
func (s *CDJSettings) GetDjmMySetting() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.DjmMySetting.Encode()
}

// GetDevSetting returns the encoded DEVSETTING body (6 bytes — wire form
// for 0x47/0x48 packets).
func (s *CDJSettings) GetDevSetting() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.DevSetting.Encode()
}

// GetDevSettingDat returns the 32-byte DEVSETTING body for the USB-export
// DEVSETTING.DAT file (8 magic + 6 fields + 18 padding).
func (s *CDJSettings) GetDevSettingDat() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.DevSetting.EncodeDat()
}

// GetTrackDetail returns the track detail column setting.
func (s *CDJSettings) GetTrackDetail() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.TrackDetail == "" {
		return "artist"
	}
	return s.cfg.TrackDetail
}

// SetTrackDetail sets the track detail column and persists.
func (s *CDJSettings) SetTrackDetail(detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.TrackDetail = detail
	s.persist()
	log.Printf("settings: track detail set to %q", detail)
}

func (s *CDJSettings) persist() {
	if err := SaveConfig(s.path, s.cfg); err != nil {
		log.Printf("settings: failed to write %s: %v", s.path, err)
	}
}
