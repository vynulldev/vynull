# SPDX-License-Identifier: GPL-3.0-or-later
.PHONY: build run run-cdj generate clean

build:
	go build -o vynull .

# rekordbox mode (the default) — no privileged ports needed
run: build
	sudo ./vynull --music-dir $(MUSIC)

# CDJ-USB source mode — needs UDP 111 (see README Requirements)
run-cdj: build
	sudo ./vynull --mode cdj --music-dir $(MUSIC)

generate: build
	./vynull --generate $(OUT) --music-dir $(MUSIC)

clean:
	rm -f vynull
