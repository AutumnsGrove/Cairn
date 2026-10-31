# S12. Reading UI

## Purpose

The calm half of the app: everything you look at after the review is written. Home, Drafts, the Library, a
saved entry with its history, and deletion that never erases. Plain rows, no card chrome, quick to scan on a
phone.

## Depends on

S10 and S04's queries through the S09 REST endpoints (or the dev stack). **S13 depends on it** for the Settings
shell.

## Where we start

Mockup pages 03 and 04 and the empty states on page 07; [`../../PLAN.md`](../../PLAN.md) section 2 (Home,
Drafts, Tags).

## Settled

- Home is a launcher: drafts that are waiting, recent entries, a first-run empty state with tap-to-fill
  examples, the composer where the thumb is, and Drafts, Library and Settings in the top bar with a drafts
  badge.
- The Drafts list states what each draft is waiting for; drafts never expire.
- The Library is grouped by month with medium chips, a sort, and cursor pagination; the entry page shows the
  latest entry, the other entries on the same work, details, and earlier versions with Restore as draft.
- Delete exists only in the entry menu, confirms inline with Keep as the default, goes to a Deleted list with
  Restore, and is never offered in chat.
- Search and the Ask button arrive in S16; their places are reserved.

## Scope

1. **S12.1 Home.** The launcher, the first-run state, the composer routing a first message to a new entry
   session, and the badge.
2. **S12.2 Drafts.** The list with waiting reasons and resume.
3. **S12.3 Library.** Groups, chips, sort, pagination, the empty and no-results states.
4. **S12.4 Entry page.** The saved view, other entries on the work, details, the versions screen, restore as a
   draft revision, Edit and Log again.
5. **S12.5 Delete and Deleted.** The menu, the inline confirmation, the Deleted list, Restore.
6. **S12.6 Settings shell.** The settings list and its rows (Updates and Services arrive in S13).

## Tests and exit

| Route | Cases |
|---|---|
| Happy | Each mockup frame on pages 03 and 04 (except chat) has a state, a baseline and an axe pass; resuming a draft returns to its card |
| Bad | A cursor from another query is rejected; a huge title and a long review render without breaking layout; delete is not reachable from chat; a malformed entry id shows a not-found state |
| Sad | The library loading slowly shows skeleton rows, not a spinner in the middle; a failed list shows a retry; a deleted entry reopened by a stale link says so |

## Gates

None.

## Open questions

- The page size and the skeleton row count (proposed: 50 and 8).

## Done when

The reading screens match the mockups on a phone, every empty and error state has a test, and soft delete and
restore are demonstrated through the UI.
