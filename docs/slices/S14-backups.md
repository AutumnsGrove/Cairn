# S14. Backups

## Purpose

Protect the first real review from the day it exists. A nightly SQLite snapshot, a mirror to R2 through the
`rclone` remote already on the box, receipts the app can show, and a restore that has actually been performed.
Local-first: R2 is a mirror, never the source of truth, and a failed mirror is a warning, never a lost review.

## Depends on

S13 (it runs where Sepia runs) and S04 (the `mirrors` table). It is the last slice of Phase 1.

## Where we start

The `rclone` remote `r2:` is configured for Vaultwarden (`vaultwarden-setup/vaultwarden-backup.sh` is the
precedent). The bucket `r2:sepia-backups` does not exist yet.

## Settled

- Nightly at 03:30: a SQLite snapshot into `data/backups/` (rotating), then `rclone sync` to `r2:sepia-backups/`.
- Covers and media are mirrored too; receipts go in `mirrors`; failures show as a warning in the app.
- Soft delete means every backup holds every copy; there is no purge.
- A restore is an offline command, never an HTTP endpoint (a database file cannot safely be swapped under a
  running connection).

## Scope

1. **S14.1 Snapshot.** An online backup (the SQLite backup API or `VACUUM INTO`), an integrity check on the
   result, rotation, and a size and age policy.
2. **S14.2 Mirror.** `rclone` invoked with a timeout and bounded output, partial-upload detection, a receipt row
   for every run (`target`, `status`, `bytes`), and a clear failure state.
3. **S14.3 Schedule.** *Open:* a host systemd timer calling the app, or an in-app scheduler. Proposed: a host
   timer, the Vaultwarden precedent, so a stuck container cannot silently stop backups.
4. **S14.4 The screen.** The Backups part of "Backups and export": the last run, the size, any error, and a
   Back up now button.
5. **S14.5 Restore.** `sepia restore` into a scratch location, with a verification that the restored library
   equals the source, and a documented drill.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | A nightly run produces a verified snapshot, mirrors it, and records a receipt |
| Bad | A corrupt snapshot is detected and never mirrored as good; `rclone` output is bounded and cannot inject into logs; a restore of a tampered snapshot is refused |
| Sad | R2 unreachable, an auth failure, a partial upload, a full disk, a snapshot taken mid-write, the process killed during a run: each ends in a visible state and no corrupted backup replaces a good one |

**Property:** restore then compare equals the source library. **The drill:** a restore exercised on a scratch
copy is part of "done".

## Gates

**G6** before the first real review is stored on the potato: a backup that reached R2, a restore tested on a
scratch copy, and the receipts screen working.

## Open questions

- Host timer versus in-app scheduler (proposed: host timer).
- Retention (proposed: seven daily, four weekly, kept in R2).

## Done when

A backup has reached R2 and appeared in the app, a restore has been performed for real, and the operator has
cleared G6.
