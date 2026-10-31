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
