// SPDX-License-Identifier: GPL-3.0-or-later

package prolink

import (
	"testing"

	"github.com/vynulldev/vynull/analysis"
)

// goldenBeats is a deterministic 32-beat grid at 120 BPM (500 ms apart), used
// to lock the beat-grid / phrase blob encoders independently of the detector.
func goldenBeats() []float64 {
	b := make([]float64, 32)
	for i := range b {
		b[i] = float64(i) * 500
	}
	return b
}

func TestPQT2GoldenHash(t *testing.T) {
	checkGolden(t, "PQT2 (beat grid, 0x2c04)",
		GeneratePQT2(120, goldenBeats(), 0),
		"056e1cb0129614b1746adc9ae3696ee49c778f6a24510078aea746b3bb55ce3b")
}

func TestBeatGridGoldenHash(t *testing.T) {
	checkGolden(t, "beat grid (0x2204)",
		analysis.GenerateBeatGrid(120, 60000, 0),
		"6ab4b71bf3643d6d1be9f3ff6659e85fddf950365e1dcec650980743ca86e620")
}

func TestBeatGridFromBeatsGoldenHash(t *testing.T) {
	checkGolden(t, "beat grid from beats",
		analysis.GenerateBeatGridFromBeats(&analysis.BeatResult{BPM: 120, Beats: goldenBeats(), Downbeat: 0}),
		"8a9ba25e847b08ae25b12dde6d22c50053761cb5d757cd5a2c3b6892dcbde41e")
}

func TestPSSIGoldenHash(t *testing.T) {
	phrases := []analysis.Phrase{
		{StartBeat: 1, EndBeat: 16, Kind: 1, StartMs: 0, EndMs: 8000},
		{StartBeat: 17, EndBeat: 32, Kind: 5, StartMs: 8000, EndMs: 16000},
	}
	checkGolden(t, "PSSI (song structure)",
		GeneratePSSI(phrases, 120),
		"b919eb9fd2a5cb5e32e7cebdf3f53d48794effacaeec2964a3526a80011d31a9")
}

func TestPVB2GoldenHash(t *testing.T) {
	checkGolden(t, "PVB2", GeneratePVB2(),
		"28a54fd044a18b6041d33ac63e8dcc2722ce7d9df7a5c24f70b28f975654aac8")
}

func TestPVBRGoldenHash(t *testing.T) {
	checkGolden(t, "PVBR", GeneratePVBR(1000000),
		"229cfc7bde86ad0a4c49541930d5a411533342397107c7ca844616d41daac7f1")
}
