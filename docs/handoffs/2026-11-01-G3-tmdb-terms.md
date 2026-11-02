# G3: TMDB terms, answered 2026-11-01

**Gate.** G3 in [`../BUILD.md`](../BUILD.md) 6: "S06: before final cover caching. What TMDB's terms say about
caching and attribution, and the decision they imply."

**Status: answered.** Autumn confirmed all four points below on 2026-11-01 before any cover code was written.

## What was read

- TMDB API terms of use: <https://www.themoviedb.org/documentation/api/terms-of-use>
- TMDB logos and attribution: <https://www.themoviedb.org/about/logos-attribution>
- TMDB image basics: <https://developer.themoviedb.org/docs/image-basics>

Only the public terms were read. No TMDB account, key or data was used to answer this gate.

## What the terms say

1. **Attribution is required, with exact words.** The terms require this notice, placed prominently in or on the
   application: *"This [website, program, service, application, product] uses TMDB and the TMDB APIs but is not
   endorsed, certified, or otherwise approved by TMDB."* The TMDB logo must identify the use of TMDB, must sit
   below the app's own marks in prominence, and must not imply endorsement.
2. **Caching is capped at six months.** You may not cache "for longer than 6 months, any information obtained
   through or from TMDB or the TMDB APIs". On termination you must promptly delete or purge all TMDB content,
   including cached content.
3. **Local image storage is not forbidden.** The terms do not require hot-linking; they do forbid using TMDB as
   an image host for banners or graphics. A locally stored poster is cached TMDB content, so it falls under the
   six-month cap.
4. **Commercial use needs a separate written agreement.** Cairn is private and single-user, so that agreement is
   not needed. The terms also forbid excessive bandwidth or anything that degrades TMDB's service; no numeric
   rate limit is stated, so Cairn imposes its own.

## The decision Autumn made

1. **Ship the exact notice and the TMDB logo.** The notice is a constant in the provider package, with the logo
   as a bundled asset. The rule: it appears in Settings, About, and on any screen that shows TMDB data. S06 ships
   the string and the asset; S11 and S12 place them. Wording for Cairn: *"This application uses TMDB and the
   TMDB APIs but is not endorsed, certified, or otherwise approved by TMDB."* (The bracketed placeholder in the
   terms is replaced with "application", which is what Cairn is.)
2. **Store covers locally with a 180-day refresh.** Every cover records when it was fetched (the file's own
   modification time, so there is no second copy of the state). A cover older than 180 days is re-fetched on its
   next use. This is stricter than the terms require and needs no background job to be correct: freshness is
   checked when the cover is read.
3. **Covers ship in S06** under these two rules rather than in a follow-up slice.
4. **No hot-linking.** Offline, the library still shows its covers.

## What this changes in the plan

- `PLAN.md`'s "covers are downloaded once and stored locally; nothing is hot-linked" stands, with the 180-day
  refresh added as the mechanism that keeps it inside the terms.
- A deletion path for cached TMDB content (the termination clause) is not needed while the app is hers alone, but
  the records to support one now exist: covers are a directory keyed by provider and id, and `data/` is not the
  database.
- No new dependency was needed for any of this.

## The one asset that is not in the tree

The notice text ships as `providers.TMDBAttribution`, with a test pinning the exact words. The **TMDB logo file**
is not in the repository: it is TMDB's brand asset, and it has to be dropped into `web/static/` (and imported by
S11, not hot-linked) before any screen shows TMDB data. Recording it here rather than inventing a placeholder
image: a fake logo would violate the same clause a missing one does.

## What is still the operator's

- The live check: a real TMDB key, and a real film, miniseries and remake coming back correctly
  (`docs/slices/S06-providers.md`, "Done when"). Nothing in the repository or in CI may use a key.
