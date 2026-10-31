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

## Open questions

- *Assumed, confirm:* `sqlc`; the trigger rule for accepting a web field; retention of `session_events` after a
  session closes (proposed: seven days).

## Done when

The protected tables cannot be deleted from or altered by any SQL the application, a test or a manual shell can
run, every list query uses an index, and a crash test proves the database is consistent afterwards.
