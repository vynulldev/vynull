// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/vynulldev/vynull/internal/ffmpeg"
)

// DecodePCM decodes an audio file to mono float32 samples at the given sample rate.
// Requires ffmpeg on PATH.
func DecodePCM(filePath string, sampleRate int) ([]float32, error) {
	return DecodePCMContext(context.Background(), filePath, sampleRate)
}

// DecodePCMContext is DecodePCM, killing ffmpeg and returning ctx's error
// when ctx ends first.
func DecodePCMContext(ctx context.Context, filePath string, sampleRate int) ([]float32, error) {
	cmd := ffmpeg.Command(ctx,
		"-i", filePath,
		"-f", "f32le",
		"-ac", "1",
		"-ar", fmt.Sprintf("%d", sampleRate),
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

	// Collect the samples in fixed blocks and copy them once into a slice of
	// the exact length: growing one slice by append allocated about five
	// times the track and briefly held two copies of it.
	const blockSamples = 1 << 20
	var blocks [][]float32
	block := make([]float32, 0, blockSamples)
	total := 0
	buf := make([]byte, 4*8192)
	for {
		// ReadFull fills the whole buffer, a whole number of samples, until
		// the end of the stream; a plain Read could stop mid-sample.
		n, err := io.ReadFull(stdout, buf)
		for i := 0; i+3 < n; i += 4 {
			if len(block) == cap(block) {
				blocks = append(blocks, block)
				block = make([]float32, 0, blockSamples)
			}
			// Each sample is 4 bytes, little-endian float32.
			block = append(block, math.Float32frombits(binary.LittleEndian.Uint32(buf[i:])))
			total++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			cmd.Wait()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("ffmpeg read: %w", err)
		}
	}
	samples := make([]float32, 0, total)
	for _, b := range append(blocks, block) {
		samples = append(samples, b...)
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg (%s): %s: %w", filePath, stderrBuf.String(), err)
	}

	return samples, nil
}
