# CK3 Experience Notes

Source: local `ck3_modding_wiki` notes under the current mod workspace.

These notes are workflow and generation hints. They are not engine authority.
Confirm risky mechanics through indexed scripts, `.info` schema hints, lookup tools,
and fresh `ck3-index scan` diagnostics.

## Scripting

- Effects and triggers are never standalone prose. They need an argument or block.
- Trigger-like blocks are usually AND blocks and may early-out after the first false trigger.
- `trigger_if` chains should terminate with `trigger_else`, even when the else block is empty.
- `limit = {}` inside iterators is a trigger context; iterator bodies are effect or trigger contexts depending on iterator family.
- `any_*` iterators are trigger-style; `every_*`, `random_*`, and `ordered_*` are effect-style.

## Scopes

- `scope:name` means a saved scope, not an event target. Do not prefix normal event targets such as `root`, `this`, `prev`, `primary_heir`, or database ids with `scope:`.
- Saved scopes are local to an uninterrupted script/effect chain. They should not be assumed to exist in unrelated events.
- `save_scope_as` is for effect context. In trigger context prefer temporary saved-scope forms when valid.
- Reusing the same saved-scope name overwrites the previous value.
- In `every_*` loops, saving the same scope name repeatedly leaves only the last value after the loop.

## Scripted Effects And Triggers

- Scripted effects behave like textual macro expansion and must not recurse.
- Scripted effects with template arguments should save important arguments into named scopes/values before complex nested use.
- Avoid ambiguous `root` and `prev` inside reusable scripted effects unless the caller context is tightly controlled.
- Scripted triggers can normally be used with `= yes` or negated with `= no`.

## On Actions

- Prefer adding a custom on_action through an existing on_action's `on_actions = {}` list.
- Avoid directly overriding upstream `trigger` or `effect` blocks on vanilla on_actions; this is fragile and may produce engine errors.
- `events`, `random_events`, and `on_actions` are append-style areas; direct trigger/effect replacement is the risky area.

## Localization

- CK3 expects `localization`, not `localisation`.
- Localization files should be under `localization/<language>/` or `localization/replace/<language>/`.
- Replace folders are for overriding existing localization values.
- Localization proves display text only. It must not be used as proof of mechanics.
- Event-style keys commonly use `.t`, `.desc`, `.a`, `.b`, and `.tt`.

## GUI

- Scripted GUIs live under `common/scripted_guis/` and may receive extra scopes from UI.
- GUI/scripted GUI scope assumptions should be checked against concrete vanilla examples before generation.
