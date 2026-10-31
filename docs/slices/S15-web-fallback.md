# S15. Web fallback

## Purpose

Let Cairn find what the APIs miss, through the colocated Polaris, without ever letting a web guess become a fact
on its own. Add `lookup_web`, the free-form escape hatch, the unverified-field flow with its source panel and
Accept, and the **Ask Polaris** button on a failed lookup.

## Depends on

S08 (the agent), S06 (providers, for the failure cards), S11 (the card) and the S01.3 findings about Polaris's
endpoint and networking. Phase 2.

## Where we start

Polaris exposes an ask API (`gateway.AskRequest` and `AskResponse`, with `source`, `citations`, `cost_usd`). The
path and the container networking are confirmed in S01.3. The card's unverified markers exist as designed states
in S11 but are inert.

## Settled

- Two tools: **`lookup_web`** (structured arguments: title, medium, creator, the field wanted; the default) and
  **`ask_polaris_freeform`** (a free prompt, logged only, no confirm tap).
- Calls carry `source: cairn` and live as normal Polaris threads (its sidebar only shows its own source); the
  thread id and citations are stored with the snapshot.
- Web-derived fields are **proposed, never canonical**: shown unverified with the source URL and a snippet,
  accepted with one tap, with an Accept all.
- `lookup_web` runs on its own when the APIs return nothing, and is offered as **Ask Polaris** when an API errors
  or times out; if Polaris is unreachable the button is hidden or disabled with a reason.
- Review text must not reach a structured Polaris request (invariant 5).

## Scope

1. **S15.1 Polaris client.** The confirmed endpoint, `source: cairn`, bounded prompts and responses, timeouts,
   citations parsing, cost capture, and a health probe for the Services screen.
2. **S15.2 `lookup_web`.** A prompt built by code from structured arguments with an **output contract** (labelled
   fields, each with a source URL), parsed defensively; a field without a citation is dropped.
3. **S15.3 `ask_polaris_freeform`.** The escape hatch, always logged with its full prompt.
4. **S15.4 The unverified flow.** Snapshots with `trust = unverified` and the citations, the accept operation (the
   one allowed update to a snapshot), the card markers, the inline source panel, Accept all, and the footer note.
5. **S15.5 The failure card.** Ask Polaris on `lookup_failed`, the disabled-with-reason state, and the Polaris
   row on the Services screen.
6. **S15.6 World fake.** A fake Polaris with answers, citations, an answer containing an injection attempt, a
   slow answer, and unreachable.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | A work no API knows is found; its fields appear unverified with sources; Accept writes them and marks the snapshot accepted |
| Bad | The web answer contains instructions: shown as quoted text and obeyed by nothing; a field without a citation is dropped; a prompt containing review text in structured mode is rejected by a test on the request log; an oversized or malformed answer is refused |
| Sad | Polaris is unreachable (sticky), slow, or errors: the button is disabled with the reason, the user can still log without details; a cancelled lookup leaves no partial canonical data |

**Privacy assertion:** in structured mode the world's request log shows only titles, creators and ids.

## Gates

None. (S01 closed the endpoint question.)

## Open questions

- Whether `ask_polaris_freeform` should ever need a confirm tap (decided: no, logged only; revisit if it feels
  risky).

## Done when

A play with no API match can be logged end to end with web-sourced fields that stay unverified until accepted,
and the failed-lookup card offers three working ways on.
