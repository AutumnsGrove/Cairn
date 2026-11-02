# S04. Data layer

## Purpose

SQLite as the source of truth: the schema from the plan, append-only migrations, and **database-level triggers
that make "nothing is hard-deleted" and "history is append-only" true even if the code is wrong**. Typed
repositories over a single writer and a pool of readers.

## Depends on

S03 (the types). **Runs in parallel with S05 and S06.** S07 needs it.

## Where we start

The schema is drafted in [`../../PLAN.md`](../../PLAN.md) section 6. No code exists.

## Settled

- `modernc.org/sqlite`, pure Go; one serialised writer beginning `IMMEDIATE`, a pool of read-only readers.
- Tables: `works`, `work_sources`, `entries`, `answers`, `sessions`, `session_events`, `entry_revisions`,
  `decision_calls`, `tags`, `entry_tags`, `media`, `mirrors`.
- Soft delete via `deleted_at`; triggers abort delete on `entries`, `entry_revisions`, `work_sources`,
  `answers`; `entry_revisions` also rejects update. There is no purge.
- A draft is an entry with `status = draft`; drafts never appear in library queries or the export.
- Ratings are integers; `finished_on` is the phone's local date.

## Scope

1. **S04.1 Migrations.** Append-only, positional (`user_version`), embedded, applied at start inside a
   transaction, with a refusal to start on a database newer than the binary.
2. **S04.2 Schema and triggers.** All tables, indexes, foreign keys on, and the triggers. The one legitimate
   update to a snapshot is **accepting a web field**: a trigger allows changing `trust` from `unverified` to
   `accepted` (and setting `accepted_at`) and forbids every other change to that row (*assumed, confirm*).
3. **S04.3 Repositories.** Typed access (*assumed: `sqlc`; confirm*), one writer, readers read-only; works and
   sources, entries (create draft, save, soft delete, restore), answers, revisions (written in the same
   transaction as the confirm that creates them), media, mirrors, decision calls.
4. **S04.4 Sessions and the event log.** Append with a strictly increasing `seq` per session, read since `N`,
   compaction of text deltas at turn end, retention after close.
5. **S04.5 Queries.** Library listing with cursor pagination, recent entries, the Drafts list with their
   waiting reason, entries on a work, revisions of an entry, deleted entries. Every list query has an
   `EXPLAIN QUERY PLAN` test that asserts an index.
6. **S04.6 Doctor.** An integrity check, a trigger-presence check (the triggers must exist and fire), and a
   file-permission check helper.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | Create, save, edit with a revision, soft delete, restore, list; migrations from empty reach the current version |
| Bad | **Raw SQL** `DELETE` and `UPDATE` on every protected table is rejected by the database, bypassing the repositories; a revision update is rejected; a second `accepted` change to a snapshot is rejected; a SQL-injection-shaped title is stored verbatim and never executed |
| Sad | A locked database, a full disk, a crash mid-transaction (a subprocess killed with SIGKILL, then reopened and checked), a corrupt file detected by the integrity check, a database newer than the binary refuses to start, a cancelled context releases the writer |

**Properties:** the event-log `seq` is strictly increasing under concurrent appenders; a revision is created
exactly when an entry is confirmed over a saved one. **Coverage floor** on the trigger and repository code.

## Gates

None.

## Decisions (answered 2026-10-31, before starting)

- **`sqlc` is used**, as the standards assumed. Plain `.sql` query files, generated typed Go committed, and the
  `drift` job fails the build when the generated code and the queries disagree.
- **`session_events` is kept forever.** It is not history (the conversation lives in `sessions.messages_json`),
  but it is also never pruned: the log is small, and deleting the only record of what a reconnecting client was
  told buys nothing.
- **The accept trigger allows exactly one change**: `trust` from `unverified` to `accepted`, with `accepted_at`
  going from null to a value. Every other update to `work_sources` aborts, and accepting the same row twice
  aborts, because the second accept would be a change of an already-accepted row.

## Built (2026-10-31)

`internal/store` is complete for Phase 1: migrations (now two, both append-only), the schema and its eight
triggers, the doctor with its active probe, the writer/reader split, and repositories over 45 generated queries
for works, sources, entries, answers, revisions, sessions, the event log, tags, media and mirrors. Library and
draft lists are cursor-paginated, and every list query has an `EXPLAIN QUERY PLAN` test that asserts an index
and no temporary sort. Coverage is 85%.

The decisions Autumn made are at the top of this file. Two things the build settled on its own: **a draft is not
a version**, so the first confirm writes no revision, and **accepting a source is an error when it changes
nothing**, because a silent no-op would look like an accepted field that is not accepted.

## Done when

The protected tables cannot be deleted from or altered by any SQL the application, a test or a manual shell can
run, every list query uses an index, and a crash test proves the database is consistent afterwards.
