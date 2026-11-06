# Testing strategy

Operator requirement: **build it strong from the first line.** Test first, test every route, fuzz what
parses untrusted input, and make untested code hard to merge. This follows the standards already set in
Ivy (`ivy/docs/TESTING.md`) and Polaris's live-verification culture, adapted to Sepia. Status: DRAFT
(2026-10-31).

## 0. Principles

1. **Test-driven.** Write the test, watch it fail for the right reason, make it pass, refactor. Never
   write production code without a failing test that asks for it.
2. **Integration-weighted.** Most tests drive the real core against **the world** (section 3): in-process
   fakes for every outside service. Unit tests are for pure, branchy logic: parsing, merging, thresholds.
3. **Every boundary gets all three routes** (section 1). A change is not done until its happy, bad and sad
   routes are tested.
4. **Every bug gets a regression test, and the test must be seen failing without the fix** (revert the
   fix, confirm red, restore).
5. **Fakes for plumbing, the real thing for judgment.** A fake model proves wiring. A real model on the
   fixture corpus proves behaviour. Both are required, and only the fake runs on every push.
6. **Tests encode the safety rules.** Section 5 turns each rule in `PLAN.md` section 11 into an assertion.
7. **Determinism first.** Injected clocks and ID generators, no sleeps, seeded randomness with the seed
   printed on failure.
8. **The mockups are the UI spec.** Every frame in `mockups/` becomes a named end-to-end state with a
   screenshot baseline (section 6).

## 1. The three routes

| Route | Meaning | Examples at the protocol boundary | Examples at the metadata boundary |
|---|---|---|---|
| **Happy** | The expected input, the expected success | A well-formed `message`, a valid `confirm` | TMDB returns one clean match |
| **Bad** | Input that is wrong, hostile or enormous | Malformed JSON, a stale `base_version`, a hostile `Origin`, a 10 MB message, a replayed `client_msg_id` | A response with missing fields, a 50 MB body, HTML where JSON was expected, prompt-injection text in a title |
| **Sad** | The world misbehaves while the input is fine | The socket drops mid-reply, the disk is full, the database is locked, the update restarts the container | A timeout, a 5xx, a rate limit, a sticky outage, a slow stream that never finishes |

Rules:

- **Sad faults are sticky.** An outage that clears on the second try hides retry bugs. Tests assert the
  behaviour on the second and third call too.
- **A cancelled context is a sad route** at every boundary, and gets a test.
- Table-driven tests list the three routes as named rows so a missing route is visible in review.

## 2. Layers

| Layer | Tool | What it covers |
|---|---|---|
| Unit | `go test`, table-driven | Rating parser, span locator and snapper, merge rules, threshold and suppression logic, question selection, the update-view derivation |
| Fuzz | Go native fuzzing, seed corpora in the repo | Section 4 |
| Property | `testing/quick` or `rapid`-style generators | Section 5b |
| Integration | The real core against the world, a temp SQLite | The whole review flow, sessions, resume, revisions, soft delete, export and import, migrations |
| Protocol contract | `httptest` and a WebSocket test client | Every event and client message in [`PROTOCOL.md`](PROTOCOL.md), every REST endpoint |
| Model plumbing | Fake OpenRouter (chat with tool calls, plus `/systemone`) | Tool loop, guardrails, caps, injection handling, cost ledger rows |
| Model judgment (evals) | Real models on the fixture corpus, on demand | Thresholds, extraction quality, hint quality. Costs cents and is logged |
| Frontend unit and component | Vitest and the Svelte testing library | Stores, the socket client and its resume logic, the rating control, the update view |
| End to end | Playwright against the real binary and the world | Every mockup state, on a phone viewport first |
| Visual regression | Playwright screenshots, pixel diff | One baseline per mockup frame |
| Accessibility | axe in Playwright | Contrast, labels, focus order, reduced motion |
| Live | The real potato, real providers | Update flow, Polaris reachability, R2 mirror, resource use. Manual |
| Performance | `go test -bench` with budget assertions | Cold start, library search over a large corpus, resume replay |

## 3. The world

`internal/world` is an in-process set of fakes, driven from Go tests, Playwright, and a dev CLI, so the
whole app runs offline with no keys.

| Fake | Behaviours |
|---|---|
| TMDB, Open Library | Canned works and editions, series and translation edge cases, ids that disagree |
| OpenRouter chat | Scripted tool-call sequences, streaming, a malformed tool call, a model that tries to save an opinion it was not given |
| OpenRouter `/systemone` (decisions) | Scripted odds per check, an error, a timeout, a result that contradicts the deterministic parse |
| Polaris ask | Answers with citations, an answer with an injection attempt in the text, unreachable |
| rclone and R2 | Success, a partial upload, an auth failure |
| Clock, IDs | Injected, so "last night" and "2 h ago" are exact |

Every fake takes **faults**: slow, error, malformed, huge, drop, and sticky versions of each. The world
also **records every outbound request**, which the privacy assertions in section 5 read.

## 4. Fuzz targets

Each target ships a seed corpus of real and hostile cases. New crashers are committed as regression
seeds.

| Target | Why it is fuzzed |
|---|---|
| Rating parser | "4 and a bit", "8/10", "4.3", unicode digits, a very long string, negative and huge numbers |
| Span locator and snapper | The core of "my words are mine": any input must yield only text that exists in the source, or nothing |
| Normaliser and title matcher | Merge and duplicate detection |
| Merge functions | Never lose a field, never panic on conflicting providers |
| WebSocket client-message decoder | Hostile frames, truncated JSON, deep nesting, oversized fields |
| Decisions and question-bank YAML loaders | A bad registry must fail loudly at load, never at use |
| TMDB and Open Library response decoders | Untrusted JSON from the network |
| Import and export reader | A corrupt or malicious archive |
| Host and Origin checks | Odd `Host` and `Origin` headers, null origins, trailing dots, ports, and a rebinding name |

## 5. Safety assertions

Each rule in `PLAN.md` section 11 is a test that fails if the rule is broken.

| Rule | Assertion |
|---|---|
| My words are mine | For any model output, every character of a saved body appears in the user's messages, or the entry is marked typed. `save_entry` refuses an opinion the user did not give |
| Nothing saves without confirm | No code path writes a non-draft entry without a `confirm` message, including via the general chat |
| Source snapshots are append-only | Updating or deleting a snapshot, a revision, an answer or an entry is rejected by the database, not just by the code |
| Web facts are proposed | A web-sourced field never reaches the canonical work without an accept |
| Review text goes only where named | The world's request log shows review text only in the chat and decisions calls, never in a metadata or Polaris request (structured mode) |
| Classifier output never decides | With the decisions fake returning confident wrong answers, a rating or body is still enforced by deterministic code |
| Nothing is hard-deleted | `DELETE` on the protected tables fails at the database |
| Only my own origin and host | A hostile `Origin` on the socket upgrade and on every state-changing request is refused, and a `Host` off the allow-list is refused on every request |
| Pre-read never blocks | A hung or failing decisions call lets the turn complete with a notice |

### 5b. Properties

- **Export then import round-trips** to an identical library.
- **Resume equals live:** replaying events from any `last_seq` yields the same state as watching live.
- **Revisions are append-only and ordered;** restoring never loses a version.
- **Merge is idempotent,** and re-merging never deletes a snapshot.
- **Sequence numbers are strictly increasing** within a session, under concurrent clients.
- **Idempotency:** the same `client_msg_id` applied twice equals applying it once.

## 6. Frontend, end to end, visuals

- Each mockup frame becomes a named Playwright state (for example `card/web-fields`, `system/offline`),
  asserting the content and a pixel baseline, on a phone viewport first and then a laptop.
- Each trust state asserts its **shape or label, not just its colour** (verified, unverified, image-read).
- axe runs on every state. Reduced motion is tested: the progress bar and the pulse stop.
- Offline and reconnect, a sleeping tab, and a version-mismatch reload are driven in a real browser.

## 7. Live checks and evals

- **Evals** run the fixture corpus (about 30 sample messages with expected parse, expected decision
  outcome and expected tool sequence) against the real chat model and the real decisions backend. They
  set and check thresholds, and they are the only place model quality is judged.
- **Live** checks run on the potato: the real update and rollback, Polaris and R2, and resource use. They
  are manual, in a protected environment, never required for a merge, and never triggered by a PR.

## 8. Definition of done (per change)

- [ ] The test was written first and seen failing for the right reason.
- [ ] Happy, bad and sad routes for every boundary the change touches, including a cancelled context and a
      second call.
- [ ] A regression test for any bug, verified failing without the fix.
- [ ] Anything that parses untrusted input has a fuzz target and a seed corpus.
- [ ] An integration test through the real boundary (SQLite, HTTP, WebSocket), not only mocks.
- [ ] For UI: the Playwright state, its baseline, and an axe pass, on a phone viewport.
- [ ] For anything a model touches: a fake-model plumbing test plus the relevant safety assertion, and an
      eval run if instructions, thresholds or models changed.
- [ ] A live check on the potato when it touches update, deployment, backup or resource use, recorded in
      the commit.
- [ ] Docs updated, and `go vet`, `staticcheck`, `govulncheck` and the race detector clean.
