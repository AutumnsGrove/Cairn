# The session protocol

How the browser and the Go service talk during a session. Specified before code because the UI mockups
must show every state this protocol can produce. Decisions are from the 2026-10-31 planning session.

Principles, in the order they matter:

1. **A small Cairn vocabulary.** The browser never sees nova's events. The service translates them
   (section 7), so a nova change cannot break the frontend.
2. **The server owns the state.** The socket is a window onto a session, not the session itself. A turn
   keeps running if the phone sleeps, and a reconnecting client catches up.
3. **Snapshots, not patches.** The draft card is small, so every change sends the whole card with a
   version number.
4. **Everything the UI asks the user is one mechanism:** an `interaction_request` answered by an
   `interaction_response`.

## 1. Connection

- `GET /ws/sessions/{session_id}?last_seq=N` upgrades to a WebSocket. `last_seq` is the highest sequence
  number the client has seen (0 for a fresh connection).
- **One socket per open session.** Library, drafts count, search and entry pages are plain REST that
  refresh when the app regains focus. A new session is created with `POST /api/sessions`
  (`{kind: "entry" | "general"}`) before connecting.
- **Origin check on the upgrade, and on every state-changing request.** Browsers do not apply the
  same-origin rule to WebSockets, and this app has no login, so a hostile page opened on a phone that is
  on the tailnet could otherwise reach it. The service accepts only its own origin
  (`https://lepotato.taild95077.ts.net:8893`, and `http://localhost:*` in dev). JSON POSTs also require
  `Content-Type: application/json`, which a cross-site form cannot send.
- **Heartbeat.** The server sends a ping every 20 s. A client that hears nothing for 45 s treats the
  socket as dead and reconnects.
- **Reconnect.** Backoff of 0.5, 1, 2, 4, 8 s, capped at 15 s, with jitter, and an immediate attempt when
  the page becomes visible again (the phone woke up). Always with the current `last_seq`.
- **A turn does not depend on the socket.** If the client disappears mid-reply, the server finishes the
  turn and logs the events. Stopping is explicit (`stop`).

## 2. Envelope

Every server message is one JSON object:

```json
{ "seq": 41, "type": "text_delta", "session_id": "s_8f2", "ts": "2026-10-31T14:03:22Z", "data": { } }
```

- **Persisted events** carry a `seq`, strictly increasing within the session, and are replayed on resume.
- **Live-only events** have `"seq": null`, are never replayed, and a client must treat their absence as
  normal. (The same convention Polaris uses for `cost_update`.)

Every client message carries a `client_msg_id` (a UUID the client makes), so a retry after a reconnect
cannot be applied twice.

## 3. Server to client

| Type | Persisted | Data | Meaning |
|---|---|---|---|
| `hello` | no | `session`, `state`, `draft` (latest snapshot or null), `open_interactions[]`, `head_seq`, `full_state` | Sent first on every connect. If `full_state` is true the client rebuilds from this and ignores replay (the log was compacted past `last_seq`) |
| `user_message` | yes | `message_id`, `client_msg_id`, `text` | The persisted ID of what I typed, sent as soon as it is saved, so the UI can show it and retry or edit from it |
| `preread` | yes | `odds` (every check), `fired`, `cost_usd` | The decisions result for this message. Feeds the "why" sheet |
| `activity` | no | `label` (for example "Reading", "Looking up TMDB") | Drives the small progress indicator while the agent works |
| `assistant_start` | yes | `message_id` | A reply is beginning |
| `text_delta` | yes | `message_id`, `text` | One chunk of the reply |
| `assistant_end` | yes | `message_id` | The reply is complete |
| `tool_start` | yes | `tool`, `label` | A tool call began; `label` is a friendly string |
| `tool_end` | yes | `tool`, `ok`, `inputs`, `summary`, `sources[]`, `cost_usd` | The call finished. `inputs` and `sources` feed the "why" sheet; `summary` is one line |
| `interaction_request` | yes | `interaction_id`, `kind`, `payload` | The service needs something from me (section 4) |
| `interaction_resolved` | yes | `interaction_id`, `by` (`user` or `server`) | The request was answered or withdrawn; other devices clear it |
| `draft_snapshot` | yes | `version`, `draft` | The whole entry card, after any change. See [`ENTRY_CARD.md`](ENTRY_CARD.md) |
| `saved` | yes | `entry_id`, `version` | The entry was committed. The session closes after this |
| `discarded` | yes | none | The draft was discarded. The session closes after this |
| `ack` | no | `client_msg_id`, `status` (`accepted`, `duplicate`, `rejected`), `reason?` | The reply to a client message. A `duplicate` returns the original outcome |
| `cost_update` | no | `session_total_usd` | The running total for the session. Always the full total, never a delta; the UI overwrites |
| `notice` | yes | `level` (`info`, `warn`), `code`, `text` | Something the user should know, such as "the pre-read was unavailable for this message" |
| `error` | yes | `code`, `message`, `retryable` | A failure. `retryable` tells the UI whether to offer Try again |
| `queue_update` | no | `queued` (count) | A message I sent mid-turn is waiting for the current turn to finish |

## 4. Interactions

One mechanism for every question the service asks me. `interaction_request.kind` decides the payload:

| `kind` | When | Payload | Response `value` |
|---|---|---|---|
| `choose_medium` | The medium read is split | `options[]` (for example book, film) | the chosen option |
| `choose_candidate` | After a lookup | `candidates[]` (3 to 5, each with provider, id, title, year, creator, medium, thumb) and a "none of these" | a candidate id, or `none` |
| `question_form` | The question bank was filtered | `questions[]` (id, prompt, type, options, optional `hint`) | `answers[]`, with skipped questions marked, or `skip_all` |
| `revisit_choice` | The work is already in the library | the previous entry's summary | `new_entry` or `edit_existing` |
| `fill_gap` | A required piece is missing and cannot be parsed (a rating, a few words of review) | `field`, `prompt` | the value |

The `hint` on a question is the suggested answer: shown as a tappable hint, **never pre-selected**.

An interaction stays open until resolved. Drafts never expire, so neither do interactions. If I type a
normal message while one is open, the session bias rule in
[`decisions.draft.yaml`](decisions.draft.yaml) decides whether it answers the interaction or starts
something new.

## 5. Client to server

| Type | Data | Effect |
|---|---|---|
| `message` | `text`, `client_date`, `tz` | A normal chat message. The date and timezone make "last night" resolve against where I am |
| `interaction_response` | `interaction_id`, `value` | Answers an open interaction; this is the tool result the agent is waiting on |
| `draft_edit` | `base_version`, `set` (field to value map) | Direct edit of the card. Stale `base_version` means `rejected` plus a fresh `draft_snapshot` |
| `accept_unverified` | `base_version`, `fields[]` or `all` | Accept web-derived or manual fields into the canonical work |
| `include_left_out` | `base_version`, `segment_id` | Move a left-out segment into the body |
| `confirm` | `base_version` | The one commit. Rejected if the version is stale or the card does not satisfy section 2 of `ENTRY_CARD.md` |
| `discard` | none | Throw the draft away; the session closes |
| `stop` | none | Cancel the turn in flight |
| `ping` | none | Optional keepalive from the client |

Every message gets an `ack`. A `rejected` ack always says why, and for version conflicts is followed by
the current `draft_snapshot`.

## 6. Ordering, concurrency, resume

- **One turn at a time per session.** A `message` that arrives mid-turn is queued behind it (shown with a
  `queue_update`), one deep. A second one is rejected until the first is taken.
- **Two devices on one session is fine.** Every event goes to both. The draft `version` increments on every
  change, whoever made it. An edit made against an old version is rejected and the editor is sent the
  current snapshot. The first `confirm` wins; the other device then receives `saved`.
- **Idempotency.** The server remembers each `client_msg_id` for the life of the session. A repeat gets
  `ack: duplicate` carrying the first outcome, and changes nothing.
- **Resume.** On connect with `last_seq=N`, the server sends `hello`, then replays every persisted event
  with `seq > N` in order, then streams live. If it cannot (the log was compacted past N), `hello` says
  `full_state: true` and the client rebuilds from the snapshot in `hello`.
- **Log size.** `text_delta` events are compacted into the final message when a turn ends. The log is for
  resume only; the conversation itself is stored in `sessions.messages_json` and survives.

## 7. How nova's events map

The service translates inside, so the browser sees only section 3.

| Nova | Cairn |
|---|---|
| `text_delta` | `text_delta` |
| `message_start` / `message_end` | `assistant_start` / `assistant_end` |
| `tool_start` / `tool_end` | `tool_start` / `tool_end` (inputs and sources attached) |
| `ask_reviewer`, `media_lookup` results needing a choice | `interaction_request` |
| `usage` | `cost_update` |
| `retry`, `compaction` | `notice` |
| `error` | `error` |
| `thinking_*` | dropped (not shown in v1) |

The pre-read and the fidelity check are not nova events. The service runs them through nova's `decide`
package and emits `preread` itself.

## 8. REST outline

Plain requests, all behind the same origin check. Detailed shapes belong with the code, not here.

- `POST /api/sessions`, `GET /api/sessions/{id}`, `GET /api/sessions?state=active` (the Drafts list)
- `GET /api/entries`, `GET /api/entries/{id}`, `GET /api/entries/{id}/revisions`,
  `POST /api/entries/{id}/delete` (soft; plain entry view only)
- `GET /api/works/{id}`, `GET /api/search?q=`
- `GET /covers/{id}` (static), `GET /healthz`, `POST /api/export`

## 9. Fixture cases to write

- reconnect with `last_seq` mid-reply: no lost or duplicated text;
- reconnect after compaction: `full_state` rebuild matches the live state;
- the same `client_msg_id` sent twice: one effect;
- a stale `base_version` edit: rejected, snapshot returned, no change;
- two clients, concurrent `confirm`: exactly one `saved`;
- a message arriving mid-turn: queued, then processed in order;
- a hostile `Origin` on the upgrade and on a POST: refused;
- the decisions backend unavailable: a `notice`, and the turn still completes.
