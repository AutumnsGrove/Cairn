# Notes

Findings and open items in the order they are learned, each with a date. Uncertain things go here,
then get surfaced to Autumn or promoted into `PLAN.md` once decided.

## 2026-10-30 — Planning start

### The idea, as Autumn said it

> "basically letterboxed, good reads, all that, all in one … i'll just submit free form text like
> 'hey so i finished watching this movie, blade runner 2049, i really fucking loved it easy 5/5
> stars…' and the agent will use its tools to basically go in and fill in the blanks, then use an
> askuserquestion sort of UI tool where we fill in the rest of stuff … and then i can host my own
> form of reviews. right now im using fable (app) for book reviewing/storing and i'd just like to
> have my own setup now … mirror backups to r2 … build it for any sort of media from the very
> beginning and then we cross reference with other libraries like themoviedb or open library … and
> try and merge it all into one nice pretty entry."

Two apps named, deliberately as inspiration rather than as a spec: **Letterboxd** (films) and
**Goodreads** (books). Neither is to be cloned; the point is one habit across media, on this
infrastructure.

### Facts confirmed on the potato (2026-10-30)

| Check | Result |
|---|---|
| Port 8893 free | Yes. In use: 22 53 80 443 5335 8080 8081 8418 8766 8888 8890 8891 8892 8899 11434 |
| `rclone` R2 remote already configured | Yes — `r2:`, used by `vaultwarden-setup/vaultwarden-backup.sh` against `r2:vaultwarden-backups`. A new bucket for this app reuses the same remote |
| Docker + Compose on the box | Docker 29.6, Compose 5.3 |
| Embedding target for the agent loop | `projects/nova` — `docs/INTEGRATION.md` describes the intended library-embedding shape and `examples/embed/` is a compiling, tested reference |
| Question-card UI in nova | The extension protocol's `ui_request` supports `form`, `select`, `input`, `confirm`, `notify`, `widget` — enough for the `ask_reviewer` cards without inventing a UI vocabulary |

### Nova integration facts worth not re-learning

- `agent.New(cfg).Run(...)` is the loop; the old `agent.Run(ctx, cfg, history)` is a compatibility
  wrapper.
- Consumers depend on the harness layer (`agent`, `tool`, `provider/*`, `message`, `proto`) and must
  **not** import `mode`, `config`, `session`, `tui`, `bundled` — `scripts/importcheck` enforces it in
  nova's own CI, so an app that reaches into the app shell will break on nova's gate rather than here.
- App-specific state rides on `tool.Context.Data` and is type-asserted back inside a tool. Nova never
  imports the consumer's types.
- Nova is a versioned module: use a local `replace ../nova` while it's unstable, then a git tag.

### Uncertain, needs Autumn

Superseded by the 2026-10-31 session below. The remaining open items live in
[§3 of PLAN.md](PLAN.md#3-open-questions).

## 2026-10-31 — Planning Q&A, and the name

The planning session turned the sketch into a chat-first design. The decisions are in
[§2 of PLAN.md](PLAN.md#2-decisions-already-made); this section keeps what was learned along the way.

### Renamed

OmniReviewer is now **Cairn**. Checked: nothing called cairn on the AutumnsGrove account or in the local
projects folder. Plenty of unrelated GitHub projects use the word (the closest to confusing a web search
are two AI-agent tools, `oritera/Cairn` and `cairn-dev/cairn`), which only matters for searching, not for
our own namespaces. The GHCR check (`ghcr.io/autumnsgrove/cairn`) failed on a shell quoting error and was
**not** confirmed; re-run it before the first image is published.

### Fable has no export (2026-10-31)

Confirmed by Autumn: there is no way to export reviews from Fable. The migration is therefore image import
inside Cairn (Phase 3), not a script and not a coding-agent insert, because a direct insert would bypass the
draft, confirm tap and span rules. The pinned model's OpenRouter listing shows `text` and `image` input, but
that is metadata, not a test; Phase 0 tries it on real Fable screenshots before anything depends on it.

### Nova already has the decisions contract

`nova/decide` defines `Decider`, a typed `Request` (shared `State`, a map of named `Question`s), an OpenRouter
wire client in `decide/openrouter`, and `Roles`, which maps an application role to `Role{Model, Fallback,
Timeout}` and refuses `-latest` and `~` aliases by default. It sits inside the layers consumers may import,
and tools call it through `tool.Context.Decide`. Cairn uses it rather than writing its own interface.

### Polaris WebSocket conventions worth keeping

Live-only events (`cost_update`, which overwrites rather than adds), optional events whose absence is
normal, the persisted message id sent back at once so a turn can be retried or edited, and answering a
pending question as simply the next ordinary message. Cairn's own vocabulary is in `docs/PROTOCOL.md`.

### Naming: "decisions"

The class of fast typed models is called **decisions** in all Cairn code, config and docs (Polaris already
has a `decisions/` package, and Ivy planned a `decide()` interface). Jev is the first backend and is named
only in the `backend:` block of the decisions config, so another model can replace it.

### Jev, as learned from `ivy/docs/JEV.md` (verified there by live calls, not re-verified here)

- `POST {openrouter_base}/systemone`, model `jev-1.14`, the same OpenRouter key as chat.
- A question is `{type: "choice", instructions, criteria: {option: "when this applies"}}`; the answer is a
  probability per option plus a confidence. Questions in one call are evaluated independently and in
  parallel. About 250 ms for 20 questions, about $0.042 per million input tokens, free output, 32k-token
  context (overrun is a clean 400, not silent truncation).
- "Cannot hallucinate" only means the answer is one of the given options. Calibration holds across many
  predictions, not for any single one, so the output is a hint.
- Model yes/no as a `choice` with `yes`/`no`/`none` options; `noul` and `score` calibration is not measured.
- Polaris's Oracle (`Polaris/docs/oracle.md`) is the design to copy: one batched call, a quiet default on
  every check, per-check thresholds, notes framed to the model as hints, odds shown to the user, and it
  never blocks a turn (2.5 s timeout).

### Polaris as the web tool

- `gateway.AskRequest` has `source`, `anonymous` (ghost thread), `focus_mode`, `quick_mode`; `AskResponse`
  returns `answer`, structured `citations`, `cost_usd`, `thread_id`. Production listens on
  `127.0.0.1:8899`.
- The Polaris sidebar only shows threads from its own source, so Cairn's lookups (tagged `source: cairn`)
  stay out of sight and still leave a full trace.
- **Not yet confirmed:** the exact ask endpoint path, and how a container on the same host reaches Polaris's
  host-published port. Both are Phase 0 items.

### Uncertain, still needs a look

- Whether Fable can export book reviews in a usable format.
- Whether the Jev tags that matter for a review (format, themes) are reliably separable at all; the
  Phase 0 spike records real odds on sample messages before any threshold is set.
- Nova is leaning embedded, but that is still to be shown by the spike rather than decided.

## 2026-10-31 (later) — Fable's descriptors, testing standard

- **Fable's "describe this book" step** (screenshots from Autumn): category tabs (Characters, Plot, Setting,
  Writing Style, Content Warnings), each with a star rating and about 20 multi-select chips mixing praise and
  criticism. Cairn adopts the tabs and chips (a closed vocabulary, shown dashed as hints, never pre-selected)
  but **not** the per-category stars: one overall quarter-star rating only. Vocabularies are written fresh,
  not copied (`docs/question-banks/`).
- **Testing and CI standard** adapted from Ivy: `docs/TESTING.md` and `docs/CI.md`. The repo is public, so the
  public-repo security rules apply, and tests use synthetic reviews only.

## 2026-10-31 (later) — what S00 taught

- **SvelteKit 3 is a different shape.** No `svelte.config.js` (it is an error, not a warning): the adapter,
  CSP and prerender settings go in `sveltekit({...})` inside `vite.config.ts`, and `sveltekit()` returns a
  promise of plugins. `tsconfig.json` extends `"$app/tsconfig"`. Worth knowing before S10 touches the shell.
- **Node 26 + pnpm 10 resolve from `web/node_modules` first,** so a package that a *build output* imports must
  be a dependency of the app, not only of Kit. `cookie` is the first one.
- **The mockups are boards, not phone pages.** Each page is a 390px frame laid out in a row for a desktop
  window; a phone viewport shows the first frame cut off. Render the board wide to review it, and compare a
  single frame when checking the app.
- **A borrowed global `node_modules` in `$HOME` breaks builds in confusing ways.** If a build fails with a
  CommonJS named-export error from inside `.svelte-kit/output`, look up the directory tree before debugging
  the app.

### 2026-10-31 — the container bind question, opened by S00

The service refused to start in the container with `CAIRN_ADDR=0.0.0.0:8893`, which is the right refusal: the
loopback rule lives in `config.Load`, not in a compose file. That leaves the potato's deployment with two
shapes (host networking, or a published loopback port with an explicit exception), and the decision is Autumn's
at S13. Recommendation and the trade-off are in `docs/slices/S13-deploy-and-update.md`.

Worth noting: with host networking, the "how does the container reach Polaris" question in `PLAN.md` 3
disappears, because both processes share the host's loopback.

## 2026-10-31 — S03, and what fuzzing found in a day

- **Three real bugs in the span locator, all found by the fuzzer, none by the example tests:** an infinite loop
  in exact matching, a byte-versus-rune offset bug in the normaliser, and a Unicode digit helper that assumed
  blocks never abut. The lesson is not "fuzz more" but "the locator is subtle enough that examples are not
  evidence".
- **Unicode decimal blocks are not all ten long as a run.** The mathematical digit sets sit next to each other,
  so the value is the offset from the start of the contiguous run modulo ten, not the offset from the block.
- **A homoglyph should match.** Storing the source's own word is what matters; refusing the match would make
  screenshot imports worse for no safety gain.
