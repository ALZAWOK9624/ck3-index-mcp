# GUI visual inspection

Read for layout or appearance changes. Use [GUI preview](gui-preview.md) for the renderer's evidence and runtime-sample contract.

Select a known indexed file, type, or template before requesting `ck3_gui operation=preview`. Use `format=visual` for the approximate textured PNG; `format=both html_mode=inspector` supplies a diagnostic PNG and interactive HTML whose Visual mode also supports appearance review. The HTML is not restricted to tree inspection.

## Layout and assets

Inspect resolved types and vanilla uses rather than importing CSS assumptions. Expansion, margins, cross-axis placement, anchors, font settings, and clipping must follow the resolved Jomini widget. Preview approximations do not establish universal engine rules.

Do not assume every parent clips descendants: a scroll viewport and an ordinary container can differ. Likewise, missing local text properties can come from resolved types/templates; lack of CSS inheritance is not proof that every field must be repeated.

Check a texture's role before compositing it. A mask contributes coverage rather than ordinary image colors. Tint, alpha, blend mode, and the background beneath a borrowed vanilla template all affect the final contrast.

Inspect original texture dimensions and any reported resize. A reduced preview is adequate for layout at that resolution, not for assessing every source detail.

## Reproducible scenarios

Provide exact `runtime_facts` when known or explicitly chosen review values are needed to exercise a state. Label chosen samples as examples. Missing expressions remain unknown; do not fix clipping caused only by an `<unknown>` placeholder before testing representative text.

Review long localization, changing runtime text, hidden content, and different list sizes. Fixed-height text may overflow; autoresize, anchors, and flow behavior still need engine validation. Avoid universal character-count or language-width ratios.

Inspect at least the relevant rendered states and revise demonstrated faults. Preserve deliberate overflow or spacing when actual engine evidence supports it. Final gameplay appearance and interaction require CK3, especially shaders, animation, unresolved templates, and complex bindings.
