# GUI and heraldry evidence

Read for GUI inspection, preview scenarios, or coat-of-arms work. This describes the current renderer, not a complete Jomini interpreter.

## Selecting and reading a preview

Use `ck3_gui operation=summary` if the relevant file/type/template is unknown, then narrow to it. GUI `path_prefix` selects symbols; dependency resolution can still involve other active files. Compare `files` with `resolution_files` when tracing inheritance or overrides.

For appearance, use `operation=preview format=visual`. For diagnostic geometry plus a browsable artifact, use `format=both html_mode=inspector`; `html_mode=static` is for a script-free document. Choose the relevant language (`raw`, `english`, `simp_chinese`, or `bilingual`).

Read `approximate`, `warnings`, nodes, semantics, textures, and missing runtime facts before judging fidelity. Unknown conditions remain unknown. Supported samples describe a controlled scenario; they are not observed save state.

## Explicit runtime state

- `runtime_facts`: exact atomic expressions mapped to typed values. The bounded evaluator composes supported logic/comparisons and progress bindings. Missing facts and unsupported expressions are reported.
- `sample_values`: exact per-expression/text overrides when needed; these take precedence for that property. Check matched-node counts and unused samples. Texture samples must point to an indexed source-relative `gfx/` asset, not a URL or arbitrary client path.
- `model_samples`: explicit rows for one selected grid/datamodel, with stable unique row ids and exact row-local samples. Check unused samples; cloned rows are provided review data.
- `action_effects`: only independently known scenario postconditions for an exact normalized action. Do not invent engine consequences to make a button appear functional. Builtin supported semantics cannot be overridden.

Inspector Visual mode uses textures/text and can replay supported clicks. Disable Replay clicks when selection should not change state. Repeated supported actions run in source order; unsupported actions remain metadata/log entries. The tool does not execute game effects. Inspect runtime statistics, missing facts, and unsupported plans.

## Assets, text, and layout limits

Embedded textures prove a bounded asset was decoded. Unsupported, oversized, missing, and dynamic textures stay explicit approximations. Check `texture_ref.resized` and original dimensions before judging source quality. The preview supports bounded mirroring, texture frames, slices/nine-slice borders, and allowlisted blends; this does not cover every shader or transform.

Static localization references and supported conditional/runtime text plans can resolve from the active language and explicit facts. Compare original and resolved values when provenance matters. `<unknown>`, `<runtime>`, partial or unresolved output must not be replaced with invented game context.

Standard hover state facts, tooltip overlays, flow, grids, progress, and scrolling have bounded support. Inspect resolved `type_chain` before treating a scroll widget as an ordinary container. A known-hidden parent suppresses its subtree; `ignoreinvisible` affects supported flow/grid layout. Blank model rows, complex anchors, unresolved templates, animation, and engine state machines still require CK3.

Use the interactive HTML to test relevant language changes, visibility, selection, scrolling, tooltips, and known click consequences. A clean preview proves controlled translation/simulation, not exact engine behavior. See [visual inspection](gui-visual-design.md) and [composition](gui-composition.md) only when appearance needs work.

## Coat of arms

`ck3_coat_of_arms operation=inspect` resolves patterns, colors, emblems, and missing references. `operation=assets` lists supported winning assets; `operation=render` draws the composited field before frame/material/dirt overlays.

Use actual heraldry under `common/coat_of_arms/coat_of_arms/`; sibling options/template/dynamic-definition folders do not serve the same purpose. Inspect named-color warnings and per-emblem overrides. A preview is subject to the same visibility boundary as the definition; it cannot expose a private coat of arms through public rendering.
