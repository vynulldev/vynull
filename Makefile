# SPDX-License-Identifier: GPL-3.0-or-later
.PHONY: build test check run run-cdj generate e2e clean

build:
	go build -o vynull .

test:
	go test ./...

# The full pre-release gate: build, vet, formatting, unit tests, then the
# e2e suite (see the e2e target's requirements below).
check:
	go build ./...
	go vet ./...
	@fmt=$$(gofmt -l .); if [ -n "$$fmt" ]; then echo "gofmt needed:"; echo "$$fmt"; exit 1; fi
	go test ./...
	$(MAKE) e2e

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
