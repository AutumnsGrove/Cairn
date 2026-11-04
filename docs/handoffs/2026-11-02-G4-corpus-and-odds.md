# G4: the S08 corpus, the odds table and the thresholds

**Gate.** G4 in [`../BUILD.md`](../BUILD.md) 6: "S08: before tuning any threshold. The fixture corpus, the odds
table from a real run, the proposed thresholds, and the labelling you need from the operator."

**Status: waiting for Autumn.** S08.1 to S08.8 are built, the fake-model suite is green, and a four-case real-title
live smoke run reached valid cards against the real model and providers. The threshold decision and the corpus
labelling are still hers.

## Where this is

S08 is complete in the repository and stops at this gate. `main` is green: `make check` and `golangci-lint` pass,
including the race suite, the fuzz smoke, the web checks and the guard. The slice's status line says "waiting at
G4". Nothing in the repository, a fixture, a test or a log is a real review, title, person, screenshot or key. The
corpus titles (`The Lantern Keeper`, `The Quarry Line`) and people (`Ada Voss`, `Ilse Marrow`) are invented.

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
- `cmd/cairn-eval`: the operator's runner against the real model and decisions backend; `make eval` runs the live
  corpus.
- Migration 0005 (`turn_calls`), the `CAIRN_OPENROUTER_API_KEY` and `CAIRN_MODEL` settings (pinned, no aliases),
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

## What I need

1. **Run the live eval.** From the repo root, with her key in the environment (the command never prints, logs or
   commits it):

   ```bash
   CAIRN_OPENROUTER_API_KEY=... CAIRN_TMDB_API_KEY=... make eval > /tmp/cairn-eval.json
   ```

   It runs the four real-title cases against the real model and the real decisions backend and writes a JSON
   report: every case's odds, what the model actually did, and where the card differed from the expected card. The
   chat model costs a few cents for the corpus at the pinned prices; the decisions calls are fractions of a cent.
   The command exits 1 if any case failed, and 2 if the key is missing. A missing TMDB key is normal: the film/TV
   cases then report the provider as off. The invented 30-case corpus is the CI check, not this run.
2. **Label the corpus.** For each case, is the expected card what she would want? The ones that most need her eye:
   - the vague and missing ratings (`book-vague-rating`, `book-no-rating`, `film-vague`, `film-no-rating`,
     `tv-no-rating`): should the agent ask, and is the fill value stored as she expects?
   - the framing-only cases (`book-framing-only`, `film-framing-only`): is asking her for the few words, then
     storing them as a typed body, the right fallback?
   - the split medium (`cross-split-medium`): is book the right answer for "the lantern keeper"?
   - the hostile cases (`book-hostile-message`, `film-hostile-message`, `cross-injection`,
     `cross-title-injection`): is the honest quote the body she would expect, with the instruction ignored?
   - the revisit cases (`book-already-reviewed`, `cross-revisit-new-entry`): edit the saved entry or make a new one?
   - the outage and empty cases (`cross-provider-down`, `cross-empty`, `cross-only-rating`): is "log without
     details" or "no card" the right outcome at this stage, before Polaris (S15)?
   - the format cases (`book-audiobook`, `book-ebook`, `film-cinema`, `film-physical`): is the format she would
     have picked?
3. **Approve or change the thresholds.** These are the starting values from
   [`../../config/decisions/policy.yaml`](../../config/decisions/policy.yaml), not tuned. G4 replaces them from real
   odds on the corpus.

   | Check | Threshold | Notes |
   |---|---|---|
   | `intent` | 0.65 | `edit_entry` 0.80, `answer_pending` 0.75, `import_review` 0.75 |
   | `medium` | 0.70 | Ambiguity: runner-up at least 0.25 and leader gap under 0.30 asks "which one?" |
   | `format` | 0.75 | Only when the intent is `log_review` |
   | `revisit` | 0.80 | Backed by the exact work match in code, not the probability alone |
   | `extract_fidelity` | 0.60 | `drops_opinion` 0.60; `adds_content` is logged as a bug, never shown as a warning |
   | `mentions_*` | 0.75 | One default for the nine topic checks |
   | Session override | 0.80 | Inside an entry session the session's own reading stands unless the pre-read is this sure |

4. **Confirm the two recorded answers** from 2026-11-02: the prompt is terse and plain with no emojis and may ask
   more than three questions across rounds; the corpus is 10 book, 10 film/TV, 10 cross-cutting.

To reproduce the fake run, which is the CI check and not hers:

```bash
go test ./internal/agent -run TestCorpusAgainstTheFakeModel -v
```

It prints `corpus: 30/30 cases pass` and writes a temporary report with the scripted odds and the current
thresholds.

## What happens next

Once she has answered, S08's status moves to done, the thresholds (if changed) are edited in
`config/decisions/policy.yaml` with a note of the odds they came from, and S09 starts: the socket-backed
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
- **Keys are single-use for a live check.** The temporary keys appeared in the chat transcript and must be rotated;
  a key must never be committed, logged or pasted into a fixture.
