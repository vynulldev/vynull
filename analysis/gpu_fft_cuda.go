// SPDX-License-Identifier: GPL-3.0-or-later
//go:build cuda

package analysis

// #cgo LDFLAGS: -lcufft -lcudart
// #include <cufft.h>
// #include <cuda_runtime.h>
// #include <stdlib.h>
//
// // gpu_batch_fft performs batch real-to-complex FFTs on the GPU using cuFFT.
// // Input:  contiguous float array, batchCount * fftSize elements.
// // Output: contiguous float array, batchCount * (fftSize/2+1) * 2 elements (re,im pairs).
// // Returns 0 on success.
// static int gpu_batch_fft(const float *input, float *output, int fftSize, int batchCount) {
//     if (batchCount <= 0 || fftSize <= 0) return -1;
//
//     size_t inputSize  = (size_t)fftSize * batchCount * sizeof(float);
//     size_t outBins    = (size_t)(fftSize / 2 + 1);
//     size_t outputSize = outBins * batchCount * sizeof(cufftComplex);
//
//     cufftReal    *d_input  = NULL;
//     cufftComplex *d_output = NULL;
//
//     cudaError_t cerr;
//     cerr = cudaMalloc((void**)&d_input, inputSize);
//     if (cerr != cudaSuccess) return 1;
//
//     cerr = cudaMalloc((void**)&d_output, outputSize);
//     if (cerr != cudaSuccess) { cudaFree(d_input); return 2; }
//
//     cerr = cudaMemcpy(d_input, input, inputSize, cudaMemcpyHostToDevice);
//     if (cerr != cudaSuccess) { cudaFree(d_input); cudaFree(d_output); return 3; }
//
//     cufftHandle plan;
//     int n[1] = {fftSize};
//     cufftResult res = cufftPlanMany(&plan,
//         1, n,
//         NULL, 1, fftSize,
//         NULL, 1, (int)outBins,
//         CUFFT_R2C, batchCount);
//     if (res != CUFFT_SUCCESS) {
//         cudaFree(d_input); cudaFree(d_output);
//         return 4;
//     }
//
//     res = cufftExecR2C(plan, d_input, d_output);
//     if (res != CUFFT_SUCCESS) {
//         cufftDestroy(plan); cudaFree(d_input); cudaFree(d_output);
//         return 5;
//     }
//
//     cerr = cudaDeviceSynchronize();
//     if (cerr != cudaSuccess) {
//         cufftDestroy(plan); cudaFree(d_input); cudaFree(d_output);
//         return 7;
//     }
//
//     cerr = cudaMemcpy(output, d_output, outputSize, cudaMemcpyDeviceToHost);
//     cufftDestroy(plan);
//     cudaFree(d_input);
//     cudaFree(d_output);
//     return (cerr == cudaSuccess) ? 0 : 6;
// }
//
// static int gpu_available(void) {
//     int count = 0;
//     cudaError_t err = cudaGetDeviceCount(&count);
//     return (err == cudaSuccess && count > 0) ? 1 : 0;
// }
import "C"

import (
	"log"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	gpuOnce      sync.Once
	gpuAvailable bool
	gpuEnabled   atomic.Bool
	// cudaCallMu serializes calls into gpu_batch_fft so two analysisWorker
	// goroutines can't both be in the CUDA path simultaneously. Concurrent
	// compute on a GPU that also drives the desktop tends to stall the
	// Nvidia compositor — observed in a desktop freeze on 2026-05-19.
	// cuFFT itself isn't thread-safe across plan create/destroy either,
	// so this mutex doubles as correctness protection.
	cudaCallMu sync.Mutex
)

// GPUDefault is the default value for the --gpu flag. On a cuda build we want
// it on by default so users get the GPU FFT path without remembering an extra
// flag; if no actual CUDA device is present at runtime, EnableGPU still logs
// a fallback notice and batchFFTGPU drops to the CPU path automatically.
const GPUDefault = true

func init() {
	gpuOnce.Do(func() {
		gpuAvailable = C.gpu_available() == 1
		if gpuAvailable {
			log.Printf("analysis: CUDA GPU detected")
		}
	})
}

// HasGPU returns true if a CUDA GPU is available for FFT acceleration.
func HasGPU() bool {
	gpuOnce.Do(func() {
		gpuAvailable = C.gpu_available() == 1
	})
	return gpuAvailable
}

// EnableGPU enables or disables GPU-accelerated analysis.
// Only effective if a CUDA GPU is available (HasGPU() == true).
func EnableGPU(enable bool) {
	gpuEnabled.Store(enable)
	if enable && HasGPU() {
		log.Printf("analysis: GPU-accelerated FFT enabled")
	} else if enable {
		log.Printf("analysis: --gpu requested but no CUDA GPU available, using CPU")
	}
}

// UseGPU returns true if GPU acceleration is both available and enabled.
func UseGPU() bool {
	return gpuEnabled.Load() && gpuAvailable
}

// batchFFTGPU runs batch real-to-complex FFTs on the GPU via cuFFT.
// Each window is fftSize float64 values. Returns real and imag parts
// for the first fftSize/2+1 bins of each window.
func batchFFTGPU(windows [][]float64, fftSize int) (realOut, imagOut [][]float64) {
	batchCount := len(windows)
	if batchCount == 0 {
		return nil, nil
	}

	if !UseGPU() {
		return batchFFTCPU(windows, fftSize)
	}

	// Pack input as contiguous float32 array.
	input := make([]float32, batchCount*fftSize)
	for i, w := range windows {
		off := i * fftSize
		for j := 0; j < fftSize && j < len(w); j++ {
			input[off+j] = float32(w[j])
		}
	}

	// Output: each window produces fftSize/2+1 complex values (2 floats each).
	outBins := fftSize/2 + 1
	output := make([]float32, batchCount*outBins*2)

	cudaCallMu.Lock()
	rc := C.gpu_batch_fft(
		(*C.float)(unsafe.Pointer(&input[0])),
		(*C.float)(unsafe.Pointer(&output[0])),
		C.int(fftSize),
		C.int(batchCount),
	)
	cudaCallMu.Unlock()
	if rc != 0 {
		log.Printf("analysis: GPU FFT failed (rc=%d), falling back to CPU", rc)
		return batchFFTCPU(windows, fftSize)
	}

	// Unpack into per-window real/imag slices.
	// Allocate backing arrays contiguously to reduce GC pressure.
	realBacking := make([]float64, batchCount*outBins)
	imagBacking := make([]float64, batchCount*outBins)
	realOut = make([][]float64, batchCount)
	imagOut = make([][]float64, batchCount)
	for i := 0; i < batchCount; i++ {
		r := realBacking[i*outBins : (i+1)*outBins]
		im := imagBacking[i*outBins : (i+1)*outBins]
		base := i * outBins * 2
		for j := 0; j < outBins; j++ {
			r[j] = float64(output[base+j*2])
			im[j] = float64(output[base+j*2+1])
		}
		realOut[i] = r
		imagOut[i] = im
	}
	return realOut, imagOut
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

func gpuInfo() string {
	if HasGPU() {
		return "CUDA GPU (cuFFT)"
	}
	return "CPU fallback"
}
