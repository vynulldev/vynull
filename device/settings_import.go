// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ParseSettingsFile strips the 104-byte wrapper from a MYSETTING / MYSETTING2
// / DJMMYSETTING / DEVSETTING .DAT file and returns the body bytes plus the
// brand/version strings from the header.
//
// Wrapper format (104 bytes):
//
//	[4]  len_strings (always 0x60, LE)
//	[32] brand   (e.g. "PIONEER", "PioneerDJ", "PIONEER DJ"; null- or space-padded)
//	[32] software (e.g. "rekordbox", or player model like "CDJ-2000NXS2")
//	[32] version (e.g. "0.001", "1.000", firmware like "1.85")
//	[4]  len_data (LE) — body length
//	[...] data body (len_data bytes)
//	[2]  CRC16-XMODEM checksum (LE)
//	[2]  0x0000
func ParseSettingsFile(data []byte) (body []byte, brand, software, version string, err error) {
	const headerSize = 104
	if len(data) < headerSize+4 {
		err = fmt.Errorf("file too short (%d bytes)", len(data))
		return
	}
	lenStrings := binary.LittleEndian.Uint32(data[0:4])
	if lenStrings != 0x60 {
		err = fmt.Errorf("unexpected len_strings 0x%x (want 0x60)", lenStrings)
		return
	}
	brand = trimPad(string(data[4:36]))
	software = trimPad(string(data[36:68]))
	version = trimPad(string(data[68:100]))
	lenData := binary.LittleEndian.Uint32(data[100:104])
	if int(lenData) > len(data)-headerSize-4 {
		err = fmt.Errorf("len_data %d exceeds file size", lenData)
		return
	}
	body = data[headerSize : headerSize+int(lenData)]
	return
}

// trimPad removes both null and space padding from a fixed-width string field.
// rekordbox uses either depending on the file (PIONEER MYSETTING uses
// null-pad, "PIONEER DJ" DEVSETTING uses space-pad).
func trimPad(s string) string {
	return strings.TrimRight(s, "\x00 ")
}

// ImportSettingsDir reads MYSETTING.DAT, MYSETTING2.DAT, DJMMYSETTING.DAT,
// and DEVSETTING.DAT from dir (or any subset that exists), decodes them,
// and merges them into cfg. Files not found are skipped silently.
// Returns the list of files actually imported, for logging.
func ImportSettingsDir(dir string, cfg *SettingsConfig) ([]string, error) {
	var imported []string

	importOne := func(name string, decode func([]byte, *SettingsConfig)) error {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		body, brand, software, version, err := ParseSettingsFile(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		log.Printf("settings: importing %s (brand=%q software=%q version=%q body=%d bytes)",
			name, brand, software, version, len(body))
		decode(body, cfg)
		imported = append(imported, name)
		return nil
	}

	if err := importOne("MYSETTING.DAT", func(b []byte, c *SettingsConfig) {
		c.MySetting = DecodeMySetting(b)
	}); err != nil {
		return imported, err
	}
	if err := importOne("MYSETTING2.DAT", func(b []byte, c *SettingsConfig) {
		c.MySetting2 = DecodeMySetting2(b)
	}); err != nil {
		return imported, err
	}
	if err := importOne("DJMMYSETTING.DAT", func(b []byte, c *SettingsConfig) {
		c.DjmMySetting = DecodeDjmMySetting(b)
	}); err != nil {
		return imported, err
	}
	// DEVSETTING.DAT on USB is 32 bytes: 8-byte magic header (same as
	// MYSETTING/DJMMYSETTING) + 6 bytes of DEVSETTING + 18 bytes padding.
	// Only the middle 6 bytes are the documented DEVSETTING fields (same
	// layout as the 6-byte wire body sent in 0x47/0x48 packets).
	if err := importOne("DEVSETTING.DAT", func(b []byte, c *SettingsConfig) {
		if len(b) >= 14 {
			c.DevSetting = DecodeDevSetting(b[8:14])
		}
	}); err != nil {
		return imported, err
	}
	return imported, nil
}
