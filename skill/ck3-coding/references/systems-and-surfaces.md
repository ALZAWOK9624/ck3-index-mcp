# Choosing the system: which surface carries which content

CK3 is not one game. It is roughly a dozen systems accumulated over years of
expansions, each with its own UI, its own AI, and its own player expectations.
Vanilla exposes **124 scriptable object types**.

The single most consequential decision in a mod is which of them your content
belongs to — and the most common failure is not making that decision at all.
Events and decisions are what everyone knows, so everything becomes an event or
a decision, and the mod feels flat no matter how good the writing is.

**Core principle: pick the system whose shape already matches your content, and
you inherit its interface, its AI behaviour, its tooltips and the player's
existing habits for free. Build custom only when nothing fits.** A custom panel
is a cost you pay forever; an activity intent is a cost Paradox already paid.

---

## Part 1 — Event windows

Vanilla has 9,791 events. The window is not decoration; it tells the player what
kind of thing is happening before they read a word.

| `type` | vanilla | what it means to the player |
|---|---|---|
| `character_event` | 6,556 (67%) | Someone is in front of me, now |
| `activity_event` | 1,445 (15%) | This is happening because I went somewhere |
| *(none — hidden)* | ~1,006 (10%) | Nothing. The player never sees it |
| `letter_event` | 598 (6%) | Someone far away is telling me something |
| `court_event` | 186 (2%) | This is happening in public, in my hall |

**`character_event` — the default, and the reason to justify the others.** One
or two people, present, addressing the player. If you cannot say who is standing
there, it is probably the wrong window.

**`letter_event` — distance is the content.** Use it when the sender's absence
matters: news from a war you are not at, a demand from someone who did not come
in person, a report. Using it for someone in the room throws away the one thing
it says. Using `character_event` for distant news forces an implausible meeting.

**`court_event` — the audience is the content.** The value is that other people
are watching. Reach for it when the outcome is a public commitment: a petition,
a judgment, a scene whose weight comes from being witnessed. At 2% of vanilla it
is deliberately rare; frequent throne-room scenes cheapen the room.

**`activity_event` — structural, not stylistic.** It participates in activity
context. Do not pick it for the look.

**Vary the window within one chain; the contrast is the point.** A chain is not
obliged to pick one window and keep it. An assembly convened in the hall is a
`court_event` because the realm is watching; the envoy who comes to you
privately afterwards is a `character_event` because nobody else is in the room.
Rendering both the same way flattens a distinction the player would otherwise
feel without being told. Ask the question per event, not per feature.

**About one vanilla event in ten has no window at all.** Hidden events are
machinery: bookkeeping, delayed chains, cleanup. A modder who makes every event
visible is generating noise. **If the player has no decision and learns nothing
they need, hide it.** A notification is not an event; use `message` or
`send_interface_toast`.

**`theme` sets icon, background and sound.** Vanilla's most-used are `realm`
(446), `education` (291), `travel` (255), `faith` (238), `administrative` (219).
Pick the theme matching the *subject*, not the mood you want. A wrong theme is
worse than a plain one: the player reads the icon before the title and you have
lied to them.

---

## Part 2 — Choosing a mechanism

Each expansion added a system with a shape. Match your content to the shape.

| Reach for | When the content is | Scriptable surface |
|---|---|---|
| **Activity** | People gather somewhere for a purpose, over time, with guests and phases | `activity`(21), `activity_intent`(59), `activity_pulse_action`(307), `travel_option`(29) |
| **Scheme** | One character works on another, secretly or not, over weeks, with a chance of exposure | `scheme_type`(76), `scheme_agent_type`(65), `scheme_countermeasure`(24), `hook_type`(44) |
| **Struggle** | A region is locked in a multi-party conflict with phases nobody fully controls | `struggle_catalyst`(127) |
| **Legend** | A deed becomes a story that spreads and outlives its subject | `legend_seed`(38), `legend_chronicle`(33) |
| **Artifact / Court** | Objects with history, and a hall that displays status | `artifact_type`(65), `artifact_template`(71), `artifact_feature`(632), `court_position`(81) |
| **Culture** | Practices that spread, blend and diverge between peoples | `culture_tradition`(198), `culture_pillar`(162) |
| **Faith** | Beliefs with rules, sins, virtues and reformability | `religion`(606), `faith`(140), doctrines |
| **Government / contract** | The terms between a ruler and their subjects | `government`(18), `subject_contract`(64), `task_contract`(159), `law`(188) |
| **Domicile** | A seat that grows and is carried or lost | `domicile_building`(1620) |
| **Character interaction** | One character asks something of another, right now | `character_interaction`(470) |
| **Decision** | The ruler acts on their realm, alone, at a moment of their choosing | `decision`(431) |
| **Trait / lifestyle** | A durable fact about who someone is | `trait`(301), `lifestyle_perk`(162) |

**How to choose.** Ask three questions in order:

1. **Who acts?** One character on another → interaction or scheme. A ruler on
   their realm → decision. A region on everyone → struggle.
2. **Over what span?** An instant → interaction or decision. Weeks with
   uncertainty → scheme. A journey with phases → activity. Generations →
   culture, faith, legend, dynasty.
3. **Who else sees it?** Private → scheme, secret. Public → court event,
   legend, struggle.

**The commonest mistake is building a decision plus a custom panel for something
that is an activity, a scheme, or a contract.** Those systems come with
scheduling, AI participation, guest lists, exposure mechanics and interfaces
that already work. Reproducing a tenth of that by hand is a large cost and a
worse result.

**The second commonest is a "system" that is really a trait.** If it is a
durable fact about a person with a few effects, it is a trait. Do not build a
panel for it.

---

## Part 3 — When to build a GUI at all

A custom window is the most expensive surface in a mod and the least maintained.
Earn it.

**Do not build one when:**

- The state fits in a tooltip. A modifier with a good description is read more
  often than a panel that must be opened.
- The player only ever reads it. Reading belongs on an existing surface — the
  character sheet, the realm tab, a modifier tooltip.
- It shows one number. That is a modifier.
- The content already has a home. Artifacts have an inventory; court positions
  have a court screen. Adding a parallel window splits the player's attention
  and doubles what you maintain.

**Build one when:**

- Several values must be **compared** at once, and comparison is the point. The
  faction panel earns its existence because the player's decision depends on
  four factions' relative standing, which no tooltip can show.
- The player acts from it, and the action depends on the displayed state.
- The state changes over time and the player must track a trend.

**If you build one, design the panel and the formula together.** If the state
needs more than one screen, the system has too many terms. Four entities × two
values is at the edge of what a single panel reads cleanly.

**Prefer extending a vanilla window** over adding a new one, when the content
belongs to something the player already opens.

---

## Part 4 — Exemplars

**Struggle** is the best-designed system Paradox has shipped for modders to
imitate. It models a situation no single character controls, with phases that
change what everyone can do, and it survives its participants. If your content
is "this region is a mess for a century", it is a struggle, not fifty events.

**Activity intents** are the cleanest expression of "the player states a goal,
then the world responds". The intent is chosen up front and colours everything
after. Copy this shape whenever a player commits to an approach before knowing
the outcome.

**Court positions** demonstrate content that is simultaneously a job, a
relationship and a status display, without a custom window — because it reuses
the court screen. Look here before building a panel for "roles people hold".

**Legends** show a deed becoming a story that outlives its subject and spreads
independently. Reach for this when the point is that people *tell* of something,
not that it happened.

**Traits** carry more design weight than their size suggests: durable, visible,
inherited by reputation, and already integrated with AI personality. Many
"systems" a mod wants are three traits and an opinion modifier.

---

## Core philosophy

1. **The system you choose is a design decision, not a technical one.** It
   determines the pace, the audience, and what the player thinks they are doing.
2. **Inherit before you build.** Every vanilla system carries UI, AI and habit.
   Custom carries only what you wrote and only for as long as you maintain it.
3. **The window is a claim about what is happening.** A letter says the sender
   is elsewhere. A court event says people are watching. Choose the one that is
   true.
4. **Silence is a design choice.** One vanilla event in ten is invisible. If the
   player gains no decision and no needed knowledge, hide it.
5. **A custom panel is a debt.** Take it on only when comparison across several
   values is the mechanic itself.
