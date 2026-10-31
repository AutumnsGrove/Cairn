# Build brief

Read this **after** `CLAUDE.md` and **before** writing any code. It is the standing instruction for
building Cairn, written the way Ivy's chunk briefs are: the invariants that must hold, the decisions
already made, the traps earlier work found, and the points where you must stop and hand the work back.
**Nothing is built yet.** This file exists so that any agent, starting cold, can pick up a slice and
finish it without asking what the project is.

If this file and another doc disagree, stop (trigger T11) and ask. `PLAN.md` is the source of truth for
what Cairn is; this file is the source of truth for how it is built.

## 0. Read order

1. `CLAUDE.md`, then this file.
2. [`STANDARDS.md`](STANDARDS.md) (code, API, frontend, limits), [`TESTING.md`](TESTING.md),
   [`CI.md`](CI.md).
3. [`slices/README.md`](slices/README.md): the order, the dependency graph and the status table.
4. Your slice's plan in `docs/slices/`, then only the specs it links.
5. For any UI: the matching page in [`../mockups/`](../mockups/) is the spec. Open it in a browser.

The specs a slice may link: `PLAN.md`, `PRODUCT.md`, `DESIGN.md`, [`ENTRY_CARD.md`](ENTRY_CARD.md),
[`PROTOCOL.md`](PROTOCOL.md), [`UPDATE.md`](UPDATE.md), the question banks and
[`decisions.draft.yaml`](decisions.draft.yaml).

## 1. Invariants (a violation is a bug even if every test is green)

1. **My words are mine.** A saved body is made only of text that exists in the user's own messages, or text
   the user typed on the card. No model output is ever saved as an opinion, a rating or a quote.
2. **Nothing saves without the confirm tap.** Drafts are not entries. There is no code path, including the
   general chat and an import, that writes a non-draft entry without a `confirm`.
3. **Local-first.** SQLite before R2, always. A mirror failure is a warning, never a lost review.
4. **Append-only history.** Snapshots, revisions, answers and entries are never updated in place or deleted;
   the **database** rejects it (triggers), not only the code. There is no purge.
5. **Review text goes only where the plan names.** The chat model and the decisions backend see it. Metadata
   APIs and Polaris get titles, creators and ids through structured arguments. The one exception is
   `ask_polaris_freeform`, which is logged. Tests assert this against the world's request log.
6. **Model output is data, never authority.** A probability steers; deterministic code decides anything that
   must be true. Web text is quoted material, never instructions.
7. **Web and image facts are proposed.** They reach the canonical work only after the user's accept tap.
8. **Only my own origin and host.** The `Origin` and `Host` headers are checked on the WebSocket upgrade and on
   every state-changing request. Both, because a rebinding attack makes the two the attacker's name.
9. **Secrets never leave the process.** Not in logs, errors, API bodies, fixtures, or the repo. The repo is
   public.
10. **Synthetic data only in the repo and in CI.** A real review of the user's, a real screenshot, or a real
    API key must never appear in a test, a fixture, a snapshot or a log.
11. **Nothing in CI reaches a live provider, the potato, or a real review.** Live checks are the operator's.
12. **Bounded everything** ([`STANDARDS.md`](STANDARDS.md) 4a): size, count, depth, time, with a defined outcome
    above each limit. Unbounded and silently truncated are both bugs.
13. **Off and down are normal.** The decisions layer, Polaris, TMDB, Open Library and R2 are each optional at
    runtime. A missing or failing one degrades one feature with a visible, plain-worded state, and never
    blocks logging a review.
14. **Pure Go, no cgo** in every build. `go test -race` is the only cgo use.
15. **A mock is replaced in the same change** that makes its real counterpart exist, never left to rot.

## 2. Decisions that are settled (do not re-litigate)

All of `PLAN.md` section 2 is settled. The ones builders most often want to reopen:

- **Nova is embedded** (`agent.New(cfg).Run(...)`), not shelled out to, and Cairn stores its own sessions,
  because consumers must not import nova's `session`, `mode`, `config`, `tui` or `bundled`.
- **"Decisions" is the vendor-neutral name** for the fast typed models. Use nova's `decide` package and
  `Roles`. **Jev appears only in config.**
- **The main model is `deepseek/deepseek-v4.1-flash-1017`**, pinned. Aliases (`-latest`, `~`) are refused.
- **Ratings are integer quarter-stars, 1 to 20.** One overall rating; categories have no stars.
- **The body is extracted spans, never composed.** The span locator is the heart of invariant 1.
- **The Describe step is a closed vocabulary of chips.** The model may point at a chip; it cannot invent one.
- **Dark only.** No light theme. Lexend and Fraunces, self-hosted. Lucide icons, bundled.
- **A work is the Open Library work, not an edition;** an audiobook is the same work with a different
  `format`; a series is metadata.
- **Soft delete only.** The general chat can edit with a confirm card and can never delete.
- **WebSocket with a server-side event log, full draft snapshots with a version, one `interaction` mechanism.**
- **Updates:** host-side watcher, pinned digest, health check, rollback. The container never touches Docker.
- **Port 8893**, loopback only on the potato, `tailscale serve` is the way in; the health check, the
  watcher and Meridian's tile all use `http://localhost:8893/healthz`. Reserved and verified free
  2026-10-30 (8892 is Warbler, 8894 is Coffer). Nothing else may claim it.
- **Backups and the update flow ship in Phase 1.**

## 3. Traps found by earlier work

- **`yes` and `no` are booleans in YAML 1.1.** Quote them as map keys. A 1.1 parser silently turns the quiet
  option of a yes/no check into `false`.
- **Fill and stroke on an SVG `<use>` sprite are overridden by the icon's own rule,** not inherited. Expose
  them as CSS variables with defaults.
- **A rule on an element beats inheritance.** This bit three times in the mockups. Re-render after every CSS
  change and look at it.
- **The decisions model takes text only** and has a 32k-token context; overrun is a clean error, not
  truncation. Clip deliberately, test the edge, and never send an image.
- **Nova's `importcheck` fails on forbidden imports.** The gate is in nova's CI, not ours, so an app that
  reaches into nova's app shell breaks nova's build. Use `replace ../nova` while it is unstable, then a tag.
- **Polaris's ask endpoint path and container networking are unconfirmed** (open question 3). Do not guess.
- **TMDB's image and attribution terms are unread** (open question 2). The cover-caching decision is not
  final until they are.
- **iOS suspends a backgrounded PWA and kills its socket.** Reconnect on `visibilitychange`, resume by
  sequence number, and treat a version mismatch in `hello` as a reload.
- **A float rating drifts** (`4.75` can become `4.749999`). Store the integer.
- **The same message can arrive twice** after a reconnect. Every client message has a `client_msg_id`.
- **Do not trust a green suite for UI.** Render it, look at it, on a phone viewport.
- **Re-verify a claim before relying on it.** OpenRouter's model listing says the pinned model reads images;
  that is metadata, not a test.

## 4. Repository layout and the dependency rule

```
cairn/
  cmd/cairn/             main and the CLI (serve, verify, export, import)
  cmd/cairn-dev/         the offline dev stack: the real service against the world (never shipped)
  api/                   openapi.yaml and events.schema.json: the contract, written first
  internal/
    domain/              pure: rating, dates, spans, title matching, merge, work identity
    store/               SQLite, migrations, triggers, repositories (sqlc)
    banks/               question-bank loader and selection (pure)
    decisions/           registry loader and the pre-read, over nova's decide package
    providers/           tmdb/, openlibrary/, polaris/: clients behind small interfaces
    session/             the session engine: drafts, interactions, event log, resume
    agent/               nova embedding, tools, guardrails, the pipeline
    server/              HTTP, WebSocket, Origin and Host checks, REST, update API
    update/              the app side of the update flow (version, status, results)
    backup/              snapshot, rclone mirror, export, import, verify
    world/               in-process fakes with fault injection (tests and cairn-dev only)
    config/              strict config loading
  config/                shipped defaults: decisions.yaml, questions/*.yaml
  web/                   SvelteKit (Svelte 5), adapter-static, embedded into the binary
  compose/               docker-compose.yml, the host watcher, systemd units
  docs/  mockups/
```

**The dependency rule** (an architecture test fails the build on a violation): `domain` imports nothing
internal. `store`, `banks`, `decisions`, `providers` import `domain` only. `session` imports `domain`,
`store`, `banks`. `agent` imports those plus `decisions` and `providers`. `server` imports `session` and
`agent`. `world` is imported only from `_test.go` files and `cmd/cairn-dev`. Nothing outside `providers`
names a provider endpoint, and nothing outside `agent` imports nova.

## 5. How a slice is worked

1. Read the slice plan. Resolve its open questions with the operator **in a batch, before starting**, and
   record the answers in the plan's Decisions section. A slice with an unanswered question is not ready.
2. **Write the failing tests first** for the happy, bad and sad routes ([`TESTING.md`](TESTING.md) 1), and
   watch them fail for the right reason.
3. Implement until green, then refactor. Keep functions small and the core pure.
4. Run `make check` (format, vet, lint, race tests, a short fuzz smoke, frontend checks). For UI, render the
   mockup frame and the real screen side by side on a phone viewport and compare.
5. Commit in small steps. Present-tense subject under 50 characters, a body that says why. No emojis. Refer
   to the operator as she/her.
6. Update the slice's status in `slices/README.md` and add a line to [`BUILD-LOG.md`](BUILD-LOG.md).
7. If you hit a gate or a trigger, **stop** (section 7).

Branching (assumed, confirm at S00): `main` is protected once CI exists; each slice is a branch and a pull
request that must pass the required checks. Before S00 lands, commits go to `main`.

## 6. Escalation gates

A gate is a committed handoff file, not a chat message, so a reviewer who has seen none of the session can
resume. The operator answers; nothing past the gate proceeds until she does.

| # | When | What you bring |
|---|---|---|
| G1 | S01: **before the first live call that spends money** | Which service, the expected cost in cents, and a request for the key. Ask first; keys are never committed |
| G2 | S01: **before reading any real review or screenshot** | What you need, where it stays (outside the repo), and what will be written down from it (findings only, never content) |
| G3 | S06: before final cover caching | What TMDB's terms say about caching and attribution, and the decision they imply |
| G4 | S08: **before tuning any threshold** | The fixture corpus, the odds table from a real run, the proposed thresholds, and the labelling you need from the operator |
| G5 | S13: **before the first production deploy** | An update and a deliberately failed update with rollback, demonstrated on a scratch container; the secrets plan |
| G6 | S14: **before the first real review is stored on the potato** | A backup that reached R2, a restore tested on a scratch copy, and the receipts screen working |
| G7 | S18: before any image is sent to a model | What leaves the device, the size and count limits, the stored copy, and the operator's yes |
| G8 | End of each phase | The whole phase, for a review, with the live checks listed as the operator's |

**Triggers: stop and hand back if any of these is true.**

- **T1** Any path where review text reaches a metadata API, Polaris (structured mode) or a log.
- **T2** Any write path that stores a non-draft entry without a `confirm`.
- **T3** Any update or delete of a snapshot, revision, answer or entry that the database does not reject.
- **T4** Any model output that sets a rating, a body or a verdict without containment in the user's text.
- **T5** Any web or image fact that becomes canonical without an accept tap.
- **T6** Any request accepted with a hostile `Origin` or `Host`.
- **T7** Any test, fixture or CI path that can reach a live provider or contains a real review or secret.
- **T8** Any limit missing from the limits table, or any unbounded read of data the sender controls.
- **T9** A new dependency without a `STACK.md` entry, or any cgo.
- **T10** A decision, spec or scope change that is not already in `PLAN.md`.
- **T11** This file and another doc disagree.

**How to stop:** write `docs/handoffs/<date>-<gate>-<slug>.md` (what you did, what is true now, what you need,
written for someone who has seen none of the session), commit it, change the slice's status line to
"waiting at <gate>", and send one message beginning `STOP: this needs Autumn:`. Do no other work until she
has answered.

## 7. Definition of done (every slice)

[`TESTING.md`](TESTING.md) section 8, plus:

- The slice's tests cover happy, bad and sad routes, and the second attempt for anything with a boundary.
- CI is green on every required check. `govulncheck` is clean.
- Any mock the slice replaces is gone, with its fixtures.
- Every new limit is in the limits table in [`STANDARDS.md`](STANDARDS.md); every new dependency is in
  `STACK.md`.
- The plan, specs and `slices/README.md` are updated in the same commit as the code.
- **Live checks are the operator's** and are listed as their own pending line, never folded into a slice
  marked done: a real-provider run, the potato, the update flow, resource use, and accuracy on real
  reviews.

## 8. Living documents

| File | Holds |
|---|---|
| [`BUILD-LOG.md`](BUILD-LOG.md) | What was built, when, by which slice, and what was learned. Append-only |
| `../NOTES.md` | Findings and uncertainties, dated |
| `handoffs/` | One file per gate or stop |
| `slices/README.md` | The status table. Keep it true |
