# Writing and localization

Read when authoring or reviewing displayed text. Respect the user's prose scope: creating a missing key does not authorize filling every description, tooltip, or event body.

## Meaning and voice

Use the project's existing localization keys and accepted names. Keep the intended narrator, viewpoint, date, and knowledge consistent; an in-world voice is a stylistic choice unless the project requires it. Vary event structure to fit the scene instead of forcing a standard introduction or number of options.

Explain actionable consequences truthfully. Show costs, thresholds, hidden state changes, and random outcomes through the supported tooltip mechanism when the player needs them to choose. Verify those claims against script and localization references. Localization alone does not establish how a mechanic works.

A reused mechanical concept may benefit from a `game_concept` definition and `[concept|E]` links. Repeated ordinary words do not all require new concepts. Reuse an existing concept when its meaning matches.

## Syntax and display

Inspect nearby language headers, keys, quoting, and encoding before editing. Preserve the project's conventions. Check literal quotes and escape handling against actual parser/engine examples; do not add or remove escaping solely from JSON or YAML habits.

Balance bracket expressions and supported formatting spans. Close opened `#...` formats with `#!` where required. Use emphasis to identify values, warnings, or terms without relying on color alone. Do not invent formatting tags.

Review literal macros, nested localization references, and dynamic scope expressions. A missing runtime value cannot be filled by guessing a character, title, or amount. Static review can catch known syntax/reference faults but cannot render every game context.

Validate complete proposed localization files with `ck3_review` or the patch preflight as appropriate, refresh written files, and inspect localization diagnostics. Preview the languages and UI scale intended for release; string length or language alone does not predict rendered width.
