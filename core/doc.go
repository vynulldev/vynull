// SPDX-License-Identifier: GPL-3.0-or-later

// Package core is the brand-neutral heart of vynull: the domain model (tracks,
// cues, playlists, analysis, player state) and the interfaces that pluggable
// adapters implement — live link protocols (Backend/Source) and library file
// formats (Importer/Exporter).
//
// Dependency rule: core imports nothing from analysis, link/*, format/*, or api;
// everything else imports core. Keeping the model and contracts free of any one
// ecosystem's wire formats (Pro DJ Link, StageLinq) or file formats (rekordbox,
// Engine) makes a second protocol or library format an additive adapter rather
// than a fork.
//
// Status: proposed foundation for the modular-backends refactor (see
// docs/design/modular-backends.md). This package defines the target model;
// concrete packages migrate onto it in later steps, so nothing imports it yet.
package core
