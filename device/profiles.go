// SPDX-License-Identifier: GPL-3.0-or-later

package device

import "strings"

// DeviceProfile is a known Pioneer/AlphaTheta player model a virtual deck can
// present as. For now it carries the identity a client reads off the network:
// Name is the model string broadcast in the keep-alive and status device-name
// field (what prolink-tools and rekordbox display as the model), and Firmware
// is the 4-char version reported in the status packet (status offset 0x7c).
//
// This is the seed of per-model emulation. Name is accurate today; Firmware and
// future capability fields (hot-cue banks, waveform colour, generation) are to
// be filled from real captures and link-spec's per-model `-status` goldens,
// which is why Firmware is optional — an empty value leaves the status builder's
// current default in place rather than asserting a version we have not verified.
type DeviceProfile struct {
	Key      string `json:"key"`      // stable lookup id, e.g. "cdj-3000"
	Name     string `json:"name"`     // wire device-name string, e.g. "CDJ-3000"
	Firmware string `json:"firmware"` // 4-char status firmware, e.g. "3.00"; "" = leave the default
}

// deviceProfiles is the built-in model table, newest first. Keys match the
// device-model names link-spec uses for its goldens, so a profile can later be
// validated against the matching `<key>-status` golden.
var deviceProfiles = []DeviceProfile{
	{Key: "cdj-3000", Name: "CDJ-3000"},
	{Key: "cdj-2000nxs2", Name: "CDJ-2000NXS2"},
	{Key: "cdj-2000nexus", Name: "CDJ-2000nexus"},
	{Key: "cdj-2000", Name: "CDJ-2000"},
	{Key: "xdj-xz", Name: "XDJ-XZ"},
	{Key: "xdj-rx3", Name: "XDJ-RX3"},
	{Key: "xdj-1000mk2", Name: "XDJ-1000MK2"},
	{Key: "xdj-az", Name: "XDJ-AZ"},
}

// DeviceProfiles returns the built-in model table (a copy, newest first).
func DeviceProfiles() []DeviceProfile {
	out := make([]DeviceProfile, len(deviceProfiles))
	copy(out, deviceProfiles)
	return out
}

// ProfileByKey looks up a model profile by its key or (case-insensitively) by
// its wire name. Returns false if no built-in profile matches.
func ProfileByKey(key string) (DeviceProfile, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, p := range deviceProfiles {
		if p.Key == k || strings.ToLower(p.Name) == k {
			return p, true
		}
	}
	return DeviceProfile{}, false
}
