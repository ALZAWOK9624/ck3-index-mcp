# CK3 GUI: making it look right

Everything here was verified against a running game, not inferred. Where a claim
is still unproven it says so.

## Look at it

A GUI is judged by looking. Render it, look at the picture, name what is wrong,
fix that, render again. Two rounds minimum, and the first round's verdict has to
land on something specific -- an edge that does not line up, a gap that is not
the same as its neighbour, text that touches a border -- not "looks fine".

```
ck3_gui operation=preview format=visual language=simp_chinese width=1120 height=700
```

`format=visual` approximates the game: real textures, real text, real colours,
correct mask compositing. `format=png` is a different tool -- a diagnostic
layout audit of kind-keyed boxes -- and cannot answer whether something looks
right. `format=html` is for browsing the tree, not for judging appearance.

**Inject runtime values before judging fit.** Unresolved bindings render as
`<unknown>`, which is far wider than the `50 / 100` the player sees, so a row
that fits in game looks broken in the preview. Pass the real numbers through
`runtime_facts` and the text metrics become true:

```json
{"runtime_facts": [
  {"expression": "GetPlayer.MakeScope.ScriptValue('..._satisfaction_ui_value')", "value": 50}
]}
```

Judging text width against `<unknown>` is the single easiest way to "fix" a
layout that was never broken.

## CSS instincts that do not transfer

The two languages look alike -- a tree of boxes with sizes, margins and flow
containers -- and a model that writes good HTML will reach for CSS reflexes.
These five are wrong here, and each one has produced a real bug:

| CSS expectation | jomini reality |
|---|---|
| `align-self: stretch` respects the parent's padding | `layoutpolicy_horizontal = expanding` fills the parent's **full** width and ignores its `margin`. A card with `margin = { 6 6 }` gives explicit `size = { 198 ... }` children a 6px inset and expanding children none, so one card ends up with two alignment baselines. |
| flex children stretch on the cross axis | flow containers **centre** on the cross axis. A 110-wide `portrait_head` in a 210-wide vbox sits at x+50, not x+0. |
| a parent's font and colour cascade | there is no cascade. Every widget states its own `default_format`, `fontsize` and `fonttintcolor`. |
| `font-size: 13` is 13 CSS pixels at whatever DPI | `fontsize = 13` is 13 **pixels**. Renderers that treat it as points at 96 DPI draw it a third too large. |
| a parent clips its children | it does not. Deliberate overflow is ordinary: `portrait_button` is `100x125` inside a `110x120` `portrait_head` and hangs five pixels above it on purpose. |

## A vanilla token carries its context

The most expensive mistake is borrowing a vanilla template without the
background it was designed against.

`Background_Area_Dark` (`gui/shared/backgrounds.gui`) is:

```
texture = "gfx/interface/component_masks/mask_rough_edges.dds"
color = { 0 0 0 0.6 }
```

The texture is a **mask**, not a picture: near-white RGB with the ragged edge
carried in its alpha. The engine multiplies, so the panel's colour comes from
`color` and its shape from the mask. Compositing it as an ordinary image yields
a light grey box instead of a dark panel -- the same class of error the coat of
arms renderer warns about, where channels are masks rather than pictures.

It is also **designed for CK3's dark UI**. Placed over a light parchment
background at `alpha = 0.58`, the effective black is `0.6 x 0.58 = 0.35`, which
lands the panel in mid tone. Whether the text on it stays readable is then a
question you have to answer, not one the template answered for you.

Before reusing a vanilla `using =` or template, check what it is drawn over in
vanilla. If your background differs, the token's contrast guarantee does not
come with it.

## Sizes that are landmines

**Fixed-height multiline text.** `text_multi` with `size = { 0 50 }` at
`fontsize = 13` fits two lines of Chinese with no slack. The same sentence in
English is three or more lines at the same width and truncates silently. If a
box holds localised prose, size it for the longest language you intend to ship,
not the one you are testing in.

**Unsized text labels.** A `text_label_center` with no `size` makes the layout
infer one, and the inference is generous -- wider than the card it sits in.
In game the label auto-fits, so this shows up only in tooling, but it is a
signal that the widget is under-specified.

## Numbers worth copying

From a shipped four-column panel that reads well in game:

- Card `210x410`, `margin = { 6 6 }`, `spacing = 5`; inner content explicitly `198` wide
- Faction name `fontsize = 20`, description `fontsize = 13`, window title `fontsize = 28`, subtitle `fontsize = 15`
- Stat row `size = { 198 23 }`, its progress bar `size = { 198 14 }`
- Four `210`-wide cards plus three `expand` in a `912`-wide hbox distributes 24px between cards

One caution on this panel: the faction name sits 6px from the card's top edge
and reads cramped against the border. 10-12px is the smallest change with the
most visible return.

## Unproven

- Whether `expand` inside a stat row leaves the gap the tooling shows. The game
  renders the label and its value adjacent; the preview spreads them. Do not
  restyle a row on the preview's spacing alone until this is resolved.
