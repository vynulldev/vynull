# SPDX-License-Identifier: GPL-3.0-or-later
.PHONY: build run run-rb generate clean

build:
	go build -o vynull -buildvcs=false .

run: build
	sudo ./vynull --music-dir $(MUSIC)

run-rb: build
	sudo ./vynull --mode rekordbox --music-dir $(MUSIC)

generate: build
	./vynull --generate $(OUT) --music-dir $(MUSIC)

clean:
	rm -f vynull
