---
name: Sepia
description: A quiet, warm, hand-placed journal for everything you finish.
---

<!-- SEED: re-run /impeccable document once there is code, to capture the actual tokens and components. -->

# Design System: Sepia

## 1. Overview

**Creative North Star: "Ink in a Dim Room"**

Sepia is cuttlefish ink, and the warm tone a print was given so it would keep. The interface follows
that: dark and warm-neutral, modest, tactile, unshowy, built to last, and never a filter or a tint. It
is designed first for the moment right after finishing something, in a dim room, one thumb on a phone, and then for the
calm of flicking back through the library. Restraint always beats decoration.

The system is warm-neutral and flat at rest, with a single low-chroma lichen-green accent used for
primary actions, selection and state. Book covers and film posters are the only strong colour on
screen. The review body, in the user's own words, is the largest and most generous text on every
screen it appears on; the agent, the metadata and the controls recede around it. Motion is
responsive: it gives feedback and conveys state changes, and nothing else.

It explicitly rejects gamified tracking (streaks, badges, goals), Goodreads-style clutter, the
generic AI chat look (rounded bubbles, purple-blue gradients, sparkle icons), star-burst rating
UI, and neon or corporate SaaS polish.

**Key Characteristics:**
- Dark only: one low-glare, warm-tinted theme for a dim room. There is no light theme.
- Restrained colour: warm neutrals plus one lichen accent on at most about a tenth of any screen.
- Lexend for the interface and the reading text, with a separate header face for titles.
- Flat by default; depth comes from tonal layering, and state changes are the only reason to move.
- Everything reachable with one thumb; the main action sits where the thumb already is.

## 2. Colors

Restrained strategy: tinted warm neutrals and one muted moss-green accent. Specific values are to be
resolved during implementation.

### Primary
- **Lichen** (moss green, to be resolved): Primary actions, the current selection, filled rating stars, the
  confirm bar, and the verified-and-accepted state. Moderate chroma, enough to read clearly as green
  without glowing in a dark room.

### Neutral
- **Warm stone and paper tones** (to be resolved): every neutral is tinted toward the same warm hue.
  A darker layer for the page, a slightly lifted layer for panels and the entry card. No pure black
  and no pure white anywhere.

### Named Rules
**The Covers Carry the Colour Rule.** The interface chrome stays restrained so posters and covers are
the strongest colour on any screen. The accent never competes with a cover, and it stays within about a tenth to a seventh of any screen.

**The Not By Colour Alone Rule.** Verified, unverified and image-read are different states. Each one
uses a marker shape or label as well as a hue, never a hue alone.

## 3. Typography

**Interface and body font:** Lexend, self-hosted (SIL Open Font License), with a system sans fallback.
**Header font:** Fraunces, self-hosted (SIL Open Font License), a soft and warm serif. A separate face for work titles and screen headings only.

**Character:** Lexend is open, even and highly legible, designed to reduce reading effort, which suits
long reviews read in low light. The header face adds a quiet editorial voice and keeps titles from
looking like interface labels.

### Hierarchy
- **Review body:** the largest text on a screen, generous line height, a line length of 65 to 75
  characters. The user's own words.
- **Titles:** the header face, used for work titles and section headings only.
- **Labels and metadata:** Lexend at small sizes, quiet in weight and tone.
- A tighter scale ratio (about 1.125 to 1.2) between steps, as in a product UI, with one exception:
  the review body is deliberately larger than the surrounding controls.

### Named Rules
**The Her Words Rule.** The review body is the largest, most readable text wherever it appears. If a
control or an agent message is more prominent than the user's own words, the layout is wrong.

## 4. Elevation

Flat at rest. Depth is conveyed by tonal layering (a slightly lifted panel layer over the page), not
by shadows. Shadows appear only as a response to state, such as a dragged rating or an open drawer,
and stay soft and low.

### Named Rules
**The Flat-By-Default Rule.** Surfaces are flat at rest. If something has a shadow while idle, remove
it.

## 5. Components

Not yet defined. This section is filled in on the first scan after the mockups exist.

## 6. Do's and Don'ts

### Do:
- **Do** let covers and posters be the strongest colour on screen.
- **Do** make the review body the largest, most generous text, set at 65 to 75 characters per line.
- **Do** pair every state colour with a marker shape or label.
- **Do** keep tap targets generous and the primary action within thumb reach.
- **Do** keep motion to feedback and state changes, 150 to 250 ms, eased out.
- **Do** use Lucide for every icon, at one stroke weight, bundled with the app. Never from a CDN.
- **Do** self-host every font; nothing loads from outside.

### Don't:
- **Don't** add streaks, badges, goals or counters that celebrate the user (gamified tracking).
- **Don't** pack the screen like Goodreads: dense cover rows, social noise, shelves of shelves.
- **Don't** use rounded message bubbles, purple-blue gradients or sparkle icons (the generic AI chat look).
- **Don't** use big gold stars, confetti or reaction animations on ratings (star-burst rating UI).
- **Don't** use neon glows, hero-metric cards or badge-everything polish (neon and corporate SaaS).
- **Don't** use pure black or pure white; tint every neutral toward the warm hue.
- **Don't** use a coloured side stripe on cards or list items, or gradient text.
- **Don't** use modals as the first answer; prefer inline and progressive patterns.
