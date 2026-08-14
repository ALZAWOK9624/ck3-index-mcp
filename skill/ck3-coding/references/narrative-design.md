# Narrative in CK3: situations, not plots

CK3 is a sandbox where the player writes their own story out of systems. Any
authored plot competes with that, and loses. Four failure modes follow, and all
four are common:

- **The rail.** A chain that plays out the same regardless of what the player
  does. They are reading someone else's story.
- **The quest log.** Decision A unlocks B unlocks C. A to-do list in a surcoat.
- **Event soup.** Flavour events that never accumulate. Individually fine,
  collectively forgotten.
- **The severed thread.** A story opens, the player goes to war, and it is never
  heard from again.

The best narrative system Paradox has shipped is **struggle**, and it is not a
plot: it is a *situation* with phases that nobody fully controls. Legends are
the same — state that spreads and outlives its subject.

**Design the situation. Do not design the sequence.**

---

## 1. State first, events second

The story lives in variables, flags and modifiers that persist. Events are
**windows onto that state**, not the state itself.

**The test: delete an event. Does the story still exist?** If deleting it
deletes the story, that was a cutscene.

A shipped four-faction balance passes: the story is the standing of four
factions, which persists and is on screen; events sample it and comment.

## 2. Decisions are the player's verb, events are the world's

- The player must act to advance → **decision**
- The world acts on the player → **event**

**Never use an event to force the player's next step.** That is the rail. An
event may create pressure, deliver news, or offer a choice the world imposes; it
must not be the mechanism by which the plot advances on schedule.

## 3. Pacing comes from thresholds, not timers

A timer-driven chain runs at the same speed whatever the player does. A
threshold-driven one moves when the player pushes state across a line, and feels
responsive because it is.

**A pulse checks state. It does not advance plot.**

## 4. Every thread needs three exits

Win, lose, and **abandon**. A story that can only be completed is a chore, and
the player who ignores it deserves an outcome rather than a frozen thread.
Abandonment should cost something; silence is not a consequence.

## 5. Branch on state, not on choice history

Do not store "picked option B in event 3" and read it ten events later. The
combinations explode and the player cannot see the causation.

Let the option **change state** — a trait, an opinion modifier, a variable — and
let later content read that state. The branch becomes legible to the player,
composes with everything else in the game, and survives content you have not
written yet.

## 6. Anchor state to titles, not characters

The player will die mid-thread, be succeeded, or be conquered. State on a
character dies with them; state on `title:` survives succession, which is why a
governance system keeps its variables on the realm's title rather than its
holder.

Anchor to a character only when the story is explicitly about one life.

Plan the interruptions explicitly: succession, conquest by an outsider,
extinction of the line, and the player switching characters. Each needs a
defined outcome, and "nothing happens and the thread stays open forever" is not
one.

## 7. Spine and texture

**Decisions carry the spine; events carry the texture.** The spine is the three
to six moments where the player commits. Everything else is the world reacting.

If a thread needs fifteen decisions, it is a quest log.

## 8. The shape to aim for

A loop, paced by the player, not a line:

```
situation exists and is visible
  -> world applies pressure        (events on thresholds)
  -> player commits                (a decision, taken when they judge the moment)
  -> the situation changes shape   (state change, new phase)
  -> repeat, with the situation now different
```

## 9. CK3 has no ending

A completed arc leaves a hole, because the game continues. Two honest exits:

- **Convert the resolution into permanent state** — a new government, faith,
  title or law. The player now plays in a changed world.
- **Make the situation cyclical** — the struggle re-enters a phase.

Do not design an arc that ends in nothing.

---

# on_action: the hot path

Recurring `on_action` hooks are the most reliable way to make a mod stutter,
because their cost is multiplied by the population. Treat every line inside one
as performance-critical. This applies to all scripting, not only narrative.

## Choose the narrowest pulse

`yearly_pulse` fires for **every character** — tens of thousands in a normal
game. `yearly_playable_pulse` fires for playable rulers, a small fraction of
that. Picking the wrong one costs an order of magnitude for no benefit.

Ask what the smallest population is that could possibly matter, and hook the
pulse that matches it.

## The trigger block is the hot path

It runs for every character on every fire, including all the ones that will fail
it. Order it cheapest-first, so the common case exits immediately.

Cheap: `has_title = title:x`, `has_character_flag`, `has_variable`, `is_ai`,
`is_landed`, a government or trait check.

Expensive: any `any_` iterator, `any_vassal`, `any_courtier`, region and
distance queries, anything walking a list.

**Never open a trigger with an `any_` iterator.** Put a scalar check in front of
it that eliminates almost everyone first.

A correct hook:

```
# Append to the existing pulse. Do not redefine it.
yearly_playable_pulse = {
    on_actions = { k10_nameless_republic_annual_governance_on_action }
}

k10_nameless_republic_annual_governance_on_action = {
    trigger = {
        has_title = title:k_k10        # one scalar check, eliminates everyone else
    }
    effect = {
        k10_nameless_republic_annual_governance_tick_effect = yes
    }
}
```

The pulse decides **whether**. A scripted effect does the **what**. Keep the
work out of the hook itself so it is never paid by characters who fail the gate.

## Append, never overwrite

Redefining a vanilla pulse replaces its list of on_actions and silently disables
everything Paradox and every other mod attached to it. Declare the vanilla pulse
with only your own named on_action inside; the game merges the lists.

## Iterators inside a pulse

`every_vassal` inside a yearly pulse that already runs per ruler is quadratic.
If a pulse must iterate:

- prefer `random_` when one is enough,
- bound the list with a `limit` that a cheap check can satisfy,
- and consider whether the work belongs on the *other* side — hooking a pulse
  that already fires for the population you wanted is cheaper than iterating to
  find it.

## One-time work belongs on a one-time hook

Setup goes on `on_game_start_after_lobby`, not on a pulse guarded by a flag. A
guarded pulse pays its trigger forever to do nothing.

## Cache traversals

A scope path walked three times in one effect is three traversals. Resolve it
once into a variable and read the variable, especially inside anything that
repeats.
