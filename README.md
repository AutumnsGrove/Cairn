# Cairn

A cairn is a stack of stones left on a trail to say "someone passed this way". This is the same thing
for everything I finish: one private place to review any medium.

Reviews for **any** medium in one place: films, TV, books, games, albums, podcasts, theatre, whatever
comes next. Not a catalogue of what exists — a record of what *I* thought, typed in a chat the way I
actually talk, with an agent doing the boring part: pulling the title, the year, the creators, the cover
from the right library, merging them into one clean entry, and then asking me the handful of questions
that turn a paragraph of enthusiasm into a review worth keeping.

Replaces a mix of apps (Fable for books today) with one thing on my own infrastructure that I can
point at from my phone, that keeps its own backups in Cloudflare R2, and that will still open in ten
years.

**Status: planning only.** No code yet. This repo exists so the shape is written down before it is
built. `PLAN.md` is the source of truth; `README.md` is the pitch.

## Why

- **One habit, one place.** Today: one app for books, another mental bucket for films, a third for
  games. The medium-specific apps are good at *their* medium and useless across media.
- **Free-form in, structured out.** Rating-and-typing into a 12-field form is why reviews don't get
  written. "I finished Blade Runner 2049 and it floored me, easy 5/5" should be enough to start, with
  the agent filling blanks and asking the rest as questions.
- **My reviews, my data, my copy.** SQLite on the potato, mirrored to R2, exportable as plain JSON.
  No subscription, no app deciding my ratings are theirs.
- **Media-agnostic from day one.** Retrofitting "books only" into "everything" is the version of this
  that never happens.

## The shape, in one picture

```
 phone (installed PWA, Tailscale on)
   │  chat: "finished X, loved it, 5/5, the sound design though…"
   ▼
 Go service on the potato
   │  1. Fast decision model (Jev) reads the message first (intent, medium, format, themes)
   │  2. Nova agent loop (embedded) ──► OpenRouter
   │       tools: media_lookup · lookup_web · merge_candidates · ask_reviewer · save_entry
   │                 │                │
   │           TMDB / Open Library   Polaris (web fallback, source: cairn)
   │  3. one draft entry card ──► I tap to fix anything, then confirm
   │                                │
   │                                ▼
   │                      SQLite (works + reviews + answers)
   │                      └──► nightly rclone mirror → R2
   └── HTTPS via tailscale serve, tile in Meridian
```

## Read this first

- [`PLAN.md`](PLAN.md) — what gets built, in what order, and every decision made so far
- `PLAN.md` §3 — the **open questions** still to answer, which is the whole point of writing this down
  before any code

## Non-negotiables, early

- Private by default. Tailnet-only, no accounts, no passwords — the same posture as everything else
  here.
- No review is ever lost to a failed save: local-first writes, mirrored after.
- Star ratings and reviews are mine; providers are only ever used to *look things up*, never to store
  my opinion. Nothing saves without my confirm tap.
- A classifier or a web search can suggest; only deterministic code and I decide what is true.
- No vendor gets a permanent monopoly on the data: everything exports as JSON.

## Licence

MIT — same as Polaris, Meridian and Warbler.
