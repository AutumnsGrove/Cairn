# S09. Server and protocol

## Purpose

The network face of the app: the HTTP server, the WebSocket endpoint and the REST handlers, all implementing
the S02 contract, with the Host and Origin guard in front of everything. After this, the backend is complete
enough for the real frontend to run against.

## Depends on

S02 (the contract), S07 (the session engine) and S08 (the agent). S00 supplies the server skeleton and the
guard. **S13 depends on it.**

## Where we start

S00 serves `/healthz`, the embedded page and the Host and Origin middleware. The contract exists, the engine
and the agent exist, nothing connects them.

## Settled

- `GET /ws/sessions/{id}?last_seq=N`: `hello` first, then replay, then live; a ping every 20 s; a dead client
  after 45 s; frames at most 64 KiB; the connection closed with a stable code when abused.
- REST handlers per the contract: sessions, entries and revisions, works, search (a stub until S16), media upload
  (a stub until S18), version, update and services (S13), export (S19).
- Host and Origin checks on every request and on the upgrade; security headers; the error envelope;
  compression; cursor pagination.
- A turn does not depend on the socket: it continues when the client disappears.

## Scope

1. **S09.1 HTTP.** Routing from the contract, the middleware chain, security headers, compression with
   precompressed embedded assets, request ids, structured logs with no review text.
2. **S09.2 WebSocket.** Upgrade checks, `hello` with `server_version`, replay from `last_seq`, heartbeat,
   dispatch of every client message to the session engine with its `ack`, frame and rate limits, clean shutdown.
3. **S09.3 REST handlers.** Each endpoint with request validation against the schema, bounded bodies, stable
   error codes, and the same guard applied.
4. **S09.4 Contract tests.** Every response and every event is validated against the schema; every golden
   example can be produced.
5. **S09.5 The dev stack.** `cmd/cairn-dev` runs the real service against the world with scripted scenarios, so
   the frontend stream and the end-to-end tests have a stable backend.
6. **S09.6 Lifecycle.** Graceful shutdown that lets a turn finish or hands it to a draft, restart behaviour, and
   readiness.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | A scripted session over a real socket from message to `saved`; reconnect with `last_seq` replays exactly the missed events |
| Bad | A hostile `Origin` or `Host`; an oversized or malformed frame; a replayed `client_msg_id`; a stale `base_version`; an unknown client message type; an upload over the limit; a path traversal in a media id |
| Sad | A client that drops mid-reply; a slow client with a bounded queue (oldest live-only events shed, persisted never); a reconnect storm; the service restarted during a turn; a database error mid-request returns a stable code |

**Fuzz:** the client-message decoder and the frame reader. **Load:** a bounded soak with many reconnects. **A11y
and security headers** are asserted on the static app.

## Gates

None.

## Open questions

None. The rate limit was answered by Autumn on 2026-11-04 and is recorded below.

## Decisions (recorded 2026-11-04)

1. **Client messages are limited to 10 per second and 60 per minute per session.** The limit is per session, not
   per connection, because two devices may share one session. A message over the limit is refused with the stable
   code `rate_limited` and nothing is stored; the `ack` is `rejected` with that code. Five refusals in ten seconds
   closes the socket with a stable code. A human typing on a phone is far below this, and the one-deep queue still
   refuses a second message mid-turn.
2. **The existing caps stay:** a 64 KiB frame, 200 messages per session, and one queued message per session. The
   rate limit is the fourth bound, not a replacement for any of them.
3. **A rate-limited message changes nothing.** It gets no `draft_snapshot`, no event and no ledger row; the client
   can retry later with the same `client_msg_id`.

## Decisions (recorded 2026-11-04, while building)

1. **The WebSocket is ours, not a library.** RFC 6455 is implemented in `internal/server/ws.go`: the handshake, one
   frame reader, one frame writer and the close handshake, with no extensions, no subprotocols and no compression.
   The plan asks for the frame reader to be fuzzed, and STACK.md prefers the standard library, so a dependency would
   have hidden the part this slice must prove. Close codes are stable: 1009 too big, 1007 not UTF-8, 1002 protocol,
   1001 the service is restarting, 4004 no such session, 4008 rate limited or not keeping up.
2. **Persisted events are tailed, never pushed by the writer.** Each socket polls `EventsSince` every 50 ms and a
   live bus carries `activity` and `cost_update`. A turn can therefore be written from any transaction without the
   engine or the agent knowing a socket exists, and a restart loses no event. Persisted events wait in a 256-deep
   queue and a client that cannot drain it is closed so it can replay; live-only events are shed oldest-first.
3. **The wire layer is explicit.** `internal/server/wire.go` turns stored records into the contract's shapes (the
   card with its blocks, left-out segments, unverified fields, tags, fidelity and why sheet). The OpenAPI and event
   schema are validated by tests against real output, which found and fixed five contract bugs: `draft_edit.set`
   said every value is a string (the engine stores ints and bools), `unverifiedField` had no id to accept,
   `Entry.status` was required but not declared, `WorkDetail` used `allOf` on a closed `Work`, and
   `waiting_reason` listed four names the engine never emits.
4. **Turns and the one-deep queue live in memory with the hub**, not in a socket and not in the database. A turn
   survives the connection that asked for it; a queued message is retried by the client's outbox with the same
   `client_msg_id`, which is what the ledger remembers. A stop cancels the turn and writes a `stopped` notice.
5. **Two S07 bugs surfaced and were fixed here:** `Greet(0)` replayed nothing (the protocol says everything after
   `last_seq`, and a fresh connection to a resumed draft needs the conversation), and `discard` did not remember its
   `client_msg_id`, so a retry could not be a duplicate.
6. **The card's why sheet is read from the receipts**: decisions odds and costs from `decision_calls`, turn cost
   from `turn_calls`, tool summaries and sources from the `tool_end` events, tags from `entry_tags`. No text is
   added to a ledger to make the sheet possible.

## What is built, and what is left

Built: the HTTP chain with request ids; the socket with hello, replay from zero or from `last_seq`, full-state
fallback, heartbeat, framing, rate limit, duplicate detection, every client message and its ack, stop and the
one-deep queue; the REST routes for sessions, entries, revisions, soft delete, works, covers, version and services,
with search, upload, export and update as stable `not_implemented` stubs; the wire mapping and its contract tests;
`cairn serve` and the offline `cairn-dev` stack with one scripted review; graceful drain that closes sockets and
waits for a turn.

Left before the slice is done: the slow-client shed and reconnect-storm tests, a restart in the middle of a turn, a
database failure mid-request, and the bounded soak. The happy, bad and sad routes that are written are green,
including a real-socket session from message to `saved`.

## Done when

A frontend agent can open the dev stack and drive the whole logging flow through a real socket, every
contract example validates against live output, and the hostile-request table is green.
