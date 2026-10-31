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

1. **reads the message first** — one cheap, fast decision call says what I'm doing (log a
   review, look something up, fix an entry), which medium it probably is, and what format and themes I
   mentioned;
2. **identifies the work** — pulls candidate matches from the right external library (TMDB for a film,
   Open Library for a book, MusicBrainz for an album…), confirms which one I meant, and pulls the
   canonical metadata (title, year, creators, runtime/pages, cover, genres, external IDs). Where no
   library knows the thing, it falls back to a web lookup through Polaris, clearly marked unverified;
3. **extracts what I already said** — rating, verdict, the specifics I praised or disliked, format
   (cinema / streaming / audiobook / hardback), date finished. Hard facts like the rating come from
   plain parsing of my words, not from a model's guess;
4. **asks the rest, lightly** — a Describe it step of tappable chips (characters, plot, setting, writing,
   feel, content warnings) and at most three short questions, all skippable, with answers stored *as
   structured fields*, not just prose;
5. **shows one draft entry card** — my own words cut out of what I typed, the answers as their own
   fields, the canonical metadata, my rating and tags. I can tap any field to fix it or type a correction in the chat. One confirm tap
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
  phase, see [Phase 6](#phase-6-optional-publication).)
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
| Decisions | **"Decisions" is the vendor-neutral name** for the class of fast, typed models that take conditions and options (or a yes/no check) and return a probability per option. All code, config and docs say `decisions`. The contract is **nova's own `decide` package** (`decide.Decider`, the typed `Request`, the OpenRouter client, and the `Roles` decorator that maps a role such as `cairn_preread` to a model, a timeout and a fallback), which tools reach through `tool.Context.Decide`, so another model is a config change. Nova refuses `-latest` and `~` aliases by default, which matches pinning the model. The first backend is **Jev** (`jev-1.14` via OpenRouter `/systemone`), named only in config. The **pre-read** is the step that uses it: one batched call per message, in the style of Polaris's Oracle, with a quiet default on every check, a per-check threshold, odds shown on the card, and never blocking a turn (unconfigured, errors or slower than 2.5 s means the turn runs without it). Probabilities are hints, never decisions. Checks are drafted in [`docs/decisions.draft.yaml`](docs/decisions.draft.yaml) |
| Decision routing | Inside an entry session **the session sets the default** (an open question card means a reply is answering it; a draft means a message adds to it) and a decision overrides it only above **0.80**. From the home screen the `intent` decision routes: `log_review` and `import_review` start an entry session, and `find_entries`, `edit_entry` and `chitchat` go to the general chat. The state sent with each call is **the latest message, any open question, and the work's title**, as separate sources. `format`, `revisit` and the `mentions_*` flags run only for `log_review`. A split medium read (runner-up at least 0.25 and the leader under 0.30 ahead) triggers the "book or film?" card |
| Extraction | **Deterministic parse first** (rating, date, obvious format cues). The decision model only for fuzzy fields (format, themes). If no rating is stated, the agent asks |
| Medium ambiguity | When the decision model's medium read is split (e.g. "Dune": book or film), **ask "book or film?" before any lookup** |
| Media model | Media-agnostic from the start: a `work` has a `medium` (film, tv, book, game, album, podcast, theatre, other) plus medium-specific fields as data, not columns |
| Metadata | **One real API per medium first** (TMDB for film and TV, Open Library for books; other media added as we reach them), **merged into one canonical work** with per-source snapshots kept. **Polaris is the fallback** for anything the APIs miss |
| Web lookups | Through the colocated **Polaris**, which already wraps SearXNG with a Brave fallback. Two tools: `lookup_web` (structured args: title, medium, creator, field wanted — the default) and `ask_polaris_freeform` (a free prompt, the escape hatch; logged only, no confirm tap). Calls carry `source: cairn` and live as normal Polaris threads, which its sidebar never shows. The thread id and citations are stored with the snapshot |
| Web trust | Web-derived fields are **proposed, never canonical**: shown on the card as unverified with their source URL, accepted with one tap |
| Transport | **WebSocket both ways, one socket per open session**; the library, drafts count and search are plain REST that refresh on focus. A small Cairn event vocabulary (nova's events stay server-side). The server keeps a per-session event log with sequence numbers: a client that reconnects (a sleeping phone, a killed PWA) says the last number it saw and gets everything after it replayed. The draft card syncs as a **full snapshot with a version number**; an edit made against a stale version is rejected. Every question the service asks is one `interaction_request`. The same session may be open on two devices; the first confirm wins. Spec: [`docs/PROTOCOL.md`](docs/PROTOCOL.md) |
| Question sets | **Per-medium YAML banks** with two parts, both skippable and neither pre-selected. **Describe it**: tabs of multi-select chips (for books Characters, Plot, Setting and world, Writing style, Feel, Content warnings; for film and TV Story, Performances, Visuals, Sound and music, Pacing, Feel, Content warnings), about 150 chips each, a closed vocabulary that mixes praise and criticism. Then **at most three single questions** picked by weight (ending, length, recommend, and so on). The model may point at a chip it thinks I meant, shown dashed and never selected, but it cannot invent a descriptor. Chips are stored as stable keys, so they can be filtered later. One overall rating only; categories have no stars of their own. Drafts: [books](docs/question-banks/books.yaml), [films and TV](docs/question-banks/film-tv.yaml), [shared vocabularies](docs/question-banks/shared.yaml) |
| Pre-read checks (v1) | `intent` (chitchat, log_review, import_review, find_entries, edit_entry, answer_pending), `medium`, `format`, `revisit`, `extract_fidelity` (faithful, drops_opinion, adds_content; runs on the body extract), and nine `mentions_*` yes/no flags (sound, visuals, acting, story, pacing, ending, characters, world, writing). Defined in a **hot-reloaded YAML registry**, baked into the image and bind-mounted over top as Polaris does for `prompts.yaml`. Thresholds are set from real odds in Phase 0 |
| Revisits | Logging something already in the library asks: **new entry on the same work, or edit the old one**, showing the previous entry and rating. Several entries per work form a timeline |
| Edits and history | Editing a saved entry is a draft revision with a confirm tap. Every confirmed edit stores the previous version in an **append-only revisions table, kept forever** |
| Deleting | **Soft-delete only, and hard deletes are blocked by the database itself** (a `BEFORE DELETE` trigger aborts on entries, revisions, snapshots and answers). Deleting exists only on the plain entry view, never in chat. **There is no purge**: soft-delete is permanent, and backups hold every copy |
| Drafts | Kept until saved or discarded; they never expire. A Drafts list, with a count on the home screen |
| Covers | Downloaded once, stored under `data/covers/`, mirrored to R2. No hot-linked third-party images. TMDB's image terms get checked in Phase 0 before this is final |
| Degraded mode | If a metadata service is down, the review can still be logged as a **manual, unverified work** and enriched later with a "fill in details" action. A review is never blocked by someone else's outage |
| Tags | Genres from the provider plus my own free-text tags, shown on the card to accept or edit. Nothing is applied without being shown. The descriptor chips from Describe it are separate: they are answers, stored by category, and are the structured way to filter ("everything I called morally grey") |
| Entry card | One component for the draft and the saved entry. Rating is dragged across the stars and snaps to quarters, with +/- buttons and the number always visible. Unverified fields carry a dashed marker (tap for the source) and one Accept all button. A collapsed "why" sheet shows the decision odds, the tools that ran and their sources. After confirm the session closes and the card becomes the saved entry with an Edit button. Spec: [`docs/ENTRY_CARD.md`](docs/ENTRY_CARD.md) |
| Visual design | **Dark only**, no light theme (the scene is an evening in a dim room). Warm-tinted neutrals with one **lichen** green accent (Restrained strategy, accent kept to a small share of any screen), so covers carry the colour. **Lexend** for the interface and the review text, **Fraunces** for work titles and screen headings, both self-hosted (OFL) with licences kept beside them. **Lucide** (ISC) for every icon, via `@lucide/svelte` in the app so only the icons used are bundled, never loaded from a CDN. Responsive motion, feedback and state change only, 150 to 250 ms eased out. No chat bubbles: the transcript is plain blocks and the agent speaks in cards. Context in [`PRODUCT.md`](PRODUCT.md) and a seed [`DESIGN.md`](DESIGN.md); live mockups in [`mockups/`](mockups/) |
| Home | The chat input at the bottom, with drafts that are waiting and recent entries above it. Library, Drafts and Settings are one tap away in the top bar. A question typed into the composer is routed to the general chat by the `intent` decision. To open that chat on purpose, the **Library screen has an Ask button**; its start screen shows example questions and the last few chats |
| Testing | **Test first, always.** TDD with integration-weighted tests against "the world" (in-process fakes of TMDB, Open Library, the chat and decisions models, Polaris and R2, all with fault injection). **Every boundary is tested on three routes: happy, bad (invalid, hostile or huge input) and sad (the world misbehaves, with sticky outages).** Go native fuzzing on everything that parses untrusted input, property tests for the invariants, a **fixture corpus** of sample messages with the expected parse, decision outcome and tool sequence, and a Playwright state and screenshot baseline for every mockup frame. **Extensive CI from the first commit**: race detector, `staticcheck`, `golangci-lint`, `govulncheck`, CodeQL, gitleaks, dependency review, image scan, nightly fuzzing. Specs: [`docs/TESTING.md`](docs/TESTING.md) and [`docs/CI.md`](docs/CI.md) |
| Photos | My own photos on a review (a ticket, a shelf) are deferred to Phase 5. The `media` table is used earlier, by screenshot import in Phase 3 |
| Fable import | **Fable has no export.** The migration is a first-class image import in Phase 3: I attach screenshots in the chat and the agent transcribes them into drafts, asking only about gaps. **One session per title**, with any number of images for it. Originals are kept as the entry's source and mirrored to R2. The pinned model reads the images (its listing shows image input), tested on real screenshots in Phase 0, with its own role so another model can replace it |
| First media | **Books (Open Library) and films and TV (TMDB) together** in Phase 1, so the merge and disambiguation logic is proven against two real sources |
| Ratings | **Quarter-star steps from 0.25 to 5.0**, stored as an **integer 1–20 (quarter-stars)** plus the scale name, so the scale can change later without a migration. **Favourite is a separate flag**, not the same as a top rating |
| TV | A work is the **series** in v1. The schema lets an entry point at a season later, so there is no migration, but only the series flow is built |
| Review body | My words, always, **extracted and never written**. The agent returns quoted spans; code locates them in my messages (snapping a slip to my exact wording, dropping what has no match) and the body is the original text at those positions. A decisions `extract_fidelity` check is a second, never-blocking gate that warns when an opinion looks dropped. What was not kept shows under "left out" with a tap to include it. Typing the body myself is always allowed. The verdict is a verbatim span or empty. Full spec in [`docs/ENTRY_CARD.md`](docs/ENTRY_CARD.md) |
| Spoilers | **Not handled in v1.** No field, no decision check. Revisit if it starts to matter |
| Voice | **Deferred.** Typing only until the rest works |
| Storage | SQLite on the potato (`modernc.org/sqlite`, pure Go — the same choice as Warbler), plus plain files for covers |
| Backups | Nightly mirror to Cloudflare R2 using the **existing `rclone` remote** already configured for Vaultwarden (`r2:`), plus a JSON export |
| Hosting | The potato: Go binary with an embedded SvelteKit frontend (`go:embed`), Docker Compose, image pinned by digest in GHCR, `tailscale serve` on port **8893** (verified free) |
| Updates | The Meridian/Polaris/Warbler pattern: host-side watcher, digest pinned in `.env`, health check, rollback. **A settings panel with an Updates screen** (check, update now, restart, the eight states from Warbler) and a Services screen showing what each dependency does when it is down. Update is refused while a reply is being written. Spec: [`docs/UPDATE.md`](docs/UPDATE.md) |
| Discovery in Meridian | A tile with a health probe and one vital (see [§8](#8-where-it-runs)) |
| Auth | None. Tailnet-only is the boundary, exactly like Polaris and Warbler. No passwords to manage |
| Export | JSON (one file per work + review, plus a single archive) — the format the data model below *is* |
| Publishing | Private in v1. A **static public site is a later phase**; entries keep a `status` field so it needs no migration |
| Licence | MIT, matching Polaris, Meridian and Warbler |

## 3. Open questions

What is still open, each with the phase that needs it. None of these blocks the rest of the plan.

1. **Providers beyond film, TV and books** (Phase 2). MusicBrainz needs a user agent and 1 req/s; IGDB
   needs Twitch OAuth; podcasts and theatre have no obvious single source. Decide per medium as each is
   reached, and let Polaris cover the rest until then.
2. **TMDB's image and attribution terms** (Phase 0). The cover-caching decision in [§2](#2-decisions-already-made)
   is final only once this is read.
3. **Polaris's ask endpoint and container networking** (Phase 0). The exact path, and how the Cairn
   container reaches Polaris's host-published port.

Defaults assumed without asking, because they follow from decisions above. Say so if any is wrong:

- **Dates.** The phone sends its local date and timezone with every message, so "finished last night"
  resolves against where I am, not the server's clock.
- **Tool output is data, never instructions.** Web text returned by Polaris is shown to the model as
  quoted material, and no guardrail depends on the model resisting it. The deterministic checks in
  [§5](#5-the-review-flow-and-its-tools) are what keep a bad result from becoming a saved fact.
- **Spend is recorded, not yet shown.** Every decision, model and Polaris cost is stored per call; a spend
  view waits until it is wanted.

Resolved and moved into [§2](#2-decisions-already-made): the name, public or private, the rating scale,
the unit of TV review, whether the agent saves silently (it never does), voice input, spoilers, the
first providers, the general chat's powers, transport, the decisions registry, the free-form Polaris guard,
the GHCR name (not publicly taken; first publish confirms), edit history, deletion, draft expiry, question
sets, the main model, testing, revisits, covers, degraded mode, tags, the home screen, photos, the
first media, the Describe it step with content warnings, and the settings and update flow.

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
 │   pre-read: decisions (one batched call per message)      │
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
- **The pre-read is a hint, not a gate.** The decisions call runs before the model turn, its odds are shown on the
  card, and each check only acts when its non-quiet option clears a threshold. The model sees the result
  framed as an automatic pre-read, not an instruction, so a wrong guess gets ignored instead of obeyed.
  Anything that must be true (a rating, a body) is enforced by deterministic code, never by a decision model.
- **The question cards are a tool call.** `ask_reviewer` is a model-callable tool whose result is
  "here are the questions to ask, here's their shape". The Go service turns that into UI; the answers
  come back as the tool result. No prompt is trusted to produce JSON that the UI then guesses at.
- **Local-first writes.** A finished entry is committed to SQLite before anything else happens; R2 is a
  mirror, never a source of truth. A failed mirror shows a warning in the app, not a lost review.

### Why nova rather than a bespoke loop

- The harness already has tools, providers, permissioning, sessions, compaction and a JSONL protocol
  (`projects/nova/docs/`). A new app shouldn't re-implement tool-calling.
- Multi-step flows (look up → disambiguate → ask → save) are exactly what a loop with tools is for.
- Nova's extension protocol has a similar idea (`ui_request`: `form`, `select`, `input`, `confirm`), but
  Cairn's cards (tabs of chips, an entry card) are its own vocabulary in
  [`docs/PROTOCOL.md`](docs/PROTOCOL.md), so nova's UI events are not reused.

**Deferred architectural choice, now leaning one way:** embed nova directly (my own UI, my own state)
vs ship Cairn as a nova *extension* first. A custom chat with entry cards and a general-chat mode points
strongly at embedding. Phase 0 confirms it with a spike, not an opinion.

## 5. The review flow and its tools

### The flow

| Step | Who | What happens |
|---|---|---|
| 1 | me | Free-form message in a chat, typed, from the phone |
| 2 | app | **Pre-read:** one batched decisions call reads the message (intent, medium, format, themes). Skipped if empty; never blocks the turn |
| 3 | app | **Deterministic parse:** rating, date and obvious cues pulled out of my words by plain rules. Nothing found means the field is left for the questions |
| 4 | me | If the medium read was split, a quick "book or film?" card, answered before any lookup |
| 5 | agent | `media_lookup` — search the right library by title (+ creator/year if I gave them) → 3–5 candidates with tiny payloads. `lookup_web` runs on its own when the APIs return nothing, and is offered as **Ask Polaris** when an API errors or times out |
| 6 | me | Taps the right one, or "none of these" |
| 7 | agent | `merge_candidates` — fetches full metadata for the chosen work, stores a per-source snapshot, returns the merged canonical draft with web-sourced fields marked unverified |
| 8 | agent | `ask_reviewer` — the **Describe it** step (category tabs of chips, hints dashed) plus up to 3 single questions, each with a type (`tags`, `choice`, `short_text`, `skip`) |
| 9 | me | Answers, skips some, edits |
| 10 | agent | Builds the draft entry: body and verdict as verbatim spans of my messages (code-verified, then decision-checked), answers as their own fields, canonical metadata attached |
| 11 | me | Reviews the entry card: tap-to-edit any field, or type a correction in the chat; one confirm tap |
| 12 | app | `save_entry` → SQLite commit → export row queued → R2 mirror on the next run |

Nothing in steps 2–10 writes a saved entry. Step 11 is the only commit, and it is always mine to make.
In-progress work lives in a draft that can be resumed after closing the app.

Branches off the main flow:

- **Revisit.** If the work is already in the library (the `revisit` decision, backed by an exact match
  on the chosen work), the card shows the previous entry and rating and asks: new entry, or edit the
  old one.
- **Outage.** If a metadata service is down, times out or finds nothing, the agent says so and offers three
  ways on: **Try again**, **Ask Polaris** (the web lookup; hidden or disabled with a reason if Polaris is
  itself unreachable), or **Log it without details**, which logs against a manual, unverified work that a
  "fill in details" action can retry later.
- **Edit.** Saying "change my Dune rating" in the general chat opens a draft revision of that entry with
  the same card and confirm tap. The previous version goes into the revisions table.

### Tool contracts (shape, not final names)

| Tool | In | Out |
|---|---|---|
| `media_lookup` | `query`, optional `medium`, `year`, `creator` | up to 5 candidates: `provider`, `provider_id`, `title`, `year`, `creator`, `medium`, `thumb` |
| `lookup_web` | `title`, `medium`, optional `creator`, `field` | labelled fields each with a source URL, plus the Polaris thread id and citations. Calls Polaris with `source: cairn` |
| `ask_polaris_freeform` | `prompt` | Polaris's answer and citations. The escape hatch; logged only, no confirm tap |
| `merge_candidates` | `provider`, `provider_id` | merged canonical draft + `sources[]` snapshots; flags conflicting fields and any web-derived (unverified) ones |
| `ask_reviewer` | `medium`, `categories[]` (tabs of chips), `questions[]` (each with `id`, `prompt`, `type`, `options?`, `skipped_ok`) | `answers[]` — chip selections per category and typed values, plus what was skipped |
| `save_entry` | the full draft | committed entry id |
| `read_source_image` | `media_id` | a transcription: `title`, `creator`, `rating_shown`, `date_shown`, `text`. Stored as the media row's `transcript_json`; used as the "original" for span extraction. Phase 3 |
| `search_library` | text, filters | my own past entries (so "have I reviewed this already?" and "what did I watch in March?" are answerable). Reads only; edits happen through a draft revision and the confirm card |

The decisions layer is not a model-callable tool. It is a pipeline step the service runs before each turn.

Guardrails enforced in code, not in the prompt:

- `save_entry` refuses if `rating` or `body` is empty, or if either was never seen in my own input or
  answers. The agent cannot save an opinion I didn't give.
- The body and verdict are checked for containment in my messages (or marked as typed by me) before a
  draft is built; text that cannot be located is dropped, never kept.
- Ratings are accepted only as a quarter-star value. A parsed value that isn't on a quarter snaps to the
  nearest one and the card shows what was stored; a vague phrase ("a four and a bit") is asked about,
  never guessed.
- Web-derived fields cannot reach the canonical work without my accept tap.
- `ask_reviewer` is capped (one Describe step, at most three single questions, and a limit on rounds) so a
  chatty model can't turn the flow into an interrogation. If I skip everything, the entry still saves.
- Every tool call, decisions pre-read (with its odds and cost) and Polaris call is logged with its inputs, so
  "why does this entry say that" is answerable.

## 6. Data model

Media-agnostic because media-specific columns are how this kind of app dies. Medium-specific facts live
in JSON, validated per medium by a schema table in code.

```sql
works (                      -- the thing itself, canonical and merged
  id, medium, title, original_title, year,
  creator_json,              -- [{role: director|author|artist|developer, name}] — role is data
  details_json,              -- medium-specific: runtime, pages, isbn, platform, alternate_titles, series…
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
  verdict TEXT,                        -- one line, a verbatim span of my text or empty
  body TEXT,                           -- my words
  body_origin TEXT,                    -- extracted | typed | mixed | imported
  imported_from TEXT,                  -- e.g. fable; NULL for entries written in Cairn
  body_spans_json TEXT,                -- where each kept span sits in the session's messages
  format TEXT,                         -- cinema | streaming | audiobook | hardback | …
  finished_on TEXT,                    -- local date
  status TEXT,                         -- draft | saved | published (published is Phase 6)
  deleted_at TEXT,                     -- soft delete only; hard deletes are blocked by trigger
  created_at, updated_at
)

answers (                    -- the blanks the agent asked me to fill; descriptor chips are answer_type `tags` (answer_json a list of stable values)
  id, entry_id, question_id, prompt, answer_type, answer_json, skipped INTEGER
)

sessions (                   -- the app's own conversations; nova's are not importable
  id, kind,                  -- entry | general
  title,                     -- for a general chat, its first question, so past chats can be listed
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

decision_calls (            -- every decisions call, so thresholds can be tuned from real use
  id, session_id, message_index, backend, model,
  odds_json,                 -- every check's full probability vector
  fired_json,                -- which checks cleared their threshold
  cost_usd, latency_ms, created_at
)

tags (id, name) · entry_tags (entry_id, tag_id, source)   -- source: genre | mine
media (id, entry_id, session_id, path, kind,   -- kind: source_screenshot (Phase 3) | photo (Phase 5)
       transcript_json)                -- what the model read from the image, for source_screenshot
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
   that lets Open Library and a web source agree they mean the same book. A film adaptation has its own
   IDs and stays a separate work.
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

### What counts as one work

- **Books.** A work is the Open Library *work* (the abstract book), not an edition. A translation or a
  different printing is the same work. The edition I actually read (ISBN, publisher, pages, narrator)
  is kept in `details_json`, and the year is the work's first publication year, not the edition's.
- **Audiobooks.** The same work as the book. Print versus audiobook is the entry's `format`, so reading and
  then listening gives two entries on one work, shown as a timeline.
- **Series.** A work is what I actually read. A volume is its own work; the series name and position live
  in `details_json` so the library can filter by series. An omnibus is one work if that is what I read.
  There is no parent "series" work in v1.
- **Film and TV.** TMDB's movie and TV ids are distinct, a miniseries is TV, and a remake or adaptation is
  its own work. TV is series-level in v1 (see [§2](#2-decisions-already-made)).

A merge never deletes a snapshot. `work_sources` is the audit trail, and re-merging is a command, not a
migration.

## 8. Where it runs

| Fact | Value |
|---|---|
| URL | `https://lepotato.taild95077.ts.net:8893` (tailnet only) |
| Port | `8893` — **verified free** on the potato 2026-10-30 (in use: 22 53 80 443 5335 8080 8081 8418 8766 8888 8890 8891 8892 8899 11434) |
| Container | Compose project at `~/Cairn`, image pinned by digest in `.env`, published `127.0.0.1:8893` only, non-root, read-only rootfs |
| Data | `~/Cairn/data/` — `cairn.db`, `covers/`, `media/` (source screenshots, later photos), `exports/`, `backups/` |
| Config | `decisions.yaml` (the checks: wording, options, thresholds, backend) and `questions/*.yaml` (the per-medium banks and the shared vocabularies), baked into the image and bind-mounted over top so host edits hot-reload, the way Polaris handles `prompts.yaml` |
| Health | `GET /healthz` → `{"status":"ok","version":"r…"}` only (two fields, like Warbler's) |
| Secrets | `OPENROUTER_API_KEY` (main model and the Jev decisions backend, one key) and metadata-provider keys in `~/Cairn/.env`; `rclone` config for R2 already exists on the box |
| Polaris | Colocated at `127.0.0.1:8899`. Cairn calls its ask API (`gateway.AskRequest`/`AskResponse`: `source`, `citations`, `cost_usd`). How the Cairn container reaches the host-published Polaris port (host networking or `host.docker.internal`) is a Phase 0 item |
| Updates | Host watcher + `cairn-update.path`, digest pinned, health check, rollback |
| Backups | Nightly at 03:30: SQLite snapshot → `data/backups/` (rotating) → `rclone sync` to `r2:cairn-backups/`; JSON export on demand |
| Meridian | Tile `everyday`/`backbone` with a health probe on `http://localhost:8893/healthz`; vital: **reviews this month** (a count that means something) |
| Health check | Adds `check_http "Cairn" "http://localhost:8893/healthz"` to `maintenance-setup/service-healthcheck.sh` in the same change that deploys it |

The outside traffic this app causes is: the model calls (OpenRouter, including the decisions backend), metadata lookups to
the provider APIs (titles and ids only), web lookups through Polaris (which searches via SearXNG, with
Brave as a fallback), and the R2 mirror. Nothing else leaves the box.

## 9. Phases

Each phase ends with "Done when". Stop and ask at every **[HUMAN]** item.

The phases are delivered as **vertical slices**, one plan file each, so another agent can pick one up cold.
The order, the dependency graph and the status table are in [`docs/slices/README.md`](docs/slices/README.md),
and the standing rules are in [`docs/BUILD.md`](docs/BUILD.md).

| Phase | Slices |
|---|---|
| 0 Prove the loop | S00 walking skeleton, S01 spikes |
| 1 One entry, two sources, end to end | S02 contract, S03 domain core, S04 data layer, S05 banks and decisions, S06 providers, S07 session engine, S08 agent pipeline, S09 server, S10 frontend foundation, S11 logging UI, S12 reading UI, S13 deploy and update, S14 backups |
| 2 Every medium, one library | S15 web fallback, S16 search and chat, S17 more media |
| 3 Import from screenshots | S18 |
| 4 Trust it with the data | S19 |
| 5, 6 Comfort, publication | S20 parked |

### Phase 0: Prove the loop (spike, throwaway code allowed)

- Read `projects/nova/docs/INTEGRATION.md` and `examples/embed/`; write a small program that runs one
  nova conversation with **one** tool (`media_lookup` against TMDB or Open Library).
- Make one real **decisions** call (the Jev backend, through nova's `decide` package and `Roles`) from Go with a few review-shaped questions (intent,
  medium) and record the odds on a handful of sample messages, including a split one like "Dune".
- Make one real **Polaris** call with `source: cairn`; confirm the exact endpoint path, the response, the
  `citations` shape, and that the Cairn container can reach it.
- Answer the deferred architectural choice in [§4](#4-architecture) (embed vs extension) with what the
  spike showed.
- Get a TMDB key and an Open Library client working (rate limits and attribution requirements written down).
- **UI mockups of every screen: done** (40 frames across seven pages in [`mockups/`](mockups/)). Phase 0 only re-checks them on a real phone.
- Pull a handful of real Open Library works and editions (a series volume, a translated book, an audiobook
  edition) and write down which fields are reliable: first publication year, series, ISBNs, covers.
- Test the pinned model on about five real Fable screenshots (star icons, small type, a long review across two images) and record what it gets wrong.
- Read TMDB's image and attribution terms and write the cover-caching decision down.
- Write the first **fixture corpus** (about 30 sample messages with expected parse, expected decision outcome
  and expected tool sequence), then run the pre-read and the pinned model against it. Set the first decision
  thresholds from the real odds.
- **[HUMAN]** autumn reviews the corpus and the thresholds.

**Done when:** a free-form sentence in a terminal produces a real candidate list from real metadata,
the decisions and Polaris calls both work from Go, and the embed-or-extension choice is written down.

### Phase 1: One entry, two sources, end to end

- Go service + embedded SvelteKit shell; SQLite with the schema above; `/healthz`.
- Sessions: a per-entry chat over a WebSocket with a server-side event log and resume, resumable drafts,
  and a Drafts list.
- The revisit flow (new entry or edit the old one) and the manual-entry fallback for outages (Try again and
  Log without details; Ask Polaris arrives with `lookup_web` in Phase 2).
- Revisions and soft-delete, with the database triggers that block hard deletes.
- Pre-read → `media_lookup` → pick → `merge_candidates` → `ask_reviewer` → draft card → confirm →
  `save_entry`.
- Two sources wired properly: **books (Open Library) and films and TV (TMDB)**, so the merge and
  disambiguation logic is exercised from day one.
- The entry card ([`docs/ENTRY_CARD.md`](docs/ENTRY_CARD.md)): metadata block, my body, answers, rating (quarter-stars), unverified markers.
- **Deployment from the first push:** the image published to GHCR by digest, the host watcher
  (`cairn-update.path`), Settings with the **Updates** and **Services** screens
  ([`docs/UPDATE.md`](docs/UPDATE.md)), and the Meridian tile and health check.
- **Nightly SQLite snapshot and R2 mirror from the first deploy**, so the first real review is already backed up.

**Done when:** a review typed as a sentence becomes a saved entry with correct metadata and 3+ answers,
from the phone, over Tailscale, with no manual field entry, for both a book and a film; an update has been
run from the Updates screen on the potato, including a deliberately failed one that rolled back; and a
nightly backup has run and appeared in R2.

### Phase 2: Every medium, one library

- Medium table with per-medium providers and per-medium question sets.
- `lookup_web` and the unverified-field flow, so anything an API misses can still be logged.
- Library view: filter by medium, sort by date/rating, search my text (SQLite FTS5).
- The general chat, backed by `search_library`, with edits going through a confirm card.
- Duplicate detection and the "this already exists" path.

**Done when:** reviews of four different media coexist in one library and the filters work on a phone.

### Phase 3: Import from screenshots

- Attach images in the chat: an upload to the session (`POST /api/sessions/{id}/media`), stored under
  `data/media/`, with a `media` row per image.
- `read_source_image`: the pinned model transcribes a screenshot into title, creator, the rating and date
  as shown, and the review text. A message with images and no text defaults to the import intent by
  plain code; the decisions layer reads only the accompanying text (it takes text, not images).
- **One session per title, any number of images** (a long review can span two screenshots). The draft goes
  through the normal flow: the transcription is the "original" the body spans are taken from, the
  screenshot sits beside the card, fields read from the image carry a marker and need my tap, and the
  agent asks only about gaps (a date, an ambiguous title).
- Entries record `imported_from` (for example `fable`) and `body_origin = imported`; the screenshots are
  kept as the entry's source and mirrored to R2.
- Fable has no export, so this is the migration: the first real use is bringing the Fable reviews in.

**Done when:** a Fable screenshot becomes a saved entry with the screenshot kept beside it, and ten real
Fable reviews have been imported and read back correctly.

### Phase 4: Trust it with the data

- The backups that have run since Phase 1 gain receipts in the app (the Backups and export screen), and a
  **restore is exercised for real**.
- JSON export (single file + per-entry) and an import that round-trips.
- `cairn verify`: files present, covers accounted for, database integrity, archive readable.

**Done when:** I can export everything, wipe a test copy, import it back, and diff clean; and an R2
restore has actually been performed, not just designed.

### Phase 5: Comfort

- Photos, season-level TV reviews, voice input, spoiler handling — whichever of these turned out to
  matter.

**Done when:** the thing I reach for is this app, not a notes file.

### Phase 6: Optional publication

A static, read-only build of published entries, no login, no writes. Entries already carry a `status`
field, so this adds a build step and a way to mark an entry published, and nothing else.

## 10. Costs

| Item | Estimate |
|---|---|
| Agent model calls per review | a handful of small calls; a review should cost **cents**, and the flow is designed to be 3–6 model turns, not 30. At the pinned model's prices a 20k-token-in, 2k-token-out turn is roughly $0.004 |
| Decisions pre-read | on the Jev backend about $0.042 per million input tokens and free output, so a batched call is a fraction of a cent. Logged in `decision_calls.cost_usd` |
| Polaris web lookups | Polaris reports `cost_usd` per ask; it is stored with the snapshot. Most reviews should never need one |
| Metadata | TMDB and Open Library free (attribution required); MusicBrainz free with a rate limit |
| Storage on the potato | a review is kilobytes; covers are ~100 KB each, so a thousand entries is ~100 MB. Kept screenshots add roughly 300 KB to 1 MB each, so 200 imported reviews is about 100 to 200 MB |
| R2 | the free tier is far beyond this |
| Electricity | the potato is already running |

If a review costs more than a few cents, the design has drifted: the fix is fewer, fatter tool calls,
not a cheaper model.

## 11. Rules that are not negotiable

1. **My words are mine.** The agent structures, asks and chooses; it never invents an opinion, a
   rating, or a quote. The body is made only of spans that code found in my messages, and `save_entry`
   enforces it, not a prompt.
2. **Nothing saves without my confirm tap.** Drafts are not entries.
3. **Local-first.** SQLite before R2, always. A mirror failure is a warning, never a lost review.
4. **Source snapshots are append-only.** A silent metadata rewrite of my history is a bug. Web-derived
   facts are proposed and marked unverified until I accept them.
5. **Review text goes only where the plan names.** The LLM (OpenRouter, including the decisions backend) sees my text and any screenshots I attach; that
   is the cost of the feature. Metadata APIs and Polaris get titles, creators and ids, via structured
   arguments by default. The one path where free text could reach a web search is
   `ask_polaris_freeform`, which is the deliberate escape hatch and is always logged.
6. **Classifier output never decides.** A decision model's probabilities steer and suggest. Anything that must be true
   (a rating, a body, a quarter-star value) is enforced by deterministic code.
7. **Export must outlive the app.** If this code vanished tomorrow, the JSON should still be readable.
8. **Tailnet-only.** No Funnel, no public port, no accounts.
9. **Nothing is ever hard-deleted.** Deletes are soft, enforced by the database, and there is no purge.
   Every confirmed edit keeps the previous version.
10. **Only my own origin and host may talk to the service.** There is no login, and browsers do not apply
    the same-origin rule to WebSockets, so the `Origin` header is checked on the socket upgrade and on every
    state-changing request, and the `Host` header is checked against an allow-list on every request. Both,
    because a DNS-rebinding attack makes `Origin` and `Host` the attacker's own name.

## 12. Working agreement

- **One slice at a time.** Read [`docs/BUILD.md`](docs/BUILD.md) and the slice plan first; commit small; push per slice.
- **Stop and ask at every [HUMAN] item.** Don't invent answers to my questions.
- Keep scope tight: no new dependency, provider or service without asking.
- Small readable functions, a functional core (merge, extract, compose) with thin imperative edges;
  pure merge logic is tested hard because it is the part that can silently corrupt history.
- No telemetry, no third-party scripts, fonts or icons loaded from outside.
- **Test first, all three routes, fuzz the parsers, and keep CI strict from the first commit** (see [`docs/TESTING.md`](docs/TESTING.md), [`docs/CI.md`](docs/CI.md)). We are still planning: the build plan itself is held until every design question is settled, and nothing is built yet.
- Write for a reader new to the technical vocabulary; explain terms in plain language.
- Every new pre-read check and every guardrail lands with fixture-corpus cases.
- Findings and uncertainties go in `NOTES.md` as they are learned.
- No emojis anywhere: UI, docs, commit messages.
