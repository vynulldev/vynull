// SPDX-License-Identifier: GPL-3.0-or-later

package analysis

import (
	"runtime"
	"sync"
)

// stftChunk is how many frames' spectra stftChunks holds at once: enough to
// keep every CPU busy, small enough that the buffers stay a few MB.
const stftChunk = 512

// stftChunks computes the spectra of frames 0..n-1 a chunk at a time and hands
// each chunk to use, in frame order, so a caller's running sums see the frames
// in the order they always did. Within a chunk the FFTs run in parallel.
//
// fill writes frame i's (windowed) samples into buf, which arrives zeroed and
// fftSize long. use gets the chunk's first frame and its spectra, keeping only
// bins 0..bins-1: frame start+k's real and imaginary parts are
// re[k*bins:(k+1)*bins] and im[k*bins:(k+1)*bins]. The chunk's buffers are
// reused for the next chunk, so use must not keep them.
//
// It holds one chunk's bins instead of every frame's window and full spectrum
// (for a four-minute track, hundreds of MB per analysis step), and computes
// each spectrum exactly as batchFFT did.
func stftChunks(n, fftSize, bins int, fill func(i int, buf []float64), use func(start int, re, im []float64)) {
	if n <= 0 {
		return
	}
	bins = min(bins, fftSize/2+1)
	chunk := min(stftChunk, n)
	re := make([]float64, chunk*bins)
	im := make([]float64, chunk*bins)
	workers := max(1, min(runtime.NumCPU(), chunk))
	scratch := make([][2][]float64, workers)
	for w := range scratch {
		scratch[w] = [2][]float64{make([]float64, fftSize), make([]float64, fftSize)}
	}

	for start := 0; start < n; start += chunk {
		count := min(chunk, n-start)
		per := (count + workers - 1) / workers
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo, hi := w*per, min((w+1)*per, count)
			if lo >= hi {
				break
			}
			wg.Add(1)
			go func(r, i []float64, lo, hi int) {
				defer wg.Done()
				for k := lo; k < hi; k++ {
					clear(r)
					clear(i)
					fill(start+k, r)
					fft(r, i)
					copy(re[k*bins:(k+1)*bins], r[:bins])
					copy(im[k*bins:(k+1)*bins], i[:bins])
				}
			}(scratch[w][0], scratch[w][1], lo, hi)
		}
		wg.Wait()
		use(start, re[:count*bins], im[:count*bins])
	}
}
