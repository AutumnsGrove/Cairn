# Product

## Register

product

## Users

One person, the operator, using Sepia from a phone (primary) or a laptop over their own Tailscale
network. No signup, no other users. The typical moment is the evening, in a dim room, right after
finishing a film, a book, an episode or a game: still full of opinion, one hand on the phone, wanting
to get the thought down before it fades. The other moment is calm and unhurried: flicking through the
library to remember what something was like, or to find what was watched in March.

## Product Purpose

Sepia is a private review journal for every medium. The user types or pastes what they think the way
they would say it out loud, and an agent does the data entry: identifies the work, pulls verified
metadata, extracts their own words, and asks a few short questions as tappable cards. They check one
entry card, fix anything by tapping it, and confirm. Success looks like a review that was easy enough
to actually get written, that is unmistakably in the user's own words, and that is still readable
and exportable in ten years. The software should disappear behind the review.

It runs on her own hardware — a Le Potato on her tailnet — at port **8893**, and nothing it holds leaves
that machine except an encrypted backup.

## Brand Personality

Quiet, warm, considered. Named for cuttlefish ink, and for the warm tone a print was given so that it
would last: a record that keeps, and that does not shout. The name is not a photo filter and nothing in
the interface may pretend it is one: no sepia tint, no grain, no nostalgia, no imitation of old paper.
A personal journal with the care of a good
reading surface, not a dashboard and not a chat toy. Covers and posters carry the colour; the
interface around them stays restrained and tactile, closer to paper and stone than to glass and neon.
Warmth is welcome because it is a private tool, but restraint always wins over decoration.

## Anti-references

- **Gamified tracking.** Streaks, badges, goals, counters that celebrate you. Nothing should pressure
  the user to log more or reward them for logging.
- **Goodreads clutter.** Dense rows of covers, social noise, ads, shelves of shelves, and metadata
  stacked in every direction.
- **The generic AI chat look.** Rounded message bubbles, purple-blue gradients, sparkle icons. The agent
  is a tool in the corner, not a character, and the entry is the product, not the transcript.
- **Star-burst rating UI.** Big gold stars, confetti, reaction animations. A rating should feel
  considered, not rewarded.
- **Neon and corporate SaaS polish in general.** Glow accents, hero-metric cards, badge-everything.
  There is no one else to impress.

References for the right feel: Letterboxd (a poster-forward diary, a quiet record of what you
watched and thought), Day One and Apple Journal (a private, unhurried journal), and Linear (crisp,
fast, legible density, no wasted chrome).

## Design Principles

- **The review is the centrepiece.** The user's own words get the best typography and the most space.
  Metadata, agent activity and controls recede around them. If a screen is about the agent, it has
  failed.
- **Trust is visible, never loud.** Verified, unverified and read-from-an-image are different states
  and each one is legible at a glance, with its source one tap away. Honest provenance is part of
  the design, not a footnote.
- **Calm over rewarding.** No celebration, no streaks, no motion for its own sake. Saving an entry is
  quiet and final. A rating is a considered value, not a prize.
- **Chat is the front door, not the whole house.** Typing is how things get in; reading is done on
  plain, scannable views. Cards and forms beat bubbles whenever the agent needs something specific.
- **Evening, one hand, phone first.** Every decision is checked first against a dark room and a thumb.
  Tap targets are generous, the main action is where the thumb already is, and nothing glares.
- **Covers carry the colour.** The interface is restrained and warm-neutral so posters and book
  covers, the only strong colour on screen, can do the visual work.

## Accessibility & Inclusion

No formal WCAG target stated; aim for WCAG AA contrast in both themes. Dark only: one low-glare
theme designed for a dim room (a light theme was considered and dropped on 2026-10-31).
Tap targets comfortable for one-handed use. Respect `prefers-reduced-motion`. State is never carried
by colour alone: verified, unverified and image-read use a marker shape or label as well as a hue, and
the quarter-star rating always shows its exact number.
