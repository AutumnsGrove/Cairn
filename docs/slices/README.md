# Slices

Cairn is built as **vertical slices**, one plan file each, so any agent can pick one up cold, build it test
first, and leave the app working and releasable. Standing rules, invariants, gates and the definition of done
are in [`../BUILD.md`](../BUILD.md). **Read it first**, then [`../STANDARDS.md`](../STANDARDS.md). Nothing is
built yet: every status below starts at **planned**.

## How to read a slice plan

Every plan has the same sections: **Purpose**, **Depends on** (and what it can run in parallel with),
**Where we start**, **Settled** (only decisions already recorded, with where), **Scope** (numbered stages,
each small enough for one sitting), **Tests and exit** (the happy, bad and sad routes, named), **Gates**,
**Open questions** and **Done when**. A plan states only what is settled. Anything unsettled is an open
question to be asked in a batch before the slice starts and recorded in the plan's Decisions section; a slice
with an unanswered question is not ready. Items marked *assumed, confirm* are defaults taken from Ivy or
Polaris that the operator has not yet signed off.

## What a slice leaves behind

- Tests (happy, bad, sad) and any fuzz targets with seed corpora.
- The `world` fake for anything new it talks to.
- A passing `make check` and green required CI checks.
- Updated specs, a line in [`../BUILD-LOG.md`](../BUILD-LOG.md), and this table's status.

## Order and dependencies

```
S00 walking skeleton ─┬─► S01 spikes (operator keys, gates G1 G2) ──────────────► feeds S05 S06 S08 S15 S18
                      ├─► S02 contract ───────────────┬─► S09 server ◄─┐
                      │   (api/*.yaml, golden examples)└─► S10 frontend foundation ─► S11 logging UI ─► S12 reading UI
                      └─► S03 domain core ─┬─► S04 data layer ─► S07 session engine ─┐
                                           ├─► S05 banks and decisions ─────────────┼─► S08 agent pipeline ─► S09
                                           └─► S06 providers ───────────────────────┘
S09 + S12 ─► S13 deploy and update ─► S14 backups ─► [Phase 1 gate G6, live on the potato]
Phase 2:  S15 web fallback ─┐
          S16 search and chat ├─► S17 more media (repeatable)
Phase 3:  S18 screenshot import
Phase 4:  S19 export, import, verify
Later:    S20 parked
```

**Foundation first.** S00 then S02 and S03 unblock everything. After S02 (the contract) and S03 (pure types)
the work fans out: **S04, S05, S06 can run in parallel**, and the whole frontend stream (S10, S11, S12) can run
against the contract and the world without waiting for the agent. S07 needs S04. S08 needs S05, S06 and S07.
S09 needs S02, S07 and S08. S13 needs a working app (S09 and S12).

**Parallel streams.** Two agents can work at once from S02 on:

| Stream | Slices | Talks to the other through |
|---|---|---|
| Backend | S03, S04, S05, S06, S07, S08, S09 | `api/` contract and golden examples |
| Frontend | S10, S11, S12 | `api/` contract, `cairn-dev` and the world |

## Status

| Slice | Plan | Phase | Status |
|---|---|---|---|
| S00 | [Walking skeleton](S00-walking-skeleton.md) | 0 | done |
| S01 | [Spikes](S01-spikes.md) | 0 | planned |
| S02 | [Contract first](S02-contract.md) | 1 | done |
| S03 | [Domain core](S03-domain-core.md) | 1 | done |
| S04 | [Data layer](S04-data-layer.md) | 1 | done |
| S05 | [Banks and decisions](S05-banks-and-decisions.md) | 1 | done |
| S06 | [Providers](S06-providers.md) | 1 | done |
| S07 | [Session engine](S07-session-engine.md) | 1 | done |
| S08 | [Agent pipeline](S08-agent-pipeline.md) | 1 | G4 decided, one open finding |
| S09 | [Server and protocol](S09-server.md) | 1 | planned |
| S10 | [Frontend foundation](S10-frontend-foundation.md) | 1 | planned |
| S11 | [Logging flow UI](S11-logging-ui.md) | 1 | planned |
| S12 | [Reading UI](S12-reading-ui.md) | 1 | planned |
| S13 | [Deploy and update](S13-deploy-and-update.md) | 1 | planned |
| S14 | [Backups](S14-backups.md) | 1 | planned |
| S15 | [Web fallback](S15-web-fallback.md) | 2 | planned |
| S16 | [Library search and chat](S16-search-and-chat.md) | 2 | planned |
| S17 | [More media](S17-more-media.md) | 2 | planned (template) |
| S18 | [Screenshot import](S18-screenshot-import.md) | 3 | planned |
| S19 | [Export, import, verify](S19-export-import-verify.md) | 4 | planned |
| S20 | [Parked](S20-parked.md) | 5 and 6 | parked |

## Phase gates

| Phase | Ends when | Gate |
|---|---|---|
| 0 | The spikes are written up and the open questions are closed | G8 |
| 1 | A review typed as a sentence is a saved entry on the potato, the app has updated itself and rolled back a failed update, and a nightly backup is in R2 | G5, G6, G8 |
| 2 | Four media coexist in one library, search works, the general chat works | G8 |
| 3 | Ten real Fable reviews have been imported and read back correctly | G7, G8 |
| 4 | An export, a wipe and an import diff clean, and an R2 restore has really happened | G8 |

## What is real and what is mocked today

S00 and S02 through S08 are built: the service skeleton and its Host and Origin checks, the contract and its
generators, the pure domain core, the database with its triggers and repositories, the question banks and
decisions layer that reads [`../../config/`](../../config/), the TMDB and Open Library clients with their covers,
the session engine, and the agent pipeline with its tools, guards, corpus and fake-model runner. G4 is decided and S08 has one open
model-behaviour finding (see its handoff). Everything from S09 on is still plan, spec and
mockup. The decisions backend, the two metadata providers, Polaris and R2 are reachable only through the fakes in
`internal/world`; `cairn-dev` runs the real service against them, offline, with no keys. The live calls against
the real providers are the operator's.
