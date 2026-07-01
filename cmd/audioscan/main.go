// SPDX-License-Identifier: GPL-3.0-or-later

// audioscan walks a directory of audio files and reports any whose
// frames don't decode cleanly via ffmpeg. CDJ players use hardware MP3
// decoders that are less forgiving than ffmpeg / browsers — a malformed
// frame that ffmpeg recovers from often freezes the deck mid-playback.
// Run this before exporting to a USB so you can re-encode (or skip)
// problem files instead of finding out on the deck.
//
// Usage:
//
//	audioscan [-jobs N] [-v] <dir>
//
// Default: errors are surfaced; warnings (estimated-duration, missing
// VBR header) are quieter. -v prints every file plus ffmpeg's stderr.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

var audioExt = map[string]bool{
	".mp3": true, ".flac": true, ".wav": true,
	".aiff": true, ".aif": true, ".m4a": true,
}

type result struct {
	path     string
	errors   []string // hard errors — "freeze risk" on CDJ
	warnings []string // recoverable issues — "may render but not always"
}

func scan(path string) result {
	// ffmpeg -v warning is the chattiest non-debug level that surfaces
	// frame-level decode complaints; -f null discards the decoded audio.
	cmd := exec.Command("ffmpeg", "-v", "warning", "-i", path, "-f", "null", "-")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Run() // ignore exit code — partial decodes are normal

	r := result{path: path}
	for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ll := strings.ToLower(line)
		// Hard errors that have empirically frozen CDJ:
		//   - "Header missing" → MP3 frame with no sync word
		//   - "Invalid data found"
		//   - "Error while decoding"
		switch {
		case strings.Contains(ll, "header missing"),
			strings.Contains(ll, "invalid data found"),
			strings.Contains(ll, "error while decoding"),
			strings.Contains(ll, "frame missing"),
			strings.Contains(ll, "broken header"):
			r.errors = append(r.errors, line)
		case strings.Contains(ll, "estimating duration from bitrate"),
			strings.Contains(ll, "non-zero padding"),
			strings.Contains(ll, "incomplete frame"):
			r.warnings = append(r.warnings, line)
		default:
			// Unfamiliar ffmpeg complaint — classify conservatively as warning
			r.warnings = append(r.warnings, line)
		}
	}
	return r
}

func main() {
	jobs := flag.Int("jobs", runtime.NumCPU(), "parallel decode workers")
	verbose := flag.Bool("v", false, "print every file, not just problematic ones")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: audioscan [-jobs N] [-v] <dir>\n")
		fmt.Fprintf(os.Stderr, "  Scans <dir> recursively for audio files that don't decode cleanly.\n")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	root := flag.Arg(0)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "audioscan: ffmpeg not found in PATH")
		os.Exit(1)
	}

	// Collect file list first so we can show progress.
	var files []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if audioExt[strings.ToLower(filepath.Ext(p))] {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "audioscan: no audio files under", root)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "audioscan: %d files, %d workers\n", len(files), *jobs)

	// Worker pool.
	in := make(chan string, len(files))
	out := make(chan result, len(files))
	var wg sync.WaitGroup
	for i := 0; i < *jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range in {
				out <- scan(p)
			}
		}()
	}
	for _, p := range files {
		in <- p
	}
	close(in)
	go func() { wg.Wait(); close(out) }()

	var errCount, warnCount int64
	bw := bufio.NewWriter(os.Stdout)
	defer bw.Flush()
	done := int64(0)
	total := int64(len(files))
	for r := range out {
		atomic.AddInt64(&done, 1)
		if len(r.errors) > 0 {
			atomic.AddInt64(&errCount, 1)
			fmt.Fprintf(bw, "ERROR  %s\n", r.path)
			for _, e := range r.errors {
				fmt.Fprintf(bw, "       %s\n", e)
			}
		} else if len(r.warnings) > 0 {
			atomic.AddInt64(&warnCount, 1)
			if *verbose {
				fmt.Fprintf(bw, "warn   %s\n", r.path)
				for _, e := range r.warnings {
					fmt.Fprintf(bw, "       %s\n", e)
				}
			}
		} else if *verbose {
			fmt.Fprintf(bw, "ok     %s\n", r.path)
		}
		bw.Flush()
		if atomic.LoadInt64(&done)%50 == 0 {
			fmt.Fprintf(os.Stderr, "audioscan: progress %d/%d  (errors=%d warnings=%d)\n",
				done, total, atomic.LoadInt64(&errCount), atomic.LoadInt64(&warnCount))
		}
	}

	fmt.Fprintf(os.Stderr, "\naudioscan: %d errors, %d warnings, %d total\n",
		errCount, warnCount, total)
	if errCount > 0 {
		os.Exit(1)
	}
}
