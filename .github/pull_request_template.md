<!-- Thanks for contributing to Vynull! Keep PRs focused; explain the "why", not just the "what". -->

## What & why

<!-- What does this change and why? Link any related issue, e.g. Fixes #123 -->

## Hardware testing

<!-- Vynull's behaviour can only be fully verified on real decks, and matching rekordbox
     on the wire is NOT a reliable proxy for correctness. If this touches the protocol, the load
     path, analysis output, or anything a deck reacts to, test it on hardware. -->

- **Tested on:** <!-- e.g. 2× CDJ (model + fw) + DJM mixer — or "N/A: no deck-facing change" -->

## Checklist

- [ ] `go build ./...`, `go vet ./...`, and `go test ./...` pass
- [ ] `gofmt -l .` is clean
- [ ] New source files carry an SPDX header (`GPL-3.0-or-later`)
- [ ] Tested on real hardware (deck + firmware noted above), **or** this change doesn't affect deck behaviour
- [ ] I agree my contribution is licensed under the project's [GPLv3](../LICENSE)
