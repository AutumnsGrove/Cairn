# Standards

How Cairn's code is written and reviewed. Adapted from Ivy's `STANDARDS.md` (the house rules) with
Cairn-specific limits and exposure rules. The testing standard is in [`TESTING.md`](TESTING.md), CI in
[`CI.md`](CI.md), and the build process in [`BUILD.md`](BUILD.md). Status: DRAFT (2026-10-31).

## 1. Workflow: test-driven, always

Write the test, watch it fail for the right reason, make it pass, refactor. Every boundary gets happy, bad
and sad routes in the same red-green step, not afterwards. A bug gets a regression test that is seen
failing without the fix. See [`TESTING.md`](TESTING.md).

## 2. Go code standards

- **Version:** current stable Go, pinned in `go.mod`; upgrade deliberately and run the whole suite.
- **Formatting and lint, CI-enforced, zero warnings:** `gofumpt`, `go vet`, `staticcheck`, `golangci-lint`
  (errcheck, errorlint, gosec, bodyclose, noctx, contextcheck, exhaustive, revive on exported names),
  `govulncheck`. Run locally before every commit (`make check`).
- **Pure Go only.** `CGO_ENABLED=0` in every build. The one exception is `go test -race`, which needs cgo for
  the race runtime; the `nocgo` job still proves nothing of ours needs it. A dependency that needs cgo is
  rejected, not special-cased. SQLite is `modernc.org/sqlite`.
- **Standard library first:** `net/http` with the pattern mux, `log/slog`, `database/sql`, `encoding/json`,
  `embed`, `context`, `errors`, `slices`, `maps`. A dependency must earn its place with a sentence in
  `STACK.md` (version, licence, reason).
- **Small packages with narrow interfaces.** Interfaces are defined where they are *used*, kept to one to
  three methods, and exist only where there is a real and a fake implementation or a seam we need. No
  `utils` or `common`.
- **Functional core, imperative shell.** Pure functions transform data (parse a rating, locate a span,
  merge works, select questions, derive a view); a thin shell does I/O. Packages that do I/O take their
  dependencies (clock, IDs, HTTP client, DB) as arguments, never from globals or `init()`.
- **Errors** wrapped with `%w` and context, sentinel or typed errors only where callers branch, `errors.Is`
  and `errors.As` to test. Never ignore an error, never log-and-return the same one. User-facing errors carry
  a stable code; internal detail goes to logs only.
- **Context everywhere I/O happens,** first parameter, honoured for cancellation and deadlines. No goroutine
  without an owner and a shutdown path (`errgroup`); every goroutine is covered by the leak check.
- **Concurrency:** one SQLite writer (a single serialised write connection beginning transactions
  `IMMEDIATE`) and a pool of readers; never hold a transaction across network I/O; run the whole suite under
  `-race`.
- **SQL:** parameterised only. `sqlc` generates typed Go from plain `.sql` files (assumed from Ivy, confirm at
  S04), generated code committed with a CI drift check. Migrations are append-only and positional. Every query
  that can run on a large table has an `EXPLAIN QUERY PLAN` test asserting it uses an index. Triggers that
  forbid update and delete are part of the schema and tested.
- **Logging:** `slog`, structured, no secrets, and **no review text, titles of the user's reviews, screenshots
  or message content at info level**; the ledger stores ids, counts and costs, not text.
- **Time and randomness** come from an injected `Clock` and ID source. "Last night" is resolved against the
  phone's date and timezone, which the client sends.
- **Naming and layout:** short lowercase package names, documented exported names, no stuttering.
  Functions around 50 lines as a guide, one level of abstraction.
- **Comments explain why, not what.**
- **Security defaults:** treat all model output, provider JSON, web text, screenshots and request bodies as
  hostile; validate at the edge; secrets only from environment or files, never in the DB, logs or errors.

## 3. Failure paths are first-class

A feature is specified, built and tested for how it behaves when things are slow, wrong, repeated, huge or
never finish, at the same time as when they go well.

1. **Bound everything** (the limits table below), enforced at the edge with a defined outcome above it.
2. **Nothing blocks without a deadline.** Every network call, child process, lock wait and channel receive
   honours a context or a timeout. A test proves a stalled peer cannot hang the caller.
3. **No silent failure.** An error is returned or recorded where the operator will see it (a stable code, a
   UI state). "I found nothing" and "I could not look" are different results and stay distinguishable.
4. **Large data is streamed,** with a bounded buffer: images, provider bodies, exports.
5. **Repeat, resume and recover.** Operations are idempotent. The second attempt, the attempt after partial
   progress and the attempt after a crash are tested. A fault that models an outage lasts until cleared.
6. **Hostile input costs bounded time and memory.** Parsers have depth, count and size caps and a fuzz target
   that asserts they return, and return in time.
7. **The zero value is the safe one.** Defaults fail closed; unknown config keys are rejected; flags are
   named for the unsafe thing.
8. **Slow is a state, not an error.** It produces a defined, visible state (progress, retry with backoff,
   "unreachable") tested with the world's latency and unreachable faults.

For every new boundary the tests cover: **error or timeout, second call, limit exceeded, cancellation,
hostile or huge input.**

### Limits

Starting values, chosen to be generous for one person and safe on a small board. A new limit gets a row here
in the commit that introduces it, with its outcome above the limit.

| What | Limit | Outcome above it |
|---|---|---|
| A user message (text) | 8,000 characters | Refused with a stable code; nothing is stored |
| Images per message | 6, each up to 10 MB | Extra or oversize images refused, the rest kept |
| Session length | 200 messages | The session is closed with a notice; the draft is kept |
| Decisions state per call | 12,000 characters of message, 2,000 for a pending question or a work title, 24k tokens in all | Clipped deliberately, never sent whole; the clip is recorded |
| Decisions questions per call | 25 | Registry load fails if exceeded |
| Decisions deadline | 2.5 s | The turn proceeds without a pre-read, with a notice |
| Bank file | 256 KiB, 12 categories, 80 chips per category, 25 questions | Load fails with the file and key named; the previous config stays live |
| Decisions config file | 256 KiB, 2,000 characters per instruction, 300 per option description | Load fails with the file and key named; the previous config stays live |
| Config reload poll | every 2 s, by mtime and size | A change is picked up on the next tick; a broken file keeps the old config and raises one notice |
| Spans per body | 40 | Extras dropped and shown under Left out |
| Body or verdict | 8,000 and 200 characters | Refused with a stable code |
| Tool arguments per call | 8 KiB | Refused with a stable code; the tool does not run |
| Span source per body | 64 KiB | The extract cannot be built; typing the body is still allowed |
| Corpus file | 1 MiB | Load fails with the file named |
| Candidates per lookup | 5 | Truncated, the best first |
| Provider response body | 2 MiB | Aborted; counts as a sad-route failure |
| Provider call deadline | 10 s | Typed timeout; the agent shows lookup_failed and logging carries on |
| Provider pacing | TMDB 10 calls a second, Open Library 1 | Calls wait; a 429 waits for Retry-After, or backs off by default when it has none |
| Provider lookup cache | 128 entries, 5 minutes | A stale answer is served for at most 5 minutes; a restart clears it. A fetch is not cached: its document is too big to hold 128 of |
| Title from a provider | 300 characters, control and bidi characters removed | Cut at a rune boundary before it is shown or merged |
| Cover freshness | 180 days (TMDB's cache cap is 6 months) | Re-fetched when it is next read; no background job |
| Cover download | 2 MiB, 4,000 px on a side | Skipped; the work keeps its placeholder |
| Single questions per entry | 3 | Registry selection stops at 3 |
| Question rounds per session | 4 | The agent stops asking and builds the draft |
| Tool steps per turn | 12 | The turn ends with a notice; the draft is kept |
| Tokens per turn | 60k in, 8k out | The turn ends with a notice |
| Turn deadline | 120 s | Cancelled; the message is kept as a draft |
| Messages queued per session | 1 | A second is refused until the first is taken |
| Client messages | 10 per second and 60 per minute per session | Refused with `rate_limited`; five refusals in 10 s closes the socket with a stable code |
| WebSocket frame | 64 KiB | The connection is closed with a stable code |
| Polaris ask | 2,000 characters in, 90 s | Refused or timed out, offered as a retry |
| JSON request body | 64 KiB | Refused with `payload_too_large` |
| WebSocket outbound queue | 256 frames per connection | A persisted event that cannot be queued closes the socket so the client replays from its `last_seq`; live-only events are shed oldest first |
| List page size | 50 default, 100 max | Capped |
| Search query | 200 characters | Refused |

## 4. API standards

- **Contract first.** `api/openapi.yaml` (REST) and `api/events.schema.json` (the WebSocket events and client
  messages) are written before the code. Go and TypeScript types are generated from them, generators are
  build-time tools, outputs are committed, and CI fails on drift. Tests are written against the contract.
- Cursor pagination for every list, stable ordering, explicit `limit` caps. Never offset.
- **Idempotency:** every mutating client message carries a `client_msg_id`, and the server returns the first
  outcome for a repeat.
- **Errors:** `{code, message, detail?}` with a matching HTTP status; the same codes the UI maps to its copy.
  Unknown `/api` paths and wrong methods answer the same envelope.
- **Instants are RFC 3339 in UTC;** the API never sends a display time. The browser formats with an injectable
  clock and zone. `finished_on` is the phone's local date.
- **Headers:** every API reply carries `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `X-Frame-Options: SAMEORIGIN`, and a deny-all CSP. The static app carries a
  strict CSP with no inline script and no third-party origins.
- Every response is compressed and carries correct `ETag` and `Cache-Control` for static assets.

## 5. Frontend standards

- **SvelteKit with Svelte 5 (runes), `adapter-static`, TypeScript strict, pnpm** with a committed lockfile and
  `--frozen-lockfile` in CI. Pure CSS with custom properties, the tokens from `mockups/cairn.css` as the
  source; no raw colour or pixel literals outside the token file. No Tailwind, no CSS-in-JS.
- **One typed client module;** components never call `fetch` or open a socket directly. Types come from the
  generated contract, never hand-copied.
- **The socket client is a tested state machine:** connect, resume with `last_seq`, offline outbox, heartbeat,
  backoff, and reload on a version mismatch. It is tested in Vitest with a fake socket and in Playwright with
  a real one.
- **Fonts and icons are bundled** (Lexend and Fraunces as woff2, Lucide through `@lucide/svelte`). Nothing
  loads from outside. Fill and stroke on icons are CSS variables.
- **Dependencies are rare and small;** each needs a `STACK.md` entry and its compressed cost noted. Prefer
  platform features.
- **Accessibility is part of done:** labels, focus order, contrast, reduced motion, 44 px touch targets, and
  state never carried by colour alone. axe runs in every end-to-end state.
- **The mockups are the spec.** A screen is done when it matches its frame on a phone viewport and the
  Playwright baseline for that frame exists.
- Browser targets: current Safari on iOS (primary), current Safari on macOS, current Firefox, Chromium for CI.
- ESLint, Prettier and `svelte-check` with zero warnings.

## 6. Access and exposure

- **Never public.** No Tailscale Funnel, no `0.0.0.0` on a public interface. The service listens on
  `127.0.0.1` behind `tailscale serve`. There are no accounts, and authentication is a single middleware seam.
- **Host and Origin checks, both.** A request whose `Host` is not on the allow-list (`allowed_hosts`, the
  tailnet name and loopback) is refused, reads included, which defeats DNS rebinding. A state-changing
  request or a WebSocket upgrade whose `Origin` does not match is refused, which defeats drive-by CSRF and
  cross-site WebSocket hijacking. Both are tested in dev and prod modes.
- **Secrets** live in `~/Cairn/.env` with mode 0600; the doctor check verifies it. The OpenRouter key is the
  crown jewel.
- The container is non-root with a read-only root filesystem and no Docker socket.

## 7. Performance

- Compression is part of the skeleton: static assets are precompressed at build (brotli, plus gzip) and
  embedded; dynamic JSON is compressed per request.
- Budgets are tests: initial JS and CSS bytes after compression, cold start, library search over a large
  corpus, WebSocket resume replay. Hot paths have a benchmark compared with `benchstat`.
- Memory is a budget on the potato: well under 100 MB resident at idle, asserted by a test over a large
  seeded library.
- Real numbers come from the potato, not the dev machine.

## 8. Dependencies and supply chain

- Prefer pure Go, actively maintained, permissively licensed. Record version, licence and reason in
  `STACK.md`. Pin exact versions; `go.sum` and `pnpm-lock.yaml` are committed.
- `govulncheck` and `pnpm audit` in CI. A dependency update is a normal pull request with the full suite.
- Wrap a library at the boundary so swapping it touches one package.

## 9. Review checklist

[`TESTING.md`](TESTING.md) section 8 applies to every change, plus:

- [ ] The test was written first and seen failing (noted in the commit).
- [ ] Failure paths are covered: limits in the table, deadlines and cancellation, no silent error, the second
      attempt, a stalled peer, and hostile or huge input where a boundary is touched.
- [ ] Linters, `-race` tests and frontend checks pass locally.
- [ ] No new dependency without a `STACK.md` entry, and no cgo.
- [ ] Hot-path changes carry a benchmark before and after.
- [ ] No secrets, real reviews or personal data in logs, fixtures or commits.
- [ ] Docs, the plan and the slice status updated in the same commit.
