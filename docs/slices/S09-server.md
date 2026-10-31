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

- The rate limit on client messages per second (proposed: generous, only to bound abuse).

## Done when

A frontend agent can open the dev stack and drive the whole logging flow through a real socket, every
contract example validates against live output, and the hostile-request table is green.
