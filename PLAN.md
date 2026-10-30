# OmniReviewer — Plan

> Holding name. Rename before the first release; keep the folder and repo name until then so links
> don't rot twice.

The source of truth for what gets built. Read this before writing code; update it when a decision
changes, in the same commit as the change. Anything still unknown belongs in
[§3 Open questions](#3-open-questions), not in code.

## Contents

1. [What it is](#1-what-it-is)
2. [Decisions already made](#2-decisions-already-made)
3. [Open questions](#3-open-questions)
4. [Architecture](#4-architecture)
5. [The review flow and its tools](#5-the-review-flow-and-its-tools)
6. [Data model](#6-data-model)
7. [Cross-referencing and merging](#7-cross-referencing-and-merging)
8. [Where it runs](#8-where-it-runs)
9. [Phases](#9-phases)
10. [Costs](#10-costs)
11. [Rules that are not negotiable](#11-rules-that-are-not-negotiable)
12. [Working agreement](#12-working-agreement)

---

## 1. What it is

A private, single-user review journal for **every medium**, with an agent doing the data entry.

I type or dictate a paragraph the way I'd say it out loud:

> "hey so i finished watching blade runner 2049, i really loved it, easy 5/5, the sound design
> carried the whole thing and i did not expect the ending to land like that"

The agent then:

1. **identifies the work** — pulls candidate matches from the right external library (TMDB for a film,
   Open Library for a book, MusicBrainz for an album…), confirms which one I meant, and pulls the
   canonical metadata (title, year, creators, runtime/pages, cover, genres, external IDs);
2. **extracts what I already said** — rating, verdict, the specifics I praised or disliked, spoiler
   mention, format (cinema / streaming / audiobook / hardback), date finished;
3. **asks the rest as questions** — a short, medium-appropriate set ("how was the setting?", "which
   character stayed with you?", "was it too long?") as tappable cards, with answers stored *as
   structured fields*, not just prose;
4. **saves one clean entry** — my review text woven with the answers, the canonical metadata, my
   rating, tags, and a snapshot of what each provider said, so later lookups can't silently rewrite
   history.

That entry is then browsable, searchable, filterable across media, and mirrored to R2.

### Goals

- One place for reviews of **any** medium, media-agnostic in the schema from day one.
- Free-form input as the primary interface. Forms are a fallback, never the front door.
- An agent-assisted fill-in flow that asks good questions and never invents my opinion.
- Canonical, merged metadata per work, with per-provider provenance kept.
- Reviews are local-first and backed up off-box (R2), with a JSON export that doesn't need this app.
- Phone-first: an installed PWA over Tailscale, same as Warbler.
- Runs on **Nova** (`projects/nova`) for the agent loop, tools and provider handling.

### Non-goals (v1)

- Multi-user, sharing, accounts, public profiles. One person, one journal.
- A social network, discovery feed, or recommendations engine.
- Being a media *catalogue* (no "browse everything ever released"). The library only holds things I
  have something to say about.
- Public exposure. Tailnet-only, like everything else here.
- Rewriting or re-scraping provider metadata wholesale; we snapshot, we don't mirror their databases.
- Native iOS/Android apps.

## 2. Decisions already made

| Topic | Decision |
|---|---|
| Name | `OmniReviewer` is a **holding name**; rename before the first release, keep repo/folder name until then |
| Agent runtime | **Nova** (`github.com/AutumnsGrove/nova`), the harness in `projects/nova` — not a bespoke loop, not a LangChain-style dependency |
| Media model | Media-agnostic from the start: a `work` has a `medium` (film, tv, book, game, album, podcast, theatre, other) plus medium-specific fields as data, not columns |
| Metadata | Looked up from external libraries per medium (TMDB, Open Library, MusicBrainz, IGDB, …), **merged into one canonical work** with per-provider snapshots kept |
| Ratings | My own scale, stored as a number plus the scale it was given in, so the scale can change later without a migration |
| Review body | My words, always. The agent may structure, ask, and punctuate; it must never write opinions I didn't express |
| Storage | SQLite on the potato (`modernc.org/sqlite`, pure Go — the same choice as Warbler), plus plain files for covers |
| Backups | Nightly mirror to Cloudflare R2 using the **existing `rclone` remote** already configured for Vaultwarden (`r2:`), plus a JSON export |
| Hosting | The potato: Go binary with an embedded SvelteKit frontend (`go:embed`), Docker Compose, image pinned by digest in GHCR, `tailscale serve` on port **8893** (verified free) |
| Updates | The Meridian/Polaris/Warbler pattern: host-side watcher, digest pinned in `.env`, health check, rollback |
| Discovery in Meridian | A tile with a health probe and one vital (see [§8](#8-where-it-runs)) |
| Auth | None. Tailnet-only is the boundary, exactly like Polaris and Warbler. No passwords to manage |
| Export | JSON (one file per work + review, plus a single archive) — the format the data model below *is* |
| Licence | MIT, matching Polaris, Meridian and Warbler |

## 3. Open questions

Answer these before Phase 1. Each has a recommendation; none of them blocks writing this plan.

1. **The name.** `OmniReviewer` is a placeholder. Suggestions to react to: *Ledger*, *Margin*,
   *Nightjar*, *Plume*, *Folio*, *Shelf*. (House style so far: Polaris, Ivy, Meridian, Warbler —
   one word, evocative, not descriptive.)
2. **Public or private?** Private-only (recommended, matches "my own form of reviews" as a personal
   journal), or private app that can also *publish* a subset as a static site later? This changes the
   export step and nothing else, so it can be deferred — but knowing now avoids a schema that can't
   carry a "published" flag.
3. **Rating scale.** 5 whole stars with halves? 10-point? A 5-star + "favourite" flag (recommended:
   5 stars with halves, plus a separate `favourite` boolean — favourites are not the same as 5/5)?
4. **What counts as "finished"** for TV — a season or a series? (Recommendation: a `work` is the
   series, a review can attach to a series, a season, or both; v1 ships series-level only.)
5. **Does the agent ask before saving, or save and let me edit?** (Recommendation: draft first, one
   confirm tap — never write silently.)
6. **Voice input?** Warbler already has the microphone plumbing and a working PWA; dictating a review
   would suit the habit better than typing. Speech-to-text via what — the browser's own dictation
   (free, no dependency) or a local model? (Recommendation: browser dictation for v1, revisit later.)
7. **Fable migration.** Can Fable export book reviews (CSV/markdown/JSON)? If yes, an import command in
   Phase 4; if no, copy-paste as manual entries. Needs checking in the app.
8. **Which providers are acceptable.** TMDB and Open Library are free with attribution; MusicBrainz
   needs a user agent and 1 req/s; IGDB needs Twitch OAuth; Goodreads has no real API; Hardcover/
   StoryGraph are options for books. Which set is worth the key handling — and does a commercial-API
   key for one medium add a cost worth avoiding?
9. **Spoilers.** A per-entry `spoilers: none | mild | full` field (recommended, always visible at the
   top) or per-paragraph marking?
10. **Photos.** Do reviews ever carry my own photos (a theatre ticket, a bookshelf, a cinema screen)?
    If yes it's Phase 4 and storage/backup sizing changes.

## 4. Architecture

```
 PHONE (installed PWA, Tailscale on)
 ┌────────────────────────────────────────────────────────────┐
 │ SvelteKit (static, embedded in the Go binary)              │
 │  free-form composer · question cards · entry view · library│
 └───────────────────────┬────────────────────────────────────┘
                         │ HTTPS via tailscale serve :8893
 THE POTATO (Docker)     ▼
 ┌────────────────────────────────────────────────────────────┐
 │ Go service (127.0.0.1:8893)                                │
 │   HTTP API + SSE      (my UI)                              │
 │   embedded nova agent loop  ──► OpenRouter (or local)      │
 │     tools: media_lookup · merge_candidates · ask_reviewer  │
 │            · save_entry · search_library                   │
 │   SQLite (works, entries, answers, provider snapshots)     │
 │   data/covers/*                                            │
 │   data/exports/*.json                                      │
 └───────────────────────┬────────────────────────────────────┘
                         │ nightly rclone (already configured for Vaultwarden)
                         ▼
                   Cloudflare R2  r2:omnireviewer-backups/
```

Three properties worth stating because they decide how the code is shaped:

- **The agent is a service, not a script.** nova is embedded as a library
  (`agent.New(cfg).Run(...)`, the shape `examples/embed/` proves) rather than shelled out to. My domain
  types ride on `tool.Context.Data`; nova never imports them. That keeps the loop swappable and testable.
- **The question cards are a tool call.** `ask_reviewer` is a model-callable tool whose result is
  "here are the questions to ask, here's their shape". The Go service turns that into UI; the answers
  come back as the tool result. No prompt is trusted to produce JSON that the UI then guesses at.
- **Local-first writes.** A finished entry is committed to SQLite before anything else happens; R2 is a
  mirror, never a source of truth. A failed mirror shows a warning in the app, not a lost review.

### Why nova rather than a bespoke loop

- The harness already has tools, providers, permissioning, sessions, compaction and a JSONL protocol
  (`projects/nova/docs/`). A new app shouldn't re-implement tool-calling.
- Multi-step flows (look up → disambiguate → ask → save) are exactly what a loop with tools is for.
- Nova's extension protocol gets the question-card UI shape for free (`ui_request`: `form`, `select`,
  `input`, `confirm`), which means the same flow can later run in the TUI without a second design.

**Open architectural choice, deferred:** embed nova directly (recommended — my own UI, my own state) vs
ship OmniReviewer as a nova *extension* first (fastest to feel, but the UI is limited to nova's fixed
components and a phone client still has to exist). Phase 0 answers it with a spike, not an opinion.

## 5. The review flow and its tools

### The flow

| Step | Who | What happens |
|---|---|---|
| 1 | me | Free-form message, typed or dictated, from the phone |
| 2 | agent | `media_lookup` — search the right library by title (+ creator/year if I gave them) → 3–5 candidates with tiny payloads (title, year, creator, medium, id) |
| 3 | me | Taps the right one, or "none of these" |
| 4 | agent | `merge_candidates` — fetches full metadata for the chosen work, stores a per-provider snapshot, returns the merged canonical draft |
| 5 | agent | Extracts rating/format/date/verdict from my text; anything missing goes in the question list, not invented |
| 6 | agent | `ask_reviewer` — 3–6 medium-appropriate questions, each with a type (`stars`, `choice`, `scale`, `short_text`, `skip`) and a suggested answer it thinks I implied |
| 7 | me | Answers, skips some, edits |
| 8 | agent | Composes the draft entry: my words first, answers woven in as their own fields, canonical metadata attached |
| 9 | me | One confirm tap (or edits) |
| 10 | app | `save_entry` → SQLite commit → export row queued → R2 mirror on the next run |

Nothing in steps 2–8 writes to the database. Step 9 is the only commit, and it is always mine to make.

### Tool contracts (shape, not final names)

| Tool | In | Out |
|---|---|---|
| `media_lookup` | `query`, optional `medium`, `year`, `creator` | up to 5 candidates: `provider`, `provider_id`, `title`, `year`, `creator`, `medium`, `thumb` |
| `merge_candidates` | `provider`, `provider_id` | merged canonical draft + `sources[]` snapshots; flags conflicting fields |
| `ask_reviewer` | `medium`, `questions[]` (each with `id`, `prompt`, `type`, `options?`, `skipped_ok`) | `answers[]` — typed values, plus what was skipped |
| `save_entry` | the full draft | committed entry id |
| `search_library` | text, filters | my own past entries (so "have I reviewed this already?" is answerable) |

Guardrails enforced in code, not in the prompt:

- `save_entry` refuses if `rating` or `body` is empty, or if either was never seen in my own input or
  answers. The agent cannot save an opinion I didn't give.
- `ask_reviewer` is capped (questions and total rounds) so a chatty model can't turn the flow into an
  interrogation. If I skip everything, the entry still saves.
- Every tool call and result is logged with its inputs, so "why does this entry say that" is answerable.

## 6. Data model

Media-agnostic because media-specific columns are how this kind of app dies. Medium-specific facts live
in JSON, validated per medium by a schema table in code.

```sql
works (                      -- the thing itself, canonical and merged
  id, medium, title, original_title, year,
  creator_json,              -- [{role: director|author|artist|developer, name}] — role is data
  details_json,              -- medium-specific: runtime, pages, isbn, track count, platform…
  cover_path, cover_source,
  primary_provider, primary_id,
  created_at, updated_at
)

work_sources (               -- one row per provider that contributed (history, not source of truth)
  id, work_id, provider, provider_id,
  raw_json,                  -- exactly what the provider said, when it said it
  fetched_at
)

entries (                    -- my review of a work; one work can have several over time
  id, work_id,
  rating REAL, rating_scale TEXT,      -- "5", "stars_5_halves"
  favourite INTEGER,                   -- separate from rating, on purpose
  verdict TEXT,                        -- one line
  body TEXT,                           -- my words
  spoilers TEXT,                       -- none | mild | full
  format TEXT,                         -- cinema | streaming | audiobook | hardback | …
  finished_on TEXT,                    -- local date
  status TEXT,                         -- draft | published(private) — see open question 2
  created_at, updated_at
)

answers (                    -- the blanks the agent asked me to fill
  id, entry_id, question_id, prompt, answer_type, answer_json, skipped INTEGER
)

tags (id, name) · entry_tags (entry_id, tag_id)
media (id, entry_id, path, kind)        -- optional photos, Phase 4+
mirrors (id, ran_at, target, status, bytes)   -- R2 backup receipts
```

Rules:

- **Nothing derived is required.** Delete every provider snapshot and the review still stands.
- **Ratings carry their scale**, so switching from 5-star to 10-point later is an edit, not a
  migration of meaning.
- **`answers` is deliberately denormalised** (prompt text stored with the answer) — the question set
  will change, and an old review must still make sense.
- **Timestamps are the phone's local date** for `finished_on` (a review is about the evening I finished
  it, not UTC).

## 7. Cross-referencing and merging

The "one nice entry" promise, in order of what wins:

1. **Match by provider ID** when a provider is canonical for that medium (a TMDB id is stable).
2. **Match by external IDs** across providers (`imdb`, `isbn`, `musicbrainz`, `igdb`) — the join key
   that makes "book + audiobook + film adaptation" still be three works, not one.
3. **Match by (title, year, primary creator)** normalised — and when two candidates are within a small
   distance, **ask me** rather than guess. Wrong merges are worse than duplicates.

Field-level merge rules:

| Field | Rule |
|---|---|
| Title | Primary provider's title; the others are kept as `alternate_titles` |
| Year | Earliest original release among providers, with the disagreements recorded |
| Creators | Union, deduplicated by normalised name, roles preserved |
| Cover | Primary provider's; a better-resolution source can replace it, and the old snapshot stays |
| Details | Union of medium-specific fields; conflicts recorded, never silently overwritten |

A merge never deletes a snapshot. `work_sources` is the audit trail, and re-merging is a command, not a
migration.

## 8. Where it runs

| Fact | Value |
|---|---|
| URL | `https://lepotato.taild95077.ts.net:8893` (tailnet only) |
| Port | `8893` — **verified free** on the potato 2026-10-30 (in use: 22 53 80 443 5335 8080 8081 8418 8766 8888 8890 8891 8892 8899 11434) |
| Container | Compose project at `~/OmniReviewer`, image pinned by digest in `.env`, published `127.0.0.1:8893` only, non-root, read-only rootfs |
| Data | `~/OmniReviewer/data/` — `omni.db`, `covers/`, `exports/`, `backups/` |
| Health | `GET /healthz` → `{"status":"ok","version":"r…"}` only (two fields, like Warbler's) |
| Secrets | `OPENROUTER_API_KEY` (or the chosen provider) and provider keys in `~/OmniReviewer/.env`; `rclone` config for R2 already exists on the box |
| Updates | Host watcher + `omnireviewer-update.path`, digest pinned, health check, rollback |
| Backups | Nightly at 03:30: SQLite snapshot → `data/backups/` (rotating) → `rclone sync` to `r2:omnireviewer-backups/`; JSON export on demand |
| Meridian | Tile `everyday`/`backbone` with a health probe on `http://localhost:8893/healthz`; vital: **reviews this month** (a count that means something) |
| Health check | Adds `check_http "OmniReviewer" "http://localhost:8893/healthz"` to `maintenance-setup/service-healthcheck.sh` in the same change that deploys it |

The outside traffic this app causes is: provider lookups (metadata only, no review text) and the R2
mirror. Nothing else leaves the box, and none of my text goes to a metadata provider.

## 9. Phases

Each phase ends with "Done when". Stop and ask at every **[HUMAN]** item.

### Phase 0: Prove the loop (spike, throwaway code allowed)

- Read `projects/nova/docs/INTEGRATION.md` and `examples/embed/`; write a 200-line program that runs one
  nova conversation with **one** tool (`media_lookup` against TMDB or Open Library).
- Answer the deferred architectural choice in [§4](#4-architecture) with what the spike showed.
- Get a TMDB and an Open Library key working (rate limits, attribution requirements written down).
- **[HUMAN]** autumn answers [§3](#3-open-questions) questions 1–4.

**Done when:** a free-form sentence in a terminal produces a real candidate list from real metadata,
and §3 q1–q4 are answered in this file.

### Phase 1: One entry, one medium, end to end

- Go service + embedded SvelteKit shell; SQLite with the schema above; `/healthz`.
- `media_lookup` → pick → `merge_candidates` → `ask_reviewer` → draft → confirm → `save_entry`.
- One medium wired properly (books *or* films — pick whichever has the better API day one).
- Entry view: metadata block, my body, answers, rating.

**Done when:** a review typed as a sentence becomes a saved entry with correct metadata and 3+ answers,
from the phone, over Tailscale, with no manual field entry.

### Phase 2: Every medium, one library

- Medium table with per-medium providers and per-medium question sets.
- Library view: filter by medium, sort by date/rating, search my text (SQLite FTS5).
- Duplicate detection and the "this already exists" path.

**Done when:** reviews of four different media coexist in one library and the filters work on a phone.

### Phase 3: Trust it with the data

- Nightly snapshot + `rclone` mirror to R2, with receipts in the app; restore exercised for real.
- JSON export (single file + per-entry) and an import that round-trips.
- `omnireviewer verify`: files present, covers accounted for, database integrity, archive readable.

**Done when:** I can export everything, wipe a test copy, import it back, and diff clean; and an R2
restore has actually been performed, not just designed.

### Phase 4: Comfort

- Voice input; photos; edit history; "listen to the questions while walking" — whichever of
  [§3](#3-open-questions) 6 and 10 turned out to matter.
- Fable import, if Fable exports anything usable.

**Done when:** the thing I reach for is this app, not a notes file.

### Phase 5: Optional publication

Only if open question 2 says yes: a static, read-only build of published entries, no login, no writes.

## 10. Costs

| Item | Estimate |
|---|---|
| Agent model calls per review | a handful of small calls; a review should cost **cents**, and the flow is designed to be 3–6 model turns, not 30 |
| Metadata | TMDB and Open Library free (attribution required); MusicBrainz free with a rate limit |
| Storage on the potato | a review is kilobytes; covers are the growth (~100 KB each), so a thousand entries is ~100 MB |
| R2 | the free tier is far beyond this |
| Electricity | the potato is already running |

If a review costs more than a few cents, the design has drifted: the fix is fewer, fatter tool calls,
not a cheaper model.

## 11. Rules that are not negotiable

1. **My words are mine.** The agent structures, asks and formats; it never invents an opinion, a
   rating, or a quote. Enforced in `save_entry`, not in a prompt.
2. **Nothing saves without my confirm tap.** Drafts are not entries.
3. **Local-first.** SQLite before R2, always. A mirror failure is a warning, never a lost review.
4. **Provider snapshots are append-only.** A silent metadata rewrite of my history is a bug.
5. **No review text leaves the box.** Only titles/ids go to metadata providers. The LLM sees my text —
   that is the cost of the feature, and it must never be sent anywhere the provider list doesn't name.
6. **Export must outlive the app.** If this code vanished tomorrow, the JSON should still be readable.
7. **Tailnet-only.** No Funnel, no public port, no accounts.

## 12. Working agreement

- One phase at a time; read the matching section first; commit and push per phase.
- **Stop and ask at every [HUMAN] item.** Don't invent answers to my questions.
- Keep scope tight: no new dependency, provider or service without asking.
- Small readable functions, a functional core (merge, extract, compose) with thin imperative edges;
  pure merge logic is tested hard because it is the part that can silently corrupt history.
- No telemetry, no third-party scripts, fonts or icons loaded from outside.
- Write for a reader new to the technical vocabulary; explain terms in plain language.
- Findings and uncertainties go in `NOTES.md` as they are learned.
- No emojis anywhere: UI, docs, commit messages.
