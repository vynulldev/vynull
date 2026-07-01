// SPDX-License-Identifier: GPL-3.0-or-later

// gen-test-tones produces stereo 44.1 kHz WAV files for reverse-engineering
// rekordbox's PWV4/PWV5 encoders. Import the generated WAV into real
// rekordbox, dump the resulting .EXT via `rekordbox --anlz <ext> --anlz-csv`,
// and compare against our V2 encoder output for the same file.
//
// Modes:
//
//	tones  (default) — sequence of pure sine tones at a fixed amplitude,
//	                   each held for a few seconds. Use this to plot real's
//	                   per-band response per frequency and fit the filter shape.
//
//	ramp           — single frequency at stepped amplitudes (-40 to 0 dBFS).
//	                 Characterises d1 (luminance envelope) compression curve.
//
//	clicks         — sparse single-sample impulses at -18 dBFS. Tests whether
//	                 d0 in rekordbox is a transient detector by looking
//	                 for d0 spikes that align with the click positions.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

const (
	sampleRate    = 44100
	channels      = 2
	bitsPerSample = 16
)

var (
	defaultFreqs = []float64{
		100, 150, 200, 300, 400, 500, 600, 800, 1000,
		1200, 1500, 1800, 2000, 2500, 3000, 4000, 6000, 10000, 15000,
	}
	defaultAmpsDB = []float64{-40, -30, -24, -18, -12, -6, -3, 0}
)

func main() {
	mode := flag.String("mode", "tones", "tones | ramp | clicks")
	output := flag.String("o", "", "output WAV (default: <mode>.wav)")
	dur := flag.Float64("dur", 3.0, "seconds per tone (tones/ramp modes)")
	ampDB := flag.Float64("db", -18.0, "amplitude in dBFS (tones/clicks modes)")
	freqsArg := flag.String("freqs", "", "comma-separated frequencies in Hz (tones mode, overrides default 19-tone set)")
	rampFreq := flag.Float64("freq", 500.0, "frequency for ramp mode")
	clicksDur := flag.Float64("total", 30.0, "total duration for clicks mode (seconds)")
	clicksInterval := flag.Float64("interval", 0.2, "click interval for clicks mode (seconds)")
	flag.Parse()

	if *output == "" {
		*output = *mode + ".wav"
	}

	var samples []int16
	switch *mode {
	case "tones":
		freqs := defaultFreqs
		if *freqsArg != "" {
			freqs = nil
			for _, s := range strings.Split(*freqsArg, ",") {
				f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
				if err != nil {
					die("bad --freqs entry %q: %v", s, err)
				}
				freqs = append(freqs, f)
			}
		}
		amp := dBToAmplitude(*ampDB)
		samples = generateTones(freqs, *dur, amp)
		fmt.Printf("tones: %d frequencies × %.1fs at %.1f dBFS = %.1fs total\n",
			len(freqs), *dur, *ampDB, float64(len(freqs))*(*dur))
		fmt.Printf("frequencies: ")
		for i, f := range freqs {
			if i > 0 {
				fmt.Printf(", ")
			}
			fmt.Printf("%.0f", f)
		}
		fmt.Println()

	case "ramp":
		samples = generateAmpRamp(*rampFreq, *dur, defaultAmpsDB)
		fmt.Printf("ramp: %.0f Hz at %d amplitudes × %.1fs = %.1fs total\n",
			*rampFreq, len(defaultAmpsDB), *dur, float64(len(defaultAmpsDB))*(*dur))
		fmt.Printf("amplitudes (dBFS): ")
		for i, a := range defaultAmpsDB {
			if i > 0 {
				fmt.Printf(", ")
			}
			fmt.Printf("%.0f", a)
		}
		fmt.Println()

	case "clicks":
		amp := dBToAmplitude(*ampDB)
		samples = generateClicks(*clicksDur, *clicksInterval, amp)
		nClicks := int(*clicksDur / *clicksInterval)
		fmt.Printf("clicks: %d single-sample impulses every %.0f ms over %.1fs at %.1f dBFS\n",
			nClicks, *clicksInterval*1000, *clicksDur, *ampDB)

	default:
		die("unknown --mode %q (must be tones|ramp|clicks)", *mode)
	}

	if err := writeWAV(*output, samples); err != nil {
		die("write %s: %v", *output, err)
	}
	durSec := float64(len(samples)/channels) / float64(sampleRate)
	fmt.Printf("wrote %s (%d samples, %.2fs, %d bytes)\n",
		*output, len(samples)/channels, durSec, 44+len(samples)*2)
}

func dBToAmplitude(db float64) float64 {
	return math.Pow(10, db/20)
}

// generateTones emits each frequency held for durSec, interleaved L+R, at given amplitude.
func generateTones(freqs []float64, durSec, amp float64) []int16 {
	perTone := int(float64(sampleRate) * durSec)
	out := make([]int16, 0, perTone*channels*len(freqs))
	for _, f := range freqs {
		for i := 0; i < perTone; i++ {
			v := amp * math.Sin(2*math.Pi*f*float64(i)/float64(sampleRate))
			s := floatToInt16(v)
			out = append(out, s, s) // both channels identical
		}
	}
	return out
}

// generateAmpRamp emits one frequency at each amplitude in turn.
func generateAmpRamp(freq, durSec float64, ampsDB []float64) []int16 {
	perStep := int(float64(sampleRate) * durSec)
	out := make([]int16, 0, perStep*channels*len(ampsDB))
	for _, db := range ampsDB {
		amp := dBToAmplitude(db)
		for i := 0; i < perStep; i++ {
			v := amp * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate))
			s := floatToInt16(v)
			out = append(out, s, s)
		}
	}
	return out
}

// generateClicks emits silence with single-sample impulses every intervalSec.
// Each impulse is a single sample at the given amplitude. d0 in rekordbox
// spikes to 255 sporadically — if clicks line up with d0 spikes, d0 is a
// transient detector.
func generateClicks(totalSec, intervalSec, amp float64) []int16 {
	n := int(float64(sampleRate) * totalSec)
	out := make([]int16, n*channels)
	step := int(float64(sampleRate) * intervalSec)
	for pos := step; pos < n; pos += step {
		s := floatToInt16(amp)
		out[pos*channels] = s
		out[pos*channels+1] = s
	}
	return out
}

func floatToInt16(v float64) int16 {
	if v > 1.0 {
		v = 1.0
	} else if v < -1.0 {
		v = -1.0
	}
	return int16(v * 32767)
}

// writeWAV writes a minimal RIFF/WAVE PCM-16 stereo file.
func writeWAV(path string, samples []int16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dataLen := uint32(len(samples) * 2) // 2 bytes per int16
	byteRate := uint32(sampleRate * channels * bitsPerSample / 8)
	blockAlign := uint16(channels * bitsPerSample / 8)

	// RIFF header
	if _, err := f.Write([]byte("RIFF")); err != nil {
		return err
	}
	binary.Write(f, binary.LittleEndian, uint32(36+dataLen))
	f.Write([]byte("WAVE"))

	// fmt chunk
	f.Write([]byte("fmt "))
	binary.Write(f, binary.LittleEndian, uint32(16))            // chunk size
	binary.Write(f, binary.LittleEndian, uint16(1))             // PCM
	binary.Write(f, binary.LittleEndian, uint16(channels))      // channels
	binary.Write(f, binary.LittleEndian, uint32(sampleRate))    // sample rate
	binary.Write(f, binary.LittleEndian, byteRate)              // byte rate
	binary.Write(f, binary.LittleEndian, blockAlign)            // block align
	binary.Write(f, binary.LittleEndian, uint16(bitsPerSample)) // bits/sample

	// data chunk
	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, dataLen)
	return binary.Write(f, binary.LittleEndian, samples)
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
