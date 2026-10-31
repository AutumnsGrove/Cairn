# S08. Agent pipeline

## Purpose

Wire the model in, safely. Embed nova, expose Cairn's tools, and orchestrate the review flow from the first
message to a draft card, with every guardrail enforced in code. Deliver the **fixture corpus** and the runner
that judge the pipeline, first against a fake model in CI and then against the real one as the operator's eval.

## Depends on

S05 (pre-read), S06 (providers), S07 (session engine), and the S01 findings. **S09 depends on it.**

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

## Gates

**G4**: the corpus, the odds table from a real run, the proposed thresholds, and the labelling needed from the
operator, before any threshold is treated as tuned. Real evals are the operator's, run on demand.

## Open questions

- The system prompt's voice (proposed: terse, plain, no cheerfulness, no emojis).
- The number of corpus messages per medium.

## Done when

The pipeline passes its three routes against the fake, the corpus runner produces a report, the safety
assertions are green, and (as the operator's live check) the real model completes the flow on the corpus within
the cost caps.
