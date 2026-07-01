// SPDX-License-Identifier: GPL-3.0-or-later
//go:build !cuda

package analysis

import (
	"log"
	"sync"
)

// HasGPU returns false when built without CUDA support.
func HasGPU() bool { return false }

// GPUDefault is the default value for the --gpu flag. Without the cuda build
// tag there is no GPU code path, so default to false.
const GPUDefault = false

// EnableGPU is a no-op when built without CUDA support.
func EnableGPU(enable bool) {
	if enable {
		log.Printf("analysis: --gpu requested but binary built without CUDA support (use: go build -tags cuda)")
	}
}

// UseGPU returns false when built without CUDA support.
func UseGPU() bool { return false }

// batchFFTGPU falls back to CPU FFT when built without CUDA.
func batchFFTGPU(windows [][]float64, fftSize int) (realOut, imagOut [][]float64) {
	return batchFFTCPU(windows, fftSize)
}

// batchFFTCPU runs FFTs in parallel on CPU using goroutines.
func batchFFTCPU(windows [][]float64, fftSize int) (realOut, imagOut [][]float64) {
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

func gpuInfo() string { return "CPU only (build with -tags cuda for GPU)" }
