// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vynulldev/vynull/pdb"
	"github.com/vynulldev/vynull/proto"
)

type playerState struct {
	status    *proto.CDJStatus
	lastSeen  time.Time
	trackName string
	artist    string
	key       string
}

var (
	mu      sync.Mutex
	players = make(map[uint8]*playerState)
	pdbDB   *pdb.Database
)

func main() {
	iface := flag.String("interface", "", "network interface (required)")
	pdbPath := flag.String("pdb", "", "path to export.pdb for track name lookup (optional)")
	flag.Parse()

	if *iface == "" {
		fmt.Fprintf(os.Stderr, "Usage: monitor --interface <iface> [--pdb <export.pdb>]\n")
		os.Exit(1)
	}

	if *pdbPath != "" {
		var err error
		pdbDB, err = pdb.Open(*pdbPath)
		if err != nil {
			log.Printf("pdb: %v (track names unavailable)", err)
		} else {
			log.Printf("pdb: loaded %d tracks", len(pdbDB.Tracks))
		}
	}

	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", 50002))
	if err != nil {
		log.Fatalf("bind port 50002: %v", err)
	}
	defer conn.Close()

	go receiveLoop(conn)

	// Display loop.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		render()
	}
}

func receiveLoop(conn net.PacketConn) {
	buf := make([]byte, 512)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		if n < 0x24 {
			continue
		}

		status, ok := proto.ParseCDJStatus(buf[:n])
		if !ok {
			continue
		}

		// Look up track info from PDB.
		var trackName, artist, key string
		if pdbDB != nil && status.TrackID > 0 {
			t := pdbDB.TrackByID(status.TrackID)
			if t != nil {
				trackName = t.Title
				artist = t.Artist
				key = t.Key
			}
		}

		mu.Lock()
		players[status.DeviceNumber] = &playerState{
			status:    status,
			lastSeen:  time.Now(),
			trackName: trackName,
			artist:    artist,
			key:       key,
		}
		mu.Unlock()
	}
}

func render() {
	mu.Lock()
	defer mu.Unlock()

	// Clear screen.
	fmt.Print("\033[H\033[2J")
	fmt.Println("\033[1m  VYNULL · DJ LINK MONITOR\033[0m")
	fmt.Println(strings.Repeat("─", 72))

	// Sort players by device number.
	var nums []uint8
	for num := range players {
		nums = append(nums, num)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })

	now := time.Now()
	var masterBPM float64

	for _, num := range nums {
		p := players[num]
		if now.Sub(p.lastSeen) > 5*time.Second {
			continue // stale
		}

		s := p.status
		if s.IsMaster && s.BPM > 0 {
			masterBPM = float64(s.BPM) / 100.0
		}

		// Player header.
		masterTag := "  "
		if s.IsMaster {
			masterTag = "M "
		}

		stateColor := "\033[90m" // gray
		switch {
		case s.IsPlaying:
			stateColor = "\033[32m" // green
		case s.PlayState == 0x06:
			stateColor = "\033[33m" // yellow (cued)
		case s.PlayState == 0x05:
			stateColor = "\033[33m" // yellow (paused)
		}

		fmt.Printf("\n  \033[1m%s[%d] %s\033[0m\n", masterTag, s.DeviceNumber, s.Name)

		// Play state.
		state := s.PlayStateString()
		fmt.Printf("    %s● %s\033[0m", stateColor, state)
		if s.IsSync {
			fmt.Print("  \033[36mSYNC\033[0m")
		}
		if s.IsOnAir {
			fmt.Print("  \033[31mON-AIR\033[0m")
		}
		fmt.Println()

		// Track info.
		if s.TrackID > 0 {
			title := p.trackName
			if title == "" {
				title = fmt.Sprintf("Track #%d", s.TrackID)
			}
			fmt.Printf("    \033[1m%s\033[0m\n", title)
			if p.artist != "" {
				fmt.Printf("    %s\n", p.artist)
			}

			// BPM + Key + Pitch.
			bpm := float64(s.BPM) / 100.0
			pitch := float64(s.Pitch-0x100000) / float64(0x100000) * 100.0
			line := fmt.Sprintf("    BPM: %.1f", bpm)
			if pitch != 0 {
				line += fmt.Sprintf(" (%+.1f%%)", pitch)
			}
			if p.key != "" {
				line += fmt.Sprintf("  Key: %s", p.key)
			}
			if s.BeatInBar > 0 {
				beats := [4]string{"·", "·", "·", "·"}
				if s.BeatInBar >= 1 && s.BeatInBar <= 4 {
					beats[s.BeatInBar-1] = "●"
				}
				line += fmt.Sprintf("  Beat: %s", strings.Join(beats[:], " "))
			}
			fmt.Println(line)
		}
	}

	// Master tempo.
	if masterBPM > 0 {
		fmt.Printf("\n  %s\n", strings.Repeat("─", 40))
		fmt.Printf("  Master Tempo: \033[1m%.1f BPM\033[0m\n", masterBPM)
	}

	fmt.Printf("\n  \033[90m%s\033[0m\n", time.Now().Format("15:04:05"))
}
