# Handoffs

One file per gate or stop, so a reviewer who has seen none of the session can resume. Named
`<date>-<gate>-<slug>.md`, for example `2026-11-04-G1-decisions-live-call.md`. Committed, never edited after the
operator has answered; the answer goes in a new section at the bottom.

A handoff has these sections:

- **Where this is.** The slice and stage, and what is true now (tests, CI, what is merged).
- **What I did.** Short and concrete.
- **What I need.** The exact decision, key, file or approval, and the options with a recommendation.
- **What happens next.** What resumes once it is answered.
- **Risks.** What could go wrong, and how it was bounded.

How to stop (from `docs/BUILD.md` section 6): write the handoff, commit it, set the slice's status line to
"waiting at <gate>", and send one message beginning `STOP: this needs Autumn:`. Do no other work until she answers.
