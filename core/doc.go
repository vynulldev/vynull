// SPDX-License-Identifier: GPL-3.0-or-later

// Package core is the brand-neutral heart of vynull: the domain model (tracks,
// cues, playlists, analysis, player state) and the interfaces that pluggable
// adapters implement — live link protocols (Backend/Source) and library export
// formats (Exporter). Import adapters live in package library (see
// docs/design/import-layer.md).
//
// Dependency rule: core imports nothing from analysis, link/*, format/*, or api;
// everything else imports core. Keeping the model and contracts free of any one
// ecosystem's wire formats (Pro DJ Link, StageLinq) or file formats (rekordbox,
// Engine) makes a second protocol or library format an additive adapter rather
// than a fork.
//
// Status: draft (see docs/design/modular-backends.md). Package analysis
// projects its results into core.Analysis (Result.Core), which only tests
// exercise so far, and nothing implements Backend, Source or Exporter yet, so
// these contracts are not final.
package core
