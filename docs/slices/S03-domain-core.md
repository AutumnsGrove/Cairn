# S03. Domain core

## Purpose

The pure heart of Cairn: the functions that guarantee "my words are mine". Rating parsing, date resolution, the
span locator, title matching, work merging and the save-time guards. No I/O, no clocks, no globals. This is the
most heavily tested code in the project, because it is what stops a model from putting words in the user's mouth
and a merge from silently rewriting history.

## Depends on

S00. **Runs in parallel with S01 and S02.** Everything else imports `internal/domain`.

## Where we start

Specs only: [`../ENTRY_CARD.md`](../ENTRY_CARD.md) (spans, rating, required fields),
[`../../PLAN.md`](../../PLAN.md) sections 5 to 7 (guardrails, data model, merge rules).

## Settled

- Ratings are **integer quarter-stars 1 to 20**; a value off a quarter snaps to the nearest one and is shown;
  a vague phrase is asked about, never guessed.
- **The body is spans.** Exact match first, then the closest passage by a normalised comparison; the stored text
  is the original's exact wording; a span with no close match is dropped and shown as left out; overlaps merge.
  The verdict is a span or empty. Typed text counts as the user's own input.
- Merge rules per field and **what counts as one work** ([`../../PLAN.md`](../../PLAN.md) 7).
- `save` requires a rating and a non-empty body made of the user's own words.
- Dates resolve against the **phone's** date and timezone, passed in.

## Scope

1. **S03.1 Rating.** `ParseRating(text) -> {value, status}` where status is stated, snapped, vague or none;
   `Format`; the scales handled ("4.5", "8/10", "4 out of 5", "four stars", "4 and a bit", unicode digits).
2. **S03.2 Dates.** `ResolveDate(text, now, zone)` for "last night", "yesterday", "on friday", "this morning",
   explicit dates; a clear result, an ambiguous one, or none.
3. **S03.3 Spans.** `Locate(source, quotes) -> {spans, leftOut}` and `Compose(spans)`; the normaliser
   (case, whitespace, punctuation for matching only); the closest-passage search with a cutoff and a bounded
   cost; overlap merging; verdict handling; a `ContainedIn(source, text)` proof used by the save guard.
4. **S03.4 Format cues.** Deterministic cues for cinema, streaming, audiobook, print, ebook, physical media, live.
5. **S03.5 Titles.** Normalisation and a same-work decision with a distance measure and thresholds; a result of
   same, different or ask.
6. **S03.6 Merge.** `Merge(sources) -> {work, conflicts}`: title, year, creators, cover, details, alternate
   titles; web-sourced fields stay proposals; idempotent; never loses a field; records disagreements.
7. **S03.7 Draft and guards.** `Entry` and `Draft` types, `ValidateForSave`, the state of each card block
   (`verified`, `unverified`, `accepted`, `image-read`), the required-to-confirm rule, and the diff used by the
   edit screen.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | "easy 4.5" gives 18; "5/5" gives 20; a clean quote is located exactly; two sources merge into one work |
| Bad | "4.3" snaps to 4.25 and says so; "a four and a bit" is vague; a quote the user never wrote is dropped; a 10 MB string is refused in bounded time; unicode lookalikes; a span that spans a newline; conflicting years across providers |
| Sad | (Pure code has no outages.) Cancellation of a long locate returns promptly; a pathological input cannot blow up time or memory |

**Fuzz targets with seed corpora:** the rating parser, the span locator and the title normaliser, the merge
function. **Properties:** every located span exists in the source; merge is idempotent and never loses a field;
`Compose(Locate(s, q))` is a subsequence of `s`. **Coverage floor 95% on `internal/domain`.**

## Gates

None.

## Open questions

- The closest-passage cutoff and the title-distance thresholds are *starting values*; they are tuned from the
  fixture corpus in S08 (G4).

## Done when

The domain package has no imports from elsewhere in the repo, every function has its three routes, the fuzz
targets run clean for the nightly time box, and a reviewer can read `spans.go` and believe invariant 1.
