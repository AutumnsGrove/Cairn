# S01. Spikes

## Purpose

Answer the open questions with evidence, before anything depends on the answers. The output is written
findings, not code. Spike code is throwaway: it lives under `spikes/`, is never imported by production code,
and may be deleted after the findings are written.

## Depends on

S00 (for the repo and the CI that keeps keys out). **Runs in parallel with S02 and S03.** Feeds S05, S06, S08,
S15 and S18.

## Where we start

Every claim below is unverified: nova's embed shape (read from docs only), the decisions backend (documented in
Ivy and Polaris), Polaris's ask API (types read, path not confirmed), OpenRouter's model listing (metadata only),
TMDB's terms (unread).

## Settled

The questions to answer are fixed ([`../../PLAN.md`](../../PLAN.md) 3 and 9, Phase 0). The method is not: pick
the cheapest honest test for each.

## Scope

Each stage writes `docs/spikes/NN-name.md` with the question, the method, the result with numbers (latency,
cost), the decision, and what it changes in the plan.

1. **S01.1 Nova embed.** Read `nova/docs/INTEGRATION.md` and `examples/embed/`. Run one conversation with one
   tool in a throwaway program, using `replace ../nova`. Confirm the import rules, `tool.Context.Data`, and the
   event stream we will translate. **Decide embed versus extension** and write it down.
2. **S01.2 Decisions.** One real call through nova's `decide` package and `Roles` against the Jev backend, with
   review-shaped questions. Record the odds on ten sample messages including a split one ("Dune") and a message
   that tries to address the model. Record latency and cost. (**G1** before the call.)
3. **S01.3 Polaris.** Confirm the ask endpoint path, the request and response, `citations`, `source` tagging,
   and how a container on the potato reaches Polaris's host-published port. Try an unreachable Polaris.
4. **S01.4 Providers.** Get a TMDB key and an Open Library client working. Pull real works and editions: a
   series volume, a translated book, an audiobook edition, a miniseries, a remake. Write down which fields are
   reliable (first publication year, series, ISBNs, covers, external ids), the rate limits and the attribution
   requirements.
5. **S01.5 The pinned model.** Run the pinned chat model on five scripted flows to see tool-calling behaviour,
   spans quoting accuracy, and cost per turn. Confirm it honours aliases refusal.
6. **S01.6 Vision.** Try the pinned model on about five of the operator's real Fable screenshots (star icons,
   small type, a review across two images). Record what it gets wrong. (**G2**: the screenshots stay outside the
   repo, and only findings are written down.)
7. **S01.7 TMDB terms.** Read TMDB's image and attribution terms and state what caching is allowed. (Input to **G3**
   in S06.)

## Tests and exit

Spikes are exempt from test-first, but a spike that produces a rule produces a regression test for it: the
fixture it used (synthetic or public-domain metadata only, never a real review) is saved for S05, S06 and S08.

| Route | Cases the spikes must probe |
|---|---|
| Happy | One clean call per service |
| Bad | A malformed response, a title containing a prompt injection, an oversized body, an HTML error page |
| Sad | A timeout, a 5xx, a rate limit, an unreachable service, and a second call after each |

## Gates

**G1** before the first call that spends money: which service, the expected cost in cents, a request for the key.
**G2** before reading any real screenshot.

## Open questions

- Which five Fable screenshots (the operator picks; they stay outside the repo).
- Whether the operator wants the spike keys separate from the production keys (recommended).

## Done when

`PLAN.md` section 3 has no open questions, embed versus extension is decided in writing, and `docs/spikes/`
holds a finding for each stage. Findings that change the plan are applied to it in the same commit.
