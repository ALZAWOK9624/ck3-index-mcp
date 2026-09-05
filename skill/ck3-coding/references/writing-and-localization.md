# Writing and localization

This reference is deliberately short, and the shortest section is the most
important one.

---

## Event and flavour prose: not specified, on purpose

There is no template here for writing an event, and there should not be one.

A specification for prose produces prose that reads like a specification. Give a
model a formula — hook, escalation, two balanced options, closing beat — and it
will produce ten thousand events that are all structurally identical and
recognisably machine-made. That failure is worse than uneven writing, because it
is uniform: a reader stops noticing individual events and starts noticing the
formula, and once seen it cannot be unseen.

So the guidance is deliberately thin:

- Write it as someone in that world would say it.
- Vary. Length, shape, whose voice it is, whether anything happens at all.
- Two real options beat five ornamental ones, and one honest option beats two
  where one is obviously wrong.
- Mechanics go in the tooltip. Voice goes in the description. Never swap them.

Beyond that, write. If a future revision of this file adds a prose template,
that is a regression, not an improvement.

The constraint that *does* bind is the fiction, not the form: in-world text is
written by someone at some time, so it cannot know what happened after it was
written, cannot describe its own culture from outside, and cannot use vocabulary
its author lacks.

---

## Concepts: define anything reused

**If a term appears across several pieces of content, it needs a
`game_concept` entry.** Vanilla ships 1,047 of them, which is the measure of how
seriously Paradox takes this.

A concept gives the term one definition in one place, a hover the player can
reach from anywhere it appears, and a link syntax that keeps the prose short:

```
#! use the concept, do not re-explain the term inline
[concept|E]
```

The failure mode without it is a term explained slightly differently in four
tooltips, drifting apart over a year of edits, with the player never sure
whether two phrasings mean the same thing. A bespoke faction system, a custom
resource, a new office — each is a concept before it is content.

---

## Colour in text: garnish, not paint

Formatting codes (`#high`, `#P`, `#V`, `#warning`, `#color_yellow`, …) are for
**picking out** the words that carry information: a number, a name, a threshold,
a warning.

They stop working when overused. A line where half the words are coloured has no
emphasis at all, only noise, and the player's eye gives up and reads it flat.
The practical ceiling is a few coloured spans per paragraph — enough that the
coloured words are the ones you would want someone to remember if they only
skimmed.

Two mechanical notes:

- Every opened format must be closed with `#!`. A missing close does not fail
  loudly; it bleeds the colour through the rest of the string.
- Straight quotes inside a localized value are safe. They are a typographic
  choice, not a syntax error.

---

## Length

Text sits in a box someone sized. `text_multi` with a fixed height truncates
silently, and Chinese fits roughly twice as much as English in the same width,
so prose that fills a box in the language you are writing in will overflow in
the language you are not.

Write to the box, or size the box for the longest language you intend to ship.
