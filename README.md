# OmniReviewer

**A holding name.** It will be renamed before the first release — the thing it describes is "one
private place to review anything I finish", and that deserves a better word than OmniReviewer.

Reviews for **any** medium in one place: films, TV, books, games, albums, podcasts, theatre, whatever
comes next. Not a catalogue of what exists — a record of what *I* thought, typed the way I actually
talk, with an agent doing the boring part: pulling the title, the year, the creators, the cover from
the right library, merging them into one clean entry, and then asking me the handful of questions
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
   │  free-form: "finished X, loved it, 5/5, the sound design though…"
   ▼
 Go service on the potato  ──►  Nova agent loop (embedded, projects/nova)
   │                                │  tools: media_lookup · merge_entry · ask_reviewer · save_entry
   │                                ▼
   │                          one canonical entry  ──►  SQLite (works + reviews + answers)
   │                                                    └──► nightly rclone mirror → R2
   └── HTTPS via tailscale serve, tile in Meridian
```

## Read this first

- [`PLAN.md`](PLAN.md) — what gets built, in what order, and every decision made so far
- `PLAN.md` §5 — **open questions I have to answer before building starts**, which is the whole point
  of writing this down now

## Non-negotiables, early

- Private by default. Tailnet-only, no accounts, no passwords — the same posture as everything else
  here.
- No review is ever lost to a failed save: local-first writes, mirrored after.
- Star ratings and reviews are mine; providers are only ever used to *look things up*, never to store
  my opinion.
- No vendor gets a permanent monopoly on the data: everything exports as JSON.

## Licence

MIT — same as Polaris, Meridian and Warbler.
