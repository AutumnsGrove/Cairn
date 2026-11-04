# S08. Agent pipeline

## Purpose

Wire the model in, safely. Embed nova, expose Cairn's tools, and orchestrate the review flow from the first
message to a draft card, with every guardrail enforced in code. Deliver the **fixture corpus** and the runner
that judge the pipeline, first against a fake model in CI and then against the real one as the operator's eval.

## Depends on

S05 (pre-read), S06 (providers), S07 (session engine), and the S01 findings. **S09 depends on it.**

## Status

**Built 2026-11-02; live-checked 2026-11-04; G4 decided 2026-11-04; done.** The fake-model suite and the invented corpus are
green. A real-title live smoke run reached valid book, film and TV cards against the real model, the real Jev
pre-read, OpenLibrary and TMDB. `medium` moved from 0.70 to 0.65 and the rest stayed. A model that ends a turn in prose where a tool call was due now gets one nudge
(`stallNudge`). The second nudge has not been re-checked live; the handoff says how. The handoff is
[`../handoffs/2026-11-02-G4-corpus-and-odds.md`](../handoffs/2026-11-02-G4-corpus-and-odds.md).

## Where we start

The flow is specified in [`../../PLAN.md`](../../PLAN.md) section 5. The model is
`deepseek/deepseek-v4.1-flash-1017`. Nova's harness layers are the only imports allowed.

## Settled

- **Nova is embedded** (`agent.New(cfg).Run(...)`); only `agent`, `tool`, `provider/*`, `message`, `proto` and
  `decide` are imported, and only from `internal/agent`. App state rides on `tool.Context.Data`.
- Tools: `media_lookup`, `merge_candidates`, `ask_reviewer`, `save_entry`, `search_library` (read only here);
  `lookup_web` and `ask_polaris_freeform` arrive in S15, `read_source_image` in S18.
- `ask_reviewer` emits the Describe step and up to three single questions through the session engine and waits
  for the response as its tool result.
- **Guardrails in code:** `save_entry` refuses without a rating and a body made of the user's own words; the
  body and verdict pass the span containment check; ratings must be a quarter-star value; web and image facts
  need an accept; `ask_reviewer` is capped; every tool call, pre-read and cost is logged.
- Caps: 12 tool steps, 60k tokens in and 8k out, a 120 s turn deadline, four question rounds.
- The pre-read is a hint framed as an automatic note, never an instruction.

## Scope

1. **S08.1 Embedding.** Provider config (OpenRouter, the pinned id, no aliases), `tool.Context.Data`, an
   architecture test that nothing else imports nova, and the translation from nova's events to session events.
2. **S08.2 Tools.** Each tool with strict argument decoding (fuzzed), bounded results, and the guard that
   applies to it. `ask_reviewer` blocks on the user's response without holding resources.
3. **S08.3 Orchestration.** Pre-read, deterministic parse, the medium-ambiguity card, lookup, the candidate card,
   merge, Describe, the draft build (spans, verdict), the fidelity check, the draft card; the revisit and
   `lookup_failed` branches.
4. **S08.4 Prompts.** The system prompt, the hint framing, and tool descriptions, kept as versioned files, with
   a test that they never tell the model it may write the user's words.
5. **S08.5 Budgets.** The caps above, the cost ledger rows (`decision_calls` and a turn ledger), and the
   behaviour at each cap (a notice and a kept draft).
6. **S08.6 The fixture corpus.** About 30 invented sample messages with the expected parse, the expected
   decision outcome and the expected tool sequence, including hostile and awkward ones, plus a runner.
7. **S08.7 Injection and abuse tests.** A title, a web snippet or a message that addresses the model; a model
   that tries to save an opinion, a rating or a span it was not given; a tool-argument payload that is huge or
   malformed.
8. **S08.8 Degraded paths.** Pre-read down, a provider down, the model erroring or timing out mid-turn, each
   producing the right card or notice.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | The whole flow for a book and a film against the fake model reaches a draft card with the right spans, answers and metadata |
| Bad | A fake model that invents a rating, rewrites the user's words, calls `save_entry` early, loops on tools, emits a malformed tool call, or obeys an instruction in a title: each is refused or bounded and the user's text is unchanged |
| Sad | The model times out or errors mid-turn, a provider is down (sticky), the decisions layer is down, the process is killed: the message is kept as a draft and the right card or notice appears |

**Safety assertions** from [`../TESTING.md`](../TESTING.md) section 5 live here: my words, no save without a
confirm, review text only where named, classifier output never decides. **Cost:** a test asserts the worst case
stays inside the caps.

## Built (2026-11-02)

- **The embedding.** `internal/agent` builds nova's OpenRouter provider from `CAIRN_OPENROUTER_API_KEY` and
  `CAIRN_MODEL` (pinned, no aliases), runs `agent.New(cfg).Run`, and translates nova's events into the session
  events the protocol names. The arch test already proves no other layer imports nova.
- **The tools.** `media_lookup`, `merge_candidates`, `ask_reviewer`, `save_entry` and `search_library`, each with a
  strict decoder (`DisallowUnknownFields`, no trailing value, an 8 KiB cap, fuzzed), a bounded result and the
  guard that applies to it. `ask_reviewer` opens the interaction through the session engine and blocks on
  `agent.Responder` without holding a transaction; S09 supplies the socket-backed implementation.
- **The pipeline.** The pre-read, the deterministic parse (rating, date, format), the medium-ambiguity card, the
  lookup, the candidate card, the merge, the Describe step, the extracted body and verdict, the fidelity check
  (when the decisions layer is up), the draft card, and the revisit and `lookup_failed` branches. `save_entry`
  builds the card; the confirm tap still commits.
- **The prompts** are versioned files under `internal/agent/prompts/`, with a test that no file invites the model
  to write, rewrite or invent her words, and that the hint is framed as an automatic note.
- **The budgets.** 12 tool steps, 60k input tokens by a conservative estimate, 8k output, a 120 s deadline, four
  question rounds and one Describe step. Every turn writes a `turn_calls` row and every pre-read a `decision_calls`
  row; the running total is read from both, never a counter.
- **The corpus.** Thirty invented messages (ten book, ten film/TV, ten cross-cutting) in
  `internal/agent/testdata/corpus.json`, with the expected parse, pre-read outcome, tool order and card. The CI
  runner is `TestCorpusAgainstTheFakeModel`. A separate four-case real-title corpus
  (`internal/agent/testdata/corpus-live.json`) is the operator's live smoke run: the invented titles cannot be
  found by the real providers, so the live run needs real works. `make eval` (`cmd/cairn-eval`) runs the live
  corpus; the invented one stays the fake/CI suite.
- **The hostile paths.** A title or message that addresses the model, an invented body or verdict, a rating passed
  as an argument, a huge title, malformed tool JSON, a provider outage, a decisions outage, a model error, a model
  timeout, the step cap and the token cap all have tests.

**Settled while building.** The interaction request event now carries `interaction_id`, `kind` and `payload`, not
the payload alone, because the client cannot draw a question it cannot name. `store.workFromRow` now keeps the
work's id; without it the merge and the revisit check named an empty work. `UpdateDraft` now writes `work_id`, which
is how a manual work is replaced by the real one. `session.StartDraft`, `SetWork`, `SetExtractedBody`,
`UserMessages` and `SourceText` are the agent's small additions to the S07 engine.

**Not this slice.** `lookup_web`, `ask_polaris_freeform` (S15) and `read_source_image` (S18) are not offered. The
fidelity check runs only when the decisions layer is configured; it never blocks.

**Live check (2026-11-04).** The pinned chat model and the Jev pre-read both answer. A four-case real-title smoke
run built valid cards for Dune (book and film) and The Bear (TV), and exercised the `fill_gap` rating branch. The
two strict differences were the model splitting a body into two exact spans and choosing a verdict; both are her
own words and the cards are valid. The medium check is the weak one: "finished dune" read film 0.44 / unknown
0.42 / book 0.13, "watched dune on netflix" film 0.97, "the bear on tv" tv 0.67 (below the 0.70 threshold), and
the no-rating Dune unknown 0.47 / book 0.37. The `choose_medium` card is what keeps the flow moving. The
deterministic format cue corrected the pre-read's "streaming" for "on tv" to `broadcast`, which is the code-decides
rule working. Three bugs were found and fixed: a provider error could carry `api_key` in a URL, an unknown medium
handed the model an error instead of `choose_medium`, and the candidate result omitted the id the model needs to
merge.

## Gates

**G4**: the corpus, the odds table from a real run, the proposed thresholds, and the labelling needed from the
operator, before any threshold is treated as tuned. Real evals are the operator's, run on demand.

## Open questions

None. The two below were answered by Autumn on 2026-11-02 and are recorded as decisions.

## Decisions (recorded 2026-11-02)

1. **The prompt's voice is terse and plain**: short sentences, no praise, no persona, no emojis. It may ask more
   than three questions across the session, because the code already bounds it: at most three single questions
   per `ask_reviewer` form and four question rounds per session. The prompt says neither number; the code is the
   cap.
2. **The corpus is 30 messages**: 10 book, 10 film/TV, 10 cross-cutting (split medium, vague rating, no rating,
   framing only, already reviewed, provider down, hostile title, injection in the message, oversized input,
   empty input). A case records the expected parse, the expected pre-read outcome and the expected tool order.
3. **`save_entry` builds the draft card; it never commits.** The tool validates the rating and the body and writes
   the draft through the session engine, then says the card is waiting for her confirm. Only the confirm tap
   commits (docs/BUILD.md invariant 2). The tool keeps its plan name so the model's vocabulary is unchanged.
4. **Provider config is explicit.** `CAIRN_OPENROUTER_API_KEY` enables the model; `CAIRN_MODEL` defaults to the
   pinned `deepseek/deepseek-v4.1-flash-1017`. A `-latest` or `~` alias is refused at startup, because a model
   that moves silently makes every recorded cost and eval meaningless.
5. **The user is an interface, not a socket.** `agent.Responder` opens an interaction through the session engine
   and blocks on its answer. S09 supplies the WebSocket-backed implementation; S08 proves the blocking and the
   result with a scripted responder.
6. **The turn ledger is a new table (`turn_calls`).** `decision_calls` already exists; every model turn writes one
   `turn_calls` row with tokens, cost and outcome. Both are cost records only: ids, counts and money, never text.
7. **Side-effecting tools run one at a time.** Every tool except `search_library` sets `Parallel: false`, so a
   lookup and a merge cannot race the card. `search_library` is a pure read and may run in a batch.

## Done when

The pipeline passes its three routes against the fake, the corpus runner produces a report, the safety
assertions are green, and (as the operator's live check) the real model completes the flow on the corpus within
the cost caps.

This sitting builds all of S08.1 to S08.8 and stops at **G4**: the corpus, the odds table produced by the fake
run, the proposed thresholds and the labelling asked of Autumn are in a committed handoff. No threshold is
treated as tuned until she answers.
