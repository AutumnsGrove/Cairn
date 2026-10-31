# S06. Providers

## Purpose

Real metadata for books and films and TV: TMDB and Open Library clients that turn untrusted network JSON into
Cairn's domain types, keep a raw snapshot of what each source said, fetch covers, and behave well when the
network does not. Each lives behind a narrow interface with a fake in the world.

## Depends on

S03 (domain types, merge) and S01 (the real shapes and limits). **Runs in parallel with S04 and S05.** S08 needs
it.

## Where we start

Nothing exists. S01.4 and S01.7 supply real response shapes, rate limits, attribution rules and TMDB's image
terms.

## Settled

- One real API per medium first: **TMDB** for film and TV, **Open Library** for books.
- A work is the Open Library work, not an edition; the edition read goes in `details_json`; the year is the
  first publication year; a series is metadata; TMDB movie and TV ids are distinct; a miniseries is TV
  ([`../../PLAN.md`](../../PLAN.md) 7).
- Snapshots are stored raw (`raw_json`) with `trust = verified` for an API.
- Covers are downloaded once and stored locally; nothing is hot-linked. *Final only after G3.*
- Provider calls carry titles, creators and ids only, never review text (invariant 5).
- Keys come from the environment and never appear in logs.

## Scope

1. **S06.1 The lookup interface.** `Lookup(ctx, query, medium, hints) -> candidates` and `Fetch(ctx, id) ->
   work + snapshot`, defined where used, with one to three methods per interface.
2. **S06.2 TMDB client.** Search movie and TV, details, genres, external ids, image configuration; key from the
   environment; rate limiting and `Retry-After`; the attribution text the terms require.
3. **S06.3 Open Library client.** Search works, work and edition detail, series, covers; a polite user agent;
   rate limiting.
4. **S06.4 Normalisation.** Provider JSON to domain types, defensive about every field, plus the raw snapshot.
   A candidate is tiny: provider, id, title, year, creator, medium, thumb.
5. **S06.5 Covers.** Download with a size and dimension cap, a content-type check, storage under
   `data/covers/`, a placeholder on failure. (**G3** before this is final.)
6. **S06.6 World fakes.** Fakes for both services with fixtures made of invented or public metadata, trimmed,
   plus faults.
7. **S06.7 Candidate ranking.** Rank by match quality; at most five returned; ids that disagree across sources
   are surfaced, not hidden.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | A title returns clean candidates; fetching one yields a verified work and a stored snapshot and cover |
| Bad | Missing or null fields, a 50 MB body, HTML where JSON was expected, a title containing an injection or a control character, a cover that is not an image, a cover with huge dimensions (a decompression bomb), ids that disagree |
| Sad | A timeout, a 5xx, a 429 with and without `Retry-After`, a sticky outage across the second and third call, a slow stream that never ends, a cancelled context; each ends in a defined error the agent turns into the **lookup_failed** card |

**Fuzz:** both response decoders. **Privacy test:** the world's request log shows no review text in any provider
request. **Properties:** normalisation never panics; a re-fetch of the same id yields an equal work.

## Gates

**G3** before covers are final: what the terms say about caching and attribution, and the decision they imply.

## Open questions

- Whether to cache provider responses (proposed: a short in-memory cache to respect rate limits, never persisted
  beyond the snapshot).

## Done when

Both clients pass their three routes against the world and, as the operator's separate live check, return real
data for a book, a translated book, a film, a miniseries and a remake.
