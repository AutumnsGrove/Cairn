# S20. Parked

Ideas that are deliberately **not** planned yet. Each has a reason it waits and the prerequisite that would
wake it up. None is built, designed in detail, or mocked. Do not start one without the operator moving it into
a real slice.

| Idea | Why it waits | Wakes up when |
|---|---|---|
| **Photos on a review** (a ticket, a bookshelf) | Not needed for v1; the `media` table already exists, so adding them is not a migration | S18 has shipped and the operator asks |
| **Season-level TV reviews** | Series-level is enough; the schema already carries `season` | A show where a season matters, and the operator asks |
| **Voice input** | Typing first; browser dictation may already work in the text box | The operator finds typing the bottleneck |
| **Spoiler handling** | Not in v1; no field, no decision check | It starts to matter when sharing is possible |
| **Static public site** (Phase 6) | Private by default; entries carry a `status` field so this needs no migration | The operator decides to publish something |
| **Per-category star ratings** | Decided against: one overall rating only | The operator changes her mind |
| **A spend view** | Spend is recorded per call, not yet shown | The numbers are wanted |
| **A service worker for offline reading** | Caching interacts badly with the update flow | A real need appears, and the interaction with `hello` version checks is designed |
| **A games vocabulary and bank** | Needs a game provider first | S17 picks games |

When one wakes up it gets a numbered slice with the full plan structure, and the status table in
[`README.md`](README.md) is updated in the same commit.
