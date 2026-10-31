# S17. More media (a repeatable template)

## Purpose

Add one medium at a time without changing the architecture. This file is the **template**: each medium becomes a
small sub-slice (S17a, S17b, ...) that follows the checklist below. Which media come first, and in what order, is
the operator's call; the plan names the candidates but commits to none.

## Depends on

S06 (the provider interface), S05 (banks), S15 (the web fallback covers the gaps meanwhile). Phase 2.

## Where we start

Books and film and TV exist. Everything else (games, albums, podcasts, theatre, other) is logged today through the
web fallback or as a manual work, with no provider and no tailored questions.

## Settled

- The schema is already media-agnostic: a medium is a value plus medium-specific `details_json`, validated per
  medium in code.
- Candidates and their constraints are known: MusicBrainz (albums; needs a user agent and one request per
  second), IGDB (games; needs Twitch OAuth), podcasts and theatre (no obvious single source, so Polaris covers
  them until one is chosen).
- A new medium may add a Describe vocabulary; content warnings already apply to games.

## The checklist for one medium

1. **Choose and confirm** the provider with the operator, including key handling and any cost (an
   open-question batch before starting).
2. **Client and normalisation** behind the provider interface, with a raw snapshot, a size-capped decoder, rate
   limiting and a fuzz target.
3. **What counts as one work** for the medium, written into [`../../PLAN.md`](../../PLAN.md) section 7 (an
   album versus a single, a game and its remaster, a podcast show versus an episode).
4. **The bank:** a `categories` and `questions` file, written fresh, reviewed by the operator, validated by the
   loader, plus the `mentions_*` topic links.
5. **Medium-specific facts** for the card and the entry page, and the cover shape (square for an album, tall for a
   game or book).
6. **The world fake** and fixtures made of invented or public metadata.
7. **Tests** on the three routes, the privacy assertion, and the fixture corpus extended with messages for it.
8. **Mockup check:** render the card and library row for the medium and add frames to the mockups if the
   layout changes.
9. **Docs:** the plan, the bank, the status table.

## Tests and exit

The standard three routes for a provider ([`S06`](S06-providers.md)), plus a corpus extension and a mockup
check. No change to the session engine, the protocol or the contract should be needed; if one is, stop and ask
(trigger T10).

## Gates

An open-question batch with the operator per medium, and **G4** for any new thresholds.

## Open questions

- Which medium first, and which provider (the operator decides).

## Done when

For each medium: it can be logged end to end with verified metadata, tailored chips and a correct cover, and the
library shows four media coexisting once enough have shipped (the phase's exit).
