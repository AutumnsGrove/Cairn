# S05. Banks and decisions

## Purpose

Two data-driven layers that make the agent calm and predictable: the **question banks** (what the Describe it
step and the single questions offer) and the **decisions pre-read** (the fast, typed read of each message).
Both are YAML, validated strictly, hot-reloadable, and pure where possible.

## Depends on

S03. **Runs in parallel with S04 and S06.** Uses the S01 findings (the real odds shape). S08 needs it.

## Where we start

The shipped config is in place and is what the code loads: [`../../config/questions/`](../../config/questions/)
(`shared.yaml`, `books.yaml`, `film-tv.yaml`) and [`../../config/decisions/`](../../config/decisions/)
(`wording.yaml`, `policy.yaml`) — 14 checks, their wording, the policy block and the backend roles. It moved
out of `docs/` at the start of this slice so that the files the service reads are the only copy.

## Settled

- Banks: closed chip vocabularies by category, `include` for shared ones, stable slug values, at most three
  single questions by weight, topics link to the `mentions_*` flags, hints are dashed and never pre-selected,
  at most one `short_text` question ([`../../config/questions/books.yaml`](../../config/questions/books.yaml)).
- Decisions: nova's `decide` package and `Roles`; one batched call per message; a quiet default on every check;
  thresholds per check and per option; `only_when_intent`; suppression; the medium ambiguity rule (runner-up
  at least 0.25, leader under 0.30 ahead); session bias with an override only above 0.80; text only, with the
  state sent as separate sources; a 2.5 s deadline after which the turn proceeds with a notice; Jev is named
  only in config.
- `yes` and `no` are quoted map keys.

## Decisions (recorded 2026-11-01, before the slice started)

1. **The dependency rule widens for nova's `decide` contract.** `internal/decisions` is the adapter over
   `decide.Request` and `decide.Roles`, the same way `internal/providers` owns a provider endpoint. `BUILD.md`
   4, `internal/archtest` and `STACK.md` say so, and `internal/agent` keeps the monopoly on nova's `agent`,
   `mode`, `session`, `config`, `tui` and `bundled` packages. `internal/world` may also name `decide`,
   because it is the fake for that contract. (The alternative, an agent-owned adapter, would have duplicated
   the request shape in Sepia for no gain.)
2. **Nova is pinned to its tag** in `go.mod`, not a `replace ../nova`: `v0.2.0` when this decision was
   recorded, `v0.3.4` since 2026-11-07 (`docs/BUILD-LOG.md`). The modules are pure Go and nothing resembles a
   local path in CI or in the image build. Moving the pin is a `go.mod` change plus a build log entry, never a
   local checkout.
3. **The decisions config is split in two files**: `config/decisions/wording.yaml` (the checks, their prompts
   and options) and `config/decisions/policy.yaml` (thresholds, routing, suppression, the backend roles).
   Tuning never edits wording.
4. **The draft thresholds ship unchanged** and are marked as starting values in the file and in the build log.
   G4 in S08 replaces them from recorded odds; until then a wrong threshold costs a question, never a word.
5. **Shipped config lives under `config/`**, not `docs/`. The drafts moved there at the start of the slice and
   every doc links to the new paths, so there is one copy and hot reload can bind-mount it.
6. **Two dependencies:** `gopkg.in/yaml.v3` for strict YAML (unknown keys and duplicate keys both fail) and a
   poll-based watcher with an injected ticker. No `fsnotify`: inotify on the Docker bind mounts this app uses
   is unreliable, and a poll is trivially testable and cheap at a 2 s interval.
7. **`applies_when.format` uses the domain's format names** (cinema, streaming, broadcast, audiobook,
   hardback, paperback, ebook, physical, live), and a list means "any of these". The decisions `format` check
   keeps its own option set and stays a suggestion; only the deterministic parse, her tap or a stored entry
   field is trusted.
8. **The quiet option is explicit**: each check names `quiet: <option>` in `wording.yaml`. A YAML map has no
   order, so "the first option is the quiet one" was a trap; the loader validates that the named option exists
   and that it is not also given an option threshold.
9. **Suppression ships as a mechanism with no data.** `suppress_when_intent` exists on every check and is
   tested; every shipped list is empty and G4 fills them in from real odds. `only_when_intent` is applied
   after the single batched call, because `intent` is decided inside that same call.

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

None. The two original questions (one decisions file or two; which thresholds ship) and seven more that the
code forced (the nova import rule, the pin, where the config lives, the two dependencies, the format
vocabulary, the quiet option, suppression) are answered in Decisions above.

## Done when

The shipped banks and the decisions config load, a bad file is rejected with a clear message, and the pre-read
can run against the fake and produce every outcome in the mockups.
