# G4: the S08 corpus, the odds table and the thresholds

**Gate.** G4 in [`../BUILD.md`](../BUILD.md) 6: "S08: before tuning any threshold. The fixture corpus, the odds
table from a real run, the proposed thresholds, and the labelling you need from the operator."

**Status: decided on 2026-11-04, with Autumn's delegation, from four real-title cases.** S08.1 to S08.8 are built,
the fake-model suite is green, and four real-title live runs reached valid cards against the real model and
providers. Autumn delegated the labelling and threshold decisions; they are recorded under "Decisions" below. One
model-behaviour finding is open (see Risks), and it is not a threshold question.

**Do this first: rotate the temporary keys.** They appeared in the chat transcript and in `~/Sepia/.env`. They were
used again for the 2026-11-04 runs below. After the last run the key returned 401 from OpenRouter, so it looks
rotated or revoked already; confirm that and clear the file. A key must never be committed, logged or pasted into a fixture.

## Where this is

S08 is complete in the repository and G4 is decided. `main` is green: `make check` passes, including
`golangci-lint`, the race suite, the fuzz smoke, the web checks and the guard. The slice's status line says "done".
Nothing in the repository, a fixture, a test or a log is a real review, person, screenshot or key. The 30-case
CI corpus titles (`The Lantern Keeper`, `The Quarry Line`) and people (`Ada Voss`, `Ilse Marrow`) are invented. The
four-case live corpus uses real public titles (Dune, The Bear) as metadata only; its review text is invented.

The whole book and film flows reach a draft card against the fake model: the right work, rating, date, format,
body spans, verdict and answers. A model that invents a body or a verdict cannot store it; one that tries to pass a
rating is refused by the strict decoder; a hostile title or message is data and cannot become her words. The
pre-read being down, the model erroring or timing out, the step cap and the token cap each leave her message
stored with a notice. A provider outage offers `lookup_failed`, and "log without details" creates a manual,
unverified work. The saved entry is still the only commit: `save_entry` builds the card, and nothing in S08 saves
without her confirm.

**The real model has now been run** on a four-case real-title smoke corpus, using the operator's temporary keys.
The full labelling and the threshold decision are still hers. The invented 30-case corpus remains the CI suite;
its titles cannot be found by the real providers, so `make eval` runs the real-title live corpus instead.

## What I did

- `internal/agent`: nova embedded, the five tools (`media_lookup`, `merge_candidates`, `ask_reviewer`,
  `save_entry`, `search_library`), the pipeline from a message to a draft card, the versioned prompts under
  `internal/agent/prompts/`, the budgets and the two cost ledgers.
- `internal/world/chat.go`: a scripted OpenRouter chat fake that speaks the real SSE stream, so the suite drives
  nova's real provider.
- `internal/agent/testdata/corpus.json`: 30 invented messages with the expected parse, pre-read outcome, tool order
  and card, for the fake model in CI.
- `internal/agent/testdata/corpus-live.json`: four real-title cases (Dune book, Dune film, The Bear TV, Dune with no
  rating) for the operator's live smoke run. The review text is invented; only the titles are real metadata.
- `internal/agent/corpus_test.go`: the CI runner against the fake model; it passes 30/30, and the live corpus is
  loaded but never run in CI.
- `cmd/sepia-eval`: the operator's runner against the real model and decisions backend; `make eval` runs the live
  corpus.
- Migration 0005 (`turn_calls`), the `SEPIA_OPENROUTER_API_KEY` and `SEPIA_MODEL` settings (pinned, no aliases),
  and the fixed interaction-request event payload (`interaction_id`, `kind`, `payload`).

The corpus is ten book, ten film/TV and ten cross-cutting cases:

| Group | Cases |
|---|---|
| Book | `book-clean-4-5`, `book-no-rating`, `book-vague-rating`, `book-framing-only`, `book-no-date`, `book-audiobook`, `book-ebook`, `book-title-in-message`, `book-hostile-message`, `book-already-reviewed` |
| Film/TV | `film-clean-3-5`, `film-cinema`, `tv-broadcast`, `film-no-rating`, `film-no-date`, `film-vague`, `film-framing-only`, `film-physical`, `film-hostile-message`, `tv-no-rating` |
| Cross | `cross-split-medium`, `cross-provider-down`, `cross-empty`, `cross-only-rating`, `cross-injection`, `cross-title-injection`, `cross-revisit-new-entry`, `cross-huge-title`, `cross-malformed`, `cross-step-cap` |

Each case records her message, the pre-read odds, the expected intent and medium, the expected rating, date and
format, the model's tool order, the body quote and verdict, and whether the result is a card, a manual work, a
revision or no card at all.

## Live check (2026-11-04)

The operator's temporary keys were used in a shell environment only; they are not in the repository, a fixture, a
test or a log, and the temporary environment file was deleted. **They should be rotated because they appeared in the
chat transcript.**

- The pinned chat model `deepseek/deepseek-v4.1-flash-1017` exists and answers.
- The Jev pre-read is reachable through `/systemone` and returns real odds.
- OpenLibrary and TMDB answer; TMDB had one transient timeout and the `lookup_failed` → retry branch handled it.
- A four-case real-title run built valid cards for Dune (book), Dune (film) and The Bear (TV), and exercised the
  missing-rating `fill_gap` branch. The film and TV cases matched the expected fields. The two Dune book cases
  differed only where the model chose two exact spans instead of one and chose a verdict; both are her own words
  and the cards are valid. The fidelity warning fired once (`drops_opinion`), never blocking.
- **The medium check is the weak one.** "finished dune" read film 0.44 / unknown 0.42 / book 0.13; "watched dune on
  netflix" read film 0.97; "the bear on tv" read tv 0.67, just under the 0.70 threshold; the no-rating Dune read
  unknown 0.47 / book 0.37. When the check does not fire, the `choose_medium` card is what keeps the flow moving.
  On one run the model answered `choice=unknown` while its probabilities favoured film; the decisions layer
  rejected that as inconsistent, correctly. These are the threshold and wording questions for this gate.
- The deterministic format cue corrected the pre-read's "streaming" for "on tv" to `broadcast`, which is the
  code-decides rule working.
- Three bugs were found and fixed from the live run: a provider error could carry `api_key` in a URL (now
  redacted); an unknown medium handed the model an error instead of the `choose_medium` card; and the candidate
  result omitted the id the model needs to merge.

## Odds from the real runs (2026-11-04)

Four real-title messages, the pinned chat model and the real Jev pre-read, 5 runs in all (one earlier, one from
`make eval`, three after the changes below). Probabilities are the pre-read's; "range" is across runs.

| Case | Message | Medium read (range) | Right answer | Format read | Fires at 0.70 | Fires at 0.65 |
|---|---|---|---|---|---|---|
| `live-dune-book` | "finished dune, 4.5/5. ..." | film .40-.49, unknown .40-.42, book .09-.16 | book | not_stated .99 | no, asks (split read) | no, asks (split read) |
| `live-dune-no-rating` | "finished dune. ..." | unknown .45-.53, book .35-.42 | book | not_stated 1.0 | no, asks (split read) | no, asks (split read) |
| `live-dune-film` | "watched dune on netflix, 3.5/5, ..." | film .95-.97 | film | streaming 1.0 | yes | yes |
| `live-tv-bear` | "watched the bear on tv, 4.5/5, ..." | tv .67-.74, film .17-.21 | tv | not_stated .51 / streaming .49 | only 3 of 5 runs | yes, all runs |

Every other check was far from its threshold: `intent` log_review 1.0 and `revisit` no .99-1.0 on all four. The
`mentions_*` readings are mostly 0 or 1 with a few mid values (`mentions_story` .26-.38 on the Dune book cases,
`mentions_world` .57 on The Bear), all well away from 0.75 or from a case where it mattered. The fidelity odds are
not in the report, so `extract_fidelity` has no evidence either way.

## Decisions

Autumn delegated these on 2026-11-04. They rest on very little data, so only the one change with real evidence was
made.

- **`medium` 0.70 to 0.65 (changed in `config/decisions/policy.yaml`).** The Bear read tv between 0.67 and 0.74, so
  0.70 put it on the line and it fired in only 3 of 5 runs. The only wrong leader seen was film 0.49 for a book.
  0.65 sits between them. A wrong guess costs a question and never a stored word, so the cautious side wins.
- **Everything else unchanged:** `intent` 0.65, `format` 0.75, `revisit` 0.80, `extract_fidelity` 0.60,
  `mentions_*` 0.75, session override 0.80. No reading came near them, so nothing here could justify a move.
- **A verdict is allowed in the live run.** The agent may pick a verdict if it is an exact span of her own message
  (`cmd/sepia-eval`). An invented verdict, or any change to a stated expectation, still fails. This is the "my
  words are mine" rule, and the CI corpus is unchanged.
- **Split Dune read asks "which one?".** For "finished dune" the pre-read reads film or unknown, not book. Asking
  is the right outcome: the fallback is `choose_medium`, as designed.
- **Labelling covers the four live cases only.** The 26 other cases test plumbing against the fake model. Their
  expectations are unchanged and were not re-judged against what she would want. If she wants to, the cases to look
  at are the vague and missing ratings, the framing-only cases, the revisit cases and the outage cases.
- **The two recorded answers from 2026-11-02 stand:** the prompt is terse and plain with no emojis and may ask more
  than three questions across rounds; the corpus is 10 book, 10 film/TV, 10 cross-cutting.

To reproduce the fake run, which is the CI check and not hers:

```bash
go test ./internal/agent -run TestCorpusAgainstTheFakeModel -v
```

It prints `corpus: 30/30 cases pass` and writes a temporary report with the scripted odds and the current
thresholds.

## What happens next

S08 is done. The live re-check of the second nudge, with a fresh key, is hers. The one threshold change is
already in `config/decisions/policy.yaml` with a note of its odds. S09 can start now: the socket-backed
`agent.Responder`, the wire encoding of the events, the `revises` ownership check, and the host and origin guard
around the whole turn. S09 does not depend on a threshold moving; it only depends on the pipeline this slice
leaves behind.

The live checks that stay hers: the real model on a wider real-title corpus, threshold labelling from its odds,
cost per review, and the potato.

## Risks

- **The 30-case CI corpus is invented, so it cannot prove judgment.** It proves plumbing and guards. The four-case
  live corpus proves the real path works, not that the model's choices are good; the model split one body and chose
  a verdict differently from the expectation, and both were valid. A green fake run must not be read as "the model
  is good".
- **The medium check is weak on real messages.** Dune-as-a-book can read as film/unknown, and The Bear read tv at
  0.67, below the 0.70 threshold. The `choose_medium` card is the fallback; whether the threshold or the wording
  should change is this gate's question.
- **The prompt is not a licence to ask forever.** The code caps a form at three single questions and a session at
  four rounds. "It may ask more than three questions" across rounds is the recorded answer; a prompt that tried to
  ask nine at once would be refused by `ask_reviewer`.
- **The fidelity check is a warning.** `drops_opinion` adds a notice and highlights nothing automatically; the card
  and the left-out text are the UI's job in S11. The check is skipped when the decisions layer is off, and it
  never blocks a card.
- **`format: print` stays empty.** The pre-read's `print` could be a hardback or a paperback, so the card asks
  rather than guessing. If she would rather store one, that is a decision for this gate.
- **FIXED (2026-11-04): the model ended a turn in prose where a tool call was due.** Two shapes, one nudge per
  turn (`stallNudge`, `internal/agent/state.go`). Neither nudge carries any of her words.
  1. She picked a work and no card existed: after `choose_candidate` the model answered in prose instead of
     calling `merge_candidates`, so the message was stored with no card. This hit 3 of 4 live runs before the
     fix and none of the 3 after it. The nudge says to merge the work she chose.
  2. This turn started the card but the model never tried `save_entry`, for example `live-dune-no-rating` asking
     "want to add a rating?" and leaving a card with no body. The nudge says to build the card from exact quotes,
     and that she fills in anything missing. A refused `save_entry` counts as a try and is never nudged, and a
     card left from an earlier turn is not a stall.
  Each shape has a happy, a bad and a sad test, and the failing ones were seen failing first. Costs are summed
  across both model runs, and the ledger records one row per run. **The second nudge is checked against the fake
  model only:** the key returned 401 before it could be run live. Re-run `make eval` with a fresh key to confirm.
- **The model sometimes stores the whole message as the body.** In 2 of those 4 runs `live-dune-film` stored "watched
  dune on netflix, 3.5/5, the sound was ..." as the body. It is her own words, so the rule holds, but the card
  carries the rating and framing text. The live runner still fails this on purpose; it is a prompt question.
- **Keys:** see "Do this first" at the top. A key must never be committed, logged or pasted into a fixture.
