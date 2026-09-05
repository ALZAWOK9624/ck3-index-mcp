# CK3 decisions: presentable, reviewable, usable, stable

A decision is the widest surface a mod has: it sits in the player's list next to
Paradox's own, and it is judged against them. Four qualities decide whether it
belongs there. Three of them are measurable, so measure them.

## Measure against vanilla, do not guess

Vanilla is a corpus of 431 decisions, and it is indexed. Before arguing about
whether a field is needed, count how often Paradox uses it. Field frequency
across vanilla decisions:

| field | vanilla |
|---|---|
| `effect`, `picture` | 100% |
| `is_shown` | 99% |
| `ai_will_do` | 91% |
| `ai_potential`, `is_valid_showing_failures_only` | 86% |
| `desc` | 85% |
| `ai_check_interval_by_tier` | 82% |
| `decision_group_type` | 76% |
| `is_valid` | 72% |
| `selection_tooltip` | 65% |
| `cost` | 61% |
| `sort_order` | 42% |
| `cooldown` | 38% |
| `ai_check_interval` | 16% |
| `confirm_text` | 3% |
| `should_create_alert` | 1% |

Read that as a specification, not trivia. A field at 86% is close to mandatory;
a field at 1% is a privilege you have to earn.

**But a distribution is a prior, not a verdict.** It tells you what to expect and
where to look; it does not decide a case on its own, and applying it
mechanically produces confident nonsense.

The test is whether the reason behind the number applies here. Vanilla prefers
`ai_check_interval_by_tier` on 82% of decisions because a flat interval makes a
count-level AI pay an emperor's evaluation cost -- so a decision gated by
`has_title = title:x` to a single holder has no tier spread to scale across, and
a flat interval is correct for it. Flagging that as a deviation is a review
failure, not a finding.

Cite the reason, not the percentage. If you cannot say what the number is for,
you are not ready to apply it.

Reproduce it for any object type before reviewing one:

```sql
SELECT field, COUNT(DISTINCT object_name) FROM object_fields
WHERE object_type = 'decision' AND source_name = 'game'
GROUP BY 1 ORDER BY 2 DESC
```

## Usable

**`is_valid_showing_failures_only` is the norm, not `is_valid`.** Vanilla uses
it on 86% of decisions against 72% for plain `is_valid`. The difference is the
whole player experience: a greyed-out decision that lists what is missing is a
goal, and one that is simply absent is a mystery. Put a requirement in plain
`is_valid` only when revealing it would spoil something.

Wrap conditions the player cannot read in `custom_tooltip` so the failure says
a sentence rather than dumping a trigger:

```
is_valid_showing_failures_only = {
    custom_tooltip = {
        text = k10_republic_assembly_cooldown_requirement
        title:k_k10 = { NOT = { has_variable = k10_republic_assembly_cooldown } }
    }
    is_at_war = no
}
```

**`selection_tooltip` at 65% in vanilla** answers "what will this do" before the
player commits. A decision without one asks them to click to find out.

**`should_create_alert` appears on 6 of 431 vanilla decisions.** An alert
interrupts. Spend it on something that expires or that the player asked to be
reminded of, never on something merely available.

## Stable

**Cooldowns start when the thing ends, not when it starts.** If a decision opens
a multi-step chain, set the short in-progress guard at the decision and the real
cooldown in the effect that concludes the chain. Setting the long cooldown up
front means an interrupted chain locks the feature until it expires; setting
only the short guard means a broken chain loops.

```
# at the decision: a guard, not the cooldown
set_variable = { name = ..._assembly_in_progress value = yes days = 30 }
# at the concluding effect: the real one
remove_variable = ..._assembly_in_progress
set_variable = { name = ..._assembly_cooldown value = yes years = 2 }
```

Then `is_valid` must check **both**, or an interrupted chain leaves the player
stuck behind a guard nobody clears.

**`ai_check_interval_by_tier` (82%) over `ai_check_interval` (16%).** The AI
evaluates every decision on every tick it is due; a flat interval makes a
count-level AI pay an emperor's evaluation cost. If a decision is player-only,
`ai_potential = { always = no }` costs nothing and stops the evaluation entirely.

**Two entry points to one action need one implementation.** A panel button and a
standalone decision that both open an assembly must share a scripted effect. Two
copies drift, and the drift shows up as an action that behaves differently
depending on where the player clicked.

## Reviewable

Name every object in one namespace and keep the pieces adjacent:
`<prefix>_<feature>_<thing>` across decisions, effects, script values,
localization and modifiers. `ck3_impact` can then find every reference before a
rename, and a reviewer reads the feature without a search.

Dead scripted GUIs are the common rot: defined, never referenced, and
indistinguishable from live ones on sight. Check before shipping:

```
ck3_impact operation=references id=<scripted_gui id>
```

Keep the decision thin. It should gate and delegate; the work belongs in a
scripted effect that both the decision and any other entry point can call.

## Presentable

`picture` is at 100% in vanilla -- a decision without one reads as unfinished.
`desc` at 85% is where the flavour goes; `selection_tooltip` is where the
mechanics go. Do not swap them: prose that lists modifier values reads like a
patch note, and a tooltip written as prose makes the player hunt for the number.

Write `desc` as something inside the world. It is the only place a decision can
carry a voice, and it is bounded by what a person in that setting could know.

## Reviewing a decision set

Run the field distribution for the project against the same query with
`source_name = 'project'` and compare. A field vanilla uses on 86% of decisions
and the project uses on 12% is not a style difference, it is a gap. In a real
review this surfaced, in one pass: failure reasons missing from seven decisions
in eight, no tier-scaled AI intervals anywhere, and alerts used twelve times
more often than Paradox uses them.
