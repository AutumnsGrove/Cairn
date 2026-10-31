# Cairn — Plan

> Named **Cairn** on 2026-10-31: a stack of stones left on a trail to say "I passed this way". Formerly
> the holding name OmniReviewer.

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

A private, single-user review journal for **every medium**, driven by a chat, with an agent doing the
data entry.

I type a paragraph the way I'd say it out loud:

> "hey so i finished watching blade runner 2049, i really loved it, easy 5/5, the sound design
> carried the whole thing and i did not expect the ending to land like that"

The agent then:

1. **reads the message first** — one cheap, fast classification call (Jev) says what I'm doing (log a
   review, look something up, fix an entry), which medium it probably is, and what format and themes I
   mentioned;
2. **identifies the work** — pulls candidate matches from the right external library (TMDB for a film,
   Open Library for a book, MusicBrainz for an album…), confirms which one I meant, and pulls the
   canonical metadata (title, year, creators, runtime/pages, cover, genres, external IDs). Where no
   library knows the thing, it falls back to a web lookup through Polaris, clearly marked unverified;
3. **extracts what I already said** — rating, verdict, the specifics I praised or disliked, format
   (cinema / streaming / audiobook / hardback), date finished. Hard facts like the rating come from
   plain parsing of my words, not from a model's guess;
4. **asks the rest as questions** — a short, medium-appropriate set ("how was the setting?", "which
   character stayed with you?", "was it too long?") as tappable cards, with answers stored *as
   structured fields*, not just prose;
5. **shows one draft entry card** — my review text woven with the answers, the canonical metadata, my
   rating and tags. I can tap any field to fix it or type a correction in the chat. One confirm tap
   saves it, and the snapshot of what each source said is kept so later lookups can't silently rewrite
   history.

That entry is then browsable, searchable and filterable across media in plain views, and mirrored to R2.

### Goals

- One place for reviews of **any** medium, media-agnostic in the schema from day one.
- **Chat for input and edits, plain views for reading.** The chat is the front door for everything that
  writes; browsing and searching are ordinary screens that are quick to scan on a phone.
- An agent-assisted fill-in flow that asks good questions and never invents my opinion.
- Canonical, merged metadata per work, with per-source provenance kept.
- Reviews are local-first and backed up off-box (R2), with a JSON export that doesn't need this app.
- Phone-first: an installed PWA over Tailscale, same as Warbler.
- Runs on **Nova** (`projects/nova`) for the agent loop, tools and provider handling.

### Non-goals (v1)

- Multi-user, sharing, accounts, public profiles. One person, one journal.
- A social network, discovery feed, or recommendations engine.
- Being a media *catalogue* (no "browse everything ever released"). The library only holds things I
  have something to say about.
- Public exposure in v1. Tailnet-only, like everything else here. (A static published subset is a later
  phase, see [Phase 5](#phase-5-optional-publication).)
- Rewriting or re-scraping provider metadata wholesale; we snapshot, we don't mirror their databases.
- Native iOS/Android apps.
- Voice input and spoiler handling (both deliberately out of v1, see [§2](#2-decisions-already-made)).

## 2. Decisions already made

| Topic | Decision |
|---|---|
| Name | **Cairn** (decided 2026-10-31). Local folder, GitHub repo, image and units all use `cairn` |
| Interface | **Chat for input and edits, plain views for reading.** A draft shows as an editable entry card (tap a field to fix it), and typing a correction in the chat also works |
| Conversation unit | **One short session per entry** (lives until saved or discarded; unfinished drafts are resumable) **plus a general chat** for questions about the library. The general chat may read and edit, with a confirm card for every edit. It can never delete |
| Agent runtime | **Nova** (`github.com/AutumnsGrove/nova`), embedded as a library in the Go service — not a bespoke loop, not a LangChain-style dependency. Leaning embed over "ship as a nova extension"; Phase 0 confirms it |
| Main model | **`deepseek/deepseek-v4.1-flash-1017`** through **OpenRouter** (the same key Polaris and Ivy use). A dated pin on purpose, so behaviour can't shift silently. $0.15 per million input tokens and $0.60 per million output, 1M context, tool calling. Phase 0 checks it against the fixture corpus rather than comparing models; nova's provider layer keeps a swap cheap |
| Pre-read classifier | **Jev** (`jev-1.14` via OpenRouter `/systemone`), one batched call per message, in the style of Polaris's Oracle: a quiet default on every check, a per-check threshold, odds shown on the card, never blocks a turn (unconfigured, errors or slow means the turn runs without it). Probabilities are hints, never decisions |
| Extraction | **Deterministic parse first** (rating, date, obvious format cues). Jev only for fuzzy fields (format, themes). If no rating is stated, the agent asks |
| Medium ambiguity | When Jev's medium read is split (e.g. "Dune": book or film), **ask "book or film?" before any lookup** |
| Media model | Media-agnostic from the start: a `work` has a `medium` (film, tv, book, game, album, podcast, theatre, other) plus medium-specific fields as data, not columns |
| Metadata | **One real API per medium first** (TMDB for film and TV, Open Library for books; other media added as we reach them), **merged into one canonical work** with per-source snapshots kept. **Polaris is the fallback** for anything the APIs miss |
| Web lookups | Through the colocated **Polaris**, which already wraps SearXNG with a Brave fallback. Two tools: `lookup_web` (structured args: title, medium, creator, field wanted — the default) and `ask_polaris_freeform` (a free prompt, the escape hatch; logged only, no confirm tap). Calls carry `source: cairn` and live as normal Polaris threads, which its sidebar never shows. The thread id and citations are stored with the snapshot |
| Web trust | Web-derived fields are **proposed, never canonical**: shown on the card as unverified with their source URL, accepted with one tap |
| Transport | **WebSocket both ways.** The server keeps a per-session event log with sequence numbers. A client that reconnects (a sleeping phone, a killed PWA) says the last number it saw and gets everything after it replayed |
| Question sets | **Per-medium YAML templates**: a curated bank of questions per medium, each with a type. The model picks 3 to 6 that the message hasn't already answered and phrases them; it does not invent the questions. Answers stay comparable across entries |
| Pre-read checks (v1) | `intent` (chitchat, log_review, find_entries, edit_entry, answer_pending), `medium`, `format`, `revisit`, and about eight `mentions_*` yes/no flags (sound, visuals, acting, story, pacing, ending, characters, world). Defined in a **hot-reloaded YAML registry**, baked into the image and bind-mounted over top as Polaris does for `prompts.yaml`. Thresholds are set from real odds in Phase 0 |
| Revisits | Logging something already in the library asks: **new entry on the same work, or edit the old one**, showing the previous entry and rating. Several entries per work form a timeline |
| Edits and history | Editing a saved entry is a draft revision with a confirm tap. Every confirmed edit stores the previous version in an **append-only revisions table, kept forever** |
| Deleting | **Soft-delete only, and hard deletes are blocked by the database itself** (a `BEFORE DELETE` trigger aborts on entries, revisions, snapshots and answers). Deleting exists only on the plain entry view, never in chat. **There is no purge**: soft-delete is permanent, and backups hold every copy |
| Drafts | Kept until saved or discarded; they never expire. A Drafts list, with a count on the home screen |
| Covers | Downloaded once, stored under `data/covers/`, mirrored to R2. No hot-linked third-party images. TMDB's image terms get checked in Phase 0 before this is final |
| Degraded mode | If a metadata service is down, the review can still be logged as a **manual, unverified work** and enriched later with a "fill in details" action. A review is never blocked by someone else's outage |
| Tags | Suggested from the `mentions_*` flags and the provider's genres, shown on the card for me to accept or edit. Free-text tags allowed. Nothing is applied without being shown |
| Home | The chat input at the bottom, with recent entries and the drafts count above it. Library, search and the general chat are one tap away |
| Testing | A **fixture corpus** of sample messages with the expected parse, expected Jev outcome and expected tool sequence (the Oracle model: hand-written prompts with expected winners), run against **fake TMDB, Open Library, Jev and Polaris servers**. A separate live eval runs on demand |
| Photos | Deferred to Phase 4. The `media` table stays in the schema so adding them is not a migration |
| First media | **Books (Open Library) and films and TV (TMDB) together** in Phase 1, so the merge and disambiguation logic is proven against two real sources |
| Ratings | **Quarter-star steps from 0.25 to 5.0**, stored as an **integer 1–20 (quarter-stars)** plus the scale name, so the scale can change later without a migration. **Favourite is a separate flag**, not the same as a top rating |
| TV | A work is the **series** in v1. The schema lets an entry point at a season later, so there is no migration, but only the series flow is built |
| Review body | My words, always. The agent may structure, ask, and punctuate; it must never write opinions I didn't express |
| Spoilers | **Not handled in v1.** No field, no Jev check. Revisit if it starts to matter |
| Voice | **Deferred.** Typing only until the rest works |
| Storage | SQLite on the potato (`modernc.org/sqlite`, pure Go — the same choice as Warbler), plus plain files for covers |
| Backups | Nightly mirror to Cloudflare R2 using the **existing `rclone` remote** already configured for Vaultwarden (`r2:`), plus a JSON export |
| Hosting | The potato: Go binary with an embedded SvelteKit frontend (`go:embed`), Docker Compose, image pinned by digest in GHCR, `tailscale serve` on port **8893** (verified free) |
| Updates | The Meridian/Polaris/Warbler pattern: host-side watcher, digest pinned in `.env`, health check, rollback |
| Discovery in Meridian | A tile with a health probe and one vital (see [§8](#8-where-it-runs)) |
| Auth | None. Tailnet-only is the boundary, exactly like Polaris and Warbler. No passwords to manage |
| Export | JSON (one file per work + review, plus a single archive) — the format the data model below *is* |
| Publishing | Private in v1. A **static public site is a later phase**; entries keep a `status` field so it needs no migration |
| Licence | MIT, matching Polaris, Meridian and Warbler |

## 3. Open questions

What is still open, each with the phase that needs it. None of these blocks the rest of the plan.

1. **Fable migration** (Phase 4). Can Fable export book reviews (CSV/markdown/JSON)? If yes, an import
   command; if no, copy-paste as manual entries. Needs a look inside the app.
2. **Providers beyond film, TV and books** (Phase 2). MusicBrainz needs a user agent and 1 req/s; IGDB
   needs Twitch OAuth; podcasts and theatre have no obvious single source. Decide per medium as each is
   reached, and let Polaris cover the rest until then.
3. **TMDB's image and attribution terms** (Phase 0). The cover-caching decision in [§2](#2-decisions-already-made)
   is final only once this is read.
4. **Polaris's ask endpoint and container networking** (Phase 0). The exact path, and how the Cairn
   container reaches Polaris's host-published port.

Defaults assumed without asking, because they follow from decisions above. Say so if any is wrong:

- **Dates.** The phone sends its local date and timezone with every message, so "finished last night"
  resolves against where I am, not the server's clock.
- **Tool output is data, never instructions.** Web text returned by Polaris is shown to the model as
  quoted material, and no guardrail depends on the model resisting it. The deterministic checks in
  [§5](#5-the-review-flow-and-its-tools) are what keep a bad result from becoming a saved fact.
- **Spend is recorded, not yet shown.** Every Jev, model and Polaris cost is stored per call; a spend
  view waits until it is wanted.

Resolved and moved into [§2](#2-decisions-already-made): the name, public or private, the rating scale,
the unit of TV review, whether the agent saves silently (it never does), voice input, spoilers, the
first providers, the general chat's powers, transport, the Jev registry, the free-form Polaris guard,
the GHCR name (not publicly taken; first publish confirms), edit history, deletion, draft expiry, question
sets, the main model, testing, revisits, covers, degraded mode, tags, the home screen, photos, and the
first media.

## 4. Architecture

```
 PHONE (installed PWA, Tailscale on)
 ┌────────────────────────────────────────────────────────────┐
 │ SvelteKit (static, embedded in the Go binary)              │
 │  chat (per-entry session + general) · entry card · library │
 └───────────────────────┬────────────────────────────────────┘
                         │ HTTPS via tailscale serve :8893
 THE POTATO (Docker)     ▼
 ┌────────────────────────────────────────────────────────────┐
 │ Go service (127.0.0.1:8893)                                │
 │   HTTP API + WebSocket (my UI)                             │
 │   pre-read: Jev (one batched call per message)             │
 │   embedded nova agent loop  ──► OpenRouter                 │
 │     tools: media_lookup · lookup_web · ask_polaris_freeform│
 │            · merge_candidates · ask_reviewer · save_entry  │
 │            · search_library                                │
 │   SQLite (works, entries, answers, sessions, snapshots)    │
 │   data/covers/*   data/exports/*.json                      │
 └──────┬─────────────────────────────────┬───────────────────┘
        │ web lookups (source: cairn)     │ nightly rclone
        ▼                                 ▼
   Polaris (127.0.0.1:8899)        Cloudflare R2  r2:cairn-backups/
   SearXNG, Brave fallback
```

Five properties worth stating because they decide how the code is shaped:

- **The agent is a service, not a script.** nova is embedded as a library
  (`agent.New(cfg).Run(...)`, the shape `examples/embed/` proves) rather than shelled out to. My domain
  types ride on `tool.Context.Data`; nova never imports them. That keeps the loop swappable and testable.
- **Nova owns the loop, not the conversation.** Consumers must not import nova's `session` package, so
  this app stores its own sessions, messages and resumable drafts. That is a real piece of work the
  original sketch did not have.
- **The pre-read is a hint, not a gate.** Jev runs before the model turn, its odds are shown on the
  card, and each check only acts when its non-quiet option clears a threshold. The model sees the result
  framed as an automatic pre-read, not an instruction, so a wrong guess gets ignored instead of obeyed.
  Anything that must be true (a rating, a body) is enforced by deterministic code, never by Jev.
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
  `input`, `confirm`), which means the same flow could later run in the TUI without a second design.

**Deferred architectural choice, now leaning one way:** embed nova directly (my own UI, my own state)
vs ship Cairn as a nova *extension* first. A custom chat with entry cards and a general-chat mode points
strongly at embedding. Phase 0 confirms it with a spike, not an opinion.

## 5. The review flow and its tools

### The flow

| Step | Who | What happens |
|---|---|---|
| 1 | me | Free-form message in a chat, typed, from the phone |
| 2 | app | **Pre-read:** one batched Jev call reads the message (intent, medium, format, themes). Skipped if empty; never blocks the turn |
| 3 | app | **Deterministic parse:** rating, date and obvious cues pulled out of my words by plain rules. Nothing found means the field is left for the questions |
| 4 | me | If the medium read was split, a quick "book or film?" card, answered before any lookup |
| 5 | agent | `media_lookup` — search the right library by title (+ creator/year if I gave them) → 3–5 candidates with tiny payloads. `lookup_web` is used when the APIs return nothing |
| 6 | me | Taps the right one, or "none of these" |
| 7 | agent | `merge_candidates` — fetches full metadata for the chosen work, stores a per-source snapshot, returns the merged canonical draft with web-sourced fields marked unverified |
| 8 | agent | `ask_reviewer` — 3–6 medium-appropriate questions, each with a type (`stars`, `choice`, `scale`, `short_text`, `skip`) and a suggested answer it thinks I implied |
| 9 | me | Answers, skips some, edits |
| 10 | agent | Composes the draft entry: my words first, answers woven in as their own fields, canonical metadata attached |
| 11 | me | Reviews the entry card: tap-to-edit any field, or type a correction in the chat; one confirm tap |
| 12 | app | `save_entry` → SQLite commit → export row queued → R2 mirror on the next run |

Nothing in steps 2–10 writes a saved entry. Step 11 is the only commit, and it is always mine to make.
In-progress work lives in a draft that can be resumed after closing the app.

Branches off the main flow:

- **Revisit.** If the work is already in the library (the Jev `revisit` check, backed by an exact match
  on the chosen work), the card shows the previous entry and rating and asks: new entry, or edit the
  old one.
- **Outage.** If a metadata service is down or finds nothing, the review is logged against a manual,
  unverified work, and a "fill in details" action retries the lookup later.
- **Edit.** Saying "change my Dune rating" in the general chat opens a draft revision of that entry with
  the same card and confirm tap. The previous version goes into the revisions table.

### Tool contracts (shape, not final names)

| Tool | In | Out |
|---|---|---|
| `media_lookup` | `query`, optional `medium`, `year`, `creator` | up to 5 candidates: `provider`, `provider_id`, `title`, `year`, `creator`, `medium`, `thumb` |
| `lookup_web` | `title`, `medium`, optional `creator`, `field` | labelled fields each with a source URL, plus the Polaris thread id and citations. Calls Polaris with `source: cairn` |
| `ask_polaris_freeform` | `prompt` | Polaris's answer and citations. The escape hatch; logged only, no confirm tap |
| `merge_candidates` | `provider`, `provider_id` | merged canonical draft + `sources[]` snapshots; flags conflicting fields and any web-derived (unverified) ones |
| `ask_reviewer` | `medium`, `questions[]` (each with `id`, `prompt`, `type`, `options?`, `skipped_ok`) | `answers[]` — typed values, plus what was skipped |
| `save_entry` | the full draft | committed entry id |
| `search_library` | text, filters | my own past entries (so "have I reviewed this already?" and "what did I watch in March?" are answerable). Reads only; edits happen through a draft revision and the confirm card |

Jev itself is not a model-callable tool. It is a pipeline step the service runs before each turn.

Guardrails enforced in code, not in the prompt:

- `save_entry` refuses if `rating` or `body` is empty, or if either was never seen in my own input or
  answers. The agent cannot save an opinion I didn't give.
- Ratings are accepted only as a quarter-star value. A parsed value that isn't on a quarter snaps to the
  nearest one and the card shows what was stored; a vague phrase ("a four and a bit") is asked about,
  never guessed.
- Web-derived fields cannot reach the canonical work without my accept tap.
- `ask_reviewer` is capped (questions and total rounds) so a chatty model can't turn the flow into an
  interrogation. If I skip everything, the entry still saves.
- Every tool call, Jev pre-read (with its odds and cost) and Polaris call is logged with its inputs, so
  "why does this entry say that" is answerable.

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
  verified INTEGER,          -- 0 for a manual work logged during an outage
  created_at, updated_at
)

work_sources (               -- one row per source that contributed (history, not source of truth)
  id, work_id, provider,     -- provider is an API name or "web"
  provider_id,
  raw_json,                  -- exactly what the source said, when it said it
  trust TEXT,                -- verified (an API) | unverified | accepted (web field I tapped)
  citations_json,            -- source URLs and snippets, for web-derived rows
  polaris_thread_id,         -- the trace of the lookup, for web-derived rows
  fetched_at
)

entries (                    -- my review of a work; one work can have several over time
  id, work_id,
  rating_q INTEGER,                    -- quarter-stars, 1..20 (0.25 to 5.0)
  rating_scale TEXT,                   -- "quarter_stars_20"
  favourite INTEGER,                   -- separate from rating, on purpose
  season INTEGER,                      -- NULL = the whole work; reserved for TV seasons
  verdict TEXT,                        -- one line
  body TEXT,                           -- my words
  format TEXT,                         -- cinema | streaming | audiobook | hardback | …
  finished_on TEXT,                    -- local date
  status TEXT,                         -- draft | saved | published (published is Phase 5)
  deleted_at TEXT,                     -- soft delete only; hard deletes are blocked by trigger
  created_at, updated_at
)

answers (                    -- the blanks the agent asked me to fill
  id, entry_id, question_id, prompt, answer_type, answer_json, skipped INTEGER
)

sessions (                   -- the app's own conversations; nova's are not importable
  id, kind,                  -- entry | general
  entry_id,                  -- NULL for general; set for an entry session (resumable draft)
  messages_json, state,      -- active | saved | discarded
  created_at, updated_at
)

session_events (            -- the per-session event log the WebSocket resumes from
  id, session_id, seq,       -- seq is the number a reconnecting client reports
  kind, payload_json, created_at
)

entry_revisions (            -- append-only: the previous version of an entry at each confirmed edit
  id, entry_id, revision, snapshot_json, created_at
)

preread (                    -- every Jev call, so thresholds can be tuned from real use
  id, session_id, message_index, model,
  odds_json,                 -- every check's full probability vector
  fired_json,                -- which checks cleared their threshold
  cost_usd, latency_ms, created_at
)

tags (id, name) · entry_tags (entry_id, tag_id, source)   -- source: genre | mentioned | mine
media (id, entry_id, path, kind)        -- optional photos, Phase 4+
mirrors (id, ran_at, target, status, bytes)   -- R2 backup receipts
```

Rules:

- **Nothing derived is required.** Delete every source snapshot, every pre-read and every session and the
  review still stands.
- **Ratings carry their scale and are integers**, so equality and sorting are exact (a float `4.75` can
  become `4.749999`) and switching scales later is an edit, not a migration of meaning.
- **`answers` is deliberately denormalised** (prompt text stored with the answer) — the question set
  will change, and an old review must still make sense.
- **Timestamps are the phone's local date** for `finished_on` (a review is about the evening I finished
  it, not UTC).
- **A draft is an entry with `status = draft`.** It never shows in the library views or the export.
- **Nothing is hard-deleted.** `BEFORE DELETE` triggers abort on `entries`, `entry_revisions`,
  `work_sources` and `answers`; deleting an entry sets `deleted_at`. There is no purge command, and R2
  backups keep every copy. `entry_revisions` also rejects updates, so it is append-only.

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
| Web-sourced fields | Never merged in automatically. They sit as `unverified` until I accept them; accepting writes the field and marks the snapshot `accepted` |

A merge never deletes a snapshot. `work_sources` is the audit trail, and re-merging is a command, not a
migration.

## 8. Where it runs

| Fact | Value |
|---|---|
| URL | `https://lepotato.taild95077.ts.net:8893` (tailnet only) |
| Port | `8893` — **verified free** on the potato 2026-10-30 (in use: 22 53 80 443 5335 8080 8081 8418 8766 8888 8890 8891 8892 8899 11434) |
| Container | Compose project at `~/Cairn`, image pinned by digest in `.env`, published `127.0.0.1:8893` only, non-root, read-only rootfs |
| Data | `~/Cairn/data/` — `cairn.db`, `covers/`, `exports/`, `backups/` |
| Config | `jev.yaml` (pre-read checks, options, thresholds) and `questions/<medium>.yaml` (question banks), baked into the image and bind-mounted over top so host edits hot-reload, the way Polaris handles `prompts.yaml` |
| Health | `GET /healthz` → `{"status":"ok","version":"r…"}` only (two fields, like Warbler's) |
| Secrets | `OPENROUTER_API_KEY` (main model and Jev, one key) and metadata-provider keys in `~/Cairn/.env`; `rclone` config for R2 already exists on the box |
| Polaris | Colocated at `127.0.0.1:8899`. Cairn calls its ask API (`gateway.AskRequest`/`AskResponse`: `source`, `citations`, `cost_usd`). How the Cairn container reaches the host-published Polaris port (host networking or `host.docker.internal`) is a Phase 0 item |
| Updates | Host watcher + `cairn-update.path`, digest pinned, health check, rollback |
| Backups | Nightly at 03:30: SQLite snapshot → `data/backups/` (rotating) → `rclone sync` to `r2:cairn-backups/`; JSON export on demand |
| Meridian | Tile `everyday`/`backbone` with a health probe on `http://localhost:8893/healthz`; vital: **reviews this month** (a count that means something) |
| Health check | Adds `check_http "Cairn" "http://localhost:8893/healthz"` to `maintenance-setup/service-healthcheck.sh` in the same change that deploys it |

The outside traffic this app causes is: the model calls (OpenRouter, including Jev), metadata lookups to
the provider APIs (titles and ids only), web lookups through Polaris (which searches via SearXNG, with
Brave as a fallback), and the R2 mirror. Nothing else leaves the box.

## 9. Phases

Each phase ends with "Done when". Stop and ask at every **[HUMAN]** item.

### Phase 0: Prove the loop (spike, throwaway code allowed)

- Read `projects/nova/docs/INTEGRATION.md` and `examples/embed/`; write a small program that runs one
  nova conversation with **one** tool (`media_lookup` against TMDB or Open Library).
- Make one real **Jev** call from Go against `/systemone` with a few review-shaped questions (intent,
  medium) and record the odds on a handful of sample messages, including a split one like "Dune".
- Make one real **Polaris** call with `source: cairn`; confirm the exact endpoint path, the response, the
  `citations` shape, and that the Cairn container can reach it.
- Answer the deferred architectural choice in [§4](#4-architecture) (embed vs extension) with what the
  spike showed.
- Get a TMDB key and an Open Library client working (rate limits and attribution requirements written down).
- Read TMDB's image and attribution terms and write the cover-caching decision down.
- Write the first **fixture corpus** (about 30 sample messages with expected parse, expected Jev outcome
  and expected tool sequence), then run the pre-read and the pinned model against it. Set the first Jev
  thresholds from the real odds.
- **[HUMAN]** autumn reviews the corpus and the thresholds.

**Done when:** a free-form sentence in a terminal produces a real candidate list from real metadata,
the Jev and Polaris calls both work from Go, and the embed-or-extension choice is written down.

### Phase 1: One entry, one medium, end to end

- Go service + embedded SvelteKit shell; SQLite with the schema above; `/healthz`.
- Sessions: a per-entry chat over a WebSocket with a server-side event log and resume, resumable drafts,
  and a Drafts list.
- The revisit flow (new entry or edit the old one) and the manual-entry fallback for outages.
- Revisions and soft-delete, with the database triggers that block hard deletes.
- Pre-read → `media_lookup` → pick → `merge_candidates` → `ask_reviewer` → draft card → confirm →
  `save_entry`.
- Two sources wired properly: **books (Open Library) and films and TV (TMDB)**, so the merge and
  disambiguation logic is exercised from day one.
- Entry view: metadata block, my body, answers, rating (quarter-stars).

**Done when:** a review typed as a sentence becomes a saved entry with correct metadata and 3+ answers,
from the phone, over Tailscale, with no manual field entry, for both a book and a film.

### Phase 2: Every medium, one library

- Medium table with per-medium providers and per-medium question sets.
- `lookup_web` and the unverified-field flow, so anything an API misses can still be logged.
- Library view: filter by medium, sort by date/rating, search my text (SQLite FTS5).
- The general chat, backed by `search_library`, with edits going through a confirm card.
- Duplicate detection and the "this already exists" path.

**Done when:** reviews of four different media coexist in one library and the filters work on a phone.

### Phase 3: Trust it with the data

- Nightly snapshot + `rclone` mirror to R2, with receipts in the app; restore exercised for real.
- JSON export (single file + per-entry) and an import that round-trips.
- `cairn verify`: files present, covers accounted for, database integrity, archive readable.

**Done when:** I can export everything, wipe a test copy, import it back, and diff clean; and an R2
restore has actually been performed, not just designed.

### Phase 4: Comfort

- Photos, season-level TV reviews, voice input, spoiler handling — whichever of these turned out to
  matter.
- Fable import, if Fable exports anything usable ([§3](#3-open-questions) q1).

**Done when:** the thing I reach for is this app, not a notes file.

### Phase 5: Optional publication

A static, read-only build of published entries, no login, no writes. Entries already carry a `status`
field, so this adds a build step and a way to mark an entry published, and nothing else.

## 10. Costs

| Item | Estimate |
|---|---|
| Agent model calls per review | a handful of small calls; a review should cost **cents**, and the flow is designed to be 3–6 model turns, not 30. At the pinned model's prices a 20k-token-in, 2k-token-out turn is roughly $0.004 |
| Jev pre-read | about $0.042 per million input tokens and free output, so a batched call is a fraction of a cent. Logged in `preread.cost_usd` |
| Polaris web lookups | Polaris reports `cost_usd` per ask; it is stored with the snapshot. Most reviews should never need one |
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
4. **Source snapshots are append-only.** A silent metadata rewrite of my history is a bug. Web-derived
   facts are proposed and marked unverified until I accept them.
5. **Review text goes only where the plan names.** The LLM (OpenRouter, including Jev) sees my text; that
   is the cost of the feature. Metadata APIs and Polaris get titles, creators and ids, via structured
   arguments by default. The one path where free text could reach a web search is
   `ask_polaris_freeform`, which is the deliberate escape hatch and is always logged.
6. **Classifier output never decides.** Jev's probabilities steer and suggest. Anything that must be true
   (a rating, a body, a quarter-star value) is enforced by deterministic code.
7. **Export must outlive the app.** If this code vanished tomorrow, the JSON should still be readable.
8. **Tailnet-only.** No Funnel, no public port, no accounts.
9. **Nothing is ever hard-deleted.** Deletes are soft, enforced by the database, and there is no purge.
   Every confirmed edit keeps the previous version.

## 12. Working agreement

- One phase at a time; read the matching section first; commit and push per phase.
- **Stop and ask at every [HUMAN] item.** Don't invent answers to my questions.
- Keep scope tight: no new dependency, provider or service without asking.
- Small readable functions, a functional core (merge, extract, compose) with thin imperative edges;
  pure merge logic is tested hard because it is the part that can silently corrupt history.
- No telemetry, no third-party scripts, fonts or icons loaded from outside.
- Write for a reader new to the technical vocabulary; explain terms in plain language.
- Every new pre-read check and every guardrail lands with fixture-corpus cases.
- Findings and uncertainties go in `NOTES.md` as they are learned.
- No emojis anywhere: UI, docs, commit messages.
