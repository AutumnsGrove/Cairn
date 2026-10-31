# S11. Logging flow UI

## Purpose

The screens you use every time: the session with its transcript, every interaction, and the entry card in all
its states. This is the heart of the product, and the mockups are its spec.

## Depends on

S10 (components, client, socket machine) and S02 (golden examples). Runs against the dev stack, and against the
real server once S09 lands. **S12 and S13 build on it.**

## Where we start

Mockup pages 01, 02, 06 and 07 ([`../../mockups/`](../../mockups/)) and [`../ENTRY_CARD.md`](../ENTRY_CARD.md).

## Settled

- No chat bubbles: the transcript is plain blocks, activity rows are quiet lines, and the agent speaks in cards.
- Every interaction kind has a designed card: `choose_medium`, `choose_candidate`, `describe` (tabs of chips with
  counts, dashed hints, a "you mentioned this" note), `question_form`, `revisit_choice`, `fill_gap`,
  `lookup_failed` (Try again, Ask Polaris when available, Log without details).
- The entry card: blocks in the order Work, Rating, My words, Facts, Answers, Tags, Why; the markers for
  verified, unverified and image-read; tap-to-edit; the left-out section; the fidelity warning; the sticky
  confirm bar and the saved state.
- System states: offline strip, back-and-caught-up, working with Stop, one message queued, error with retry,
  the pre-read-down notice.

## Scope

1. **S11.1 Session screen.** Transcript blocks, activity rows with the live pulse, the status strips, the composer
   with Stop and the queued state, the attach button (disabled until S18).
2. **S11.2 Interactions.** Each kind above as a component driven by an `interaction_request` and answered with an
   `interaction_response`, with hints dashed and nothing pre-selected.
3. **S11.3 Entry card, draft.** All blocks and states; the rating control; edit in place with `draft_edit` and
   its version handling; the unverified marker and source panel (live in S15); Accept all; Left out; the
   fidelity warning; the "why" sheet; required-to-confirm shown beside the button.
4. **S11.4 Entry card, saved and edit.** The saved state, Edit opening a draft revision with the "what will
   change" diff, and Log again.
5. **S11.5 Resilience.** Resume mid-reply, a stale version, a rejected action with its reason, a second device.

## Tests and exit

| Route | Cases |
|---|---|
| Happy | The Dune flow through a real socket against the dev stack: split read, pick, Describe, draft card, confirm, saved; each mockup frame has a Playwright state with a baseline and an axe pass |
| Bad | A stale version shows the current card and says why; a message over the limit is refused in place; a hint is never pre-selected; Save is disabled without a rating and without words, with the reason shown; hostile text in a title renders as plain text |
| Sad | Offline mid-flow and back, a model error with Try again, a lookup failure with its three ways on, a queued message, the pre-read down notice, a killed backend restarted mid-session |

**Fidelity to the mockups** is asserted per frame on a phone viewport. **Visual checks are looked at by a human**
(or by the agent rendering and reading the image), not trusted to a green diff alone.

## Gates

None.

## Open questions

- Copy for each error code, drafted from the mockups and confirmed by the operator in review.

## Done when

The operator can log a review end to end on a phone against the dev stack and it matches the mockups state for
state; every system state in pages 06 and 07 (except import) has a test.
