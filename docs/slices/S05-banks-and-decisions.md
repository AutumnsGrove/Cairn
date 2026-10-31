# S05. Banks and decisions

## Purpose

Two data-driven layers that make the agent calm and predictable: the **question banks** (what the Describe it
step and the single questions offer) and the **decisions pre-read** (the fast, typed read of each message).
Both are YAML, validated strictly, hot-reloadable, and pure where possible.

## Depends on

S03. **Runs in parallel with S04 and S06.** Uses the S01 findings (the real odds shape). S08 needs it.

## Where we start

Drafts exist: [`../question-banks/`](../question-banks/) (`shared.yaml`, `books.yaml`, `film-tv.yaml`) and
[`../decisions.draft.yaml`](../decisions.draft.yaml) (14 checks, wording and policy, the backend block).

## Settled

- Banks: closed chip vocabularies by category, `include` for shared ones, stable slug values, at most three
  single questions by weight, topics link to the `mentions_*` flags, hints are dashed and never pre-selected,
  at most one `short_text` question ([`../question-banks/books.yaml`](../question-banks/books.yaml)).
- Decisions: nova's `decide` package and `Roles`; one batched call per message; a quiet default on every check;
  thresholds per check and per option; `only_when_intent`; suppression; the medium ambiguity rule (runner-up
  at least 0.25, leader under 0.30 ahead); session bias with an override only above 0.80; text only, with the
  state sent as separate sources; a 2.5 s deadline after which the turn proceeds with a notice; Jev is named
  only in config.
- `yes` and `no` are quoted map keys.

## Scope

1. **S05.1 Bank loader.** Strict YAML (unknown keys fail), `include` resolution, tone groups, the slug rule and
   the `{label, value}` rename form, duplicate-value detection per category, the `applies_when` vocabulary,
   a content-warnings flag. Loading a bad bank fails loudly at start or reload, never at use.
2. **S05.2 Selection (pure).** From a bank, a medium, the pre-read's covered topics and hint candidates, the
   format and a genre hint: the categories to offer (with `covered` and validated `hints`), and up to three
   single questions. Hints are checked against the vocabulary; an unknown hint is dropped.
3. **S05.3 Decisions registry.** Load `decisions.yaml` (wording, policy, backend roles); validate at most 25
   questions, a quiet option per check, thresholds in (0, 1), no alias model ids.
4. **S05.4 Pre-read.** Build the nova `decide.Request` with separate `{source, text}` entries (message, pending
   question, work title), clip deliberately under the context limit and record the clip, call through `Roles`
   with the timeout and fallback, and return the full odds.
5. **S05.5 Policy engine (pure).** Apply thresholds and option thresholds, `only_when_intent`, suppression,
   the ambiguity gap rule, the session-bias override; output which checks fired.
6. **S05.6 Hot reload.** Watch the files, swap atomically, and on a bad file keep the old config and surface a
   notice. Bind-mounted host edits take effect without a restart.
7. **S05.7 The fidelity check.** The `extract_fidelity` call with the original and the extract as two sources;
   its result only ever produces a warning, and `adds_content` is logged as a bug.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | Dune's split read triggers the "book or film?" card; covered topics skip their single questions; a hint chip is dashed and unselected |
| Bad | A bank with a duplicate chip value, an unknown key, or an unquoted `yes` fails to load; a model-suggested hint outside the vocabulary is dropped; a 50,000-character message is clipped and the clip recorded; a message that tries to instruct the model changes odds only, and routing still obeys the thresholds |
| Sad | The decisions call times out, errors, returns malformed JSON or contradicts the deterministic parse: the turn proceeds with a notice and no pre-read; a hot reload with a broken file keeps the previous config and says so |

**Fuzz:** both YAML loaders. **Properties:** selection never returns more than three single questions, never
returns a covered topic's question, and every hint is a member of the vocabulary. **Fake:** the decisions
backend in the world, scripted per check, with every fault.

## Gates

None here. **Threshold tuning is G4 in S08**, on real odds and the fixture corpus.

## Open questions

- One decisions file or two (wording and policy), as Polaris does. *Recommended: split at build time.*
- The shipped starting thresholds, taken from the draft and replaced after G4.

## Done when

The shipped banks and the decisions config load, a bad file is rejected with a clear message, and the pre-read
can run against the fake and produce every outcome in the mockups.
