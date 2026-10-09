# CDJ emulator (virtual playing deck)

- **Status:** design draft. No code yet. Tracks issue #41 (playing-simulation mode).
- **Goal:** let Vynull *be* a virtual CDJ that plays a track — advancing a playhead through the beat grid at a set pitch, broadcasting live CDJ status and per-beat packets, controllable at runtime. Two payoffs: a genuine software deck on the Pro DJ Link network, and a software **test rig** for everything in Vynull that listens to deck state (the monitor, the stream overlay, MPRIS, play history), which today can only be exercised against the physical rig.

## Current state (what we have / lack)

- **We already parse the whole CDJ status packet** (`proto/cdj_status.go`, type 0x0a, 292 bytes), so every field we'd emit is known as the inverse of what we read:
  - `DeviceNumber` 0x24, `Active` 0x27
  - Track: `TrackDevice` 0x28, `TrackSlot` 0x29, `TrackType` 0x2a, `TrackID` 0x2c (BE u32)
  - `PlayState` 0x7b (0=empty, 3=playing, 5=paused, 6=cued)
  - `Pitch` 0x8c (BE u32), `BPM` 0x92 (BE u16)
  - `BeatInTrack` (M_b) 0xa0 (BE u32, 0xFFFFFFFF = unknown), `BeatInBar` 0xa6
- **`MarshalStatusCDJ` (`proto/status.go`) is a static idle status** — it sets only name, device number, media flags, firmware. No play state, track, position, pitch, or beat. A virtual *playing* deck needs these set dynamically.
- **No beat-packet (0x28) emitter.** We only *receive* beats (`device.listenBeats` → `Monitor.BeatTick`, reading the device number at 0x21). To emit, the full 0x28 layout has to be reverse-engineered.
- **No deck engine** — nothing advances a playhead over a grid.
- `--mode cdj` already makes us *appear* as a CDJ-USB source (device 3). The emulator adds *playing* behaviour on top of that presence.
- Prior art (same ecosystem): rbxport has `rbl-fakecdj` (CDJ emulation) and `rbl-deck` (a real virtual deck with Rubber Band master tempo). Both GPL; worth reading their approach (especially the 0x28 shape and master-tempo handling) once we pick up Phase 2.

## Scope

- **Phase 1 — status + beat emulation, no audio.** The virtual deck advances a real beat grid and broadcasts as a playing CDJ (dynamic 0x0a + 0x28 beats), runtime-controllable. This is issue #41 and the test-rig win; it needs no audio output.
- **Phase 2 — audio (later, separate).** Actually output sound (a software CDJ you can hear), with pitch-correct master tempo. Much bigger (decode + time-stretch, à la `rbl-deck` + Rubber Band). Explicitly out of scope for the first pass.

## Phase 1 architecture

1. **Virtual deck engine** (`device` package, a `SimDeck`): state = `{trackID, beats []ms, durationMs, bpm, playState, positionMs, pitchPct, playing}`. A clock advances `positionMs` in real time scaled by pitch; `beat_in_track` and `beat_in_bar` derive from the grid + position. Ops: `load`, `play`, `pause`, `cue`, `seek`, `setPitch`. The grid comes from our analysis (`analysis.Result.Beats`), so loading is "pick a library track".
2. **Dynamic CDJ status (0x0a)**: a `MarshalStatusCDJPlaying(...)` (or extend `MarshalStatusCDJ`) that sets the dynamic fields above from the SimDeck. Broadcast at the usual ~10 Hz from `device.statusBroadcastLoop` when the SimDeck is active; fall back to the static idle status otherwise.
3. **Beat-packet (0x28) emitter**: a `MarshalBeat(...)` built from the reverse-engineered 0x28 layout, scheduled to fire on each beat (from the grid at pitch) and sent on UDP 50001. This is the only piece that needs new RE.
4. **Runtime control**: HTTP endpoints (`/api/sim/load|play|pause|cue|seek|pitch|status`) and matching CLI subcommands (`vynull sim ...`), so scenarios can be scripted for testing and driven live.
5. **Broadcast wiring**: the status (50002) and beat (50001) send loops read the SimDeck when it's active; nothing else changes when it's off.

## Protocol work needed

- **Reverse-engineer the 0x28 beat packet** from a real CDJ-2000NXS2 capture (we currently decode only the device number). Expected fields: magic, type 0x28, device number (0x21), BPM, the current beat number, and the ms-to-next-beat / next-bar countdown fields other gear uses to phase-lock.
- **Confirm the dynamic 0x0a fields are byte-exact** vs a real playing CDJ (we parse them; emitting must match down to the F / sync-control / master / on-air bytes we don't currently interpret).

## Validation (load-bearing)

- **Byte-diff synthetic 0x0a and 0x28 against real CDJ-2000NXS2 captures**, field by field. A subtly-wrong packet is worse than none — see `[[track-end-load-bug]]`, where "match rekordbox" status tweaks regressed hardware and had to be reverted.
- **Test-rig first.** Validate against *our own* listeners (monitor / overlay / MPRIS / history) on a loopback or isolated network, where Vynull is the only consumer, before ever putting a fake playing CDJ on a live rig with real decks and a mixer.

## Risks / cautions

- Protocol-fidelity territory of `[[track-end-load-bug]]`: careful, hardware-validated, regression-prone. Not a quick feature.
- **On a live rig a fake playing CDJ can confuse real gear** — a DJM might try to sync to our BPM/beat, master handoff could misbehave, and claiming on-air/master is risky. Default to a safe profile (not master, on-air off) and gate "live rig" behind an explicit opt-in; isolated/test-rig is the default.
- **Device number collision.** A deck is treated as a player at numbers 1–4; we currently use 17 (rekordbox) or 3 (cdj). Emulating a player may want 1–4, which can collide with real CDJs on the network. Needs a decision (pick a free number, detect collisions, or stay at a non-player number and accept reduced realism).

## Decisions (locked 2026-10-09)

- **Status-only first.** No audio in Phase 1; audio is Phase 2.
- **Isolated / test-rig first.** The safe profile (not master, not on-air) is the default; a live rig is an explicit opt-in.
- **Control via API + CLI**, with a `--simulate` flag as the convenience shorthand that layers playing behaviour onto the existing `--mode cdj` presence.
- **Virtual-player device number is configurable**, auto-picking a free slot in 1–4 (collision-avoided); in isolated mode it is a non-issue, so this is deferred to the live-rig work.
- Still open for the requester: status-vs-beat priority, and which model's quirks matter most (feeds Phase 0 RE).

## Progress

- **Phase 1a done** (branch `cdj-emulator`), all unit-tested, no hardware needed:
  - `proto.MarshalStatusCDJPlaying` + `CDJPlayState` — overlays the dynamic fields onto the idle template; round-trips through `ParseCDJStatus`.
  - `device.SimDeck` — the playhead/grid clock: load/play/pause/cue/seek/pitch, beat-in-track + beat-in-bar derivation, end-of-track, effective BPM; injectable clock.
  - Broadcast wiring — `--simulate` (implies `--mode cdj`) attaches a `SimDeck`; the CDJ status loop emits a dynamic playing status (with a sequence counter) from its snapshot, else the idle status.
  - Control surface — `/api/sim/{status,load,play,pause,cue,seek,pitch,eject}` and `vynull sim ...`, both covered by tests.
- **Next (1b):** the 0x28 beat emitter — gated on a real-CDJ capture (Phase 0). Until then, our monitor/overlay reflect the virtual deck from the 0x0a status alone (they interpolate beats), so the test rig is already usable.
- **Also pending:** a live visual check (point the web overlay / monitor at a `--simulate` instance and confirm a loaded track plays, advances, and reaches the end); and, when `api-spec` merges, add the `/api/sim` routes + `SimDeckStatus` schema to the OpenAPI spec.

## Rough phasing

- **0.** RE the 0x28 beat packet + confirm the dynamic 0x0a fields (capture + decode). *Gated on a real-CDJ capture.*
- **1a.** SimDeck engine + dynamic 0x0a broadcast (no beats yet) → confirm our monitor/overlay show a "playing" virtual deck.
- **1b.** 0x28 beat emitter → confirm the overlay's beat-synced playhead and the API beat clock lock to it.
- **1c.** Runtime control (API + CLI) → scriptable scenarios.
- **1d.** Hardware validation: byte-diff, then a careful real-rig smoke behind the safe profile.
- **2.** (Later) audio output + master tempo.
