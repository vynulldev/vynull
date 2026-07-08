# Generalizing the library import layer

- **Status:** Proposed
- **Scope:** refactor / library import
- **Related:** [modular-backends.md](modular-backends.md) (this is the pragmatic,
  import-only slice of that doc's "format adapters" idea)

## Goal

Make adding a new source library format (Serato, Engine, iTunes, ...) a matter of
*implementing one interface and registering it*, instead of adding another
divergent function plus another `case` to a growing switch. We now have three
importers (rekordbox XML, rekordbox master.db, Traktor NML) plus the backup-zip
container, which is the rule-of-three that justifies the abstraction. It would
have been premature at one.

Non-goal: neutralizing the *result* into `core` types. Import inherently
populates `library.Library`; a brand-neutral import buys nothing until there is a
non-library consumer. This lives in `library`.

## Current shape

Three parse functions with divergent signatures, dispatched by `switch ext` in
`api.handleImportRekordbox`:

```
ImportRekordboxXML      -> (*ImportResult, []PlaylistImport, []TagImport, []ColorImport, error)
ImportRekordboxMasterDB -> (*ImportResult, []PlaylistImport, []TagImport, []ColorImport, []MasterDBAsset, []MasterDBCue, error)
ImportTraktorNML        -> (*ImportResult, []PlaylistImport, []TagImport, []ColorImport, []MasterDBCue, error)
```

Observations:

- The `.zip` case is already a *delegating importer*: extract the backup, call
  the master.db importer with the extracted `dbPath`, set `shareRoot` /
  `settingsDir`.
- After dispatch there is a ~350-line **format-agnostic apply pipeline**:
  materialize the playlist tree (incl. smart playlists), tags + MyTag
  categories, colors, ANLZ analysis + artwork from `assets`/`shareRoot`, cues
  (ANLZ first, then the `masterCues` fallback), settings from `settingsDir`,
  missing-file flagging, path remap, save, summary response. It consumes nothing
  but the union of the return slices and the include flags.
- `core.Importer` exists (Tracks + Playlists only), is unused, and does not fit
  this reality.

So the divergence is entirely in the **return signatures and the switch**. The
apply side is already generic. A bundle + interface is what cleans up the rest.

## Proposed design (all in `library`)

A single neutral result carrier every importer returns:

```go
type ImportBundle struct {
    Result      *ImportResult
    Playlists   []PlaylistImport
    Tags        []TagImport
    Colors      []ColorImport
    Cues        []ImportedCue    // renamed from MasterDBCue
    Assets      []ImportedAsset  // renamed from MasterDBAsset
    ShareRoot   string           // ANLZ/artwork root (zip extract, or the db's folder)
    SettingsDir string           // *SETTING.DAT dir
}

type ImportOptions struct {
    Path     string
    Key      string             // decryption key where required (master.db / zip)
    Include  ImportInclude      // tracks, playlists, tags, cues, analysis, artwork, settings
    Progress func(msg string)   // importer -> UI phase updates ("Extracting backup...")
}

type Importer interface {
    Label() string              // "rekordbox XML", "Traktor NML"
    Handles(path string) bool   // by extension
    RequiresKey(path string) bool
    Import(lib *Library, opt ImportOptions) (*ImportBundle, error)
}
```

A tiny registry:

```go
func Importers() []Importer            // rekordboxXML, rekordboxDB, rekordboxBackup, traktorNML
func ImporterFor(path string) Importer // first whose Handles(path) is true, else nil
```

The backup zip becomes a first-class `Importer` that extracts and then delegates
to the master.db importer, filling `ShareRoot` / `SettingsDir` on the bundle.

The whole `switch` in `api.go` collapses to:

```go
imp := library.ImporterFor(req.Path)
if imp == nil { /* 400: unsupported format */ }
if imp.RequiresKey(req.Path) && !isHex64(key) { /* 400: key required */ }
bundle, err := imp.Import(s.Library, opts)
if err != nil { /* 500 */ }
s.applyImportBundle(bundle, inc)   // the existing apply pipeline, extracted verbatim
```

Adding format #4 = implement `Importer`, append to the registry, add a UI radio.
The `applyImportBundle` method and the dialog's include-options stay untouched.

## Decisions

1. **Package `library`, not `core`.** Delete the dead `core.Importer` rather than
   force-fit it. Revisit only if a non-library consumer ever appears.
2. **Rename `MasterDB{Cue,Asset}` -> `Imported{Cue,Asset}`.** They are the shared
   carrier now that Traktor uses them; the rekordbox-era name is misleading.
   Small blast radius (the two library files + the api.go applier).
3. **Extract the ~350-line apply pipeline into `applyImportBundle`.** Pure move,
   no logic change; slims the 530-line handler and benefits every importer
   equally.
4. **Progress callback in `ImportOptions`.** The zip importer needs to emit
   "Extracting backup..."; today that lives in the handler. A `Progress func`
   keeps phase reporting with the importer that knows the phases.

## Phased plan (each phase builds green, no behavior change)

1. Introduce `ImportBundle`; change the three importers to return
   `(*ImportBundle, error)`; the switch fills a bundle. Rename the cue/asset
   types here.
2. Add the `Importer` interface + registry; wrap each importer; collapse the
   switch to `ImporterFor`. The zip case becomes a delegating `Importer`.
3. Extract the apply pipeline into `s.applyImportBundle`.
4. Delete `core.Importer`.

## Risk

Local file parsing + HTTP wiring only. No serving path, no cache format, no
hardware. Unlike the deferred part-3d flow-flip, this needs no CDJ smoke test;
the existing importer unit tests plus a green `library`/`api`/`dbserver` suite
cover it.

## Not now

- **Export.** A parallel `Exporter` interface + `ExportBundle` is the obvious
  follow-up, but the export side (rekordbox USB/PDB) is a single implementation
  today, so it has not hit rule-of-three. Do it when a second export target
  (Engine, M3U) lands.
- **Format #4.** Serato or Engine is the first real customer of this interface,
  and the reason to build it.
