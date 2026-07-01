// SPDX-License-Identifier: GPL-3.0-or-later

package device

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SettingsConfig is the human-readable config that drives both the
// USB-exported MYSETTING/MYSETTING2/DJMMYSETTING/DEVSETTING files and the
// runtime bytes served to CDJs over the network.
//
// Schema defaults match what rekordbox writes to fresh USBs. Users
// edit the YAML on disk; CDJ-pushed changes (via 0x37/0x48 protocol
// packets) are decoded back into this struct and persisted, so changes
// made from the CDJ UI survive restarts.
type SettingsConfig struct {
	MySetting    MySettingFields    `yaml:"my_setting" json:"my_setting"`
	MySetting2   MySetting2Fields   `yaml:"my_setting_2" json:"my_setting_2"`
	DjmMySetting DjmMySettingFields `yaml:"djm_my_setting" json:"djm_my_setting"`
	DevSetting   DevSettingFields   `yaml:"dev_setting" json:"dev_setting"`

	// TrackDetail is rekordbox-virtual (we use it for protocol-level
	// behaviour, not a CDJ-pushed setting). One of: artist, bpm, key,
	// album, genre, label, bitrate, time, rating, color, comments,
	// original_artist, remixer, dj_play_count, date_added, none.
	TrackDetail string `yaml:"track_detail,omitempty" json:"track_detail,omitempty"`
}

// MySettingFields backs MYSETTING.DAT (player UI settings).
type MySettingFields struct {
	OnAirDisplay       string `yaml:"on_air_display" json:"on_air_display"`        // on, off
	LCDBrightness      string `yaml:"lcd_brightness" json:"lcd_brightness"`        // one..five
	Quantize           string `yaml:"quantize" json:"quantize"`              // on, off
	AutoCueLevel       string `yaml:"auto_cue_level" json:"auto_cue_level"`        // minus_36db..minus_78db, memory
	Language           string `yaml:"language" json:"language"`              // english, french, ...
	JogRingBrightness  string `yaml:"jog_ring_brightness" json:"jog_ring_brightness"`   // off, dark, bright
	JogRingIndicator   string `yaml:"jog_ring_indicator" json:"jog_ring_indicator"`    // on, off
	SlipFlashing       string `yaml:"slip_flashing" json:"slip_flashing"`         // on, off
	DiscSlotIllumin    string `yaml:"disc_slot_illumination" json:"disc_slot_illumination"`// off, dark, bright
	EjectLock          string `yaml:"eject_lock" json:"eject_lock"`            // unlock, lock
	Sync               string `yaml:"sync" json:"sync"`                  // off, on
	PlayMode           string `yaml:"play_mode" json:"play_mode"`             // continue, single
	QuantizeBeatValue  string `yaml:"quantize_beat_value" json:"quantize_beat_value"`   // one, half, quarter, eighth
	HotcueAutoload     string `yaml:"hotcue_autoload" json:"hotcue_autoload"`       // off, on, rekordbox_setting
	HotcueColor        string `yaml:"hotcue_color" json:"hotcue_color"`          // off, on
	NeedleLock         string `yaml:"needle_lock" json:"needle_lock"`           // unlock, lock
	TimeMode           string `yaml:"time_mode" json:"time_mode"`             // elapsed, remain
	JogMode            string `yaml:"jog_mode" json:"jog_mode"`              // cdj, vinyl
	AutoCue            string `yaml:"auto_cue" json:"auto_cue"`              // off, on
	MasterTempo        string `yaml:"master_tempo" json:"master_tempo"`          // off, on
	TempoRange         string `yaml:"tempo_range" json:"tempo_range"`           // six, ten, sixteen, wide
	PhaseMeter         string `yaml:"phase_meter" json:"phase_meter"`           // type1, type2
}

// MySetting2Fields backs MYSETTING2.DAT.
type MySetting2Fields struct {
	VinylSpeedAdjust    string `yaml:"vinyl_speed_adjust" json:"vinyl_speed_adjust"`     // touch_release, touch, release
	JogDisplayMode      string `yaml:"jog_display_mode" json:"jog_display_mode"`       // auto, info, simple, artwork
	PadButtonBrightness string `yaml:"pad_button_brightness" json:"pad_button_brightness"`  // one..four
	JogLCDBrightness    string `yaml:"jog_lcd_brightness" json:"jog_lcd_brightness"`     // one..five
	WaveformDivisions   string `yaml:"waveform_divisions" json:"waveform_divisions"`     // time_scale, phrase
	Waveform            string `yaml:"waveform" json:"waveform"`               // waveform, phase_meter
	BeatJumpBeatValue   string `yaml:"beat_jump_beat_value" json:"beat_jump_beat_value"`   // half..sixty_four
}

// DjmMySettingFields backs DJMMYSETTING.DAT.
type DjmMySettingFields struct {
	ChannelFaderCurve     string `yaml:"channel_fader_curve" json:"channel_fader_curve"`       // steep_top, linear, steep_bottom
	CrossFaderCurve       string `yaml:"cross_fader_curve" json:"cross_fader_curve"`         // constant, slow_cut, fast_cut
	HeadphonesPreEQ       string `yaml:"headphones_pre_eq" json:"headphones_pre_eq"`         // post_eq, pre_eq
	HeadphonesMonoSplit   string `yaml:"headphones_mono_split" json:"headphones_mono_split"`     // stereo, mono_split
	BeatFXQuantize        string `yaml:"beat_fx_quantize" json:"beat_fx_quantize"`          // off, on
	MicLowCut             string `yaml:"mic_low_cut" json:"mic_low_cut"`               // off, on
	TalkOverMode          string `yaml:"talk_over_mode" json:"talk_over_mode"`            // advanced, normal
	TalkOverLevel         string `yaml:"talk_over_level" json:"talk_over_level"`           // minus_24db..minus_6db
	MidiChannel           string `yaml:"midi_channel" json:"midi_channel"`              // one..sixteen
	MidiButtonType        string `yaml:"midi_button_type" json:"midi_button_type"`          // toggle, trigger
	DisplayBrightness     string `yaml:"display_brightness" json:"display_brightness"`        // white, one..five
	IndicatorBrightness   string `yaml:"indicator_brightness" json:"indicator_brightness"`      // one..three
	ChannelFaderCurveLong string `yaml:"channel_fader_curve_long" json:"channel_fader_curve_long"`  // exponential, smooth, linear
}

// DevSettingFields backs DEVSETTING (served separately, 6 bytes on wire).
// byte 0 and byte 3 are protocol-required constants (0x01); only the
// 4 user-facing bytes are exposed here.
type DevSettingFields struct {
	OverviewType   string `yaml:"overview_type" json:"overview_type"`    // half, full
	WaveformColor  string `yaml:"waveform_color" json:"waveform_color"`   // blue, rgb, 3band
	KeyDisplay     string `yaml:"key_display" json:"key_display"`      // classic, alphanumeric
	WavePosition   string `yaml:"wave_position" json:"wave_position"`    // center, left
}

// ---------- enum tables (value -> byte) ----------
// All CDJ-side settings use bytes 0x80+ as enum slots. Unknown bytes
// fall through to a "0x<hex>" string representation so round-trip
// preserves arbitrary user/CDJ writes.

type enumMap struct {
	toByte map[string]byte
	toName map[byte]string
}

func mkEnum(pairs ...interface{}) enumMap {
	e := enumMap{toByte: map[string]byte{}, toName: map[byte]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		name := pairs[i].(string)
		b := byte(pairs[i+1].(int))
		e.toByte[name] = b
		e.toName[b] = name
	}
	return e
}

func (e enumMap) encode(name string, def byte) byte {
	if name == "" {
		return def
	}
	if b, ok := e.toByte[name]; ok {
		return b
	}
	// Allow "0x<hex>" fall-through for forward compatibility.
	var b byte
	if n, _ := fmt.Sscanf(name, "0x%x", &b); n == 1 {
		return b
	}
	log.Printf("settings: unknown value %q (using default 0x%02x)", name, def)
	return def
}

func (e enumMap) decode(b byte) string {
	if name, ok := e.toName[b]; ok {
		return name
	}
	return fmt.Sprintf("0x%02x", b)
}

// The setting field names, enum orderings, and byte values below follow the
// documented MYSETTING/MYSETTING2/DJMMYSETTING/DEVSETTING file-format schema
// (Deep Symmetry's Kaitai `rekordbox_mysetting.ksy` and pyrekordbox). The
// on-the-wire settings *handshake* (0x35/0x36/0x4a/0x46/0x47) was worked out
// from our own captures; this per-field schema is transcribed from that
// documented format.
var (
	onOff           = mkEnum("off", 0x80, "on", 0x81)
	lockUnlock      = mkEnum("unlock", 0x80, "lock", 0x81)
	offDarkBright   = mkEnum("off", 0x80, "dark", 0x81, "bright", 0x82)
	oneToFive       = mkEnum("one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84, "five", 0x85)
	oneToFour       = mkEnum("one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84)
	oneToThree      = mkEnum("one", 0x81, "two", 0x82, "three", 0x83)
	autoCueLevel    = mkEnum("minus_36db", 0x80, "minus_42db", 0x81, "minus_48db", 0x82,
		"minus_54db", 0x83, "minus_60db", 0x84, "minus_66db", 0x85,
		"minus_72db", 0x86, "minus_78db", 0x87, "memory", 0x88)
	language = mkEnum("english", 0x81, "french", 0x82, "german", 0x83, "italian", 0x84,
		"dutch", 0x85, "spanish", 0x86, "russian", 0x87, "korean", 0x88,
		"chinese_simplified", 0x89, "chinese_traditional", 0x8a, "japanese", 0x8b,
		"portuguese", 0x8c, "swedish", 0x8d, "czech", 0x8e, "hungarian", 0x8f,
		"danish", 0x90, "greek", 0x91, "turkish", 0x92)
	playMode          = mkEnum("continue", 0x80, "single", 0x81)
	quantizeBeatValue = mkEnum("one", 0x80, "half", 0x81, "quarter", 0x82, "eighth", 0x83)
	hotcueAutoload    = mkEnum("off", 0x80, "on", 0x81, "rekordbox_setting", 0x82)
	timeMode          = mkEnum("elapsed", 0x80, "remain", 0x81)
	jogMode           = mkEnum("cdj", 0x80, "vinyl", 0x81)
	tempoRange        = mkEnum("six", 0x80, "ten", 0x81, "sixteen", 0x82, "wide", 0x83)
	phaseMeter        = mkEnum("type1", 0x80, "type2", 0x81)
	vinylSpeedAdjust  = mkEnum("touch_release", 0x80, "touch", 0x81, "release", 0x82)
	jogDisplayMode    = mkEnum("auto", 0x80, "info", 0x81, "simple", 0x82, "artwork", 0x83)
	waveformDivisions = mkEnum("time_scale", 0x80, "phrase", 0x81)
	waveform          = mkEnum("waveform", 0x80, "phase_meter", 0x81)
	beatJumpBeatValue = mkEnum("half", 0x80, "one", 0x81, "two", 0x82, "four", 0x83,
		"eight", 0x84, "sixteen", 0x85, "thirty_two", 0x86, "sixty_four", 0x87)
	channelFaderCurve = mkEnum("steep_top", 0x80, "linear", 0x81, "steep_bottom", 0x82)
	crossFaderCurve   = mkEnum("constant", 0x80, "slow_cut", 0x81, "fast_cut", 0x82)
	preOrPostEQ       = mkEnum("post_eq", 0x80, "pre_eq", 0x81)
	stereoMono        = mkEnum("stereo", 0x80, "mono_split", 0x81)
	talkOverMode      = mkEnum("advanced", 0x80, "normal", 0x81)
	talkOverLevel     = mkEnum("minus_24db", 0x80, "minus_18db", 0x81, "minus_12db", 0x82, "minus_6db", 0x83)
	midiChannel       = mkEnum("one", 0x80, "two", 0x81, "three", 0x82, "four", 0x83,
		"five", 0x84, "six", 0x85, "seven", 0x86, "eight", 0x87,
		"nine", 0x88, "ten", 0x89, "eleven", 0x8a, "twelve", 0x8b,
		"thirteen", 0x8c, "fourteen", 0x8d, "fifteen", 0x8e, "sixteen", 0x8f)
	midiButtonType         = mkEnum("toggle", 0x80, "trigger", 0x81)
	displayBrightness      = mkEnum("white", 0x80, "one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84, "five", 0x85)
	channelFaderCurveLong  = mkEnum("exponential", 0x80, "smooth", 0x81, "linear", 0x82)
	overviewType           = mkEnum("half", 0x01, "full", 0x02)
	waveformColor          = mkEnum("blue", 0x01, "rgb", 0x03, "3band", 0x04)
	keyDisplay             = mkEnum("classic", 0x01, "alphanumeric", 0x02)
	wavePosition           = mkEnum("center", 0x01, "left", 0x02)
)

// SettingsEnums returns the valid string values for every editable
// settings field, keyed by the YAML field name. The web UI uses this
// to build <select> dropdowns. Stable order — entries within each enum
// preserve the encoded byte order (0x80, 0x81, …).
func SettingsEnums() map[string][]string {
	ordered := func(pairs ...interface{}) []string {
		out := make([]string, 0, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, pairs[i].(string))
		}
		return out
	}
	return map[string][]string{
		// MYSETTING
		"on_air_display":         ordered("off", 0x80, "on", 0x81),
		"lcd_brightness":         ordered("one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84, "five", 0x85),
		"quantize":               ordered("off", 0x80, "on", 0x81),
		"auto_cue_level":         ordered("minus_36db", 0x80, "minus_42db", 0x81, "minus_48db", 0x82, "minus_54db", 0x83, "minus_60db", 0x84, "minus_66db", 0x85, "minus_72db", 0x86, "minus_78db", 0x87, "memory", 0x88),
		"language":               ordered("english", 0x81, "french", 0x82, "german", 0x83, "italian", 0x84, "dutch", 0x85, "spanish", 0x86, "russian", 0x87, "korean", 0x88, "chinese_simplified", 0x89, "chinese_traditional", 0x8a, "japanese", 0x8b, "portuguese", 0x8c, "swedish", 0x8d, "czech", 0x8e, "hungarian", 0x8f, "danish", 0x90, "greek", 0x91, "turkish", 0x92),
		"jog_ring_brightness":    ordered("off", 0x80, "dark", 0x81, "bright", 0x82),
		"jog_ring_indicator":     ordered("off", 0x80, "on", 0x81),
		"slip_flashing":          ordered("off", 0x80, "on", 0x81),
		"disc_slot_illumination": ordered("off", 0x80, "dark", 0x81, "bright", 0x82),
		"eject_lock":             ordered("unlock", 0x80, "lock", 0x81),
		"sync":                   ordered("off", 0x80, "on", 0x81),
		"play_mode":              ordered("continue", 0x80, "single", 0x81),
		"quantize_beat_value":    ordered("one", 0x80, "half", 0x81, "quarter", 0x82, "eighth", 0x83),
		"hotcue_autoload":        ordered("off", 0x80, "on", 0x81, "rekordbox_setting", 0x82),
		"hotcue_color":           ordered("off", 0x80, "on", 0x81),
		"needle_lock":            ordered("unlock", 0x80, "lock", 0x81),
		"time_mode":              ordered("elapsed", 0x80, "remain", 0x81),
		"jog_mode":               ordered("cdj", 0x80, "vinyl", 0x81),
		"auto_cue":               ordered("off", 0x80, "on", 0x81),
		"master_tempo":           ordered("off", 0x80, "on", 0x81),
		"tempo_range":            ordered("six", 0x80, "ten", 0x81, "sixteen", 0x82, "wide", 0x83),
		"phase_meter":            ordered("type1", 0x80, "type2", 0x81),
		// MYSETTING2
		"vinyl_speed_adjust":    ordered("touch_release", 0x80, "touch", 0x81, "release", 0x82),
		"jog_display_mode":      ordered("auto", 0x80, "info", 0x81, "simple", 0x82, "artwork", 0x83),
		"pad_button_brightness": ordered("one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84),
		"jog_lcd_brightness":    ordered("one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84, "five", 0x85),
		"waveform_divisions":    ordered("time_scale", 0x80, "phrase", 0x81),
		"waveform":              ordered("waveform", 0x80, "phase_meter", 0x81),
		"beat_jump_beat_value":  ordered("half", 0x80, "one", 0x81, "two", 0x82, "four", 0x83, "eight", 0x84, "sixteen", 0x85, "thirty_two", 0x86, "sixty_four", 0x87),
		// DJMMYSETTING
		"channel_fader_curve":      ordered("steep_top", 0x80, "linear", 0x81, "steep_bottom", 0x82),
		"cross_fader_curve":        ordered("constant", 0x80, "slow_cut", 0x81, "fast_cut", 0x82),
		"headphones_pre_eq":        ordered("post_eq", 0x80, "pre_eq", 0x81),
		"headphones_mono_split":    ordered("stereo", 0x80, "mono_split", 0x81),
		"beat_fx_quantize":         ordered("off", 0x80, "on", 0x81),
		"mic_low_cut":              ordered("off", 0x80, "on", 0x81),
		"talk_over_mode":           ordered("advanced", 0x80, "normal", 0x81),
		"talk_over_level":          ordered("minus_24db", 0x80, "minus_18db", 0x81, "minus_12db", 0x82, "minus_6db", 0x83),
		"midi_channel":             ordered("one", 0x80, "two", 0x81, "three", 0x82, "four", 0x83, "five", 0x84, "six", 0x85, "seven", 0x86, "eight", 0x87, "nine", 0x88, "ten", 0x89, "eleven", 0x8a, "twelve", 0x8b, "thirteen", 0x8c, "fourteen", 0x8d, "fifteen", 0x8e, "sixteen", 0x8f),
		"midi_button_type":         ordered("toggle", 0x80, "trigger", 0x81),
		"display_brightness":       ordered("white", 0x80, "one", 0x81, "two", 0x82, "three", 0x83, "four", 0x84, "five", 0x85),
		"indicator_brightness":     ordered("one", 0x81, "two", 0x82, "three", 0x83),
		"channel_fader_curve_long": ordered("exponential", 0x80, "smooth", 0x81, "linear", 0x82),
		// DEVSETTING
		"overview_type":  ordered("half", 0x01, "full", 0x02),
		"waveform_color": ordered("blue", 0x01, "rgb", 0x03, "3band", 0x04),
		"key_display":    ordered("classic", 0x01, "alphanumeric", 0x02),
		"wave_position":  ordered("center", 0x01, "left", 0x02),
	}
}

// ---------- defaults ----------

// DefaultSettingsConfig returns sensible defaults matching what real
// rekordbox writes to fresh USB exports.
func DefaultSettingsConfig() SettingsConfig {
	return SettingsConfig{
		MySetting: MySettingFields{
			OnAirDisplay:      "on",
			LCDBrightness:     "three",
			Quantize:          "on",
			AutoCueLevel:      "memory",
			Language:          "english",
			JogRingBrightness: "bright",
			JogRingIndicator:  "on",
			SlipFlashing:      "on",
			DiscSlotIllumin:   "bright",
			EjectLock:         "unlock",
			Sync:              "off",
			PlayMode:          "single",
			QuantizeBeatValue: "one",
			HotcueAutoload:    "on",
			HotcueColor:       "off",
			NeedleLock:        "lock",
			TimeMode:          "remain",
			JogMode:           "vinyl",
			AutoCue:           "on",
			MasterTempo:       "off",
			TempoRange:        "ten",
			PhaseMeter:        "type1",
		},
		MySetting2: MySetting2Fields{
			VinylSpeedAdjust:    "touch",
			JogDisplayMode:      "auto",
			PadButtonBrightness: "three",
			JogLCDBrightness:    "three",
			WaveformDivisions:   "phrase",
			Waveform:            "waveform",
			BeatJumpBeatValue:   "sixteen",
		},
		DjmMySetting: DjmMySettingFields{
			ChannelFaderCurve:     "linear",
			CrossFaderCurve:       "fast_cut",
			HeadphonesPreEQ:       "post_eq",
			HeadphonesMonoSplit:   "stereo",
			BeatFXQuantize:        "on",
			MicLowCut:             "on",
			TalkOverMode:          "advanced",
			TalkOverLevel:         "minus_18db",
			MidiChannel:           "one",
			MidiButtonType:        "toggle",
			DisplayBrightness:     "five",
			IndicatorBrightness:   "three",
			ChannelFaderCurveLong: "exponential",
		},
		DevSetting: DevSettingFields{
			OverviewType:  "full",
			WaveformColor: "rgb",
			KeyDisplay:    "alphanumeric",
			WavePosition:  "center",
		},
		TrackDetail: "artist",
	}
}

// ---------- encoding ----------

// EncodeMySetting produces the 40-byte body for MYSETTING.DAT.
func (m MySettingFields) Encode() []byte {
	body := make([]byte, 40)
	// magic header
	copy(body[0:8], []byte{0x78, 0x56, 0x34, 0x12, 0x02, 0x00, 0x00, 0x00})
	body[8] = onOff.encode(m.OnAirDisplay, 0x81)
	body[9] = oneToFive.encode(m.LCDBrightness, 0x83)
	body[10] = onOff.encode(m.Quantize, 0x81)
	body[11] = autoCueLevel.encode(m.AutoCueLevel, 0x88)
	body[12] = language.encode(m.Language, 0x81)
	body[13] = 0x01 // u2
	body[14] = offDarkBright.encode(m.JogRingBrightness, 0x82)
	body[15] = onOff.encode(m.JogRingIndicator, 0x81)
	body[16] = onOff.encode(m.SlipFlashing, 0x81)
	body[17], body[18], body[19] = 0x01, 0x01, 0x01 // u3
	body[20] = offDarkBright.encode(m.DiscSlotIllumin, 0x82)
	body[21] = lockUnlock.encode(m.EjectLock, 0x80)
	body[22] = onOff.encode(m.Sync, 0x80)
	body[23] = playMode.encode(m.PlayMode, 0x81)
	body[24] = quantizeBeatValue.encode(m.QuantizeBeatValue, 0x80)
	body[25] = hotcueAutoload.encode(m.HotcueAutoload, 0x81)
	body[26] = onOff.encode(m.HotcueColor, 0x80)
	// 27-28: u4 (left 0x00)
	body[29] = lockUnlock.encode(m.NeedleLock, 0x81)
	// 30-31: u5
	body[32] = timeMode.encode(m.TimeMode, 0x81)
	body[33] = jogMode.encode(m.JogMode, 0x81)
	body[34] = onOff.encode(m.AutoCue, 0x81)
	body[35] = onOff.encode(m.MasterTempo, 0x80)
	body[36] = tempoRange.encode(m.TempoRange, 0x81)
	body[37] = phaseMeter.encode(m.PhaseMeter, 0x80)
	// 38-39: u6
	return body
}

// DecodeMySetting parses MYSETTING bytes back into structured fields.
// Accepts either the 40-byte .DAT-format body (with 8-byte magic header
// prefix) or the 32-byte wire-format body (just fields, magic lives in
// the surrounding packet at offset 0x28). Detects which by length and
// adjusts the field offset.
func DecodeMySetting(body []byte) MySettingFields {
	var off int // 8 for .DAT format, 0 for wire format
	switch {
	case len(body) >= 38:
		off = 8 // .DAT body has 8-byte magic prefix before fields
	case len(body) >= 30:
		off = 0 // wire body starts directly with fields
	default:
		return MySettingFields{}
	}
	return MySettingFields{
		OnAirDisplay:      onOff.decode(body[off+0]),
		LCDBrightness:     oneToFive.decode(body[off+1]),
		Quantize:          onOff.decode(body[off+2]),
		AutoCueLevel:      autoCueLevel.decode(body[off+3]),
		Language:          language.decode(body[off+4]),
		JogRingBrightness: offDarkBright.decode(body[off+6]),
		JogRingIndicator:  onOff.decode(body[off+7]),
		SlipFlashing:      onOff.decode(body[off+8]),
		DiscSlotIllumin:   offDarkBright.decode(body[off+12]),
		EjectLock:         lockUnlock.decode(body[off+13]),
		Sync:              onOff.decode(body[off+14]),
		PlayMode:          playMode.decode(body[off+15]),
		QuantizeBeatValue: quantizeBeatValue.decode(body[off+16]),
		HotcueAutoload:    hotcueAutoload.decode(body[off+17]),
		HotcueColor:       onOff.decode(body[off+18]),
		NeedleLock:        lockUnlock.decode(body[off+21]),
		TimeMode:          timeMode.decode(body[off+24]),
		JogMode:           jogMode.decode(body[off+25]),
		AutoCue:           onOff.decode(body[off+26]),
		MasterTempo:       onOff.decode(body[off+27]),
		TempoRange:        tempoRange.decode(body[off+28]),
		PhaseMeter:        phaseMeter.decode(body[off+29]),
	}
}

// EncodeMySetting2 produces the 40-byte body for MYSETTING2.DAT.
func (m MySetting2Fields) Encode() []byte {
	body := make([]byte, 40)
	body[0] = vinylSpeedAdjust.encode(m.VinylSpeedAdjust, 0x81)
	body[1] = jogDisplayMode.encode(m.JogDisplayMode, 0x80)
	body[2] = oneToFour.encode(m.PadButtonBrightness, 0x83)
	body[3] = oneToFive.encode(m.JogLCDBrightness, 0x83)
	body[4] = waveformDivisions.encode(m.WaveformDivisions, 0x81)
	// 5-9: padding
	body[10] = waveform.encode(m.Waveform, 0x80)
	body[11] = 0x81 // u2
	body[12] = beatJumpBeatValue.encode(m.BeatJumpBeatValue, 0x85)
	// 13-39: padding
	return body
}

// DecodeMySetting2 parses bytes back into structured fields.
func DecodeMySetting2(body []byte) MySetting2Fields {
	if len(body) < 13 {
		return MySetting2Fields{}
	}
	return MySetting2Fields{
		VinylSpeedAdjust:    vinylSpeedAdjust.decode(body[0]),
		JogDisplayMode:      jogDisplayMode.decode(body[1]),
		PadButtonBrightness: oneToFour.decode(body[2]),
		JogLCDBrightness:    oneToFive.decode(body[3]),
		WaveformDivisions:   waveformDivisions.decode(body[4]),
		Waveform:            waveform.decode(body[10]),
		BeatJumpBeatValue:   beatJumpBeatValue.decode(body[12]),
	}
}

// EncodeDjmMySetting produces the 52-byte body for DJMMYSETTING.DAT.
func (m DjmMySettingFields) Encode() []byte {
	body := make([]byte, 52)
	// magic header (12 bytes)
	copy(body[0:12], []byte{
		0x78, 0x56, 0x34, 0x12, 0x01, 0x00, 0x00, 0x00,
		0x20, 0x00, 0x00, 0x00,
	})
	body[12] = channelFaderCurve.encode(m.ChannelFaderCurve, 0x81)
	body[13] = crossFaderCurve.encode(m.CrossFaderCurve, 0x82)
	body[14] = preOrPostEQ.encode(m.HeadphonesPreEQ, 0x80)
	body[15] = stereoMono.encode(m.HeadphonesMonoSplit, 0x80)
	body[16] = onOff.encode(m.BeatFXQuantize, 0x81)
	body[17] = onOff.encode(m.MicLowCut, 0x81)
	body[18] = talkOverMode.encode(m.TalkOverMode, 0x80)
	body[19] = talkOverLevel.encode(m.TalkOverLevel, 0x81)
	body[20] = midiChannel.encode(m.MidiChannel, 0x80)
	body[21] = midiButtonType.encode(m.MidiButtonType, 0x80)
	body[22] = displayBrightness.encode(m.DisplayBrightness, 0x85)
	body[23] = oneToThree.encode(m.IndicatorBrightness, 0x82)
	body[24] = channelFaderCurveLong.encode(m.ChannelFaderCurveLong, 0x80)
	// 25-51: padding
	return body
}

// DecodeDjmMySetting parses bytes back into structured fields.
func DecodeDjmMySetting(body []byte) DjmMySettingFields {
	if len(body) < 25 {
		return DjmMySettingFields{}
	}
	return DjmMySettingFields{
		ChannelFaderCurve:     channelFaderCurve.decode(body[12]),
		CrossFaderCurve:       crossFaderCurve.decode(body[13]),
		HeadphonesPreEQ:       preOrPostEQ.decode(body[14]),
		HeadphonesMonoSplit:   stereoMono.decode(body[15]),
		BeatFXQuantize:        onOff.decode(body[16]),
		MicLowCut:             onOff.decode(body[17]),
		TalkOverMode:          talkOverMode.decode(body[18]),
		TalkOverLevel:         talkOverLevel.decode(body[19]),
		MidiChannel:           midiChannel.decode(body[20]),
		MidiButtonType:        midiButtonType.decode(body[21]),
		DisplayBrightness:     displayBrightness.decode(body[22]),
		IndicatorBrightness:   oneToThree.decode(body[23]),
		ChannelFaderCurveLong: channelFaderCurveLong.decode(body[24]),
	}
}

// EncodeDevSetting produces the 6-byte body for DEVSETTING (served on
// 0x46/0x47 protocol cycles). Byte 0 and 3 are protocol-required 0x01.
func (d DevSettingFields) Encode() []byte {
	body := make([]byte, 6)
	body[0] = 0x01
	body[1] = overviewType.encode(d.OverviewType, 0x02)
	body[2] = waveformColor.encode(d.WaveformColor, 0x03)
	body[3] = 0x01
	body[4] = keyDisplay.encode(d.KeyDisplay, 0x02)
	body[5] = wavePosition.encode(d.WavePosition, 0x01)
	return body
}

// EncodeDevSettingDat produces the 32-byte body for DEVSETTING.DAT (the
// USB-export form). Layout: 8-byte magic header (78 56 34 12 01 00 00 00)
// + 6-byte DEVSETTING fields + 18-byte zero padding. The wire form sent
// in 0x47/0x48 packets is just the 6 middle bytes — use Encode() for that.
func (d DevSettingFields) EncodeDat() []byte {
	body := make([]byte, 32)
	copy(body[0:8], []byte{0x78, 0x56, 0x34, 0x12, 0x01, 0x00, 0x00, 0x00})
	wire := d.Encode() // 6 bytes
	copy(body[8:14], wire)
	// 14-31: zero padding
	return body
}

// DecodeDevSetting parses the 6-byte wire body back into structured fields.
func DecodeDevSetting(body []byte) DevSettingFields {
	if len(body) < 6 {
		return DevSettingFields{}
	}
	return DevSettingFields{
		OverviewType:  overviewType.decode(body[1]),
		WaveformColor: waveformColor.decode(body[2]),
		KeyDisplay:    keyDisplay.decode(body[4]),
		WavePosition:  wavePosition.decode(body[5]),
	}
}

// ---------- JSON load/save ----------

// LoadConfig reads a SettingsConfig from path (expected to be JSON).
// If the file doesn't exist, writes a default config there and returns
// it. Missing fields in an existing file fall back to defaults — new
// schema fields don't break older config files.
//
// Migration: if path ends in ".json" and the file doesn't exist but a
// legacy ".yaml" sibling does, the YAML is loaded and re-saved as
// JSON. The original YAML is left in place as a backup.
func LoadConfig(path string) (SettingsConfig, error) {
	cfg := DefaultSettingsConfig()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Try migrating from a legacy YAML at the sibling path.
		if migrated, ok := tryMigrateYAML(path); ok {
			return migrated, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return cfg, err
		}
		if werr := writeConfigJSON(path, cfg); werr != nil {
			log.Printf("settings: failed to write default %s: %v", path, werr)
		} else {
			log.Printf("settings: wrote default config to %s", path)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig writes the config back to path as indented JSON. Used
// after CDJ pushes new bytes so changes made via the CDJ UI survive
// restarts.
func SaveConfig(path string, cfg SettingsConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeConfigJSON(path, cfg)
}

func writeConfigJSON(path string, cfg SettingsConfig) error {
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// tryMigrateYAML looks for a legacy settings.yaml next to path (when
// path itself is settings.json). If found, it parses the YAML and
// writes it as JSON at path, returning the parsed config. Leaves the
// YAML on disk as a backup so a downgrade still works.
func tryMigrateYAML(jsonPath string) (SettingsConfig, bool) {
	cfg := DefaultSettingsConfig()
	if filepath.Ext(jsonPath) != ".json" {
		return cfg, false
	}
	yamlPath := jsonPath[:len(jsonPath)-len(".json")] + ".yaml"
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return cfg, false
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Printf("settings: legacy %s found but unparseable: %v", yamlPath, err)
		return cfg, false
	}
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0o755); err != nil {
		return cfg, false
	}
	if err := writeConfigJSON(jsonPath, cfg); err != nil {
		log.Printf("settings: migration write %s: %v", jsonPath, err)
		return cfg, false
	}
	log.Printf("settings: migrated %s -> %s (YAML kept as backup)", yamlPath, jsonPath)
	return cfg, true
}
