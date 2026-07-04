# Contributing to Vynull

Thanks for your interest! Vynull is a **hobby project** — I built it to learn some Go and poke at
my CDJs. That shapes a few things: replies may be slow, the scope is deliberately narrow (a Linux
Pro DJ Link source), and "it works on my decks" is often the bar. Contributions are very welcome
anyway — especially bug reports with good hardware detail, and fixes verified on real gear.

By contributing, you agree that your contributions are licensed under the project's
[GNU General Public License v3](LICENSE).

## Reporting bugs & requesting features

Please use the issue forms (**New issue → Bug report / Feature request**). Because Vynull speaks an
undocumented protocol to real hardware, the most useful things you can include are the **exact
hardware (deck model + firmware), how you ran it, and a log** — plus a packet capture if you can
grab one.

## Development setup

Requirements:

- **Go** — see the version in [`go.mod`](go.mod)
- **ffmpeg** on your `PATH` (audio decoding)
- Linux, and — to test anything real — a CDJ/XDJ on a link-local network

```bash
git clone https://github.com/vynulldev/vynull && cd vynull
make build          # or: go build -o vynull .
go test ./...
go vet ./...
gofmt -l .          # should print nothing
```

Run it: `sudo ./vynull --interface <iface> --music-dir <dir> --web`
(see the [README](README.md) for flags, modes, and the port-111 note). CI runs build + vet + tests
on every push and PR.

## The one rule that isn't obvious: trust the hardware

Much of Vynull's behaviour can only be verified on real CDJs, and matching rekordbox
byte-for-byte on the wire is **not** a reliable proxy for correctness — changes that looked "more
correct" on the wire have regressed actual deck behaviour. If your change touches the protocol, the
load path, analysis output, or anything a deck reacts to:

- test it on real hardware, and
- state **which deck(s) + firmware** you tested in the pull request.

## Code style

- `gofmt` clean (keep `gofmt -l .` empty); run `go vet ./...` and `go test ./...`.
- Match the surrounding code — naming, comment density, and idiom.
- New source files start with an SPDX header:
  `// SPDX-License-Identifier: GPL-3.0-or-later` (`#` for scripts/YAML, `<!-- -->` for HTML/SVG).
- Commit subjects use a lowercase area prefix, e.g. `dbserver: fix …`, `analysis: …`, `main: …`.
- **Commit in UTC** to keep the history timezone-neutral — `TZ=UTC git commit …` (git stamps each
  commit with your machine's local timezone, and there's no per-repo setting for it; exporting
  `TZ=UTC` in your shell, or a commit alias, makes it automatic).
- Keep pull requests focused; explain the "why", not just the "what".

## Scope & a note on trademarks

Vynull is an independent, unofficial project and is **not affiliated with Pioneer DJ / AlphaTheta**.
"Pioneer DJ", "CDJ", "rekordbox", and "Pro DJ Link" are used only descriptively, to explain
interoperability — please don't add code, names, or assets that present the project as official or
use those marks as branding.

Protocol-analysis and debugging tooling lives in the companion **vynull-tools** repo, not here.
