# Designing a CK3 mechanic

This is a position, not a survey. Where it says "refuse", refuse.

Everything below reduces to one test: **can the player be wrong?** A system the
player cannot lose to, cannot misjudge, and cannot choose differently in is not
a mechanic. It is upkeep with a UI.

---

## 1. Before building: is there a question?

A mechanic exists to pose a question the player answers with imperfect
information and lives with. Write the question in one sentence before writing
any script. If it cannot be written, there is nothing to build.

- "What kind of realm is this becoming?" — a question
- "Do I want more income?" — not a question

**The dominance test.** If one option is correct in more than roughly four
games out of five, the branch is decoration. Either the weak option needs a
condition under which it wins, or it should be deleted. Two real options beat
five ornamental ones.

**The regret test.** A good choice leaves the player able to picture the other
branch and wonder. If nothing is given up, nothing was chosen.

---

## 2. Shape: prefer zero-sum to additive

This is the highest-leverage decision in the whole design and most mods get it
wrong by default.

An **additive** system asks how much the player accumulates. Its end state is
everything at maximum, its failure mode is inflation, and its long game is
boredom because the answer is always "more".

A **zero-sum** system asks what the player becomes. Its end state is a
*shape*, not a total.

A worked example from a shipped mod. Four factions, each with **influence**
(the four always sum to 100) and **satisfaction** (0-100, independent):

```
effect_scale = influence × satisfaction / 10000     # 0.00 - 1.00
```

Because influence is normalised, four factions at full satisfaction yield
`Σ(influence/100) = 1.00` — **exactly one full effect package**, mixed
according to who holds influence. The player can never accumulate more total
benefit. They can only decide whose benefit it is: a republic of clerks, of
kings, of priests, or of soldiers.

That single constraint does more design work than any amount of content. It
converts "grind all four up" into "decide what this country is".

**Test your own system:** if every value were maxed, would the player have a
*different* game, or just a stronger one? If the latter, find the constraint
that makes the resources trade against each other.

---

## 3. Symmetry: punishment matches reward

In the same system, discontent triggers below 25 satisfaction:

```
discontent_scale = (25 − satisfaction) × influence / 2500
```

Worst case — everyone at zero satisfaction — totals `1.00`, exactly the
magnitude of the best case. That symmetry is deliberate and it is the thing to
copy.

- Punishment weaker than reward → the system is decoration; ignoring it is
  correct play, and correct play should never be "ignore the mechanic".
- Punishment stronger than reward → players avoid the mechanic entirely, and
  you have built a tax.

**Corollary — every penalty needs two exits.** In the faction system a player
facing an angry faction can appease it (raise satisfaction) or marginalise it
(let its influence be diluted, since discontent scales with influence). One
mechanic, two legitimate answers. A penalty with a single escape is a chore; a
penalty with none is a punishment for playing.

---

## 4. Legibility: if it cannot be shown, it does not exist

A player reasons only about state they can see. This is a design constraint,
not a UI task, and it belongs in the design phase.

**Design the panel and the formula together.** If the state needs more than one
screen to display, the system has too many terms. Four factions × two values is
at the edge of what a single panel reads cleanly.

**Show the inputs, not just the output.** A modifier the player feels but
cannot attribute produces superstition rather than strategy.

**Numbers must sit inside vanilla's distribution.** `+10%` monthly income reads
as meaningful because vanilla's modifiers live around there; `+200%` is not
strong, it is uncalibrated, and it destroys the player's ability to judge every
other number in the game. This is measurable — count vanilla's values for the
modifier before choosing yours, exactly as with decision fields. Landing
outside the 5th-95th percentile of vanilla usage requires a reason you can say
out loud.

**Round numbers are a feature.** 25, 50, 100 are thresholds a player can hold
in their head. 37 is noise.

---

## 5. Fiction: the setting constrains the content

In-world text is written by someone, somewhere, at some time. A culture
description is a document that exists inside the world and therefore cannot
know what happened after it was written, cannot describe its own culture from
outside, and cannot use vocabulary its author lacks.

Breaking this is the fastest way to make a serious mod read like a wiki. A
faction described as "the republican faction, providing +10% income" is a
tooltip wearing a costume; "以岑世昌、临时议堂、乡社与文吏为核心，力图维持共和国体"
is a political position someone in that world holds.

**Mechanics go in the tooltip, voice goes in the description.** Never swap
them. Prose that lists modifier values reads like a patch note; a tooltip
written as prose makes the player hunt for the number.

---

## 6. Cost of ownership

Every mechanic is maintained by someone, and mods die of maintenance, not of
bad ideas.

Before building, answer:

- **What breaks when this grows?** A four-faction panel with hardcoded columns
  is rewritten the day a fifth faction exists. Would a data-driven list have
  cost more than the rewrite will?
- **What breaks on a mid-save update?** State in variables survives; state
  implied by content ordering does not.
- **How many places know this rule?** A formula duplicated between a script
  value, a tooltip and a description will drift, and the drift is invisible
  until a player reports that the number lies. One source, referenced.
- **What does this cost the AI?** Every decision is evaluated on a schedule for
  every character. If it is player-only, say so — `ai_potential = { always = no }`
  removes the cost entirely.

---

## 7. Refuse

A strict designer says no to these regardless of how well they are implemented:

1. **A mechanic that only adds numbers.** If removing it changes nothing but
   magnitudes, remove it.
2. **A decision that is always correct to take.** That is not a decision, it is
   a delayed effect with a click.
3. **An event with one real option.** Two options where one is obviously worse
   is one option and an insult.
4. **A modifier the player cannot perceive.** Below roughly 5% of a stat's
   normal swing, it is invisible and only costs performance.
5. **A system with no resolution.** What happens when the player wins it? A
   faction system that can never be settled is a treadmill with a story.
6. **Content that exists to be complete.** Four factions because there are four
   flavours is worse than three that mean something.

---

## 8. Reviewing a design

In order. Stop at the first failure and fix it before continuing — later
questions are meaningless if an earlier one fails.

1. **State the question** the mechanic poses, in one sentence. Failure to state
   it is failure.
2. **Name two strategies** that both win, and the conditions separating them.
   One strategy means one option.
3. **Check the shape.** Everything maxed — different game, or bigger numbers?
4. **Check symmetry.** Best case and worst case, same magnitude?
5. **Check legibility.** Can the whole state be shown at once? Do the numbers
   sit in vanilla's distribution?
6. **Check the fiction.** Could the in-world author of this text have written
   it?
7. **Check ownership.** What breaks when it grows, and how many places know
   each rule?

Questions 3, 5 and 7 are arithmetic and index queries. Do not accept an opinion
where a count is available.
