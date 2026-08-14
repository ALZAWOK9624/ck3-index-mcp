# CK3 GUI composition

`gui-visual-design.md` keeps a panel from being wrong. This one is about making
it good, which is a different job.

Some of it is arithmetic and some of it is taste. Each section says which. Where
it is arithmetic, count — do not argue.

---

## The five counts

Before judging a panel by eye, count these. Every one is mechanical, and four of
the five are usually wrong on a first draft.

| Count | Budget | Why |
|---|---|---|
| Distinct spacing values | **3** | More reads as misalignment, not variety |
| Horizontal alignment baselines | **1** per column | Two baselines is the loudest amateur tell |
| Distinct font sizes | **3** | A fourth level is a heading that should be body |
| Distinct colours | **body + muted + one accent family** | Beyond that nothing is emphasised |
| Contrast of body text | **≥ 4.5:1** against its *composited* background | Below this it is decoration, not text |

A panel that passes all five and has mediocre taste still looks professional. A
panel that fails two of them cannot be rescued by good taste.

---

## Spacing (arithmetic)

**Use one scale, and let each step mean something.**

```
4   inside a group   (label to its own value, icon to its own text)
8   between groups   (one stat row to the next)
16  between sections (the portrait block to the stats block)
24  section to frame (content to the panel edge)
```

The exact numbers matter less than that there are **few** of them and each
carries a meaning. A panel using 3, 5 and 6 is using three values for one job:
the eye cannot tell whether 5 and 6 are a distinction or a mistake, so it reads
both as a mistake.

**The one law of grouping: related things must be closer to each other than to
unrelated things.** If a label sits 6px from its value and 5px from the next
row, the reader's eye groups them wrongly and the panel feels muddy for reasons
they cannot name. This is the highest-value thing on this page. Fix it first.

**Slack collects where you put it.** A `vbox` packs from the top, so leftover
height lands at the bottom as an accidental gap. Decide where the slack should
be and put an `expand` there deliberately, or distribute it into the spacing.

---

## Alignment (arithmetic)

**One baseline per axis per column. Not two. Not "almost one".**

The most common way to break this in jomini is mixing sizing strategies inside
one container:

```
# 210-wide card with margin = { 6 6 }
text_multi   = { layoutpolicy_horizontal = expanding }   # spans 210, ignores margin
hbox         = { size = { 198 23 } }                     # spans 198, respects it
```

Six pixels. It is enough. The prose sits proud of everything beneath it and the
card reads as slightly broken. Pick one strategy per container: either
everything is explicit at the inner width, or everything expands and the margin
lives on the parent.

**Optical alignment beats mathematical alignment for text.** Centred CJK is
genuinely centred; centred Latin with punctuation often is not. Trust the render
over the arithmetic when they disagree.

---

## Type (arithmetic, then taste)

**Three levels in one panel: title, heading, body.** A fourth level almost
always means a heading is being used as body, or body as a caption.

Steps must be **visibly** different — roughly 1.25× minimum. From a shipped
panel: `28 / 20 / 13` works because each step is unmistakable. That panel also
carries a `15`, which sits too close to `13` to read as a level; it reads as an
inconsistency instead.

**Size is the weakest hierarchy tool available.** On parchment, weight and
colour separate levels at half the size cost. If a heading needs to be 1.5× the
body to feel like a heading, the problem is that it shares the body's colour.

**Remember `fontsize` is pixels.** A 13px CJK glyph is legible; 11px is not.
Below 12 for body text, stop and re-plan the layout instead.

---

## Colour (arithmetic budget, taste inside it)

**The budget: one body colour, one muted colour, one accent family.** Everything
else must earn its place by carrying information no label carries.

Worked example. A faction panel used nine colours:

- four faction hues (green / gold / violet / red)
- cream body text
- dark brown title
- gold for leader names
- **green for satisfaction values, orange for influence values**

The four faction hues earn their place: they identify who is who at a glance,
faster than reading. The last two do not. The row already says 满意度 and 影响力
— colouring the numbers as well encodes the same information twice, and the two
extra hues compete with the four that are doing real work. Setting those numbers
in the body colour calms the whole panel and costs nothing.

**The test for any colour: what would the reader fail to know if it were the
body colour?** If the answer is "nothing", it is decoration competing with your
real signals.

**Derive text colours from your background, not from vanilla's palette.** A mod
that paints on parchment cannot inherit CK3's dark-UI text colours. Compute the
composite: a panel at `color = { 0 0 0 0.6 }` with `alpha = 0.58` over parchment
is 35% black over a light ground, which lands mid-tone — and cream text on mid
tone is roughly 2.9:1, well under the 4.5:1 that body text needs. Either darken
the panel or darken the text; do not ship the middle.

**Colour is not the only accent.** Weight, a rule, a small icon and indentation
all separate content without spending from the colour budget.

---

## Ornament (taste)

Here I am stating a position rather than a measurement.

**Ornament belongs at the frame, never inside the content.** The scroll rods,
the ragged panel edges, the parchment grain — these are what make a panel yours,
and they work because they surround the content instead of interrupting it. A
decorative divider between every row is noise; one between sections is
structure.

**Reuse vanilla controls; go custom on the frame.** Anything that responds to
the mouse should be a vanilla control: the player has already learned it, and
Paradox's patches land there. Anything that merely says "this is my mod" is
where custom art earns its keep. If vanilla genuinely lacks the semantics you
need — a bar that diverges from centre, say — build it, but build it from
vanilla's own nine-slice pieces so it still reads as native.

**Restraint is the whole trick.** Almost every amateur CK3 panel is
over-decorated and under-aligned. The professional-looking ones are plain
content, precisely placed, inside a handsome frame.

---

## Reviewing a panel

Render it with `format=visual` and real `runtime_facts`, then in order:

1. **Count the five.** Fix every failure before looking at anything else.
2. **Squint.** Blur your reading of it and check that the intended hierarchy is
   still the thing you see first. If everything has equal weight, the panel has
   no hierarchy regardless of its font sizes.
3. **Check grouping.** For each element, is it closer to what it belongs with
   than to what it does not?
4. **Find the slack.** Where did leftover space land, and is that where you
   wanted it?
5. **Remove one thing.** There is almost always a divider, a colour or a label
   whose removal makes the panel better. Find it every time.
