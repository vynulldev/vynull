// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"runtime"
	"sync"
)

// batchFFT runs a batch of real-input FFTs in parallel across CPU workers.
// Each window is copied to length FftSize; the returned slices hold the first
// FftSize/2+1 bins (real-to-complex) for each input window.
func batchFFT(windows [][]float64, FftSize int) (realOut, imagOut [][]float64) {
	batchCount := len(windows)
	realOut = make([][]float64, batchCount)
	imagOut = make([][]float64, batchCount)
	outBins := FftSize/2 + 1

	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers > batchCount {
		workers = batchCount // no point spawning more workers than windows
	}
	if workers < 1 {
		workers = 1
	}
	chunkSize := (batchCount + workers - 1) / workers

	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > batchCount {
			end = batchCount
		}
		if start >= end {
			break
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				r := make([]float64, FftSize)
				im := make([]float64, FftSize)
				copy(r, windows[i])
				fft(r, im)
				realOut[i] = r[:outBins]
				imagOut[i] = im[:outBins]
			}
		}(start, end)
	}
	wg.Wait()
	return realOut, imagOut
}
