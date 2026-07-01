// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import "sync"

// batchFFT runs a batch of real-input FFTs in parallel across CPU workers.
// Each window is copied to length fftSize; the returned slices hold the first
// fftSize/2+1 bins (real-to-complex) for each input window.
func batchFFT(windows [][]float64, fftSize int) (realOut, imagOut [][]float64) {
	batchCount := len(windows)
	realOut = make([][]float64, batchCount)
	imagOut = make([][]float64, batchCount)
	outBins := fftSize/2 + 1

	var wg sync.WaitGroup
	workers := 4
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
				r := make([]float64, fftSize)
				im := make([]float64, fftSize)
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
