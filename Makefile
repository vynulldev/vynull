# SPDX-License-Identifier: GPL-3.0-or-later
.PHONY: build run run-cdj generate e2e clean

build:
	go build -o vynull .

# rekordbox mode (the default) — no privileged ports needed
run: build
	./vynull --music-dir $(MUSIC)

# CDJ-USB source mode — needs UDP 111 (see README Requirements)
run-cdj: build
	sudo ./vynull --mode cdj --music-dir $(MUSIC)

generate: build
	./vynull --generate $(OUT) --music-dir $(MUSIC)

# End-to-end suite: drives the real binary over HTTP against synthesized
# media (needs ffmpeg + the DJ-Link UDP ports free, i.e. no running instance)
e2e:
	VYNULL_E2E=1 go test ./e2e/ -v

clean:
	rm -f vynull
