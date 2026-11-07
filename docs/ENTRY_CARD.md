# The entry card

The one component that shows an entry: as an editable draft in a session, and read-only in the library.
Specified before any code because the WebSocket protocol, the question banks and the data model all hang
off it. Decisions are from the 2026-10-31 planning session; the rules they serve are in
[PLAN.md §11](../PLAN.md#11-rules-that-are-not-negotiable).

## 1. Blocks, top to bottom

| Block | Contents | States |
|---|---|---|
| Work | Cover, title, year, creators, medium | `verified` (an API), `unverified` (web or manual), `accepted` (a web field I tapped) |
| Rating | Quarter-star rating, with the favourite heart in the label row | `set`, or `empty` (shown as needed) |
| My words | Verdict (one line) and body | `extracted`, `typed`, `mixed`, `imported`; see section 3 |
| Facts | Format, date finished | `parsed`, `suggested` (from the decisions layer), `empty` |
| Left out | The parts of my messages not kept | collapsed; each segment has a tap to include it |
| Answers | The descriptor chips I picked, grouped by category, then the single questions I answered | `answered`, `skipped` (greyed) |
| Tags | Genres from the provider and my own free-text tags (not the descriptor chips, which are answers) | `suggested`, `accepted` |
| Source | The screenshot(s) an import came from, beside the transcription | present only for imports; see section 10 |
| Why | How the message was read | collapsed; see section 6 |

The draft card and the saved entry are the same component. A saved entry is the card with editing
turned off and an Edit button on.

## 2. Required to confirm

Only two things are required, and both are enforced in `save_entry` as well as in the button state:

- a **rating** (a quarter-star value, 0.25 to 5.0), and
- a **non-empty body** that is made of my own words.

Everything else is optional. If I skip every question, the entry still saves. If I only said "5/5" and
nothing else, the agent asks for a few words or I type them; it does not write any.

## 3. The body and the verdict

My words are mine (rule 1), so the body is extracted, never written. The agent only chooses which of my
words to keep.

1. **The agent returns quoted spans.** For the body, a list of quotes; for the verdict, at most one
   short quote. Each quote is taken from my messages in this session. The agent never returns a
   rewritten paragraph.
2. **Code locates each span** in the concatenation of my messages. The match is exact first, then the
   closest passage by a normalised comparison (case, whitespace and punctuation ignored for matching
   only). A match is replaced with **the original's exact wording**, so a slip in case or punctuation
   becomes what I actually wrote. A span with no close-enough passage is dropped and its text appears
   under Left out.
3. **The body is the original text at those positions,** in the original order, with overlaps merged.
   The verdict goes through the same containment check, or stays empty.
4. **Left out is everything else** in my messages. The usual content is the framing ("hey so i finished
   watching..."), and it is left out by default. Each segment can be included with one tap.
5. **The fidelity gate.** A decisions `extract_fidelity` check is asked with my original text and the extract as
   two separate `{source, text}` entries. Options: `faithful` (the quiet default), `drops_opinion`,
   `adds_content`. It never blocks:
   - `drops_opinion` over its threshold: the card shows a warning and highlights the left-out text that
     probably holds the missing opinion.
   - `adds_content` can only be a bug, since step 2 makes additions impossible. It is logged loudly.
6. **Typing it myself is always allowed.** Editing the body on the card replaces the extracted text with
   what I typed. Text I type counts as my own input for the `save_entry` guard. The entry records which
   case it is: `body_origin` is `extracted`, `typed`, `mixed` or `imported`.

What is stored with the entry: the body text, the verdict, `body_origin`, and `body_spans_json` (where
each kept span sits in which session message), so containment can be re-checked later.

## 4. Rating control

- **Drag across the five stars.** The value snaps to quarters (0.25 steps) as the finger moves.
- **A minus and a plus button** nudge by 0.25 for precision.
- **The exact number always shows** beside the stars, so the stored value is never hidden.
- A parsed value that is not on a quarter snaps to the nearest quarter, and the card shows what was
  stored. A vague phrase ("a four and a bit") leaves the control empty and the agent asks.
- The favourite flag is a separate toggle (a heart), never implied by the rating.

## 5. Unverified fields

A field that came from the web or was entered manually carries a small dashed marker.

- **Tapping the marker** shows the source URL and the quoted snippet, and offers Accept or Edit for that
  field.
- **One Accept all button** accepts every unverified field at once, for the common case where the web
  facts look right.
- Accepting writes the field into the canonical work and marks its snapshot `accepted`. Nothing
  unverified becomes canonical without that tap (rule 4).
- A manual work created during an outage carries the marker on its whole Work block until a "fill in
  details" retry finds it.

## 6. The "why" sheet

Collapsed by default, in the style of Polaris's Oracle info sheet. It shows:

- every pre-read check's winning option and its odds, and which checks fired (cleared their threshold);
- which tools ran, in order, with their inputs;
- the sources behind any web-derived field (URL, snippet, Polaris thread id);
- the cost of the pre-read and of the turn, each as what the call consumed and how much of it a plan's
  allowance covered. An allowance is not money spent, and the sheet says so rather than showing one number
  that could mean either.

It is how thresholds get tuned from real use and how "why does this entry say that" gets answered.

## 7. Editing

- **In place.** Tapping a field makes it editable; changes apply to the draft immediately and never
  save by themselves.
- **By chat.** Typing a correction ("make it 4.5") in the session edits the same draft.
- **One confirm bar** is pinned to the bottom of the card. Its button is disabled until section 2 is
  satisfied.
- **Editing a saved entry** opens a new draft revision of it with the same card. Confirming stores the
  previous version in the append-only revisions table first.

## 8. After confirm

The session closes. In the chat the card becomes the saved entry with a link and an Edit button, and the
entry appears in the library. Further changes start a new draft revision, which keeps sessions short and
the draft-or-saved state unambiguous.

## 9. Fixture cases to write

For the Phase 0 corpus, so this behaviour has tests before it has code:

- a message that is all framing plus a rating ("finished it, 4/5") -> empty body, the agent asks;
- a normal review with framing around it -> the framing under Left out, the review as the body;
- an agent span with a one-word slip -> snapped to my original wording;
- an agent span that is invented -> dropped, shown as nothing and logged;
- a message with two separate opinions where the agent keeps one -> `drops_opinion` warning;
- a body I edited by hand -> `body_origin = typed`, still saves;
- a vague rating -> empty control, not a guessed value.

## 10. Imports

A review brought in from screenshots (the Fable migration) uses the same card with three differences.

- **The "original" is a transcription.** The model reads the image into text, and the body spans are
  taken from that transcription. Because the transcription step is where a word could slip, **the
  screenshot is shown beside the card** so I can compare against the real source.
- **Fields read from the image carry a marker** (the rating and date as shown, the title, the creator)
  and need my tap, like unverified web fields. The `save_entry` guard accepts a value read from an
  attached image as my own input only once I have accepted it.
- **The entry records where it came from:** `body_origin = imported` and `imported_from` (for example
  `fable`). The screenshots stay with the entry as its source and are mirrored to R2.
- Bank questions are not asked for an import unless something required is missing; a review written
  months ago is not re-interviewed. A missing date or an ambiguous title is a `fill_gap` or
  `choose_candidate` interaction as usual.

Fixture cases: a rating drawn as star icons; a review split across two images; a screenshot that is not a
review at all; a transcription that differs from the image by one word (caught by the side-by-side, not
by code).
