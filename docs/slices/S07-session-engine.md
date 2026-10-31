# S07. Session engine

## Purpose

The state machine at the centre of the app, with **no model and no network**: sessions, drafts, interactions,
the event log, resume, idempotency and the confirm. It is driven by plain method calls in tests and, later, by
the agent and the server. Because it is deterministic, it is where the protocol's guarantees are proven.

## Depends on

S03 and S04. **S08 and S09 depend on it.** It can be built with a scripted stand-in for the agent.

## Where we start

[`../PROTOCOL.md`](../PROTOCOL.md) defines the behaviour; S02 supplies the types; S04 supplies storage.

## Settled

- One socket per session; a per-session event log with a strictly increasing `seq`; persisted and live-only
  events; resume from `last_seq` or a full-state rebuild if compacted past it.
- The draft syncs as a **full snapshot with a version**; a stale `base_version` is rejected with the current
  snapshot; the first `confirm` wins when two devices are open.
- One mechanism for questions: `interaction_request` and `interaction_response`.
- `client_msg_id` idempotency; one message queued per session; limits from the table in
  [`../STANDARDS.md`](../STANDARDS.md).
- A draft never expires; a session closes on save or discard; an entry session is resumable until then.
- Editing a saved entry is a draft revision; confirm stores the previous version first.

## Scope

1. **S07.1 Lifecycle.** Create an entry or general session, state transitions (active, saved, discarded), the
   Drafts list with each draft's **waiting reason** (needs a rating, web fields to accept, pick which one).
2. **S07.2 Interactions.** Open, resolve, withdraw and restore interactions of every kind, including `describe`
   and `lookup_failed`; an interaction survives a restart.
3. **S07.3 Draft operations.** `draft_edit`, `accept_unverified`, `include_left_out`, `confirm`, `discard`,
   `stop`, each with optimistic concurrency and the required-to-confirm rule from the domain guards.
4. **S07.4 Event log.** Append, replay since `N`, `hello` construction with `server_version`, `full_state`
   fallback, compaction at turn end, retention.
5. **S07.5 Idempotency and queue.** The `client_msg_id` ledger returning the first outcome, the one-deep queue,
   a second message refused with a stable code.
6. **S07.6 Revisit and edit.** Detect an existing work, offer new entry or edit, create a draft revision of a
   saved entry, and write the revision on confirm in one transaction. Soft delete and restore.
7. **S07.7 Limits.** Enforce the session and message limits with the defined outcomes.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | A scripted turn produces events in order; confirm saves exactly once; resume from any `seq` yields the same state as watching live |
| Bad | A stale `base_version` is rejected with a fresh snapshot and no change; a replayed `client_msg_id` has one effect; `confirm` with no rating or a body not made of the user's words is refused; a message over the limit is refused; two simultaneous `confirm`s produce one `saved` |
| Sad | The process is killed mid-turn and restarted: the draft, the open interaction and the log survive and resume; the database is locked or the disk is full: a stable error and no partial state; a client that never reconnects leaves the session intact |

**Properties:** resume equals live from every cut point; `seq` is strictly increasing under concurrent clients;
idempotency (applying the same message twice equals once); a revision exists exactly when a saved entry was
confirmed over. Run under `-race` with a leak check.

## Gates

None.

## Open questions

- The Drafts list's waiting-reason vocabulary (proposed from the mockups: needs a rating, web fields to accept,
  waiting for you to pick, ready to save).

## Done when

Every behaviour in [`../PROTOCOL.md`](../PROTOCOL.md) section 6 has a test, the golden examples from S02 can all
be produced by the engine, and a killed-and-restarted session resumes with nothing lost.
