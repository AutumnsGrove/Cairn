# S02. Contract first

## Purpose

Write the contract before the code, so the backend and the frontend can be built by different agents at the
same time against the same thing. Produce the OpenAPI document for REST, the JSON Schema for the WebSocket,
generated Go and TypeScript types, golden example messages for every state in the mockups, and the drift
check that keeps them honest.

## Depends on

S00. **Runs in parallel with S01 and S03.** Unblocks S09 and S10.

## Where we start

[`../PROTOCOL.md`](../PROTOCOL.md) describes the events and client messages in prose and tables, and
[`../UPDATE.md`](../UPDATE.md) lists the update endpoints. No schema exists.

## Settled

- The envelope, every server event and client message, the interaction kinds (`choose_medium`,
  `choose_candidate`, `describe`, `question_form`, `revisit_choice`, `fill_gap`, `lookup_failed`), persisted and
  live-only events, idempotency, resume ([`../PROTOCOL.md`](../PROTOCOL.md)).
- Contract first, generated types, committed outputs, a CI drift check ([`../STANDARDS.md`](../STANDARDS.md) 4).
- The error envelope `{code, message, detail?}`, cursor pagination, RFC 3339 instants.
- The draft card shape is the entry card spec ([`../ENTRY_CARD.md`](../ENTRY_CARD.md)).

## Scope

1. **S02.1 Events schema.** `api/events.schema.json` covering every server event and client message, with the
   persisted or live-only flag, the interaction payloads, and the draft snapshot shape (blocks and states from
   the entry card spec, including `unverified`, `image-read`, `left_out`, `fidelity`).
2. **S02.2 REST.** `api/openapi.yaml` for sessions, entries (read, revisions, soft delete), works, search, media
   upload, version and update, restart, services, export. Cursor pagination, the error envelope, and the stable
   error code registry.
3. **S02.3 Generators.** Go types and server stubs, TypeScript types and a typed client, produced by pinned
   build-time tools (*assumed: `oapi-codegen`, `openapi-typescript` and `json-schema-to-typescript`; confirm*).
   `make gen`, committed output, and the **drift** CI job.
4. **S02.4 Golden examples.** A JSON example for every message and every interaction, **named after the
   mockup frame it represents** (for example `card/web-fields`, `system/offline`). Both streams test against
   them: the backend must be able to produce each one, and the frontend must render each one.
5. **S02.5 Contract validator.** A small Go and TypeScript helper that validates a message against the schema,
   used by the server tests, the Vitest tests and the Playwright tests.
6. **S02.6 Error codes.** One registry of stable codes (refused message too long, stale version, rate limited,
   not found, unavailable, and so on) with the plain-worded UI copy that maps to each.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | Every golden example validates; generated types compile in both languages; `make gen` is a no-op on a clean tree |
| Bad | Malformed variants of each message (wrong type, missing field, extra field, oversized string, deep nesting) are rejected by the validator; the schema decoder is fuzzed |
| Sad | An unknown event type from a newer server is tolerated by the client (logged, ignored); a truncated frame is a decode error, not a panic |

## Gates

None.

## Open questions

- *Assumed, confirm:* the generators named in S02.3; REST under `/api/v1` kept internal.
- Whether to model the WebSocket in AsyncAPI instead of plain JSON Schema (recommended: JSON Schema, simpler).

## Done when

A frontend agent can build any screen from the generated types and a golden example without reading server
code, a backend agent can build any handler from the schema without reading UI code, and CI fails if either
drifts.
