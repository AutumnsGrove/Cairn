# S19. Export, import and verify

## Purpose

Make the data outlive the app. A JSON export that does not need Cairn to read, an import that round-trips, a
`cairn verify` that proves the on-disk state is whole, and the Backups and export screen. Rule 7: if this code
vanished tomorrow, the archive should still be readable.

## Depends on

S14 (backups and receipts), S04 (the data), S12 (Settings). Phase 4.

## Where we start

Nightly backups and a restore drill exist from S14. There is no export or import, and no verification command.

## Settled

- Export is JSON: one file per work with its entries, plus a single archive. It includes revisions, answers,
  snapshots and the tags; it excludes drafts. Soft-deleted entries are included and marked.
- The format is the data model, documented so that a reader needs no Cairn code.
- Import round-trips into an empty database and refuses a non-empty one.
- `cairn verify` checks files present, covers and media accounted for, database integrity, the protective
  triggers, and that the archive is readable.

## Scope

1. **S19.1 Export.** Streaming, bounded memory, a manifest with counts and checksums, a version number, the
   documented schema in `docs/EXPORT.md`.
2. **S19.2 Import.** Validate before writing, one transaction, idempotent on retry, a clear refusal on a
   non-empty database or a newer export version.
3. **S19.3 Verify.** The checks above, with a machine-readable result and plain-worded output.
4. **S19.4 The screen.** The full Backups and export screen: receipts, Back up now, Export, the last verify.
5. **S19.5 Independence.** A small standalone reader (a script and a README) that opens the archive without
   Cairn, and a test that runs it.
6. **S19.6 The drill.** An automated restore-and-compare used by CI on a synthetic library, and a documented
   manual drill for the potato.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | Export, wipe a test copy, import, and diff clean; the standalone reader prints the same entries |
| Bad | A truncated, tampered or oversized archive; a manifest whose checksum does not match; an export from a newer version; an archive containing path traversal names or a zip bomb |
| Sad | The disk fills during export or import (nothing half-written is left); a crash mid-import leaves an empty database, not a partial one; verify on a database with a missing trigger or a missing cover reports it |

**Properties:** export then import equals the source library; import is idempotent. **Fuzz:** the archive reader.

## Gates

**G8** at the end of the phase, with an R2 restore the operator has actually performed.

## Open questions

- Compression of the archive (proposed: a single zstd or gzip stream, plain JSON inside).

## Done when

An export, a wipe and an import diff clean, `cairn verify` passes on the potato, the standalone reader works,
and an R2 restore has really happened.
