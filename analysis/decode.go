// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os/exec"
)

// DecodePCM decodes an audio file to mono float32 samples at the given sample rate.
// Requires ffmpeg on PATH.
func DecodePCM(filePath string, sampleRate int) ([]float32, error) {
	cmd := exec.Command("ffmpeg",
		"-i", filePath,
		"-f", "f32le",
		"-ac", "1",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-loglevel", "error",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start (%s): %w", filePath, err)
	}

	var samples []float32
	buf := make([]byte, 4*8192)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			// Each sample is 4 bytes, little-endian float32.
			for i := 0; i+3 < n; i += 4 {
				bits := binary.LittleEndian.Uint32(buf[i:])
				samples = append(samples, math.Float32frombits(bits))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			cmd.Wait()
			return nil, fmt.Errorf("ffmpeg read: %w", err)
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg (%s): %s: %w", filePath, stderrBuf.String(), err)
	}

	return samples, nil
}
