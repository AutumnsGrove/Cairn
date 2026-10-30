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

- The name (holding name `OmniReviewer`; rename before release).
- Whether this ever publishes anything publicly, or stays a private journal forever.
- Rating scale, and whether "favourite" is separate from a 5/5.
- TV: season or series as the unit of review.
- Whether Fable can export book reviews in a usable format (needs a look inside the app).
- Which metadata providers are acceptable, given key handling and rate limits.

All six are written up with recommendations as [§3 in PLAN.md](../PLAN.md#3-open-questions).

### Deliberately not decided yet

The one architectural choice left open: embed nova directly in this app, or ship as a nova extension
first. Phase 0 is a spike whose only job is to answer that with evidence.
